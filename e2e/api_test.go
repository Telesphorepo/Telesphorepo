//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
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
	t.Parallel()
	s := StartServer(t, ServerOpts{})
	s.CreateUser(t, "alice", "alice-password-1")

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
	// The sign-in answer and GET /api/me carry the same session view.
	type sessionView struct {
		Username             string              `json:"username"`
		Permissions          []string            `json:"permissions"`
		AllRepos             bool                `json:"all_repos"`
		SecondFactorRequired bool                `json:"second_factor_required"`
		RepoPermissions      map[string][]string `json:"repo_permissions"`
	}
	var login struct {
		Session *sessionView `json:"session"`
	}
	if err := json.Unmarshal(b, &login); err != nil || login.Session == nil || login.Session.Username != "alice" || login.Session.RepoPermissions == nil {
		t.Fatalf("login body: %s", b)
	}

	repo := RandName("view")
	s.CreateRepo(t, repo, "raw", false, nil)
	s.SetPerms(t, repo, "alice", "repo.download")
	sess := s.Login(t, "alice", "alice-password-1")
	var u sessionView
	apiJSON(t, s, http.MethodGet, "/api/me", nil, sess, 200, &u)
	if u.Username != "alice" || len(u.Permissions) != 0 || u.AllRepos || u.SecondFactorRequired ||
		!slices.Equal(u.RepoPermissions[repo], []string{"repo.download"}) || len(u.RepoPermissions) != 1 {
		t.Fatalf("me: %+v, want repo_permissions {%s: [repo.download]}", u, repo)
	}
	apiWant(t, s, http.MethodGet, "/api/me", nil, nil, http.StatusUnauthorized)

	apiWant(t, s, http.MethodPost, "/api/auth/logout", nil, sess, http.StatusNoContent)
	apiWant(t, s, http.MethodGet, "/api/me", nil, sess, http.StatusUnauthorized)
}

