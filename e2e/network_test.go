//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

// Network rules (spec §13.1): the client address behind trusted proxies, per-repo IP rules for
// read and publish, and the IP range presets.
//
// The test process reaches a server through the engine's port mapping, so the server's TCP peer
// is a private address (the bridge gateway or loopback). The default trusted_proxies trusts no
// proxy. A test that acts as any client address sets trusted_proxies to "private" first and then
// sends X-Forwarded-For.

// apiHeaders is Server.API with extra request headers.
func apiHeaders(t *testing.T, s *Server, method, path string, body any, auth Auth, hdr map[string]string) (int, []byte) {
	t.Helper()
	var r io.Reader
	isJSON := false
	switch b := body.(type) {
	case nil:
	case []byte:
		r = bytes.NewReader(b)
	case string:
		r = strings.NewReader(b)
	default:
		j, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		r, isJSON = bytes.NewReader(j), true
	}
	req := s.NewRequest(t, method, path, r)
	if isJSON {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, b := s.Do(t, req, auth)
	return resp.StatusCode, b
}

// xff returns headers with X-Forwarded-For set to chain ("" for none).
func xff(chain string) map[string]string {
	if chain == "" {
		return nil
	}
	return map[string]string{"X-Forwarded-For": chain}
}

// auditEvents returns the newest audit events of action (and repo when set).
func auditEvents(t *testing.T, s *Server, action, repo string) []auditEntry {
	t.Helper()
	q := url.Values{"action": {action}, "limit": {"200"}}
	if repo != "" {
		q.Set("repo", repo)
	}
	var page struct {
		Items []auditEntry `json:"items"`
	}
	s.MustAPI(t, http.MethodGet, "/api/audit?"+q.Encode(), nil, s.Admin(), &page)
	return page.Items
}

// signInIP fails one sign-in as a new username with headers hdr and returns the client address
// that the audit log recorded for it.
func signInIP(t *testing.T, s *Server, hdr map[string]string) string {
	t.Helper()
	user := RandName("nobody")
	st, b := apiHeaders(t, s, http.MethodPost, "/api/auth/login", map[string]string{"username": user, "password": "wrong-password"}, nil, hdr)
	if st != http.StatusUnauthorized {
		t.Fatalf("failed sign-in: status %d, want 401: %s", st, b)
	}
	for _, e := range auditEvents(t, s, "signin.failed", "") {
		if e.Actor == user {
			return e.IP
		}
	}
	t.Fatalf("no signin.failed event for %s", user)
	return ""
}

func setTrustedProxies(t *testing.T, s *Server, v any) {
	t.Helper()
	s.MustAPI(t, http.MethodPatch, "/api/settings", map[string]any{"trusted_proxies": v}, s.Admin(), nil)
}

type proxyCandidate struct {
	Address  string    `json:"address"`
	LastSeen time.Time `json:"last_seen"`
	Count    int       `json:"count"`
	Header   string    `json:"header"`
}

// candidateOf returns the proxy candidate with address addr, or nil.
func candidateOf(t *testing.T, s *Server, addr string) *proxyCandidate {
	t.Helper()
	var list []proxyCandidate
	s.MustAPI(t, http.MethodGet, "/api/network/proxy-candidates", nil, s.Admin(), &list)
	for _, c := range list {
		if c.Address == addr {
			return &c
		}
	}
	return nil
}

func TestClientIP(t *testing.T) {
	s := StartServer(t, ServerOpts{})

	peer := signInIP(t, s, nil)
	if peer == "" {
		t.Fatal("the audit log has no address for a request without X-Forwarded-For")
	}
	if c := candidateOf(t, s, peer); c != nil {
		t.Fatalf("the peer is a proxy candidate before it sent a forwarded header: %+v", c)
	}

	// Default: no proxy is trusted. The peer's X-Forwarded-For is ignored, and the server lists
	// the peer as a proxy candidate.
	if ip := signInIP(t, s, xff("203.0.113.7")); ip != peer {
		t.Fatalf("X-Forwarded-For with the default trusted_proxies: audit ip %q, want the peer %s", ip, peer)
	}
	c := candidateOf(t, s, peer)
	if c == nil || c.Count < 1 || c.Header != "X-Forwarded-For" || c.LastSeen.IsZero() {
		t.Fatalf("proxy candidate of the peer %s: %+v, want X-Forwarded-For seen at least once", peer, c)
	}
	if st, _ := s.API(t, http.MethodGet, "/api/network/proxy-candidates", nil, s.AdminBasic()); st != http.StatusUnauthorized {
		t.Fatalf("proxy candidates with admin Basic auth: status %d, want 401", st)
	}
	signInIP(t, s, map[string]string{"Forwarded": "for=203.0.113.7"})
	if c := candidateOf(t, s, peer); c == nil || c.Header != "Forwarded" {
		t.Fatalf("proxy candidate after a Forwarded header: %+v, want header Forwarded", c)
	}
	// "Trust this address" adds the candidate to trusted_proxies; X-Forwarded-For then counts and
	// the peer is no longer a candidate.
	setTrustedProxies(t, s, []string{c.Address})
	if ip := signInIP(t, s, xff("203.0.113.9")); ip != "203.0.113.9" {
		t.Fatalf("X-Forwarded-For after trusting the candidate %s: audit ip %q, want 203.0.113.9", peer, ip)
	}
	if c := candidateOf(t, s, peer); c != nil {
		t.Fatalf("the trusted peer is still a proxy candidate: %+v", c)
	}

	// "private": the peer is a trusted proxy, so X-Forwarded-For gives the client.
	setTrustedProxies(t, s, []string{"private"})
	if ip := signInIP(t, s, xff("203.0.113.7")); ip != "203.0.113.7" {
		t.Fatalf("X-Forwarded-For through a private peer: audit ip %q, want 203.0.113.7", ip)
	}
	// Right to left, skipping trusted hops: the first untrusted address is the client, not the
	// left-most one that the client itself may have forged.
	if ip := signInIP(t, s, xff("198.51.100.9, 203.0.113.8, 10.1.2.3")); ip != "203.0.113.8" {
		t.Fatalf("chain through a private hop: audit ip %q, want 203.0.113.8", ip)
	}
	if ip := signInIP(t, s, map[string]string{"X-Forwarded-For": "203.0.113.20", "Cf-Connecting-Ip": "192.0.2.77"}); ip != "203.0.113.20" {
		t.Fatalf("X-Forwarded-For with CF-Connecting-IP from a non-Cloudflare peer: audit ip %q, want 203.0.113.20", ip)
	}
	if ip := signInIP(t, s, map[string]string{"Cf-Connecting-Ip": "192.0.2.77"}); ip != peer {
		t.Fatalf("CF-Connecting-IP from a non-Cloudflare peer: audit ip %q, want the peer %s", ip, peer)
	}

	// A peer outside trusted_proxies: its X-Forwarded-For is ignored.
	setTrustedProxies(t, s, []string{"192.0.2.0/24"})
	if ip := signInIP(t, s, xff("203.0.113.7")); ip != peer {
		t.Fatalf("X-Forwarded-For from an untrusted peer: audit ip %q, want the peer %s", ip, peer)
	}
	// The peer's own address as an entry.
	setTrustedProxies(t, s, []string{peer})
	if ip := signInIP(t, s, xff("203.0.113.9")); ip != "203.0.113.9" {
		t.Fatalf("X-Forwarded-For from the trusted peer %s: audit ip %q, want 203.0.113.9", peer, ip)
	}
	// No trusted proxies at all.
	setTrustedProxies(t, s, []string{})
	if ip := signInIP(t, s, xff("203.0.113.7")); ip != peer {
		t.Fatalf("X-Forwarded-For without trusted proxies: audit ip %q, want the peer %s", ip, peer)
	}
	// Invalid entries are refused.
	for _, bad := range []string{"10.0.0.0/33", "intranet", "cloudflare", "list:nowhere", "list:Bad Name"} {
		st, b := s.API(t, http.MethodPatch, "/api/settings", map[string]any{"trusted_proxies": []string{bad}}, s.Admin())
		if st != http.StatusBadRequest {
			t.Fatalf("trusted_proxies %q: status %d, want 400: %s", bad, st, b)
		}
	}
	setTrustedProxies(t, s, []string{"private"})
	setTrustedProxies(t, s, nil) // back to the default: no proxy
	if ip := signInIP(t, s, xff("203.0.113.10")); ip != peer {
		t.Fatalf("after reset to the default: audit ip %q, want the peer %s", ip, peer)
	}
}

type ipRule struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
	Note  string `json:"note"`
}

