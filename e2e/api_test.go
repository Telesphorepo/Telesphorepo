//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func apiJSON(t testing.TB, s *Server, method, path string, body any, auth Auth, want int, out any) {
	t.Helper()
	st, b := s.API(t, method, path, body, auth)
	if st != want {
		t.Fatalf("%s %s: status %d, want %d: %s", method, path, st, want, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s %s: decode %s: %v", method, path, b, err)
		}
	}
}

func apiWant(t testing.TB, s *Server, method, path string, body any, auth Auth, want int) {
	t.Helper()
	apiJSON(t, s, method, path, body, auth, want, nil)
}

func apiPostTokenWith(t testing.TB, s *Server, sess *Session, hdr map[string]string) (int, []byte) {
	t.Helper()
	req := s.NewRequest(t, http.MethodPost, "/api/tokens", strings.NewReader(`{"name":"csrf"}`))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, b := s.Do(t, req, sess)
	return resp.StatusCode, b
}

// apiMavenPut uploads body as the admin with a publish session for repo.
func apiMavenPut(t testing.TB, s *Server, repo, path, body string) {
	t.Helper()
	req := s.NewRequest(t, http.MethodPut, "/maven/"+repo+"/"+path, strings.NewReader(body))
	resp, b := s.Do(t, req, s.PublishAuth(t, s.AdminUser, repo))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT %s: status %d: %s", path, resp.StatusCode, b)
	}
}

func TestAPISession(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	s.CreateUser(t, "alice", "alice-password-1", false)

	apiWant(t, s, http.MethodPost, "/api/auth/login", map[string]string{"username": "alice", "password": "wrong"}, nil, http.StatusUnauthorized)

	req := s.NewRequest(t, http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"alice","password":"alice-password-1"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, b := s.Do(t, req, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d %s", resp.StatusCode, b)
	}
	var sc *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "tr_session" {
			sc = c
		}
	}
	if sc == nil {
		t.Fatal("login did not set tr_session")
	}
	if !sc.HttpOnly || sc.SameSite != http.SameSiteStrictMode || sc.Secure || sc.Value == "" {
		t.Fatalf("bad cookie attributes: %+v", sc)
	}
	var u struct {
		Username string `json:"username"`
		IsAdmin  bool   `json:"is_admin"`
	}
	if err := json.Unmarshal(b, &u); err != nil || u.Username != "alice" {
		t.Fatalf("login body: %s", b)
	}

	sess := s.Login(t, "alice", "alice-password-1")
	apiJSON(t, s, http.MethodGet, "/api/me", nil, sess, 200, &u)
	if u.Username != "alice" || u.IsAdmin {
		t.Fatalf("me: %+v", u)
	}
	apiWant(t, s, http.MethodGet, "/api/me", nil, nil, http.StatusUnauthorized)

	apiWant(t, s, http.MethodPost, "/api/auth/logout", nil, sess, http.StatusNoContent)
	apiWant(t, s, http.MethodGet, "/api/me", nil, sess, http.StatusUnauthorized)
}