func TestAPICSRF(t *testing.T) {
	t.Parallel()
	s := StartServer(t, ServerOpts{})
	s.CreateUser(t, "alice", "alice-password-1")
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
	t.Parallel()
	s := StartServer(t, ServerOpts{})
	s.CreateUser(t, "alice", "alice-password-1")
	s.CreateUser(t, "bob", "bob-password-1")
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

// TestAPISessionRule checks where each credential works on the JSON API (SPEC.md "Auth model"):
// a route that changes state, or an admin route, needs a UI session, so an admin password
// alone cannot go around the second factor. HTTP Basic keeps working for the read routes.
func TestAPISessionRule(t *testing.T) {
	t.Parallel()
	s := StartServer(t, ServerOpts{})
	repo := RandName("rule")
	s.CreateRepo(t, repo, "maven", false, nil)
	s.CreateUser(t, "frank", "frank-password-1")
	s.SetPerms(t, repo, "frank", "repo.download")
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
		wantSessionRequired(t, http.MethodPost, "/api/users", map[string]any{"username": RandName("evil"), "password": "evil-password-1", "permissions": AllServerPerms}, adminPw)
		wantSessionRequired(t, http.MethodPut, "/api/users/frank/permissions", map[string]any{"permissions": AllServerPerms}, adminPw)
		wantSessionRequired(t, http.MethodPut, "/api/grants", map[string]any{"users": []string{"frank"}, "repos": []string{repo}, "perms": []string{"repo.download", "repo.publish"}}, adminPw)
		wantSessionRequired(t, http.MethodPatch, "/api/settings", map[string]any{"max_json_size": 4096}, adminPw)
		wantSessionRequired(t, http.MethodPost, "/api/repos", map[string]any{"name": RandName("evil"), "format": "maven"}, adminPw)
		wantSessionRequired(t, http.MethodPost, "/api/tokens", map[string]any{"name": "evil"}, adminPw)
		wantSessionRequired(t, http.MethodPatch, "/api/users/"+s.AdminUser, map[string]any{"password": "evil-password-1"}, adminPw)
	})

	t.Run("password and read token of a user on write routes", func(t *testing.T) {
		wantSessionRequired(t, http.MethodPost, "/api/tokens", map[string]any{"name": "more"}, frankPw)
		wantSessionRequired(t, http.MethodPatch, "/api/users/frank", map[string]any{"password": "frank-password-2"}, frankPw)
		wantSessionRequired(t, http.MethodPost, "/api/tokens", map[string]any{"name": "more"}, readToken)
		wantSessionRequired(t, http.MethodPatch, "/api/users/frank", map[string]any{"password": "frank-password-2"}, readToken)
		wantSessionRequired(t, http.MethodPost, "/api/devices/approve", map[string]string{"user_code": "BCDF-GHJK"}, frankPw)
		wantSessionRequired(t, http.MethodPost, "/api/publish/sessions/manual", map[string]string{"repo": repo}, frankPw)
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
		apiWant(t, s, http.MethodPut, "/api/grants", map[string]any{"users": []string{"frank"}, "repos": []string{repo}, "perms": []string{"repo.download", "repo.publish"}}, admin, http.StatusOK)
		// A user's own UI session changes the user's own things; permission routes stay 403.
		frank := s.SignIn(t, "frank", "frank-password-1")
		apiWant(t, s, http.MethodPost, "/api/tokens", map[string]any{"name": "ui"}, frank, http.StatusCreated)
		apiWant(t, s, http.MethodGet, "/api/users", nil, frank, http.StatusForbidden)
		apiWant(t, s, http.MethodPatch, "/api/users/frank", map[string]any{"password": "frank-password-2"}, frank, http.StatusOK)
	})

	t.Run("a UI session on format routes", func(t *testing.T) {
		// A signed-in browser downloads from a private repo with its session cookie; writes on
		// format routes still need a publish session.
		s.CreateUser(t, "gina", "gina-password-1")
		s.SetPerms(t, repo, "gina", "repo.download", "repo.publish")
		path := "/maven/" + repo + "/com/example/cookie/1.0/cookie-1.0.jar"
		body := "cookie download " + randHex(8)
		apiWant(t, s, http.MethodPut, path, body, s.PublishAuth(t, "gina", repo), http.StatusCreated)
		gina := s.SignIn(t, "gina", "gina-password-1")
		for _, sess := range []*Session{gina, s.Admin()} {
			st, b := s.API(t, http.MethodGet, path, nil, sess)
			if st != http.StatusOK || string(b) != body {
				t.Fatalf("GET %s with a UI session: status %d, body %q", path, st, b)
			}
			apiWant(t, s, http.MethodHead, path, nil, sess, http.StatusOK)
		}
		apiWant(t, s, http.MethodGet, path, nil, nil, http.StatusUnauthorized)
		st, b := s.API(t, http.MethodPut, path, "changed by a cookie", gina)
		if st != http.StatusUnauthorized || !strings.Contains(string(b), "publishing needs a publish session") {
			t.Fatalf("PUT %s with a UI session: status %d, want 401 publishing needs a publish session: %s", path, st, b)
		}
		apiWant(t, s, http.MethodPut, "/maven/"+repo+"/com/example/cookie/1.1/cookie-1.1.jar", "new", s.Admin(), http.StatusUnauthorized)
	})

	t.Run("a read token as Bearer", func(t *testing.T) {
		// "Authorization: Bearer trp_..." reads like the Basic form and still never writes.
		s.CreateUser(t, "hank", "hank-password-1")
		s.SetPerms(t, repo, "hank", "repo.download", "repo.publish")
		bearer := Bearer{s.CreateToken(t, "hank", "hank-password-1", "bearer")}
		path := "/maven/" + repo + "/com/example/bearer/1.0/bearer-1.0.jar"
		body := "bearer download " + randHex(8)
		apiMavenPut(t, s, repo, "com/example/bearer/1.0/bearer-1.0.jar", body)
		st, b := s.API(t, http.MethodGet, path, nil, bearer)
		if st != http.StatusOK || string(b) != body {
			t.Fatalf("GET %s with a Bearer read token: status %d, body %q", path, st, b)
		}
		st, b = s.API(t, http.MethodPut, path, "changed by a read token", bearer)
		if st != http.StatusUnauthorized || !strings.Contains(string(b), "publishing needs a publish session") {
			t.Fatalf("PUT %s with a Bearer read token: status %d, want 401 publishing needs a publish session: %s", path, st, b)
		}
		wantSessionRequired(t, http.MethodPost, "/api/tokens", map[string]any{"name": "more"}, bearer)
	})
}