type ipRules struct {
	Read    []ipRule `json:"read"`
	Publish []ipRule `json:"publish"`
}

func putIPRules(t *testing.T, s *Server, repo string, in ipRules) ipRules {
	t.Helper()
	var out ipRules
	s.MustAPI(t, http.MethodPut, "/api/repos/"+repo+"/ip-rules", in, s.Admin(), &out)
	return out
}

// mintFrom mints a device session of user for repo with the given client address.
func mintFrom(t *testing.T, s *Server, user, repo, from string) (int, []byte) {
	t.Helper()
	d := s.device(t, user)
	nonce := s.Challenge(t, d.ID)
	return apiHeaders(t, s, http.MethodPost, "/api/publish/sessions",
		map[string]any{"device_id": d.ID, "repo": repo, "nonce": nonce, "signature": d.Sign(nonce, repo)}, nil, xff(from))
}

func wantDenied(t *testing.T, what string, st int, b []byte, scope string) {
	t.Helper()
	want := "your address is not allowed to " + scope + " this repository"
	if st != http.StatusForbidden || !strings.Contains(string(b), want) {
		t.Fatalf("%s: status %d, want 403 %q: %s", what, st, want, b)
	}
}

func TestIPRules(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	setTrustedProxies(t, s, []string{"private"})
	repo := RandName("raw")
	s.CreateRepo(t, repo, "raw", false, nil)
	w := RandName("writer")
	s.CreateUser(t, w, "writer-pass-1234", false)
	s.SetPermission(t, repo, w, "write")
	body := []byte("ip rules " + randHex(8))
	if st, b := s.API(t, http.MethodPut, "/raw/"+repo+"/doc/file.txt", body, s.PublishAuth(t, w, repo)); st != http.StatusCreated {
		t.Fatalf("PUT before any rule: status %d: %s", st, b)
	}

	// Validation.
	for _, bad := range []ipRules{
		{Read: []ipRule{{Kind: "cidr", Value: "not-a-cidr"}}},
		{Read: []ipRule{{Kind: "list", Value: "nowhere"}}},
		{Read: []ipRule{{Kind: "preset", Value: "github-actions"}}},
		{Publish: []ipRule{{Kind: "dns", Value: "example.com"}}},
		{Read: []ipRule{{Kind: "cidr", Value: "203.0.113.0/24"}, {Kind: "cidr", Value: "203.0.113.9/24"}}},
	} {
		if st, b := s.API(t, http.MethodPut, "/api/repos/"+repo+"/ip-rules", bad, s.Admin()); st != http.StatusBadRequest {
			t.Fatalf("PUT ip-rules %+v: status %d, want 400: %s", bad, st, b)
		}
	}
	got := putIPRules(t, s, repo, ipRules{
		Read:    []ipRule{{Kind: "cidr", Value: "203.0.113.9/24", Note: "office"}, {Kind: "cidr", Value: "198.51.100.4"}},
		Publish: []ipRule{{Kind: "cidr", Value: "198.51.100.0/24", Note: "CI"}},
	})
	if len(got.Read) != 2 || got.Read[0].Value != "203.0.113.0/24" || got.Read[0].Note != "office" || got.Read[1].Value != "198.51.100.4/32" ||
		len(got.Publish) != 1 || got.Publish[0].Value != "198.51.100.0/24" {
		t.Fatalf("PUT ip-rules answer: %+v", got)
	}
	var read ipRules
	s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/ip-rules", nil, s.Admin(), &read)
	if len(read.Read) != 2 || len(read.Publish) != 1 {
		t.Fatalf("GET ip-rules: %+v", read)
	}
	if st, _ := s.API(t, http.MethodGet, "/api/repos/"+repo+"/ip-rules", nil, s.SignIn(t, w, "writer-pass-1234")); st != http.StatusForbidden {
		t.Fatalf("GET ip-rules as a non-admin: status %d, want 403", st)
	}

	// Read rules: format route and repo read API, for the admin too.
	path := "/raw/" + repo + "/doc/file.txt"
	if st, b := apiHeaders(t, s, http.MethodGet, path, nil, s.AdminBasic(), xff("203.0.113.5")); st != http.StatusOK || !bytes.Equal(b, body) {
		t.Fatalf("GET from an allowed address: status %d", st)
	}
	st, b := apiHeaders(t, s, http.MethodGet, path, nil, s.AdminBasic(), xff("192.0.2.1"))
	wantDenied(t, "GET from a refused address as admin", st, b, "read")
	st, b = apiHeaders(t, s, http.MethodGet, path, nil, s.AdminBasic(), nil)
	wantDenied(t, "GET from the private peer (not listed)", st, b, "read")
	tree := "/api/repos/" + repo + "/tree?path=doc/"
	st, b = apiHeaders(t, s, http.MethodGet, tree, nil, s.Admin(), xff("192.0.2.1"))
	wantDenied(t, "tree from a refused address with the admin UI session", st, b, "read")
	if st, b := apiHeaders(t, s, http.MethodGet, tree, nil, s.Admin(), xff("198.51.100.4")); st != http.StatusOK {
		t.Fatalf("tree from an allowed address: status %d: %s", st, b)
	}
	for _, p := range []string{"/api/repos/" + repo + "/assets", "/api/repos/" + repo + "/usage", "/api/repos/" + repo + "/status"} {
		st, b := apiHeaders(t, s, http.MethodGet, p, nil, s.Admin(), xff("192.0.2.1"))
		wantDenied(t, "GET "+p+" from a refused address", st, b, "read")
	}
	// The repo record itself stays readable, so an admin can change the rules.
	if st, b := apiHeaders(t, s, http.MethodGet, "/api/repos/"+repo, nil, s.Admin(), xff("192.0.2.1")); st != http.StatusOK {
		t.Fatalf("GET the repo record from a refused address: status %d: %s", st, b)
	}
	// Search leaves the repo out for a refused address.
	searchHits := func(from string) int {
		var page struct {
			Items []struct {
				Repo string `json:"repo"`
			} `json:"items"`
		}
		st, b := apiHeaders(t, s, http.MethodGet, "/api/search?q=file.txt", nil, s.Admin(), xff(from))
		if st != http.StatusOK || json.Unmarshal(b, &page) != nil {
			t.Fatalf("search from %s: status %d: %s", from, st, b)
		}
		n := 0
		for _, it := range page.Items {
			if it.Repo == repo {
				n++
			}
		}
		return n
	}
	if n := searchHits("203.0.113.5"); n != 1 {
		t.Fatalf("search from an allowed address: %d hits in %s, want 1", n, repo)
	}
	if n := searchHits("192.0.2.1"); n != 0 {
		t.Fatalf("search from a refused address: %d hits in %s, want 0", n, repo)
	}

	// Publish rules: minting a session, uploads and deletes.
	st, b = mintFrom(t, s, w, repo, "192.0.2.1")
	wantDenied(t, "mint a device session from a refused address", st, b, "publish")
	st, b = mintFrom(t, s, w, repo, "198.51.100.4")
	sess := mustSession(t, "mint from an allowed address")(st, b)
	st, b = apiHeaders(t, s, http.MethodPut, "/raw/"+repo+"/doc/two.txt", []byte("two"), sess.Auth(), xff("192.0.2.1"))
	wantDenied(t, "PUT from a refused address with a session", st, b, "publish")
	if st, b := apiHeaders(t, s, http.MethodPut, "/raw/"+repo+"/doc/two.txt", []byte("two"), sess.Auth(), xff("198.51.100.4")); st != http.StatusCreated {
		t.Fatalf("PUT from an allowed address: status %d: %s", st, b)
	}
	st, b = apiHeaders(t, s, http.MethodPost, "/api/repos/"+repo+"/files/delete",
		map[string][]string{"paths": {"doc/two.txt"}}, s.Admin(), xff("192.0.2.1"))
	wantDenied(t, "admin delete from a refused address", st, b, "publish")
	st, b = apiHeaders(t, s, http.MethodPost, "/api/publish/sessions/manual",
		map[string]string{"repo": repo, "totp_code": s.TOTPCode(t, w)}, s.LoginTOTP(t, w), xff("192.0.2.1"))
	wantDenied(t, "manual session from a refused address", st, b, "publish")

	// Every refusal is in the audit log with the address and the scope.
	events := auditEvents(t, s, "ip.denied", repo)
	scopes := map[string]bool{}
	for _, e := range events {
		if e.IP == "192.0.2.1" {
			scopes[fmt.Sprint(e.Detail["scope"])] = true
		}
	}
	if !scopes["read"] || !scopes["publish"] {
		t.Fatalf("ip.denied events of %s: %+v, want read and publish refusals from 192.0.2.1", repo, events)
	}

	// Empty lists lift the restriction.
	putIPRules(t, s, repo, ipRules{Read: []ipRule{}, Publish: []ipRule{}})
	if st, _ := apiHeaders(t, s, http.MethodGet, path, nil, s.AdminBasic(), xff("192.0.2.1")); st != http.StatusOK {
		t.Fatalf("GET after the rules were cleared: status %d", st)
	}
}

