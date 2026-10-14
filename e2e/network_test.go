//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	for _, bad := range []string{"10.0.0.0/33", "intranet"} {
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
		{Read: []ipRule{{Kind: "preset", Value: "nowhere"}}},
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
	st, b = apiHeaders(t, s, http.MethodDelete, "/api/repos/"+repo+"/files?path=doc/two.txt", nil, s.Admin(), xff("192.0.2.1"))
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

type presetJSON struct {
	Name      string     `json:"name"`
	Count     int        `json:"count"`
	FetchedAt *time.Time `json:"fetched_at"`
	Error     string     `json:"error"`
}

func presetsOf(t *testing.T, s *Server) map[string]presetJSON {
	t.Helper()
	var list []presetJSON
	s.MustAPI(t, http.MethodGet, "/api/network/presets", nil, s.Admin(), &list)
	out := map[string]presetJSON{}
	for _, p := range list {
		out[p.Name] = p
	}
	return out
}

// TestPresets serves the preset lists from a stub: a second server with an anonymous raw repo,
// at the paths that TR_E2E_PRESET_BASE_URL points at.
func TestPresets(t *testing.T) {
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
		ps := presetsOf(t, s)
		if ps["cloudflare"].Count == 2 && ps["github-actions"].Count == 1 && ps["cloudflare"].FetchedAt != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("presets after startup: %+v, want cloudflare 2 and github-actions 1 ranges", ps)
		}
		time.Sleep(500 * time.Millisecond)
	}
	if st, _ := s.API(t, http.MethodPost, "/api/network/presets/nowhere/refresh", nil, s.Admin()); st != http.StatusNotFound {
		t.Fatalf("refresh of an unknown preset: status %d, want 404", st)
	}

	// A preset in a repo's read rules.
	repo := RandName("raw")
	s.CreateRepo(t, repo, "raw", true, nil)
	putIPRules(t, s, repo, ipRules{Read: []ipRule{{Kind: "preset", Value: "github-actions"}}})
	if st, _ := apiHeaders(t, s, http.MethodGet, "/raw/"+repo+"/missing", nil, nil, xff("198.51.100.7")); st != http.StatusNotFound {
		t.Fatalf("GET from a GitHub Actions address: status %d, want 404 (allowed, no file)", st)
	}
	st, b := apiHeaders(t, s, http.MethodGet, "/raw/"+repo+"/missing", nil, nil, xff("203.0.113.7"))
	wantDenied(t, "anonymous GET from outside the preset", st, b, "read")

	// trusted_proxies "cloudflare": a Cloudflare hop in the chain is skipped.
	setTrustedProxies(t, s, []string{"private", "cloudflare"})
	if ip := signInIP(t, s, xff("192.0.2.50, 203.0.113.9")); ip != "192.0.2.50" {
		t.Fatalf("chain through a Cloudflare hop: audit ip %q, want 192.0.2.50", ip)
	}
	setTrustedProxies(t, s, []string{"private"})
	if ip := signInIP(t, s, xff("192.0.2.50, 203.0.113.9")); ip != "203.0.113.9" {
		t.Fatalf("chain without trusting Cloudflare: audit ip %q, want 203.0.113.9", ip)
	}

	// A failed refresh keeps the last good list and records the error.
	put("meta", "this is not JSON")
	st, b = s.API(t, http.MethodPost, "/api/network/presets/github-actions/refresh", nil, s.Admin())
	if st != http.StatusBadGateway {
		t.Fatalf("refresh of a broken list: status %d, want 502: %s", st, b)
	}
	if p := presetsOf(t, s)["github-actions"]; p.Count != 1 || p.Error == "" {
		t.Fatalf("github-actions after a failed refresh: %+v, want the last good list and an error", p)
	}
	if st, _ := apiHeaders(t, s, http.MethodGet, "/raw/"+repo+"/missing", nil, nil, xff("198.51.100.7")); st != http.StatusNotFound {
		t.Fatalf("GET from a GitHub Actions address after a failed refresh: status %d, want 404", st)
	}
	put("meta", `{"actions":["198.51.100.0/24","192.0.2.128/25"]}`)
	var p presetJSON
	s.MustAPI(t, http.MethodPost, "/api/network/presets/github-actions/refresh", nil, s.Admin(), &p)
	if p.Count != 2 || p.Error != "" {
		t.Fatalf("refresh after the fix: %+v, want 2 ranges and no error", p)
	}

	// A restart without the stub works from the stored lists.
	if err := stub.Terminate(t.Context()); err != nil {
		t.Fatalf("stop the stub: %v", err)
	}
	s.Restart(t)
	if ps := presetsOf(t, s); ps["github-actions"].Count != 2 || ps["cloudflare"].Count != 2 {
		t.Fatalf("presets after a restart without the stub: %+v", ps)
	}
	if st, _ := apiHeaders(t, s, http.MethodGet, "/raw/"+repo+"/missing", nil, nil, xff("192.0.2.200")); st != http.StatusNotFound {
		t.Fatalf("GET from a refreshed range after a restart: status %d, want 404", st)
	}
}