func TestAPIUsers(t *testing.T) {
	t.Parallel()
	s := StartServer(t, ServerOpts{})
	admin := s.Admin()
	body := map[string]any{"username": "carol", "password": "carol-password-1", "permissions": []string{}}
	var u struct {
		Username    string   `json:"username"`
		Permissions []string `json:"permissions"`
	}
	apiJSON(t, s, http.MethodPost, "/api/users", body, admin, http.StatusCreated, &u)
	if u.Username != "carol" || len(u.Permissions) != 0 {
		t.Fatalf("created: %+v", u)
	}
	apiWant(t, s, http.MethodPost, "/api/users", body, admin, http.StatusConflict)
	apiWant(t, s, http.MethodPost, "/api/users", map[string]any{"username": "dan", "password": "dan-password-1", "permissions": []string{"bogus"}}, admin, http.StatusBadRequest)

	carol := s.SignIn(t, "carol", "carol-password-1")
	apiWant(t, s, http.MethodGet, "/api/users", nil, carol, http.StatusForbidden)
	apiWant(t, s, http.MethodGet, "/api/users", nil, Basic{"carol", "carol-password-1"}, http.StatusUnauthorized)
	apiWant(t, s, http.MethodGet, "/api/users", nil, admin, 200)

	apiJSON(t, s, http.MethodPut, "/api/users/carol/permissions", map[string]any{"permissions": AllServerPerms}, admin, 200, &u)
	if strings.Join(u.Permissions, ",") != strings.Join(AllServerPerms, ",") {
		t.Fatalf("set permissions: %+v", u)
	}
	// The grant ends carol's session, which a password alone started.
	apiWant(t, s, http.MethodGet, "/api/me", nil, carol, http.StatusUnauthorized)
	// A session with server permissions needs the second factor: carol signs in again, now with TOTP.
	apiWant(t, s, http.MethodGet, "/api/users", nil, s.SignIn(t, "carol", "carol-password-1"), 200)
	// PATCH changes only the password.
	apiWant(t, s, http.MethodPatch, "/api/users/carol", map[string]any{"password": "carol-password-2"}, admin, 200)
}

func TestAPIReposAndPermissions(t *testing.T) {
	t.Parallel()
	s := StartServer(t, ServerOpts{})
	admin := s.Admin()
	s.CreateUser(t, "dave", "dave-password-1")
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
		Name        string   `json:"name"`
		Format      string   `json:"format"`
		Permissions []string `json:"permissions"`
	}
	permsOf := func(r repoRow) string { return strings.Join(r.Permissions, ",") }
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
	s.SetPerms(t, priv, "dave", "repo.download")
	s.SetPerms(t, deb, "dave", "repo.download", "repo.publish")
	var perms []struct {
		Username string   `json:"username"`
		Perms    []string `json:"perms"`
	}
	apiJSON(t, s, http.MethodGet, "/api/repos/"+priv+"/grants", nil, admin, 200, &perms)
	if len(perms) != 1 || perms[0].Username != "dave" || strings.Join(perms[0].Perms, ",") != "repo.download" {
		t.Fatalf("permissions: %+v", perms)
	}
	apiWant(t, s, http.MethodGet, "/api/repos/"+priv+"/grants", nil, daveUI, http.StatusForbidden)
	apiWant(t, s, http.MethodPut, "/api/grants", map[string]any{"users": []string{"dave"}, "repos": []string{priv}, "perms": []string{"bogus"}}, admin, http.StatusBadRequest)

	got := list(dave)
	if _, ok := got[secret]; ok {
		t.Fatalf("dave sees unpermitted repo: %+v", got)
	}
	if permsOf(got[priv]) != "repo.download" || permsOf(got[deb]) != "repo.download,repo.publish" || got[deb].Format != "debian" {
		t.Fatalf("dave list: %+v", got)
	}
	if _, ok := got[pub]; !ok || permsOf(got[pub]) != "repo.download" {
		t.Fatalf("dave misses anonymous repo: %+v", got)
	}
	// The All repositories permission holds every repository permission.
	if p := permsOf(list(admin)[secret]); p != "repo.download,repo.publish,repo.delete,repo.settings,repo.access,ci.pipelines" {
		t.Fatalf("admin's permissions on %s = %q, want every one", secret, p)
	}

	s.SetPerms(t, priv, "dave", "repo.download", "repo.publish")
	if p := permsOf(list(dave)[priv]); p != "repo.download,repo.publish" {
		t.Fatalf("after upgrade permissions = %q", p)
	}

	apiWant(t, s, http.MethodPut, "/api/grants", map[string]any{"users": []string{"dave"}, "repos": []string{priv}, "perms": []string{}}, admin, http.StatusOK)
	if _, ok := list(dave)[priv]; ok {
		t.Fatal("repo still visible after permission delete")
	}
	apiWant(t, s, http.MethodGet, "/api/repos/"+priv, nil, dave, http.StatusForbidden)
}

