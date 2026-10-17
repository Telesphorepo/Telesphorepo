//go:build e2e

package e2e

import (
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// The Keycloak fixture is in keycloak_test.go.

// secureStrippingJar stores cookies without the Secure flag: Keycloak runs over plain HTTP here,
// and a Secure cookie would otherwise never be sent back by net/http's jar.
type secureStrippingJar struct{ http.CookieJar }

func (j secureStrippingJar) SetCookies(u *url.URL, cs []*http.Cookie) {
	for _, c := range cs {
		c.Secure = false
	}
	j.CookieJar.SetCookies(u, cs)
}

var kcLoginFormRe = regexp.MustCompile(`(?s)<form[^>]*id="kc-form-login"[^>]*action="([^"]+)"|<form[^>]*action="([^"]+)"[^>]*id="kc-form-login"`)

type oidcResult struct {
	Jar         http.CookieJar
	Resp        *http.Response // the server callback's response (body drained)
	Body        []byte
	CallbackURL string // the exact callback URL Keycloak redirected to
}

// SignIn drives the authorization-code flow like a browser: start at the server, submit
// Keycloak's login form, follow the redirect back to the callback.
func (kc *Keycloak) SignIn(t testing.TB, s *Server, user, pass string) *oidcResult {
	t.Helper()
	base, _ := cookiejar.New(nil)
	jar := secureStrippingJar{base}
	c := kc.Client(jar)

	resp, err := c.Get(s.URL + oidcLoginPath)
	if err != nil {
		t.Fatalf("GET %s: %v", oidcLoginPath, err)
	}
	return kc.submitLogin(t, c, jar, resp, user, pass)
}

// submitLogin reads Keycloak's login page in resp, submits the form as user, and follows the
// redirects to the server callback.
func (kc *Keycloak) submitLogin(t testing.TB, c *http.Client, jar http.CookieJar, resp *http.Response, user, pass string) *oidcResult {
	t.Helper()
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Request.URL.Hostname() != kc.Alias {
		t.Fatalf("SSO start: ended at %s with status %d, want Keycloak login page: %s", resp.Request.URL, resp.StatusCode, page)
	}
	m := kcLoginFormRe.FindSubmatch(page)
	if m == nil {
		t.Fatalf("no kc-form-login form on Keycloak page:\n%s", page)
	}
	action := string(m[1])
	if action == "" {
		action = string(m[2])
	}
	action = html.UnescapeString(action)
	actionURL, err := resp.Request.URL.Parse(action)
	if err != nil {
		t.Fatalf("parse form action %q: %v", action, err)
	}

	resp, err = c.PostForm(actionURL.String(), url.Values{"username": {user}, "password": {pass}, "credentialId": {""}})
	if err != nil {
		t.Fatalf("POST Keycloak login form: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.Request.URL.Path != oidcCallbackPath {
		t.Fatalf("login as %s: ended at %s with status %d, want the server callback: %s", user, resp.Request.URL, resp.StatusCode, body)
	}
	return &oidcResult{Jar: jar, Resp: resp, Body: body, CallbackURL: resp.Request.URL.String()}
}

// browserAPI sends a same-origin request like the SPA's fetch, with the session in jar.
func browserAPI(t testing.TB, s *Server, method, path string, body any, jar http.CookieJar) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		r = strings.NewReader(string(b))
	}
	req := s.NewRequest(t, method, path, r)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, b := s.Do(t, req, &Session{Jar: jar})
	return resp.StatusCode, b
}

func hasCookie(jar http.CookieJar, rawURL, name string) bool {
	u, _ := url.Parse(rawURL)
	for _, c := range jar.Cookies(u) {
		if c.Name == name && c.Value != "" {
			return true
		}
	}
	return false
}

// ssoMe is the part of GET /api/me that the SSO tests read.
type ssoMe struct {
	Username           string   `json:"username"`
	Permissions        []string `json:"permissions"`
	PermissionsManaged bool     `json:"permissions_managed"`
	Kind               string   `json:"kind"`
	Email              string   `json:"email"`
}

// ssoMeOf reads GET /api/me with the SSO session in jar.
func ssoMeOf(t *testing.T, s *Server, jar http.CookieJar) ssoMe {
	t.Helper()
	st, b := browserAPI(t, s, http.MethodGet, "/api/me", nil, jar)
	if st != http.StatusOK {
		t.Fatalf("GET /api/me: status %d: %s", st, b)
	}
	var me ssoMe
	if err := json.Unmarshal(b, &me); err != nil {
		t.Fatalf("decode /api/me %s: %v", b, err)
	}
	return me
}