func TestAPICSRF(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	s.CreateUser(t, "alice", "alice-password-1", false)
	sess := s.LoginTOTP(t, "alice")

	st, b := apiPostTokenWith(t, s, sess, map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"})
	if st != http.StatusForbidden {
		t.Fatalf("cross-site: %d %s", st, b)
	}
	st, b = apiPostTokenWith(t, s, sess, map[string]string{"Sec-Fetch-Site": "cross-site"})
	if st != http.StatusForbidden {
		t.Fatalf("cross-site (no origin): %d %s", st, b)
	}
	st, b = apiPostTokenWith(t, s, sess, map[string]string{"Sec-Fetch-Site": "same-origin"})
	if st != http.StatusCreated {
		t.Fatalf("same-origin: %d %s", st, b)
	}
}

func TestAPITokens(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	s.CreateUser(t, "alice", "alice-password-1", false)
	s.CreateUser(t, "bob", "bob-password-1", false)
	alice, bob := Basic{"alice", "alice-password-1"}, Basic{"bob", "bob-password-1"}
	aliceUI, bobUI := s.SignIn(t, "alice", "alice-password-1"), s.SignIn(t, "bob", "bob-password-1")

	var tok struct {
		ID    any    `json:"id"`
		Name  string `json:"name"`
		Token string `json:"token"`
	}
	apiJSON(t, s, http.MethodPost, "/api/tokens", map[string]any{"name": "ci"}, aliceUI, http.StatusCreated, &tok)
	if !strings.HasPrefix(tok.Token, "trp_") || tok.Name != "ci" {
		t.Fatalf("token: %+v", tok)
	}
	id := fmt.Sprint(tok.ID)

	st, b := s.API(t, http.MethodGet, "/api/tokens", nil, alice)
	if st != 200 {
		t.Fatalf("list tokens: %d %s", st, b)
	}
	if strings.Contains(string(b), tok.Token) || strings.Contains(string(b), `"token"`) {
		t.Fatalf("list leaks token: %s", b)
	}
	if !strings.Contains(string(b), id) {
		t.Fatalf("list lacks token id %s: %s", id, b)
	}

	// token works as a password
	apiWant(t, s, http.MethodGet, "/api/me", nil, Basic{"x", tok.Token}, 200)

	// Only a UI session changes tokens.
	apiWant(t, s, http.MethodDelete, "/api/tokens/"+id, nil, alice, http.StatusUnauthorized)
	apiWant(t, s, http.MethodDelete, "/api/tokens/"+id, nil, bob, http.StatusUnauthorized)
	apiWant(t, s, http.MethodDelete, "/api/tokens/"+id, nil, bobUI, http.StatusNotFound)
	apiWant(t, s, http.MethodDelete, "/api/tokens/"+id, nil, aliceUI, http.StatusNoContent)
	apiWant(t, s, http.MethodGet, "/api/me", nil, Basic{"x", tok.Token}, http.StatusUnauthorized)
}

// TestAPISessionRule checks where each credential works on the JSON API (spec "Auth model"):
// a route that changes state, or an admin route, needs a UI session, so an admin password
// alone cannot go around the second factor. HTTP Basic keeps working for the read routes.
func TestAPISessionRule(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	repo := RandName("rule")
	s.CreateRepo(t, repo, "maven", false, nil)
	s.CreateUser(t, "frank", "frank-password-1", false)
	s.SetPermission(t, repo, "frank", "read")
	adminPw, frankPw := s.AdminBasic(), Basic{"frank", "frank-password-1"}
	readToken := Basic{"x", s.CreateToken(t, "frank", "frank-password-1", "read")}

	wantSessionRequired := func(t *testing.T, method, path string, body any, auth Auth) {
		t.Helper()
		st, b := s.API(t, method, path, body, auth)
		if st != http.StatusUnauthorized || !strings.Contains(string(b), "sign in through the web UI for this action") {
			t.Fatalf("%s %s: status %d, want 401 \"sign in through the web UI for this action\": %s", method, path, st, b)
		}
	}
	trust := map[string]any{
		"name": RandName("trust"), "issuer": "https://issuer.example", "audience": "telesphorepo",
		"claims": map[string][]string{"repository": {"acme/app"}}, "repos": []string{repo},
	}

	t.Run("admin password on admin routes", func(t *testing.T) {
		wantSessionRequired(t, http.MethodGet, "/api/users", nil, adminPw)
		wantSessionRequired(t, http.MethodGet, "/api/settings", nil, adminPw)
		wantSessionRequired(t, http.MethodGet, "/api/audit", nil, adminPw)
		wantSessionRequired(t, http.MethodGet, "/api/publish/trusts", nil, adminPw)
	})

	t.Run("admin password on write routes", func(t *testing.T) {
		wantSessionRequired(t, http.MethodPost, "/api/publish/trusts", trust, adminPw)
		wantSessionRequired(t, http.MethodPost, "/api/users", map[string]any{"username": RandName("evil"), "password": "evil-password-1", "is_admin": true}, adminPw)
		wantSessionRequired(t, http.MethodPut, "/api/repos/"+repo+"/permissions/frank", map[string]string{"level": "write"}, adminPw)
		wantSessionRequired(t, http.MethodPatch, "/api/settings", map[string]any{"max_json_bytes": 4096}, adminPw)
		wantSessionRequired(t, http.MethodPost, "/api/repos", map[string]any{"name": RandName("evil"), "format": "maven"}, adminPw)
		wantSessionRequired(t, http.MethodPost, "/api/tokens", map[string]any{"name": "evil"}, adminPw)
		wantSessionRequired(t, http.MethodPatch, "/api/users/"+s.AdminUser, map[string]any{"password": "evil-password-1"}, adminPw)
	})

	t.Run("password and read token of a user on write routes", func(t *testing.T) {
		wantSessionRequired(t, http.MethodPost, "/api/tokens", map[string]any{"name": "more"}, frankPw)
		wantSessionRequired(t, http.MethodPatch, "/api/users/frank", map[string]any{"password": "frank-password-2"}, frankPw)
		wantSessionRequired(t, http.MethodPost, "/api/tokens", map[string]any{"name": "more"}, readToken)
		wantSessionRequired(t, http.MethodPatch, "/api/users/frank", map[string]any{"password": "frank-password-2"}, readToken)
		wantSessionRequired(t, http.MethodPost, "/api/devices/approve", map[string]string{"user_code": "BCDF-GHJK", "totp_code": "000000"}, frankPw)
		wantSessionRequired(t, http.MethodPost, "/api/publish/sessions/manual", map[string]string{"repo": repo, "totp_code": "000000"}, frankPw)
	})

	t.Run("password and read token on read routes", func(t *testing.T) {
		for _, a := range []Auth{adminPw, frankPw, readToken} {
			apiWant(t, s, http.MethodGet, "/api/me", nil, a, http.StatusOK)
			apiWant(t, s, http.MethodGet, "/api/repos", nil, a, http.StatusOK)
			apiWant(t, s, http.MethodGet, "/api/repos/"+repo, nil, a, http.StatusOK)
			apiWant(t, s, http.MethodGet, "/api/repos/"+repo+"/tree", nil, a, http.StatusOK)
			apiWant(t, s, http.MethodGet, "/api/repos/"+repo+"/assets", nil, a, http.StatusOK)
			apiWant(t, s, http.MethodGet, "/api/repos/"+repo+"/usage", nil, a, http.StatusOK)
			apiWant(t, s, http.MethodGet, "/api/search?q=abc", nil, a, http.StatusOK)
		}
	})

	t.Run("a UI session with the second factor", func(t *testing.T) {
		admin := s.Admin()
		apiWant(t, s, http.MethodGet, "/api/users", nil, admin, http.StatusOK)
		apiWant(t, s, http.MethodPost, "/api/publish/trusts", trust, admin, http.StatusCreated)
		apiWant(t, s, http.MethodPut, "/api/repos/"+repo+"/permissions/frank", map[string]string{"level": "write"}, admin, http.StatusNoContent)
		// A user's own UI session changes the user's own things; admin routes stay 403.
		frank := s.SignIn(t, "frank", "frank-password-1")
		apiWant(t, s, http.MethodPost, "/api/tokens", map[string]any{"name": "ui"}, frank, http.StatusCreated)
		apiWant(t, s, http.MethodGet, "/api/users", nil, frank, http.StatusForbidden)
		apiWant(t, s, http.MethodPatch, "/api/users/frank", map[string]any{"password": "frank-password-2"}, frank, http.StatusOK)
	})
}

func TestAPIUsers(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	admin := s.Admin()
	body := map[string]any{"username": "carol", "password": "carol-password-1", "is_admin": false}
	var u struct {
		Username string `json:"username"`
		IsAdmin  bool   `json:"is_admin"`
	}
	apiJSON(t, s, http.MethodPost, "/api/users", body, admin, http.StatusCreated, &u)
	if u.Username != "carol" || u.IsAdmin {
		t.Fatalf("created: %+v", u)
	}
	apiWant(t, s, http.MethodPost, "/api/users", body, admin, http.StatusConflict)

	carol := s.SignIn(t, "carol", "carol-password-1")
	apiWant(t, s, http.MethodGet, "/api/users", nil, carol, http.StatusForbidden)
	apiWant(t, s, http.MethodGet, "/api/users", nil, Basic{"carol", "carol-password-1"}, http.StatusUnauthorized)
	apiWant(t, s, http.MethodGet, "/api/users", nil, admin, 200)

	apiJSON(t, s, http.MethodPatch, "/api/users/carol", map[string]any{"is_admin": true}, admin, 200, &u)
	if !u.IsAdmin {
		t.Fatalf("patch: %+v", u)
	}
	// An admin session needs the second factor: carol signs in again, now with TOTP.
	apiWant(t, s, http.MethodGet, "/api/users", nil, s.SignIn(t, "carol", "carol-password-1"), 200)
}

func TestAPIReposAndPermissions(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	admin := s.Admin()
	s.CreateUser(t, "dave", "dave-password-1", false)
	dave := Basic{"dave", "dave-password-1"}
	daveUI := s.SignIn(t, "dave", "dave-password-1")

	pub, priv, deb, secret := RandName("pub"), RandName("priv"), RandName("deb"), RandName("secret")
	s.CreateRepo(t, pub, "maven", true, nil)
	s.CreateRepo(t, priv, "maven", false, nil)
	s.CreateRepo(t, deb, "debian", false, map[string]any{"distribution": "stable", "components": []string{"main"}})
	s.CreateRepo(t, secret, "maven", false, nil)

	apiWant(t, s, http.MethodPost, "/api/repos", map[string]any{"name": pub, "format": "maven"}, admin, http.StatusConflict)
	apiWant(t, s, http.MethodPost, "/api/repos", map[string]any{"name": "Bad Name", "format": "maven"}, admin, http.StatusBadRequest)
	apiWant(t, s, http.MethodPost, "/api/repos", map[string]any{"name": RandName("x"), "format": "maven"}, daveUI, http.StatusForbidden)

	type repoRow struct {
		Name       string `json:"name"`
		Format     string `json:"format"`
		Permission string `json:"permission"`
	}
	list := func(auth Auth) map[string]repoRow {
		t.Helper()
		st, b := s.API(t, http.MethodGet, "/api/repos", nil, auth)
		if st != 200 {
			t.Fatalf("list repos: %d %s", st, b)
		}
		var rows []repoRow
		if err := json.Unmarshal(b, &rows); err != nil {
			var w struct {
				Items []repoRow `json:"items"`
			}
			if err2 := json.Unmarshal(b, &w); err2 != nil {
				t.Fatalf("decode repos %s: %v", b, err)
			}
			rows = w.Items
		}
		m := map[string]repoRow{}
		for _, r := range rows {
			m[r.Name] = r
		}
		return m
	}

	anon := list(nil)
	if _, ok := anon[pub]; !ok || len(anon) != 1 {
		t.Fatalf("anonymous list: %+v", anon)
	}

	// permissions
	s.SetPermission(t, priv, "dave", "read")
	s.SetPermission(t, deb, "dave", "write")
	var perms []struct {
		Username string `json:"username"`
		Level    string `json:"level"`
	}
	apiJSON(t, s, http.MethodGet, "/api/repos/"+priv+"/permissions", nil, admin, 200, &perms)
	if len(perms) != 1 || perms[0].Username != "dave" || perms[0].Level != "read" {
		t.Fatalf("permissions: %+v", perms)
	}
	apiWant(t, s, http.MethodGet, "/api/repos/"+priv+"/permissions", nil, daveUI, http.StatusForbidden)
	apiWant(t, s, http.MethodPut, "/api/repos/"+priv+"/permissions/dave", map[string]string{"level": "bogus"}, admin, http.StatusBadRequest)

	got := list(dave)
	if _, ok := got[secret]; ok {
		t.Fatalf("dave sees unpermitted repo: %+v", got)
	}
	if got[priv].Permission != "read" || got[deb].Permission != "write" || got[deb].Format != "debian" {
		t.Fatalf("dave list: %+v", got)
	}
	if _, ok := got[pub]; !ok {
		t.Fatalf("dave misses anonymous repo: %+v", got)
	}

	s.SetPermission(t, priv, "dave", "write")
	if p := list(dave)[priv].Permission; p != "write" {
		t.Fatalf("after upgrade permission = %q", p)
	}

	apiWant(t, s, http.MethodDelete, "/api/repos/"+priv+"/permissions/dave", nil, admin, http.StatusNoContent)
	if _, ok := list(dave)[priv]; ok {
		t.Fatal("repo still visible after permission delete")
	}
	apiWant(t, s, http.MethodGet, "/api/repos/"+priv, nil, dave, http.StatusForbidden)
}

func TestAPIAssetsPagination(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	repo := RandName("assets")
	s.CreateRepo(t, repo, "maven", false, nil)

	var want []string
	for i := 0; i < 7; i++ {
		p := fmt.Sprintf("com/acme/lib/1.0/file%d.txt", i)
		want = append(want, p)
		apiMavenPut(t, s, repo, p, fmt.Sprintf("content %d", i))
	}
	apiMavenPut(t, s, repo, "org/other/1.0/other.txt", "other")

	type page struct {
		Items []struct {
			Path   string `json:"path"`
			Size   int64  `json:"size"`
			SHA256 string `json:"sha256"`
		} `json:"items"`
		Next *string `json:"next"`
	}
	var got []string
	after := ""
	for i := 0; i < 10; i++ {
		path := "/api/repos/" + repo + "/assets?prefix=com/acme/&limit=3"
		if after != "" {
			path += "&after=" + after
		}
		var p page
		apiJSON(t, s, http.MethodGet, path, nil, s.Admin(), 200, &p)
		if len(p.Items) > 3 {
			t.Fatalf("page too large: %d", len(p.Items))
		}
		for _, it := range p.Items {
			if it.SHA256 == "" || it.Size == 0 {
				t.Fatalf("incomplete item: %+v", it)
			}
			got = append(got, it.Path)
		}
		if i == 0 && (len(p.Items) != 3 || p.Next == nil) {
			t.Fatalf("first page: %+v", p)
		}
		if p.Next == nil {
			break
		}
		after = *p.Next
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("assets = %v, want %v", got, want)
	}

	var all page
	apiJSON(t, s, http.MethodGet, "/api/repos/"+repo+"/assets?limit=200", nil, s.Admin(), 200, &all)
	if len(all.Items) != 8 || all.Next != nil {
		t.Fatalf("unfiltered: %d items, next %v", len(all.Items), all.Next)
	}
}

func TestAPISearch(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	s.CreateUser(t, "erin", "erin-password-1", false)
	erin := Basic{"erin", "erin-password-1"}
	open, hidden := RandName("open"), RandName("hidden")
	s.CreateRepo(t, open, "maven", false, nil)
	s.CreateRepo(t, hidden, "maven", false, nil)
	s.SetPermission(t, open, "erin", "read")

	apiMavenPut(t, s, open, "com/acme/zebrafish/1.0/zebrafish-1.0.txt", "a")
	apiMavenPut(t, s, hidden, "com/acme/zebrafish/2.0/zebrafish-2.0.txt", "b")

	search := func(auth Auth, q string) string {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			st, b := s.API(t, http.MethodGet, "/api/search?q="+q, nil, auth)
			if st != 200 {
				t.Fatalf("search %q: %d %s", q, st, b)
			}
			if strings.Contains(string(b), "zebrafish") || time.Now().After(deadline) {
				return string(b)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}

	b := search(s.Admin(), "ebraf")
	if !strings.Contains(b, "zebrafish-1.0.txt") || !strings.Contains(b, "zebrafish-2.0.txt") {
		t.Fatalf("admin search: %s", b)
	}
	b = search(erin, "ebraf")
	if !strings.Contains(b, "zebrafish-1.0.txt") || strings.Contains(b, "zebrafish-2.0.txt") || strings.Contains(b, hidden) {
		t.Fatalf("erin search leaks or misses: %s", b)
	}
	b = search(nil, "ebraf")
	if strings.Contains(b, "zebrafish-") {
		t.Fatalf("anonymous search sees private repos: %s", b)
	}
	apiWant(t, s, http.MethodGet, "/api/search?q=ab", nil, s.Admin(), http.StatusBadRequest)
}

func TestAPIErrors(t *testing.T) {
	s := StartServer(t, ServerOpts{Env: map[string]string{"TR_MAX_JSON_BYTES": "1024"}})

	huge := `{"name":"` + strings.Repeat("a", 4096) + `","format":"maven"}`
	apiWant(t, s, http.MethodPost, "/api/repos", huge, s.Admin(), http.StatusRequestEntityTooLarge)

	req := s.NewRequest(t, http.MethodGet, "/api/nope", nil)
	resp, b := s.Do(t, req, s.Admin())
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("/api/nope: %d %s", resp.StatusCode, b)
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("content-type %q", resp.Header.Get("Content-Type"))
	}
	var e struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(b, &e); err != nil || e.Error == "" {
		t.Fatalf("error body: %s", b)
	}
}

// TestAPIFormats covers the format registry routes: /api/formats, and the generic per-repo
// /status and /usage.
func TestAPIFormats(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	s.CreateUser(t, "erin", "erin-password-1", false)
	erin := Basic{"erin", "erin-password-1"}

	var formats []struct {
		Name          string          `json:"name"`
		Title         string          `json:"title"`
		DefaultConfig json.RawMessage `json:"default_config"`
		SettingsGroup string          `json:"settings_group"`
	}
	apiJSON(t, s, http.MethodGet, "/api/formats", nil, nil, 200, &formats)
	byName := map[string]bool{}
	for _, f := range formats {
		byName[f.Name] = true
		if f.Title == "" || !strings.HasPrefix(strings.TrimSpace(string(f.DefaultConfig)), "{") {
			t.Errorf("format %s: title %q, default_config %s", f.Name, f.Title, f.DefaultConfig)
		}
		if f.Name == "debian" && f.SettingsGroup != "debian" {
			t.Errorf("debian settings_group = %q, want debian", f.SettingsGroup)
		}
	}
	for _, n := range []string{"maven", "debian"} {
		if !byName[n] {
			t.Errorf("format %q missing from /api/formats: %+v", n, formats)
		}
	}

	st, b := s.API(t, http.MethodPost, "/api/repos", map[string]any{"name": RandName("x"), "format": "nope"}, s.Admin())
	if st != http.StatusBadRequest || !strings.Contains(string(b), "maven") {
		t.Fatalf("unknown format: status %d, want 400 listing the formats: %s", st, b)
	}

	mvn, deb := RandName("mvn"), RandName("deb")
	s.CreateRepo(t, mvn, "maven", false, nil)
	s.CreateRepo(t, deb, "debian", false, nil)

	var empty map[string]any
	apiJSON(t, s, http.MethodGet, "/api/repos/"+mvn+"/status", nil, s.Admin(), 200, &empty)
	var debStatus map[string]any
	apiJSON(t, s, http.MethodGet, "/api/repos/"+deb+"/status", nil, s.Admin(), 200, &debStatus)
	if _, ok := debStatus["pending"]; !ok {
		t.Fatalf("debian status has no pending: %v", debStatus)
	}
	apiWant(t, s, http.MethodGet, "/api/repos/"+deb+"/status", nil, erin, http.StatusForbidden)

	var usage struct {
		BaseURL  string `json:"base_url"`
		Snippets []struct {
			Title string `json:"title"`
			Lang  string `json:"lang"`
			Code  string `json:"code"`
		} `json:"snippets"`
	}
	apiJSON(t, s, http.MethodGet, "/api/repos/"+mvn+"/usage", nil, s.Admin(), 200, &usage)
	if usage.BaseURL != "http://"+s.Alias+":8080" || usage.Snippets == nil {
		t.Fatalf("usage: %+v", usage)
	}
	apiWant(t, s, http.MethodGet, "/api/repos/"+mvn+"/usage", nil, erin, http.StatusForbidden)
}
