package netrules

import (
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/config"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
)

// trustedSet is the compiled trusted_proxies setting for one settings snapshot and one preset
// snapshot.
type trustedSet struct {
	cfg        *config.Config
	presets    *map[string]*presetState
	prefixes   []netip.Prefix
	cloudflare []netip.Prefix // the cloudflare ranges when the setting names them, else nil
}

func contains(ps []netip.Prefix, a netip.Addr) bool {
	for _, p := range ps {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// trustedNow returns the trusted set of the current settings and presets, compiling it when
// either changed.
func (s *Service) trustedNow() *trustedSet {
	cfg, presets := s.cfg(), s.presets.Load()
	if t := s.trusted.Load(); t != nil && t.cfg == cfg && t.presets == presets {
		return t
	}
	t := &trustedSet{cfg: cfg, presets: presets}
	for _, e := range cfg.TrustedProxies {
		switch e {
		case PresetPrivate:
			t.prefixes = append(t.prefixes, privateRanges...)
		case PresetCloudflare:
			t.cloudflare = s.presetRanges(PresetCloudflare)
			t.prefixes = append(t.prefixes, t.cloudflare...)
		default:
			if p, err := netip.ParsePrefix(e); err == nil {
				t.prefixes = append(t.prefixes, p.Masked())
			} else if a, err := netip.ParseAddr(e); err == nil {
				a = a.Unmap()
				t.prefixes = append(t.prefixes, netip.PrefixFrom(a, a.BitLen()))
			}
		}
	}
	s.trusted.Store(t)
	return t
}

// parseHop parses one X-Forwarded-For entry: an address, optionally with a port or brackets.
func parseHop(h string) (netip.Addr, bool) {
	h = strings.TrimSpace(h)
	if a, err := netip.ParseAddr(h); err == nil {
		return a.Unmap(), true
	}
	if ap, err := netip.ParseAddrPort(h); err == nil {
		return ap.Addr().Unmap(), true
	}
	return netip.Addr{}, false
}

// ClientIP resolves the client address of r. When the TCP peer is not a trusted proxy, it is
// the peer. Otherwise X-Forwarded-For is read right to left, skipping trusted addresses; the
// first untrusted address is the client. When every hop is trusted, the left-most one is. An
// entry that does not parse stops the walk at the last address read. Without X-Forwarded-For,
// a trusted Cloudflare peer's CF-Connecting-IP gives the client.
func (s *Service) ClientIP(r *http.Request) netip.Addr {
	peer := httpx.PeerIP(r)
	t := s.trustedNow()
	if !peer.IsValid() {
		return peer
	}
	if !contains(t.prefixes, peer) {
		s.noteCandidate(peer, r.Header)
		return peer
	}
	var hops []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(v, ",")...)
	}
	client := peer
	seen := false
	for i := len(hops) - 1; i >= 0; i-- {
		if strings.TrimSpace(hops[i]) == "" {
			continue
		}
		a, ok := parseHop(hops[i])
		if !ok {
			break
		}
		client, seen = a, true
		if !contains(t.prefixes, a) {
			return a
		}
	}
	if !seen && contains(t.cloudflare, peer) {
		if a, ok := parseHop(r.Header.Get("CF-Connecting-IP")); ok {
			return a
		}
	}
	return client
}

// maxCandidates caps the proxy candidates kept in memory. A new peer over the cap replaces the
// one seen longest ago.
const maxCandidates = 20

// forwardedHeaders are the headers that mark a peer as a likely reverse proxy, in the order
// that names the candidate's header.
var forwardedHeaders = []string{"X-Forwarded-For", "Forwarded", "CF-Connecting-IP"}

// Candidate is an untrusted TCP peer that sent a forwarded header: probably a reverse proxy that
// trusted_proxies does not list yet. The server keeps candidates in memory only.
type Candidate struct {
	Address  string    `json:"address"`
	LastSeen time.Time `json:"last_seen"`
	Count    int64     `json:"count"`
	Header   string    `json:"header"` // the forwarded header of the last request
}

// noteCandidate records peer as a proxy candidate when h has a forwarded header.
func (s *Service) noteCandidate(peer netip.Addr, h http.Header) {
	header := ""
	for _, k := range forwardedHeaders {
		if h.Get(k) != "" {
			header = k
			break
		}
	}
	if header == "" {
		return
	}
	now := time.Now().UTC()
	s.candMu.Lock()
	defer s.candMu.Unlock()
	c := s.cands[peer]
	if c == nil {
		if len(s.cands) >= maxCandidates {
			var oldest netip.Addr
			var at time.Time
			for a, o := range s.cands {
				if !oldest.IsValid() || o.LastSeen.Before(at) {
					oldest, at = a, o.LastSeen
				}
			}
			delete(s.cands, oldest)
		}
		c = &Candidate{Address: peer.String()}
		s.cands[peer] = c
	}
	c.LastSeen, c.Header = now, header
	c.Count++
}

// ProxyCandidates returns the untrusted peers that sent forwarded headers, most recent first.
// A peer that trusted_proxies covers now is dropped.
func (s *Service) ProxyCandidates() []Candidate {
	t := s.trustedNow()
	s.candMu.Lock()
	out := make([]Candidate, 0, len(s.cands))
	for a, c := range s.cands {
		if contains(t.prefixes, a) {
			delete(s.cands, a)
			continue
		}
		out = append(out, *c)
	}
	s.candMu.Unlock()
	slices.SortFunc(out, func(a, b Candidate) int { return b.LastSeen.Compare(a.LastSeen) })
	return out
}

// Middleware resolves the client address once per request and attaches it to the request
// (httpx.ClientIP reads it).
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, httpx.WithClientIP(r, s.ClientIP(r)))
	})
}
