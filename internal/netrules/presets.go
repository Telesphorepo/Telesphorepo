// Package netrules holds the network rules of the server (spec §13.1): named IP range presets
// that the server fetches and refreshes daily, the client address behind trusted proxies, and
// the per-repo IP allow-lists for read and publish. Request paths read in-memory snapshots;
// the database keeps the last good preset lists and the rules.
package netrules

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/config"
	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
)

// Preset names. PresetPrivate is built in; the others are fetched.
const (
	PresetPrivate       = "private"
	PresetCloudflare    = "cloudflare"
	PresetGitHubActions = "github-actions"
)

// privateRanges is the built-in preset "private": loopback, RFC 1918, unique local (ULA) and
// link-local addresses.
var privateRanges = []netip.Prefix{
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("fe80::/10"),
}

const (
	refreshEvery = 24 * time.Hour
	checkEvery   = time.Hour
	fetchTimeout = 30 * time.Second
	maxBodyBytes = 8 << 20
	maxRanges    = 100000
	maxErrorLen  = 500

	// BaseURLEnv points every preset fetch at one test server: <base>/ips-v4, <base>/ips-v6
	// (cloudflare) and <base>/meta (github-actions). Only the e2e suite sets it.
	BaseURLEnv = "TR_E2E_PRESET_BASE_URL"
)

// source is one fetched preset.
type source struct {
	name, title string
	fetch       func(ctx context.Context, s *Service) ([]netip.Prefix, error)
}

var sources = []source{
	{PresetCloudflare, "Cloudflare", fetchCloudflare},
	{PresetGitHubActions, "GitHub Actions", fetchGitHubActions},
}

// KnownPreset reports whether name is a preset: built in or fetched.
func KnownPreset(name string) bool {
	if name == PresetPrivate {
		return true
	}
	return slices.ContainsFunc(sources, func(s source) bool { return s.name == name })
}

// presetState is the in-memory state of one fetched preset.
type presetState struct {
	ranges    []netip.Prefix
	fetchedAt int64 // unix; 0: never
	err       string
}

// Preset is a fetched preset as the admin API shows it.
type Preset struct {
	Name      string     `json:"name"`
	Title     string     `json:"title"`
	Count     int        `json:"count"`
	FetchedAt *time.Time `json:"fetched_at"`
	Error     string     `json:"error"`
}

// ErrUnknownPreset is a name that is not a fetched preset.
var ErrUnknownPreset = errors.New("unknown preset")

// Service keeps the presets, resolves client addresses and checks repo IP rules.
type Service struct {
	rq, wq *dbq.Queries
	d      *db.DB
	cfg    config.Provider
	audit  *audit.Service
	log    *slog.Logger
	client *http.Client
	base   string // BaseURLEnv; "" for the real sources

	presets   atomic.Pointer[map[string]*presetState] // copy on write
	refreshMu sync.Mutex                              // one refresh at a time
	trusted   atomic.Pointer[trustedSet]

	candMu sync.Mutex
	cands  map[netip.Addr]*Candidate // untrusted peers that sent forwarded headers

	rulesMu sync.RWMutex
	rules   map[int64]*repoRules

	wg sync.WaitGroup
}

// New returns the service. Load must run before it serves requests.
func New(d *db.DB, cfg config.Provider, au *audit.Service, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	s := &Service{
		d: d, rq: dbq.New(d.R), wq: dbq.New(d.W), cfg: cfg, audit: au, log: log,
		client: &http.Client{
			Timeout: fetchTimeout,
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 20 * time.Second,
				MaxIdleConns:          2,
				IdleConnTimeout:       30 * time.Second,
			},
		},
		base:  strings.TrimRight(os.Getenv(BaseURLEnv), "/"),
		rules: map[int64]*repoRules{},
		cands: map[netip.Addr]*Candidate{},
	}
	empty := map[string]*presetState{}
	s.presets.Store(&empty)
	return s
}

// Load reads the last good preset lists and every repo's IP rules into memory.
func (s *Service) Load(ctx context.Context) error {
	m := map[string]*presetState{}
	for _, src := range sources {
		row, err := s.rq.GetPresetRanges(ctx, src.name)
		if errors.Is(err, sql.ErrNoRows) {
			m[src.name] = &presetState{}
			continue
		}
		if err != nil {
			return fmt.Errorf("load preset %s: %w", src.name, err)
		}
		var list []string
		if err := json.Unmarshal([]byte(row.Ranges), &list); err != nil {
			return fmt.Errorf("load preset %s: %w", src.name, err)
		}
		st := &presetState{fetchedAt: row.FetchedAt, err: row.Error}
		for _, c := range list {
			if p, err := netip.ParsePrefix(c); err == nil {
				st.ranges = append(st.ranges, p)
			}
		}
		m[src.name] = st
	}
	s.presets.Store(&m)
	return s.loadRules(ctx)
}

