// Package netrules holds the network rules of the server (spec §13.1, §13.3): named IP lists
// (presets that the server fetches and refreshes daily, and custom lists that admins edit), the
// client address behind trusted proxies, and the per-repo IP allow-lists for read and publish.
// Request paths read in-memory snapshots; the database keeps the lists and the rules.
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
	"unicode"
	"unicode/utf8"

	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/config"
	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
)

// List kinds.
const (
	KindPreset = "preset" // fetched from its source, read-only ranges
	KindCustom = "custom" // edited by admins
)

// Names of the preset lists, and "private", the built-in trusted_proxies entry.
const (
	PresetCloudflare    = "cloudflare"
	PresetGitHubActions = "github-actions"
	Private             = "private"
)

// privateRanges is the trusted_proxies entry "private": loopback, RFC 1918, unique local (ULA)
// and link-local addresses.
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

	maxCustomRanges = 10000
	maxTitleChars   = 100
	maxDescChars    = 1000
	loadPage        = 500

	// BaseURLEnv points every preset fetch at one test server: <base>/ips-v4, <base>/ips-v6
	// (cloudflare) and <base>/meta (github-actions). Only the e2e suite sets it.
	BaseURLEnv = "TR_E2E_PRESET_BASE_URL"
)

// preset is one fetched list.
type preset struct {
	name, title, description, source string
	fetch                            func(ctx context.Context, s *Service) ([]netip.Prefix, error)
}

var presets = []preset{
	{PresetCloudflare, "Cloudflare", "The addresses Cloudflare connects from when it proxies a site. Trust them as a reverse proxy when the server is behind Cloudflare.",
		"https://www.cloudflare.com/ips-v4, https://www.cloudflare.com/ips-v6", fetchCloudflare},
	{PresetGitHubActions, "GitHub Actions", "The addresses of GitHub-hosted Actions runners (the \"actions\" key of the GitHub meta API).",
		"https://api.github.com/meta", fetchGitHubActions},
}

// listState is the in-memory state of one list.
type listState struct {
	id          int64
	name, kind  string
	title, desc string
	source      string
	ranges      []netip.Prefix
	fetchedAt   int64 // unix; 0: never (presets)
	err         string
	createdAt   int64
}