// ipListJSON is an IP list as the admin API returns it.
type ipListJSON struct {
	Name        string     `json:"name"`
	Kind        string     `json:"kind"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Count       int        `json:"count"`
	Ranges      []string   `json:"ranges"`
	FetchedAt   *time.Time `json:"fetched_at"`
	Error       string     `json:"error"`
	UsedBy      struct {
		Repos []struct {
			Repo  string `json:"repo"`
			Scope string `json:"scope"`
		} `json:"repos"`
		TrustedProxies bool `json:"trusted_proxies"`
	} `json:"used_by"`
}

func ipListsOf(t *testing.T, s *Server) map[string]ipListJSON {
	t.Helper()
	var list []ipListJSON
	s.MustAPI(t, http.MethodGet, "/api/ip-lists", nil, s.Admin(), &list)
	out := map[string]ipListJSON{}
	for _, l := range list {
		out[l.Name] = l
	}
	return out
}

func getIPList(t *testing.T, s *Server, name string) ipListJSON {
	t.Helper()
	var l ipListJSON
	s.MustAPI(t, http.MethodGet, "/api/ip-lists/"+name, nil, s.Admin(), &l)
	return l
}

// TestPresetIPLists serves the preset lists from a stub: a second server with an anonymous raw
// repo, at the paths that TR_E2E_PRESET_BASE_URL points at.
func TestPresetIPLists(t *testing.T) {
	stub := StartServer(t, ServerOpts{})
	ranges := RandName("ranges")
	stub.CreateRepo(t, ranges, "raw", true, map[string]any{"overwrite": "allow"})
	pub := RandName("pub")
	stub.CreateUser(t, pub, "pub-pass-1234", false)
	stub.SetPermission(t, ranges, pub, "write")
	put := func(name, body string) {
		t.Helper()
		if st, b := stub.API(t, http.MethodPut, "/raw/"+ranges+"/"+name, body, stub.PublishAuth(t, pub, ranges)); st/100 != 2 {
			t.Fatalf("stub PUT %s: status %d: %s", name, st, b)
		}
	}
	put("ips-v4", "203.0.113.0/24\n")
	put("ips-v6", "2001:db8::/32\n")
	put("meta", `{"actions":["198.51.100.0/24"],"hooks":["192.0.2.0/24"]}`)

	s := StartServer(t, ServerOpts{Env: map[string]string{"TR_E2E_PRESET_BASE_URL": stub.InternalURL + "/raw/" + ranges}})
	setTrustedProxies(t, s, []string{"private"})
	deadline := time.Now().Add(60 * time.Second)
	for {
		ls := ipListsOf(t, s)
		if ls["cloudflare"].Count == 2 && ls["github-actions"].Count == 1 && ls["cloudflare"].FetchedAt != nil {
			if ls["cloudflare"].Kind != "preset" || ls["cloudflare"].Ranges != nil {
				t.Fatalf("GET /api/ip-lists: cloudflare %+v, want a preset without its ranges", ls["cloudflare"])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("IP lists after startup: %+v, want cloudflare 2 and github-actions 1 ranges", ls)
		}
		time.Sleep(500 * time.Millisecond)
	}
	if l := getIPList(t, s, "github-actions"); !slices.Equal(l.Ranges, []string{"198.51.100.0/24"}) {
		t.Fatalf("GET /api/ip-lists/github-actions: %+v", l)
	}
	if st, _ := s.API(t, http.MethodPost, "/api/ip-lists/nowhere/refresh", nil, s.Admin()); st != http.StatusNotFound {
		t.Fatalf("refresh of an unknown list: status %d, want 404", st)
	}
	// A preset is read-only.
	if st, b := s.API(t, http.MethodPut, "/api/ip-lists/github-actions", map[string]any{"ranges": []string{"10.0.0.0/8"}}, s.Admin()); st != http.StatusBadRequest {
		t.Fatalf("PUT a preset list: status %d, want 400: %s", st, b)
	}
	if st, b := s.API(t, http.MethodDelete, "/api/ip-lists/github-actions", nil, s.Admin()); st != http.StatusBadRequest {
		t.Fatalf("DELETE a preset list: status %d, want 400: %s", st, b)
	}

	// A preset in a repo's read rules.
	repo := RandName("raw")
	s.CreateRepo(t, repo, "raw", true, nil)
	putIPRules(t, s, repo, ipRules{Read: []ipRule{{Kind: "list", Value: "github-actions"}}})
	if st, _ := apiHeaders(t, s, http.MethodGet, "/raw/"+repo+"/missing", nil, nil, xff("198.51.100.7")); st != http.StatusNotFound {
		t.Fatalf("GET from a GitHub Actions address: status %d, want 404 (allowed, no file)", st)
	}
	st, b := apiHeaders(t, s, http.MethodGet, "/raw/"+repo+"/missing", nil, nil, xff("203.0.113.7"))
	wantDenied(t, "anonymous GET from outside the list", st, b, "read")

	// trusted_proxies "list:cloudflare": a Cloudflare hop in the chain is skipped.
	setTrustedProxies(t, s, []string{"private", "list:cloudflare"})
	if ip := signInIP(t, s, xff("192.0.2.50, 203.0.113.9")); ip != "192.0.2.50" {
		t.Fatalf("chain through a Cloudflare hop: audit ip %q, want 192.0.2.50", ip)
	}
	// Cloudflare in front of Caddy, with no Caddy setting: Caddy's X-Forwarded-For holds only the
	// Cloudflare edge, and CF-Connecting-IP, passed on unchanged, gives the visitor.
	if ip := signInIP(t, s, map[string]string{"X-Forwarded-For": "203.0.113.9", "Cf-Connecting-Ip": "192.0.2.51"}); ip != "192.0.2.51" {
		t.Fatalf("Cloudflare in front of Caddy: audit ip %q, want 192.0.2.51", ip)
	}
	// A request that came to Caddy past Cloudflare: its CF-Connecting-IP is ignored.
	if ip := signInIP(t, s, map[string]string{"X-Forwarded-For": "198.51.100.40", "Cf-Connecting-Ip": "192.0.2.52"}); ip != "198.51.100.40" {
		t.Fatalf("CF-Connecting-IP past Cloudflare: audit ip %q, want 198.51.100.40", ip)
	}
	if l := getIPList(t, s, "cloudflare"); !l.UsedBy.TrustedProxies {
		t.Fatalf("cloudflare used_by: %+v, want trusted_proxies", l.UsedBy)
	}
	setTrustedProxies(t, s, []string{"private"})
	if ip := signInIP(t, s, xff("192.0.2.50, 203.0.113.9")); ip != "203.0.113.9" {
		t.Fatalf("chain without trusting Cloudflare: audit ip %q, want 203.0.113.9", ip)
	}

	// A failed refresh keeps the last good list and records the error.
	put("meta", "this is not JSON")
	st, b = s.API(t, http.MethodPost, "/api/ip-lists/github-actions/refresh", nil, s.Admin())
	if st != http.StatusBadGateway {
		t.Fatalf("refresh of a broken list: status %d, want 502: %s", st, b)
	}
	if l := ipListsOf(t, s)["github-actions"]; l.Count != 1 || l.Error == "" {
		t.Fatalf("github-actions after a failed refresh: %+v, want the last good list and an error", l)
	}
	if st, _ := apiHeaders(t, s, http.MethodGet, "/raw/"+repo+"/missing", nil, nil, xff("198.51.100.7")); st != http.StatusNotFound {
		t.Fatalf("GET from a GitHub Actions address after a failed refresh: status %d, want 404", st)
	}
	put("meta", `{"actions":["198.51.100.0/24","192.0.2.128/25"]}`)
	var l ipListJSON
	s.MustAPI(t, http.MethodPost, "/api/ip-lists/github-actions/refresh", nil, s.Admin(), &l)
	if l.Count != 2 || l.Error != "" {
		t.Fatalf("refresh after the fix: %+v, want 2 ranges and no error", l)
	}

	// A restart without the stub works from the stored lists.
	if err := stub.Terminate(t.Context()); err != nil {
		t.Fatalf("stop the stub: %v", err)
	}
	s.Restart(t)
	if ls := ipListsOf(t, s); ls["github-actions"].Count != 2 || ls["cloudflare"].Count != 2 {
		t.Fatalf("IP lists after a restart without the stub: %+v", ls)
	}
	if st, _ := apiHeaders(t, s, http.MethodGet, "/raw/"+repo+"/missing", nil, nil, xff("192.0.2.200")); st != http.StatusNotFound {
		t.Fatalf("GET from a refreshed range after a restart: status %d, want 404", st)
	}
}

// TestCustomIPLists covers the custom lists: create, edit, reference from repo rules and
// trusted_proxies, rename with the references, and delete refused while in use.
func TestCustomIPLists(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	setTrustedProxies(t, s, []string{"private"})

	// Create: validation, then a list from scratch.
	for _, bad := range []map[string]any{
		{"name": "Bad Name"},
		{"name": "office", "ranges": []string{"not-a-cidr"}},
		{"name": "office", "ranges": []string{"203.0.113.0/24", "203.0.113.9/24"}},
	} {
		if st, b := s.API(t, http.MethodPost, "/api/ip-lists", bad, s.Admin()); st != http.StatusBadRequest {
			t.Fatalf("POST /api/ip-lists %v: status %d, want 400: %s", bad, st, b)
		}
	}
	var office ipListJSON
	st, b := s.API(t, http.MethodPost, "/api/ip-lists", map[string]any{"name": "office", "title": "Office", "ranges": []string{"203.0.113.9/24", "198.51.100.4"}}, s.Admin())
	if st != http.StatusCreated || json.Unmarshal(b, &office) != nil {
		t.Fatalf("create list office: status %d: %s", st, b)
	}
	if office.Kind != "custom" || office.Title != "Office" || !slices.Equal(office.Ranges, []string{"203.0.113.0/24", "198.51.100.4/32"}) {
		t.Fatalf("created list office: %+v", office)
	}
	if st, b := s.API(t, http.MethodPost, "/api/ip-lists", map[string]any{"name": "office"}, s.Admin()); st != http.StatusConflict {
		t.Fatalf("create list office again: status %d, want 409: %s", st, b)
	}
	if st, b := s.API(t, http.MethodPost, "/api/ip-lists", map[string]any{"name": "cloudflare"}, s.Admin()); st != http.StatusConflict {
		t.Fatalf("create a list with a preset's name: status %d, want 409: %s", st, b)
	}

	// A list with a description; the title defaults to the name.
	var runners ipListJSON
	s.MustAPI(t, http.MethodPost, "/api/ip-lists", map[string]any{"name": "runners", "description": "CI runners", "ranges": []string{"192.0.2.10"}}, s.Admin(), &runners)
	if runners.Title != "runners" || runners.Description != "CI runners" || !slices.Equal(runners.Ranges, []string{"192.0.2.10/32"}) {
		t.Fatalf("created list runners: %+v", runners)
	}

	// Edit.
	var edited ipListJSON
	s.MustAPI(t, http.MethodPut, "/api/ip-lists/runners", map[string]any{"ranges": []string{"192.0.2.10", "192.0.2.11"}}, s.Admin(), &edited)
	if edited.Count != 2 || edited.Title != "runners" {
		t.Fatalf("edited list runners: %+v", edited)
	}
	if st, b := s.API(t, http.MethodPut, "/api/ip-lists/runners", map[string]any{"name": "office"}, s.Admin()); st != http.StatusConflict {
		t.Fatalf("rename onto an existing list: status %d, want 409: %s", st, b)
	}
	if st, _ := s.API(t, http.MethodPut, "/api/ip-lists/nowhere", map[string]any{"title": "x"}, s.Admin()); st != http.StatusNotFound {
		t.Fatalf("PUT an unknown list: status %d, want 404", st)
	}

	// Reference the list from a repo's publish rules: only its addresses mint.
	repo := RandName("raw")
	s.CreateRepo(t, repo, "raw", false, nil)
	w := RandName("writer")
	s.CreateUser(t, w, "writer-pass-1234", false)
	s.SetPermission(t, repo, w, "write")
	got := putIPRules(t, s, repo, ipRules{Publish: []ipRule{{Kind: "list", Value: "runners", Note: "CI"}}})
	if len(got.Publish) != 1 || got.Publish[0].Kind != "list" || got.Publish[0].Value != "runners" {
		t.Fatalf("PUT ip-rules with a list: %+v", got)
	}
	st, b = mintFrom(t, s, w, repo, "203.0.113.5")
	wantDenied(t, "mint from outside the list", st, b, "publish")
	mustSession(t, "mint from a runner address")(mintFrom(t, s, w, repo, "192.0.2.11"))

	// Reference it from trusted_proxies too.
	setTrustedProxies(t, s, []string{"private", "list:runners"})
	l := getIPList(t, s, "runners")
	if len(l.UsedBy.Repos) != 1 || l.UsedBy.Repos[0].Repo != repo || l.UsedBy.Repos[0].Scope != "publish" || !l.UsedBy.TrustedProxies {
		t.Fatalf("runners used_by: %+v", l.UsedBy)
	}

	// Delete is refused while it is used.
	st, b = s.API(t, http.MethodDelete, "/api/ip-lists/runners", nil, s.Admin())
	if st != http.StatusConflict || !strings.Contains(string(b), repo) {
		t.Fatalf("delete a list in use: status %d, want 409 naming %s: %s", st, repo, b)
	}

	// A rename carries the repo rules and trusted_proxies along.
	s.MustAPI(t, http.MethodPut, "/api/ip-lists/runners", map[string]any{"name": "ci-runners"}, s.Admin(), nil)
	if st, _ := s.API(t, http.MethodGet, "/api/ip-lists/runners", nil, s.Admin()); st != http.StatusNotFound {
		t.Fatalf("GET the old name after a rename: status %d, want 404", st)
	}
	var rules ipRules
	s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/ip-rules", nil, s.Admin(), &rules)
	if len(rules.Publish) != 1 || rules.Publish[0].Value != "ci-runners" || rules.Publish[0].Note != "CI" {
		t.Fatalf("ip-rules after the rename: %+v", rules)
	}
	items, _ := listSettings(t, s)
	if v := string(items["trusted_proxies"].Value); !strings.Contains(v, `"list:ci-runners"`) || strings.Contains(v, `"list:runners"`) {
		t.Fatalf("trusted_proxies after the rename: %s", v)
	}
	mustSession(t, "mint from a runner address after the rename")(mintFrom(t, s, w, repo, "192.0.2.10"))

	// Unused: delete works.
	putIPRules(t, s, repo, ipRules{Read: []ipRule{}, Publish: []ipRule{}})
	setTrustedProxies(t, s, []string{"private"})
	if st, b := s.API(t, http.MethodDelete, "/api/ip-lists/ci-runners", nil, s.Admin()); st != http.StatusNoContent {
		t.Fatalf("delete an unused list: status %d, want 204: %s", st, b)
	}
	if _, ok := ipListsOf(t, s)["ci-runners"]; ok {
		t.Fatal("GET /api/ip-lists still lists the deleted list")
	}
	if st, b := s.API(t, http.MethodPut, "/api/repos/"+repo+"/ip-rules", ipRules{Read: []ipRule{{Kind: "list", Value: "ci-runners"}}}, s.Admin()); st != http.StatusBadRequest {
		t.Fatalf("a rule naming a deleted list: status %d, want 400: %s", st, b)
	}

	// Admins only.
	if st, _ := s.API(t, http.MethodGet, "/api/ip-lists", nil, s.SignIn(t, w, "writer-pass-1234")); st != http.StatusForbidden {
		t.Fatalf("non-admin GET /api/ip-lists: status %d, want 403", st)
	}
	if st, _ := s.API(t, http.MethodGet, "/api/ip-lists", nil, s.AdminBasic()); st != http.StatusUnauthorized {
		t.Fatalf("GET /api/ip-lists with admin Basic auth: status %d, want 401", st)
	}
}

// parsedRanges is the answer of POST /api/ip-ranges/parse.
type parsedRanges struct {
	Ranges  []string `json:"ranges"`
	Invalid []struct {
		Line   int    `json:"line"`
		Text   string `json:"text"`
		Reason string `json:"reason"`
	} `json:"invalid"`
	Duplicates int `json:"duplicates"`
}

// TestIPRangeParse covers POST /api/ip-ranges/parse (spec §13.3): addresses and CIDRs pulled out
// of pasted text in any common layout, invalid lines with their numbers, duplicates, limits and
// admin-only access.
func TestIPRangeParse(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	parse := func(t *testing.T, text string) parsedRanges {
		t.Helper()
		var out parsedRanges
		s.MustAPI(t, http.MethodPost, "/api/ip-ranges/parse", map[string]any{"text": text}, s.Admin(), &out)
		return out
	}
	check := func(t *testing.T, text string, want []string, dups int) {
		t.Helper()
		got := parse(t, text)
		if !slices.Equal(got.Ranges, want) || len(got.Invalid) != 0 || got.Duplicates != dups {
			t.Fatalf("parse %q: %+v, want ranges %v, no invalid lines, %d duplicates", text, got, want, dups)
		}
	}

	t.Run("owner example", func(t *testing.T) {
		check(t, "34.107.78.182\n63.181.86.27\n63.178.35.73\n63.181.52.205\n18.194.216.61\n3.76.89.160\n3.40.7.192/28\n3.40.7.224/28\n",
			[]string{"34.107.78.182/32", "63.181.86.27/32", "63.178.35.73/32", "63.181.52.205/32", "18.194.216.61/32",
				"3.76.89.160/32", "3.40.7.192/28", "3.40.7.224/28"}, 0)
	})
	t.Run("separators", func(t *testing.T) {
		check(t, "192.0.2.1, 192.0.2.2 192.0.2.3\t192.0.2.4; 192.0.2.5 | 192.0.2.6\r\n",
			[]string{"192.0.2.1/32", "192.0.2.2/32", "192.0.2.3/32", "192.0.2.4/32", "192.0.2.5/32", "192.0.2.6/32"}, 0)
	})
	t.Run("csv with a header and a label column", func(t *testing.T) {
		check(t, "name,address,note\nrunner-1,192.0.2.10,primary\n\"runner-2\",\"192.0.2.11\",\"backup, second\"\n",
			[]string{"192.0.2.10/32", "192.0.2.11/32"}, 0)
	})
	t.Run("table", func(t *testing.T) {
		check(t, "| host | ipv4 |\n|------|------|\n| a | 198.51.100.1 |\n| b | 198.51.100.2 |\n",
			[]string{"198.51.100.1/32", "198.51.100.2/32"}, 0)
	})
	t.Run("comments labels and quotes", func(t *testing.T) {
		check(t, "# office\n203.0.113.7 # the router\n// old runners\nrunner-1: 3.76.89.160\nip=198.51.100.9\n'192.0.2.50'\n[2001:db8::1]\n\"10.0.0.0/8\",\n",
			[]string{"203.0.113.7/32", "3.76.89.160/32", "198.51.100.9/32", "192.0.2.50/32", "2001:db8::1/128", "10.0.0.0/8"}, 0)
	})
	t.Run("ipv6 and ipv4-mapped ipv6", func(t *testing.T) {
		check(t, "2001:db8::/32\n::ffff:192.0.2.1\n::ffff:198.51.100.0/120\n2001:DB8:0:0::5\n",
			[]string{"2001:db8::/32", "192.0.2.1/32", "198.51.100.0/24", "2001:db8::5/128"}, 0)
	})
	t.Run("unmasked cidr", func(t *testing.T) {
		check(t, "3.40.7.200/28\n2001:db8::1/32", []string{"3.40.7.192/28", "2001:db8::/32"}, 0)
	})
	t.Run("duplicates", func(t *testing.T) {
		check(t, "192.0.2.1\n192.0.2.1/32\n192.0.2.1, 192.0.2.2\n::ffff:192.0.2.2", []string{"192.0.2.1/32", "192.0.2.2/32"}, 3)
	})
	t.Run("invalid lines", func(t *testing.T) {
		got := parse(t, "192.0.2.1\nhello world\n300.1.2.3\n\n192.0.2.0/33\n192.0.2.2 and some words")
		if !slices.Equal(got.Ranges, []string{"192.0.2.1/32", "192.0.2.2/32"}) || got.Duplicates != 0 {
			t.Fatalf("parse with invalid lines: %+v", got)
		}
		want := []struct {
			line int
			text string
		}{{2, "hello world"}, {3, "300.1.2.3"}, {5, "192.0.2.0/33"}}
		if len(got.Invalid) != len(want) {
			t.Fatalf("invalid lines: %+v, want lines 2, 3 and 5", got.Invalid)
		}
		for i, w := range want {
			if g := got.Invalid[i]; g.Line != w.line || g.Text != w.text || g.Reason == "" {
				t.Fatalf("invalid line %d: %+v, want line %d %q with a reason", i, g, w.line, w.text)
			}
		}
	})
	t.Run("empty", func(t *testing.T) {
		st, b := s.API(t, http.MethodPost, "/api/ip-ranges/parse", map[string]any{"text": " \n# nothing\n"}, s.Admin())
		if st != http.StatusOK || !strings.Contains(string(b), `"ranges":[]`) || !strings.Contains(string(b), `"invalid":[]`) {
			t.Fatalf("parse empty text: status %d: %s", st, b)
		}
	})
	t.Run("too large", func(t *testing.T) {
		text := strings.Repeat("192.0.2.1\n", (1<<20)/10+1)
		if st, b := s.API(t, http.MethodPost, "/api/ip-ranges/parse", map[string]any{"text": text}, s.Admin()); st != http.StatusBadRequest {
			t.Fatalf("parse more than 1 MiB: status %d, want 400: %s", st, b)
		}
	})
	t.Run("admins only", func(t *testing.T) {
		u := RandName("plain")
		s.CreateUser(t, u, "plain-pass-1234", false)
		body := map[string]any{"text": "192.0.2.1"}
		if st, _ := s.API(t, http.MethodPost, "/api/ip-ranges/parse", body, s.SignIn(t, u, "plain-pass-1234")); st != http.StatusForbidden {
			t.Fatalf("non-admin parse: status %d, want 403", st)
		}
		if st, _ := s.API(t, http.MethodPost, "/api/ip-ranges/parse", body, s.AdminBasic()); st != http.StatusUnauthorized {
			t.Fatalf("parse with admin Basic auth: status %d, want 401", st)
		}
		if st, _ := s.API(t, http.MethodPost, "/api/ip-ranges/parse", body, nil); st != http.StatusUnauthorized {
			t.Fatalf("anonymous parse: status %d, want 401", st)
		}
	})
}