// Start refreshes stale presets now and then checks every hour, until ctx ends. A preset is
// stale when it was never fetched or its last good fetch is a day old.
func (s *Service) Start(ctx context.Context) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		t := time.NewTicker(checkEvery)
		defer t.Stop()
		for {
			now := time.Now().Unix()
			for _, src := range sources {
				st := (*s.presets.Load())[src.name]
				if st != nil && st.fetchedAt != 0 && now-st.fetchedAt < int64(refreshEvery/time.Second) {
					continue
				}
				if err := s.Refresh(ctx, src.name); err != nil && ctx.Err() == nil {
					s.log.Warn("refresh IP range preset; keeping the last good list", "preset", src.name, "err", err.Error())
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

// Wait waits for the refresh job to stop.
func (s *Service) Wait() { s.wg.Wait() }

// Presets lists the fetched presets.
func (s *Service) Presets() []Preset {
	m := *s.presets.Load()
	out := make([]Preset, 0, len(sources))
	for _, src := range sources {
		out = append(out, toPreset(src, m[src.name]))
	}
	return out
}

func toPreset(src source, st *presetState) Preset {
	p := Preset{Name: src.name, Title: src.title}
	if st != nil {
		p.Count, p.Error = len(st.ranges), st.err
		if st.fetchedAt != 0 {
			t := time.Unix(st.fetchedAt, 0).UTC()
			p.FetchedAt = &t
		}
	}
	return p
}

// Refresh fetches preset name now. A failed fetch keeps the last good list and records the
// error, which Refresh also returns. ErrUnknownPreset for a name that is not fetched.
func (s *Service) Refresh(ctx context.Context, name string) error {
	i := slices.IndexFunc(sources, func(src source) bool { return src.name == name })
	if i < 0 {
		return ErrUnknownPreset
	}
	src := sources[i]
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	ranges, err := src.fetch(ctx, s)
	if err == nil && len(ranges) == 0 {
		err = errors.New("the list has no ranges")
	}
	old := (*s.presets.Load())[name]
	next := &presetState{}
	if old != nil {
		*next = *old
	}
	if err != nil {
		msg := err.Error()
		if len(msg) > maxErrorLen {
			msg = msg[:maxErrorLen]
			for !utf8.ValidString(msg) {
				msg = msg[:len(msg)-1]
			}
		}
		next.err = msg
		if werr := s.wq.SetPresetError(context.WithoutCancel(ctx), dbq.SetPresetErrorParams{Name: name, Error: msg}); werr != nil {
			s.log.Error("record preset error", "preset", name, "err", werr.Error())
		}
		s.storePreset(name, next)
		return err
	}
	list := make([]string, len(ranges))
	for i, p := range ranges {
		list[i] = p.String()
	}
	js, _ := json.Marshal(list)
	now := time.Now().Unix()
	if err := s.wq.SavePresetRanges(context.WithoutCancel(ctx), dbq.SavePresetRangesParams{Name: name, Ranges: string(js), FetchedAt: now}); err != nil {
		return fmt.Errorf("save preset %s: %w", name, err)
	}
	s.storePreset(name, &presetState{ranges: ranges, fetchedAt: now})
	return nil
}

// Preset returns preset name as the admin API shows it.
func (s *Service) Preset(name string) (Preset, bool) {
	for _, src := range sources {
		if src.name == name {
			return toPreset(src, (*s.presets.Load())[name]), true
		}
	}
	return Preset{}, false
}

func (s *Service) storePreset(name string, st *presetState) {
	old := *s.presets.Load()
	m := make(map[string]*presetState, len(old)+1)
	for k, v := range old {
		m[k] = v
	}
	m[name] = st
	s.presets.Store(&m)
}

// presetRanges returns the current ranges of a preset (nil when unknown or never fetched).
func (s *Service) presetRanges(name string) []netip.Prefix {
	if name == PresetPrivate {
		return privateRanges
	}
	if st := (*s.presets.Load())[name]; st != nil {
		return st.ranges
	}
	return nil
}

// get fetches url and returns at most maxBodyBytes of a 200 answer.
func (s *Service) get(ctx context.Context, url string, header map[string]string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "telesphorepo")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	if len(b) > maxBodyBytes {
		return nil, fmt.Errorf("GET %s: the answer is larger than %d bytes", url, maxBodyBytes)
	}
	return b, nil
}

// parseRanges parses CIDRs, one per item, and refuses the whole list when one does not parse.
func parseRanges(items []string, from string) ([]netip.Prefix, error) {
	if len(items) > maxRanges {
		return nil, fmt.Errorf("%s: more than %d ranges", from, maxRanges)
	}
	out := make([]netip.Prefix, 0, len(items))
	for _, it := range items {
		p, err := netip.ParsePrefix(strings.TrimSpace(it))
		if err != nil {
			return nil, fmt.Errorf("%s: %q is not a CIDR", from, clipText(it, 64))
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// fetchCloudflare reads https://www.cloudflare.com/ips-v4 and /ips-v6: one CIDR per line.
func fetchCloudflare(ctx context.Context, s *Service) ([]netip.Prefix, error) {
	base := "https://www.cloudflare.com"
	if s.base != "" {
		base = s.base
	}
	var all []netip.Prefix
	for _, p := range []string{"/ips-v4", "/ips-v6"} {
		b, err := s.get(ctx, base+p, nil)
		if err != nil {
			return nil, err
		}
		var lines []string
		sc := bufio.NewScanner(strings.NewReader(string(b)))
		for sc.Scan() {
			if l := strings.TrimSpace(sc.Text()); l != "" {
				lines = append(lines, l)
			}
		}
		if err := sc.Err(); err != nil {
			return nil, fmt.Errorf("%s: %w", base+p, err)
		}
		r, err := parseRanges(lines, base+p)
		if err != nil {
			return nil, err
		}
		all = append(all, r...)
	}
	return all, nil
}

// fetchGitHubActions reads the "actions" key of https://api.github.com/meta.
func fetchGitHubActions(ctx context.Context, s *Service) ([]netip.Prefix, error) {
	base := "https://api.github.com"
	if s.base != "" {
		base = s.base
	}
	url := base + "/meta"
	b, err := s.get(ctx, url, map[string]string{"Accept": "application/vnd.github+json"})
	if err != nil {
		return nil, err
	}
	var meta struct {
		Actions []string `json:"actions"`
	}
	if err := json.Unmarshal(b, &meta); err != nil {
		return nil, fmt.Errorf("%s: not the expected JSON: %w", url, err)
	}
	return parseRanges(meta.Actions, url)
}