func TestAPIAssetsPagination(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	s := StartServer(t, ServerOpts{})
	s.CreateUser(t, "erin", "erin-password-1")
	erin := Basic{"erin", "erin-password-1"}
	open, hidden := RandName("open"), RandName("hidden")
	s.CreateRepo(t, open, "maven", false, nil)
	s.CreateRepo(t, hidden, "maven", false, nil)
	s.SetPerms(t, open, "erin", "repo.download")

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
	t.Parallel()
	s := StartServer(t, ServerOpts{Env: map[string]string{"TR_MAX_JSON_SIZE": "1KiB"}})

	huge := `{"name":"` + strings.Repeat("a", 4096) + `","format":"maven"}`
	apiWant(t, s, http.MethodPost, "/api/repos", huge, s.Admin(), http.StatusRequestEntityTooLarge)

	req := s.NewRequest(t, http.MethodGet, "/api/nope", nil)
	resp, b := s.Do(t, req, s.Admin())
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("/api/nope: %d %s", resp.StatusCode, b)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content-type %q, want application/problem+json", ct)
	}
	var e Problem
	if err := json.Unmarshal(b, &e); err != nil || e.Type == "" || e.Title == "" || e.Detail == "" || e.Status != http.StatusNotFound {
		t.Fatalf("problem document: %s", b)
	}
	if v := resp.Header.Get("Telesphorepo-Version"); v == "" {
		t.Fatal("/api/nope: no Telesphorepo-Version header")
	}
}

// TestAPIChanges covers the public change log route GET /api/changes and the version header.
func TestAPIChanges(t *testing.T) {
	t.Parallel()
	s := StartServer(t, ServerOpts{})
	for _, since := range []string{"", "?since=v0.0.0", "?since=0.0.1-rc.1"} {
		resp, b := s.Do(t, s.NewRequest(t, http.MethodGet, "/api/changes"+since, nil), nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /api/changes%s: status %d: %s", since, resp.StatusCode, b)
		}
		var out struct {
			Version string            `json:"version"`
			Changes []json.RawMessage `json:"changes"`
		}
		if err := json.Unmarshal(b, &out); err != nil || out.Changes == nil || out.Version == "" {
			t.Fatalf("GET /api/changes%s: %s, want {version, changes: [...]}", since, b)
		}
		if v := resp.Header.Get("Telesphorepo-Version"); v != out.Version {
			t.Fatalf("GET /api/changes%s: Telesphorepo-Version %q, body version %q", since, v, out.Version)
		}
	}
	resp, b := s.Do(t, s.NewRequest(t, http.MethodGet, "/api/changes?since=latest", nil), nil)
	var p Problem
	if resp.StatusCode != http.StatusBadRequest || json.Unmarshal(b, &p) != nil || p.Status != http.StatusBadRequest || p.Detail == "" {
		t.Fatalf("GET /api/changes?since=latest: status %d %s, want a 400 problem document", resp.StatusCode, b)
	}
}

// TestAPIFormats covers the format registry routes: /api/formats, and the generic per-repo
// /status and /usage.
func TestAPIFormats(t *testing.T) {
	t.Parallel()
	s := StartServer(t, ServerOpts{})
	s.CreateUser(t, "erin", "erin-password-1")
	erin := Basic{"erin", "erin-password-1"}

	var formats []struct {
		Name   string `json:"name"`
		Title  string `json:"title"`
		Config struct {
			Default json.RawMessage `json:"default"`
		} `json:"config"`
	}
	apiJSON(t, s, http.MethodGet, "/api/formats", nil, nil, 200, &formats)
	byName := map[string]bool{}
	for _, f := range formats {
		byName[f.Name] = true
		if f.Title == "" || !strings.HasPrefix(strings.TrimSpace(string(f.Config.Default)), "{") {
			t.Errorf("format %s: title %q, config.default %s", f.Name, f.Title, f.Config.Default)
		}
	}
	for _, n := range []string{"maven", "debian", "nuget"} {
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

	// Every format answers the pending rebuilds of the kernel's queue.
	var mvnStatus map[string]any
	apiJSON(t, s, http.MethodGet, "/api/repos/"+mvn+"/status", nil, s.Admin(), 200, &mvnStatus)
	if mvnStatus["pending"] != float64(0) {
		t.Fatalf("maven status: %v, want pending 0", mvnStatus)
	}
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