// List is an IP list as the admin API shows it. Ranges is left out of the list view.
type List struct {
	Name        string     `json:"name"`
	Kind        string     `json:"kind"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Count       int        `json:"count"`
	Ranges      *[]string  `json:"ranges,omitempty"` // only in the detail view
	Source      string     `json:"source"`
	FetchedAt   *time.Time `json:"fetched_at"`
	Error       string     `json:"error"`
	CreatedAt   time.Time  `json:"created_at"`
}

// Use is one repo rule scope that names a list.
type Use struct {
	Repo  string `json:"repo"`
	Scope string `json:"scope"`
}

// Usage says what names a list: repo rules and the trusted_proxies setting.
type Usage struct {
	Repos          []Use `json:"repos"`
	TrustedProxies bool  `json:"trusted_proxies"`
}

func (u Usage) inUse() bool { return len(u.Repos) > 0 || u.TrustedProxies }

var (
	// ErrNotFound is a list name that does not exist.
	ErrNotFound = errors.New("IP list not found")
	// ErrExists is a list name that is taken.
	ErrExists = errors.New("an IP list with this name already exists")
	// ErrReadOnly is a change of a preset list.
	ErrReadOnly = errors.New("a preset list is fetched from its source and cannot be changed")
	// ErrNotPreset is a refresh of a custom list.
	ErrNotPreset = errors.New("only a preset list can be refreshed")
)

// InUseError refuses to delete a list that repo rules or trusted_proxies name.
type InUseError struct {
	Usage Usage
}

func (e *InUseError) Error() string {
	var parts []string
	for _, u := range e.Usage.Repos {
		parts = append(parts, "the "+u.Scope+" rules of "+u.Repo)
	}
	if e.Usage.TrustedProxies {
		parts = append(parts, "the trusted proxies")
	}
	return "the IP list is used by " + strings.Join(parts, ", ")
}

// Service keeps the IP lists, resolves client addresses and checks repo IP rules.
type Service struct {
	rq, wq *dbq.Queries
	d      *db.DB
	cfg    config.Provider
	audit  *audit.Service
	log    *slog.Logger
	client *http.Client
	base   string // BaseURLEnv; "" for the real sources

	lists     atomic.Pointer[map[string]*listState] // copy on write under listsMu
	listsMu   sync.Mutex
	refreshMu sync.Mutex // one fetch at a time
	trusted   atomic.Pointer[trustedSet]

	candMu sync.Mutex
	cands  map[netip.Addr]*Candidate // untrusted peers that sent forwarded headers

	// rulesMu guards rules. A writer of rules, and a rename or delete of a list, holds it, so a
	// rule never names a list that is gone. Lock order: rulesMu, then listsMu.
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
	empty := map[string]*listState{}
	s.lists.Store(&empty)
	return s
}

// Load creates the preset rows on the first start and reads every list and every repo's IP
// rules into memory.
func (s *Service) Load(ctx context.Context) error {
	now := time.Now().Unix()
	for _, p := range presets {
		if err := s.wq.EnsurePresetList(ctx, dbq.EnsurePresetListParams{Name: p.name, Title: p.title,
			Description: p.description, Source: p.source, CreatedAt: now}); err != nil {
			return fmt.Errorf("create preset list %s: %w", p.name, err)
		}
	}
	m := map[string]*listState{}
	after := ""
	for {
		rows, err := s.rq.ListIPLists(ctx, dbq.ListIPListsParams{After: after, Limit: loadPage})
		if err != nil {
			return fmt.Errorf("load IP lists: %w", err)
		}
		for _, r := range rows {
			st, err := stateOf(r)
			if err != nil {
				return err
			}
			m[r.Name] = st
		}
		if len(rows) < loadPage {
			break
		}
		after = rows[len(rows)-1].Name
	}
	s.lists.Store(&m)
	return s.loadRules(ctx)
}

func stateOf(r dbq.IpList) (*listState, error) {
	var list []string
	if err := json.Unmarshal([]byte(r.Ranges), &list); err != nil {
		return nil, fmt.Errorf("load IP list %s: %w", r.Name, err)
	}
	st := &listState{id: r.ID, name: r.Name, kind: r.Kind, title: r.Title, desc: r.Description, source: r.Source,
		fetchedAt: r.FetchedAt, err: r.Error, createdAt: r.CreatedAt}
	for _, c := range list {
		if p, err := netip.ParsePrefix(c); err == nil {
			st.ranges = append(st.ranges, p)
		}
	}
	return st, nil
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
			for _, p := range presets {
				st := (*s.lists.Load())[p.name]
				if st != nil && st.fetchedAt != 0 && now-st.fetchedAt < int64(refreshEvery/time.Second) {
					continue
				}
				if err := s.Refresh(ctx, p.name); err != nil && ctx.Err() == nil {
					s.log.Warn("refresh IP list; keeping the last good list", "list", p.name, "err", err.Error())
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

func (st *listState) view(withRanges bool) List {
	l := List{Name: st.name, Kind: st.kind, Title: st.title, Description: st.desc, Count: len(st.ranges),
		Source: st.source, Error: st.err, CreatedAt: time.Unix(st.createdAt, 0).UTC()}
	if st.fetchedAt != 0 {
		t := time.Unix(st.fetchedAt, 0).UTC()
		l.FetchedAt = &t
	}
	if withRanges {
		r := rangeStrings(st.ranges)
		l.Ranges = &r
	}
	return l
}

func rangeStrings(ps []netip.Prefix) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.String()
	}
	return out
}

// Lists returns every list without its ranges, presets first, then by name.
func (s *Service) Lists() []List {
	m := *s.lists.Load()
	out := make([]List, 0, len(m))
	for _, st := range m {
		out = append(out, st.view(false))
	}
	slices.SortFunc(out, func(a, b List) int {
		if a.Kind != b.Kind {
			if a.Kind == KindPreset {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out
}

// Get returns list name with its ranges.
func (s *Service) Get(name string) (List, bool) {
	st := (*s.lists.Load())[name]
	if st == nil {
		return List{}, false
	}
	return st.view(true), true
}

// Exists reports whether list name exists.
func (s *Service) Exists(name string) bool { return (*s.lists.Load())[name] != nil }

// listRanges returns the current ranges of list name (nil when it does not exist).
func (s *Service) listRanges(name string) []netip.Prefix {
	if st := (*s.lists.Load())[name]; st != nil {
		return st.ranges
	}
	return nil
}

// storeList applies change to a copy of the snapshot and publishes it. Caller holds listsMu.
func (s *Service) storeList(change func(m map[string]*listState)) {
	old := *s.lists.Load()
	m := make(map[string]*listState, len(old)+1)
	for k, v := range old {
		m[k] = v
	}
	change(m)
	s.lists.Store(&m)
}

// Usage returns what names list name: repo rules (through repo_ip_rules_list) and
// trusted_proxies.
func (s *Service) Usage(ctx context.Context, name string) (Usage, error) {
	return s.usage(ctx, s.rq, name)
}

func (s *Service) usage(ctx context.Context, q *dbq.Queries, name string) (Usage, error) {
	rows, err := q.ListRulesOfList(ctx, name)
	if err != nil {
		return Usage{}, err
	}
	u := Usage{Repos: make([]Use, 0, len(rows)), TrustedProxies: slices.Contains(s.cfg().TrustedProxies, settings.TrustedProxyList+name)}
	for _, r := range rows {
		u.Repos = append(u.Repos, Use{Repo: r.Repo, Scope: r.Scope})
	}
	return u, nil
}

// ListInput is a custom list as an admin writes it. A nil field keeps the current value on an
// update.
type ListInput struct {
	Name        *string   `json:"name"`
	Title       *string   `json:"title"`
	Description *string   `json:"description"`
	Ranges      *[]string `json:"ranges"`
}

// InvalidError is a list input that does not validate.
type InvalidError struct{ msg string }

func (e *InvalidError) Error() string { return e.msg }

func invalid(format string, args ...any) error { return &InvalidError{fmt.Sprintf(format, args...)} }

func checkText(field, v string, max int) (string, error) {
	v = strings.TrimSpace(v)
	if !utf8.ValidString(v) || utf8.RuneCountInString(v) > max {
		return "", invalid("%s must be at most %d characters", field, max)
	}
	for _, c := range v {
		if unicode.IsControl(c) && c != '\n' {
			return "", invalid("%s must not contain control characters", field)
		}
	}
	return v, nil
}

// parseCustomRanges validates the ranges of a custom list: CIDRs (masked) or addresses (a /32
// or /128), no duplicates.
func parseCustomRanges(in []string) ([]netip.Prefix, error) {
	if len(in) > maxCustomRanges {
		return nil, invalid("a list has at most %d ranges", maxCustomRanges)
	}
	out := make([]netip.Prefix, 0, len(in))
	seen := map[netip.Prefix]bool{}
	for _, raw := range in {
		v := strings.TrimSpace(raw)
		var p netip.Prefix
		if x, err := netip.ParsePrefix(v); err == nil {
			p = x.Masked()
		} else if a, err := netip.ParseAddr(v); err == nil {
			a = a.Unmap()
			p = netip.PrefixFrom(a, a.BitLen())
		} else {
			return nil, invalid("%q is not a CIDR or an address (e.g. 203.0.113.0/24)", clipText(v, 64))
		}
		if seen[p] {
			return nil, invalid("%s is listed twice", p)
		}
		seen[p] = true
		out = append(out, p)
	}
	return out, nil
}

func checkListName(name string) error {
	if !repos.NameRE.MatchString(name) {
		return invalid("the name must be 1 to 64 lower-case letters, digits, '.', '_' or '-', starting with a letter or digit")
	}
	return nil
}

// Create adds a custom list.
func (s *Service) Create(ctx context.Context, in ListInput) (List, error) {
	var name, title, desc string
	var ranges []string
	if in.Name != nil {
		name = strings.TrimSpace(*in.Name)
	}
	if err := checkListName(name); err != nil {
		return List{}, err
	}
	if in.Title != nil {
		title = *in.Title
	}
	if in.Description != nil {
		desc = *in.Description
	}
	if in.Ranges != nil {
		ranges = *in.Ranges
	}
	title, err := checkText("title", title, maxTitleChars)
	if err != nil {
		return List{}, err
	}
	if title == "" {
		title = name
	}
	if desc, err = checkText("description", desc, maxDescChars); err != nil {
		return List{}, err
	}
	prefixes, err := parseCustomRanges(ranges)
	if err != nil {
		return List{}, err
	}
	js, _ := json.Marshal(rangeStrings(prefixes))

	s.listsMu.Lock()
	defer s.listsMu.Unlock()
	if s.Exists(name) {
		return List{}, ErrExists
	}
	now := time.Now().Unix()
	id, err := s.wq.CreateCustomList(ctx, dbq.CreateCustomListParams{Name: name, Title: title, Description: desc,
		Ranges: string(js), CreatedAt: now})
	if isUnique(err) {
		return List{}, ErrExists
	}
	if err != nil {
		return List{}, err
	}
	st := &listState{id: id, name: name, kind: KindCustom, title: title, desc: desc, ranges: prefixes, createdAt: now}
	s.storeList(func(m map[string]*listState) { m[name] = st })
	return st.view(true), nil
}

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// Update changes custom list name: its name (the repo rules that name it follow), title,
// description and ranges. It returns the list and the old name when it was renamed (""
// otherwise); the caller updates trusted_proxies for a rename.
func (s *Service) Update(ctx context.Context, name string, in ListInput) (List, string, error) {
	s.rulesMu.Lock()
	defer s.rulesMu.Unlock()
	s.listsMu.Lock()
	defer s.listsMu.Unlock()
	cur := (*s.lists.Load())[name]
	if cur == nil {
		return List{}, "", ErrNotFound
	}
	if cur.kind != KindCustom {
		return List{}, "", ErrReadOnly
	}
	next := *cur
	var err error
	if in.Name != nil {
		next.name = strings.TrimSpace(*in.Name)
		if err := checkListName(next.name); err != nil {
			return List{}, "", err
		}
		if next.name != name && s.Exists(next.name) {
			return List{}, "", ErrExists
		}
	}
	if in.Title != nil {
		if next.title, err = checkText("title", *in.Title, maxTitleChars); err != nil {
			return List{}, "", err
		}
		if next.title == "" {
			next.title = next.name
		}
	}
	if in.Description != nil {
		if next.desc, err = checkText("description", *in.Description, maxDescChars); err != nil {
			return List{}, "", err
		}
	}
	if in.Ranges != nil {
		if next.ranges, err = parseCustomRanges(*in.Ranges); err != nil {
			return List{}, "", err
		}
	}
	js, _ := json.Marshal(rangeStrings(next.ranges))
	renamed := next.name != name
	err = s.d.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		if err := q.UpdateCustomList(ctx, dbq.UpdateCustomListParams{Name: next.name, Title: next.title,
			Description: next.desc, Ranges: string(js), ID: cur.id}); err != nil {
			return err
		}
		if renamed {
			return q.RenameListRules(ctx, dbq.RenameListRulesParams{NewName: next.name, OldName: name})
		}
		return nil
	})
	if isUnique(err) {
		return List{}, "", ErrExists
	}
	if err != nil {
		return List{}, "", err
	}
	s.storeList(func(m map[string]*listState) {
		delete(m, name)
		m[next.name] = &next
	})
	old := ""
	if renamed {
		old = name
		if err := s.reloadRulesLocked(ctx); err != nil {
			return List{}, "", err
		}
	}
	return next.view(true), old, nil
}

// Delete removes custom list name. A list that repo rules or trusted_proxies name is refused
// with an *InUseError.
func (s *Service) Delete(ctx context.Context, name string) error {
	s.rulesMu.Lock()
	defer s.rulesMu.Unlock()
	s.listsMu.Lock()
	defer s.listsMu.Unlock()
	cur := (*s.lists.Load())[name]
	if cur == nil {
		return ErrNotFound
	}
	if cur.kind != KindCustom {
		return ErrReadOnly
	}
	err := s.d.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		u, err := s.usage(ctx, q, name)
		if err != nil {
			return err
		}
		if u.inUse() {
			return &InUseError{Usage: u}
		}
		return q.DeleteCustomList(ctx, cur.id)
	})
	if err != nil {
		return err
	}
	s.storeList(func(m map[string]*listState) { delete(m, name) })
	return nil
}

// Refresh fetches preset name now. A failed fetch keeps the last good list and records the
// error, which Refresh also returns. ErrNotFound for an unknown name, ErrNotPreset for a custom
// list.
func (s *Service) Refresh(ctx context.Context, name string) error {
	i := slices.IndexFunc(presets, func(p preset) bool { return p.name == name })
	if i < 0 {
		if s.Exists(name) {
			return ErrNotPreset
		}
		return ErrNotFound
	}
	p := presets[i]
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	ranges, err := p.fetch(ctx, s)
	if err == nil && len(ranges) == 0 {
		err = errors.New("the list has no ranges")
	}
	s.listsMu.Lock()
	defer s.listsMu.Unlock()
	cur := (*s.lists.Load())[name]
	if cur == nil {
		return ErrNotFound
	}
	next := *cur
	if err != nil {
		next.err = clipText(err.Error(), maxErrorLen)
		if werr := s.wq.SetPresetError(context.WithoutCancel(ctx), dbq.SetPresetErrorParams{Name: name, Error: next.err}); werr != nil {
			s.log.Error("record IP list error", "list", name, "err", werr.Error())
		}
		s.storeList(func(m map[string]*listState) { m[name] = &next })
		return err
	}
	js, _ := json.Marshal(rangeStrings(ranges))
	now := time.Now().Unix()
	if err := s.wq.SavePresetRanges(context.WithoutCancel(ctx), dbq.SavePresetRangesParams{Name: name, Ranges: string(js), FetchedAt: now}); err != nil {
		return fmt.Errorf("save IP list %s: %w", name, err)
	}
	next.ranges, next.fetchedAt, next.err = ranges, now, ""
	s.storeList(func(m map[string]*listState) { m[name] = &next })
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
