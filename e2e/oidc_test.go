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
	"time"
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
	return kc.SignInTo(t, s, user, pass, "")
}

// SignInTo is SignIn from the sign-in page with ?next=: the UI path to open after it ("" for none).
func (kc *Keycloak) SignInTo(t testing.TB, s *Server, user, pass, next string) *oidcResult {
	t.Helper()
	base, _ := cookiejar.New(nil)
	jar := secureStrippingJar{base}
	c := kc.Client(jar)

	start := s.URL + oidcLoginPath
	if next != "" {
		start += "?" + url.Values{"next": {next}}.Encode()
	}
	resp, err := c.Get(start)
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
// gets none. An SSO person approves a device after a new sign-in at Keycloak, then publishes with
// it. A change of the map takes effect at the next sign-in. Without a claim, a sign-in changes
// nothing.
//
// A provider that ignores prompt=login and max_age returns the old auth_time of its own session,
// and the callback refuses the approval: the test signs in at Keycloak first in a browser of its
// own, sets oidc.approval_max_age to its least, 10 s, and waits until that sign-in is older.
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
		"oidc.permission_claim": "groups", "oidc.permission_map": map[string][]string{kcAdminGroup: s.ServerPerms(t)},
		"oidc.approval_max_age": "10s"}, s.Admin(), nil)
	stale := kc.SignIn(t, s, "alice", kcAlicePassword)
	staleSignedIn := time.Now()

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
		if me.Username != "alice" || strings.Join(me.Permissions, ",") != strings.Join(s.ServerPerms(t), ",") || !me.PermissionsManaged ||
			me.Kind != "oidc" || me.Email != "alice@e2e.test" {
			t.Fatalf("GET /api/me = %+v, want alice, every server permission, managed, kind oidc, alice@e2e.test", me)
		}
		st, b := s.API(t, http.MethodPut, "/api/users/alice/permissions", map[string]any{"permissions": []string{"activity.read"}}, s.Admin())
		wantAPIStatus(t, "change the managed permissions of alice", st, b, http.StatusConflict)
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
		// The sign-in page passes on the page to open after it.
		next := "/repo/a-repo/files/com/?build=2"
		bob := kc.SignInTo(t, s, "bob", kcBobPassword, next)
		if bob.Resp.StatusCode != http.StatusFound || bob.Resp.Header.Get("Location") != "/#"+next || !hasCookie(bob.Jar, s.URL, "tr_session") {
			t.Fatalf("bob callback: status %d Location %q, want 302 /#%s with a session: %s", bob.Resp.StatusCode, bob.Resp.Header.Get("Location"), next, bob.Body)
		}
		if me := ssoMeOf(t, s, bob.Jar); me.Username != "bob" || len(me.Permissions) != 0 || !me.PermissionsManaged {
			t.Fatalf("GET /api/me = %+v, want bob without server permissions, managed", me)
		}
	})

	// The device subtests run while alice holds every server permission.
	en, key := s.StartEnrollment(t, "e2e alice laptop")

	t.Run("device: no grant", func(t *testing.T) {
		for _, proof := range []string{"", "totp 000000", "grant not-a-grant"} {
			if st, b := approveWith(t, s, alice.Jar, en.UserCode, proof); st != http.StatusUnauthorized {
				t.Fatalf("approve with the proof %q: status %d, want 401: %s", proof, st, b)
			}
		}
	})

	var grant string
	t.Run("device: new sign-in", func(t *testing.T) {
		a := kc.approveSSO(t, s, alice.Jar, en.UserCode, "alice", kcAlicePassword)
		q := a.AuthURL.Query()
		if q.Get("prompt") != "login" || q.Get("max_age") != "0" {
			t.Fatalf("authorization request %s: want prompt=login and max_age=0", a.AuthURL.Redacted())
		}
		if a.Error != "" || a.Grant == "" || a.Code != en.UserCode {
			t.Fatalf("callback sent the browser to code %q, error %q, grant set %v; want code %s and a grant", a.Code, a.Error, a.Grant != "", en.UserCode)
		}
		grant = a.Grant
	})

	t.Run("device: approve, create a repo and publish", func(t *testing.T) {
		if st, b := approveWith(t, s, alice.Jar, en.UserCode, "grant "+grant); st != http.StatusCreated {
			t.Fatalf("approve with the grant: status %d, want 201: %s", st, b)
		}
		st, poll := s.PollEnrollment(t, en.DeviceCode)
		if st != http.StatusOK || poll.DeviceID == 0 {
			t.Fatalf("poll after approval: status %d, answer %+v", st, poll)
		}
		repo := RandName("sso-dev")
		if st, b := browserAPI(t, s, http.MethodPost, "/api/repos", map[string]any{"name": repo, "format": "raw"}, alice.Jar); st/100 != 2 {
			t.Fatalf("POST /api/repos with the SSO session: status %d: %s", st, b)
		}
		d := &Device{ID: poll.DeviceID, User: "alice", Key: key}
		ps := mustSession(t, "mint a session with alice's device")(s.Mint(t, d, repo))
		body := "published by an SSO device\n"
		req := s.NewRequest(t, http.MethodPut, "/raw/"+repo+"/sso/hello.txt", strings.NewReader(body))
		if resp, b := s.Do(t, req, ps.Auth()); resp.StatusCode/100 != 2 {
			t.Fatalf("PUT with the device's session: status %d: %s", resp.StatusCode, b)
		}
		resp, b := s.Do(t, s.NewRequest(t, http.MethodGet, "/raw/"+repo+"/sso/hello.txt", nil), s.AdminBasic())
		if resp.StatusCode != http.StatusOK || string(b) != body {
			t.Fatalf("GET the published file: status %d, body %q", resp.StatusCode, b)
		}
	})

	t.Run("device: a grant works once and for one code", func(t *testing.T) {
		en2, _ := s.StartEnrollment(t, "e2e alice second laptop")
		if st, b := approveWith(t, s, alice.Jar, en2.UserCode, "grant "+grant); st != http.StatusUnauthorized {
			t.Fatalf("approve another code with a used grant: status %d, want 401: %s", st, b)
		}
		en3, _ := s.StartEnrollment(t, "e2e alice third laptop")
		a := kc.approveSSO(t, s, alice.Jar, en2.UserCode, "alice", kcAlicePassword)
		if a.Grant == "" {
			t.Fatalf("callback for %s: error %q, want a grant", en2.UserCode, a.Error)
		}
		if st, b := approveWith(t, s, alice.Jar, en3.UserCode, "grant "+a.Grant); st != http.StatusUnauthorized {
			t.Fatalf("approve %s with the grant of %s: status %d, want 401: %s", en3.UserCode, en2.UserCode, st, b)
		}
	})

	// The browser of stale signed in at Keycloak once, at the start, and never again.
	t.Run("device: stale sign-in", func(t *testing.T) {
		time.Sleep(time.Until(staleSignedIn.Add(12 * time.Second)))

		en, _ := s.StartEnrollment(t, "e2e alice stale laptop")
		auth := kc.startDeviceSignIn(t, s, stale.Jar, en.UserCode)
		q := auth.Query()
		q.Del("prompt")
		q.Del("max_age")
		auth.RawQuery = q.Encode()

		// Keycloak still has the session of that sign-in, so without prompt=login it sends the
		// browser straight back to the callback, with that sign-in's auth_time.
		resp, err := kc.Client(stale.Jar).Get(auth.String())
		if err != nil {
			t.Fatalf("GET the authorization request without prompt and max_age: %v", err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.Request.URL.Path != oidcCallbackPath {
			t.Fatalf("authorization request without prompt: ended at %s with status %d, want the server callback: %s", resp.Request.URL, resp.StatusCode, b)
		}
		a := approvalPage(t, &oidcResult{Resp: resp, Body: b})
		if a.Error != "stale_sign_in" || a.Grant != "" || a.Code != en.UserCode {
			t.Fatalf("callback sent the browser to code %q, error %q, grant set %v; want code %s and error stale_sign_in", a.Code, a.Error, a.Grant != "", en.UserCode)
		}
		if st, b := browserAPI(t, s, http.MethodGet, "/api/devices/enroll?user_code="+url.QueryEscape(en.UserCode), nil, stale.Jar); st != http.StatusOK {
			t.Fatalf("look up the code after the refused approval: status %d, want 200 (still pending): %s", st, b)
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
