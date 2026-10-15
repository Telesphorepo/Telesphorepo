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

func TestOIDC(t *testing.T) {
	kc := newKeycloak(t)
	s := StartServer(t, ServerOpts{Env: map[string]string{
		"TR_OIDC_ISSUER":        kc.Issuer,
		"TR_OIDC_CLIENT_ID":     kcClientID,
		"TR_OIDC_CLIENT_SECRET": kcClientSecret,
		"TR_OIDC_ADMIN_CLAIM":   "groups",
		"TR_OIDC_ADMIN_VALUES":  kcAdminGroup,
	}})
	// The redirect URL must point at the host-mapped server URL, known only after start, so
	// Keycloak starts second with exactly that redirect URI registered.
	redirect := s.URL + oidcCallbackPath
	kc.Start(t, redirect)
	// Saving oidc.redirect_url also proves the server reaches Keycloak's discovery document.
	s.MustAPI(t, http.MethodPatch, "/api/settings", map[string]any{"oidc.redirect_url": redirect}, s.Admin(), nil)

	var cfg struct {
		OIDC bool `json:"oidc"`
	}
	s.MustAPI(t, http.MethodGet, "/api/auth/config", nil, nil, &cfg)
	if !cfg.OIDC {
		t.Fatal("GET /api/auth/config: oidc = false, want true")
	}

	alice := kc.SignIn(t, s, "alice", kcAlicePassword)

	t.Run("admin signs in", func(t *testing.T) {
		if alice.Resp.StatusCode != http.StatusFound || alice.Resp.Header.Get("Location") != "/" {
			t.Fatalf("callback: status %d Location %q, want 302 /: %s", alice.Resp.StatusCode, alice.Resp.Header.Get("Location"), alice.Body)
		}
		if !hasCookie(alice.Jar, s.URL, "tr_session") {
			t.Fatal("no tr_session cookie after SSO login")
		}
		st, b := browserAPI(t, s, http.MethodGet, "/api/me", nil, alice.Jar)
		if st != http.StatusOK {
			t.Fatalf("GET /api/me: status %d: %s", st, b)
		}
		var me struct {
			Username    string   `json:"username"`
			Permissions []string `json:"permissions"`
			Kind        string   `json:"kind"`
			Email       string   `json:"email"`
		}
		if err := json.Unmarshal(b, &me); err != nil {
			t.Fatalf("decode /api/me %s: %v", b, err)
		}
		if me.Username != "alice" || strings.Join(me.Permissions, ",") != strings.Join(AllServerPerms, ",") || me.Kind != "oidc" || me.Email != "alice@e2e.test" {
			t.Fatalf("GET /api/me = %+v, want alice, every server permission, kind oidc, alice@e2e.test", me)
		}
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

	t.Run("non-admin is refused", func(t *testing.T) {
		bob := kc.SignIn(t, s, "bob", kcBobPassword)
		if loc := bob.Resp.Header.Get("Location"); bob.Resp.StatusCode != http.StatusFound || !strings.HasSuffix(loc, "error=not_authorized") {
			t.Fatalf("bob callback: status %d Location %q, want 302 /#/login?error=not_authorized: %s", bob.Resp.StatusCode, loc, bob.Body)
		}
		if hasCookie(bob.Jar, s.URL, "tr_session") {
			t.Fatal("bob got a tr_session cookie")
		}
		after := ""
		for {
			var page struct {
				Items []struct {
					Username string `json:"username"`
				} `json:"items"`
				Next *string `json:"next"`
			}
			s.MustAPI(t, http.MethodGet, "/api/users?after="+url.QueryEscape(after), nil, s.Admin(), &page)
			for _, u := range page.Items {
				if u.Username == "bob" {
					t.Fatal("GET /api/users lists bob; a refused SSO login must not create a user")
				}
			}
			if page.Next == nil || *page.Next == "" {
				break
			}
			after = *page.Next
		}
	})
}