// TestOIDC signs people in with Keycloak. With the permission claim groups, a sign-in sets the
// server permissions from the claim map (members of kcAdminGroup get every one), and nobody can
// change them on the server (409). Any person at the provider signs in; bob, outside the group,
// gets none. A change of the map takes effect at the next sign-in. Without a claim, a sign-in
// changes nothing.
func TestOIDC(t *testing.T) {
	t.Parallel()
	kc := newKeycloak(t)
	s := StartServer(t, ServerOpts{Env: map[string]string{
		"TR_OIDC_ISSUER":        kc.Issuer,
		"TR_OIDC_CLIENT_ID":     kcClientID,
		"TR_OIDC_CLIENT_SECRET": kcClientSecret,
	}})
	// The redirect URL must point at the host-mapped server URL, known only after start, so
	// Keycloak starts second with exactly that redirect URI registered.
	redirect := s.URL + oidcCallbackPath
	kc.Start(t, redirect)
	// Saving oidc.redirect_url also proves the server reaches Keycloak's discovery document.
	s.MustAPI(t, http.MethodPatch, "/api/settings", map[string]any{"oidc.redirect_url": redirect,
		"oidc.permission_claim": "groups", "oidc.permission_map": map[string][]string{kcAdminGroup: AllServerPerms}}, s.Admin(), nil)

	t.Run("a map without a claim or with an unknown permission", func(t *testing.T) {
		for _, bad := range []map[string]any{
			{"oidc.permission_claim": "", "oidc.permission_map": map[string][]string{"x": {"activity.read"}}},
			{"oidc.permission_map": map[string][]string{"x": {"nowhere.manage"}}},
			{"oidc.permission_map": map[string][]string{"x": {"repo.publish"}}},
		} {
			if st, b := s.API(t, http.MethodPatch, "/api/settings", bad, s.Admin()); st != http.StatusBadRequest {
				t.Fatalf("PATCH %v: status %d, want 400: %s", bad, st, b)
			}
		}
	})

	var cfg struct {
		OIDC bool `json:"oidc"`
	}
	s.MustAPI(t, http.MethodGet, "/api/auth/config", nil, nil, &cfg)
	if !cfg.OIDC {
		t.Fatal("GET /api/auth/config: oidc = false, want true")
	}

	alice := kc.SignIn(t, s, "alice", kcAlicePassword)

	t.Run("the claim map gives the permissions", func(t *testing.T) {
		if alice.Resp.StatusCode != http.StatusFound || alice.Resp.Header.Get("Location") != "/" {
			t.Fatalf("callback: status %d Location %q, want 302 /: %s", alice.Resp.StatusCode, alice.Resp.Header.Get("Location"), alice.Body)
		}
		if !hasCookie(alice.Jar, s.URL, "tr_session") {
			t.Fatal("no tr_session cookie after SSO login")
		}
		me := ssoMeOf(t, s, alice.Jar)
		if me.Username != "alice" || strings.Join(me.Permissions, ",") != strings.Join(AllServerPerms, ",") || !me.PermissionsManaged ||
			me.Kind != "oidc" || me.Email != "alice@e2e.test" {
			t.Fatalf("GET /api/me = %+v, want alice, every server permission, managed, kind oidc, alice@e2e.test", me)
		}
		st, b := s.API(t, http.MethodPut, "/api/users/alice/permissions", map[string]any{"permissions": []string{"activity.read"}}, s.Admin())
		wantAPIStatus(t, "change the managed permissions of alice", st, b, http.StatusConflict)
	})

	t.Run("admin creates a repo", func(t *testing.T) {
		repo := RandName("sso")
		st, b := browserAPI(t, s, http.MethodPost, "/api/repos", map[string]any{"name": repo, "format": "maven"}, alice.Jar)
		if st < 200 || st > 299 {
			t.Fatalf("POST /api/repos with SSO session: status %d: %s", st, b)
		}
		s.MustAPI(t, http.MethodGet, "/api/repos/"+repo, nil, s.Admin(), nil)
	})

	t.Run("no Basic auth for SSO users", func(t *testing.T) {
		for _, pw := range []string{"", "!", kcAlicePassword, "anything"} {
			if st, b := s.API(t, http.MethodGet, "/api/me", nil, Basic{"alice", pw}); st != http.StatusUnauthorized {
				t.Fatalf("Basic alice:%q on /api/me: status %d, want 401: %s", pw, st, b)
			}
		}
	})

	t.Run("no API tokens for SSO users", func(t *testing.T) {
		st, b := browserAPI(t, s, http.MethodPost, "/api/tokens", map[string]any{"name": "nope"}, alice.Jar)
		if st != http.StatusForbidden {
			t.Fatalf("POST /api/tokens with SSO session: status %d, want 403: %s", st, b)
		}
	})

	t.Run("reused state", func(t *testing.T) {
		u, err := url.Parse(alice.CallbackURL)
		if err != nil {
			t.Fatal(err)
		}
		state := u.Query().Get("state")
		if state == "" || u.Query().Get("code") == "" {
			t.Fatalf("callback URL %s has no state/code", alice.CallbackURL)
		}
		req, _ := http.NewRequest(http.MethodGet, alice.CallbackURL, nil)
		req.AddCookie(&http.Cookie{Name: "tr_oidc", Value: state})
		resp, b := s.Do(t, req, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("replayed callback: status %d, want 400: %s", resp.StatusCode, b)
		}
	})

	t.Run("tampered state", func(t *testing.T) {
		jar, _ := cookiejar.New(nil)
		sess := &Session{Jar: jar}
		resp, b := s.Do(t, s.NewRequest(t, http.MethodGet, oidcLoginPath, nil), sess)
		if resp.StatusCode != http.StatusFound || !hasCookie(jar, s.URL+oidcCallbackPath, "tr_oidc") {
			t.Fatalf("GET %s: status %d (want 302) or no tr_oidc cookie: %s", oidcLoginPath, resp.StatusCode, b)
		}
		resp, b = s.Do(t, s.NewRequest(t, http.MethodGet, oidcCallbackPath+"?state=bogus&code=x", nil), sess)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("callback with tampered state: status %d, want 400: %s", resp.StatusCode, b)
		}
		// A state matching its cookie but never issued by the server is refused as well.
		req := s.NewRequest(t, http.MethodGet, oidcCallbackPath+"?state=bogus&code=x", nil)
		req.AddCookie(&http.Cookie{Name: "tr_oidc", Value: "bogus"})
		resp, b = s.Do(t, req, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("callback with unknown state: status %d, want 400: %s", resp.StatusCode, b)
		}
	})

	t.Run("a person outside the map signs in without permissions", func(t *testing.T) {
		bob := kc.SignIn(t, s, "bob", kcBobPassword)
		if bob.Resp.StatusCode != http.StatusFound || bob.Resp.Header.Get("Location") != "/" || !hasCookie(bob.Jar, s.URL, "tr_session") {
			t.Fatalf("bob callback: status %d Location %q, want 302 / with a session: %s", bob.Resp.StatusCode, bob.Resp.Header.Get("Location"), bob.Body)
		}
		if me := ssoMeOf(t, s, bob.Jar); me.Username != "bob" || len(me.Permissions) != 0 || !me.PermissionsManaged {
			t.Fatalf("GET /api/me = %+v, want bob without server permissions, managed", me)
		}
	})

	t.Run("a sign-in replaces the permissions from the map", func(t *testing.T) {
		s.MustAPI(t, http.MethodPatch, "/api/settings", map[string]any{
			"oidc.permission_map": map[string][]string{kcAdminGroup: {"activity.read"}}}, s.Admin(), nil)
		again := kc.SignIn(t, s, "alice", kcAlicePassword)
		if me := ssoMeOf(t, s, again.Jar); strings.Join(me.Permissions, ",") != "activity.read" {
			t.Fatalf("alice after the map change: %+v, want activity.read", me)
		}
	})

	t.Run("without a claim a sign-in changes nothing", func(t *testing.T) {
		s.MustAPI(t, http.MethodPatch, "/api/settings", map[string]any{"oidc.permission_claim": "", "oidc.permission_map": map[string][]string{}},
			s.Admin(), nil)
		var u struct {
			Permissions        []string `json:"permissions"`
			PermissionsManaged bool     `json:"permissions_managed"`
		}
		s.MustAPI(t, http.MethodPut, "/api/users/alice/permissions", map[string]any{"permissions": []string{"signing.manage"}}, s.Admin(), &u)
		if strings.Join(u.Permissions, ",") != "signing.manage" || u.PermissionsManaged {
			t.Fatalf("PUT the permissions of alice: %+v, want signing.manage, not managed", u)
		}
		again := kc.SignIn(t, s, "alice", kcAlicePassword)
		if me := ssoMeOf(t, s, again.Jar); strings.Join(me.Permissions, ",") != "signing.manage" || me.PermissionsManaged {
			t.Fatalf("alice after a sign-in without a claim: %+v, want signing.manage, not managed", me)
		}
	})
}
