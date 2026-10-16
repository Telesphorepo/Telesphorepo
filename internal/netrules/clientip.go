package netrules

import (
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
)

// trustedSet is the compiled set of trusted proxies for one settings snapshot, one IP list
// snapshot and one set of trusted list rows.
type trustedSet struct {
	snap       *settings.Snapshot
	lists      *map[int64]*listState
	rows       *[]int64
	prefixes   []netip.Prefix
	cloudflare []netip.Prefix // the ranges of the list "cloudflare" when the trusted proxies include it, else nil
}

func contains(ps []netip.Prefix, a netip.Addr) bool {
	for _, p := range ps {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// trustedNow returns the trusted set: the literal entries of trusted_proxies, and the ranges of
// the lists that the rows of trusted_proxy_lists and TR_TRUSTED_PROXIES include. It compiles the
// set again when the settings, the IP lists or the rows changed.
func (s *Service) trustedNow() *trustedSet {
	snap, lists, rows := s.set(), s.ids.Load(), s.trustedLists.Load()
	if t := s.trusted.Load(); t != nil && t.snap == snap && t.lists == lists && t.rows == rows {
		return t
	}
	t := &trustedSet{snap: snap, lists: lists, rows: rows}
	for _, id := range slices.Concat(*rows, s.envLists) {
		st := (*lists)[id]
		if st == nil {
			continue
		}
		if st.name == PresetCloudflare {
			t.cloudflare = st.ranges
		}
		t.prefixes = append(t.prefixes, st.ranges...)
	}
	for _, e := range TrustedProxies.Get(snap) {
		if e == Private {
			t.prefixes = append(t.prefixes, privateRanges...)
			continue
		}
		if strings.HasPrefix(e, TrustedProxyList) {
			continue // resolved once at startup (envLists)
		}
		if p, err := netip.ParsePrefix(e); err == nil {
			t.prefixes = append(t.prefixes, p.Masked())
		} else if a, err := netip.ParseAddr(e); err == nil {
			a = a.Unmap()
			t.prefixes = append(t.prefixes, netip.PrefixFrom(a, a.BitLen()))
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

// Client resolves the client of r: its address (see clientIP) and its scheme. The scheme is
// https when the connection is TLS. When the TCP peer is a trusted proxy, the forwarded proto
// (X-Forwarded-Proto, else the proto of Forwarded) gives the scheme. From any other peer a
// forwarded proto of https makes the scheme https too: the scheme only shapes the URLs that the
// server sends back to the same client, so such a peer can only make its own client use https.
func (s *Service) Client(r *http.Request) httpx.Client {
	ip, trusted := s.clientIP(r)
	proto, ok := forwardedProto(r.Header)
	https := r.TLS != nil || proto == "https"
	if trusted && ok {
		https = proto == "https"
	}
	return httpx.Client{IP: ip, HTTPS: https}
}

// forwardedProto returns the scheme that the first proxy saw: the first entry of
// X-Forwarded-Proto, else the proto of the first element of Forwarded (RFC 7239). It is "http"
// or "https", and ok is false when neither header gives one of them.
func forwardedProto(h http.Header) (proto string, ok bool) {
	if v := h.Get("X-Forwarded-Proto"); v != "" {
		first, _, _ := strings.Cut(v, ",")
		return schemeOf(first)
	}
	if v := h.Get("Forwarded"); v != "" {
		first, _, _ := strings.Cut(v, ",")
		for pair := range strings.SplitSeq(first, ";") {
			k, val, _ := strings.Cut(pair, "=")
			if strings.EqualFold(strings.TrimSpace(k), "proto") {
				return schemeOf(strings.Trim(strings.TrimSpace(val), "\""))
			}
		}
	}
	return "", false
}

// schemeOf returns s as "http" or "https", and false for any other value.
func schemeOf(s string) (string, bool) {
	switch s = strings.ToLower(strings.TrimSpace(s)); s {
	case "http", "https":
		return s, true
	}
	return "", false
}

// clientIP resolves the client address of r, and whether the TCP peer is a trusted proxy. When
// the peer is not a trusted proxy, the address is the peer. Otherwise X-Forwarded-For is read
// right to left, skipping trusted addresses; the first untrusted address is the client. When
// every hop is trusted, the left-most one is. An entry that does not parse stops the walk at the
// last address read. When that address is a trusted Cloudflare address, CF-Connecting-IP gives
// the client. So Cloudflare in front of Caddy needs no setting in Caddy: Caddy passes
// CF-Connecting-IP on unchanged.
func (s *Service) clientIP(r *http.Request) (netip.Addr, bool) {
	peer := httpx.PeerIP(r)
	t := s.trustedNow()
	if !peer.IsValid() {
		return peer, false
	}
	if !contains(t.prefixes, peer) {
		s.noteCandidate(peer, r.Header)
		return peer, false
	}
	var hops []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(v, ",")...)
	}
	client := peer
	for i := len(hops) - 1; i >= 0; i-- {
		if strings.TrimSpace(hops[i]) == "" {
			continue
		}
		a, ok := parseHop(hops[i])
		if !ok {
			break
		}
		client = a
		if !contains(t.prefixes, a) {
			return a, true
		}
	}
	if contains(t.cloudflare, client) {
		if a, ok := parseHop(r.Header.Get("CF-Connecting-IP")); ok {
			return a, true
		}
	}
	return client, true
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
