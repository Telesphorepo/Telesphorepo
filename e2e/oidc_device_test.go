//go:build e2e

package e2e

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// oidcDevicePath starts the new SSO sign-in that approves a device.
const oidcDevicePath = "/api/auth/oidc/device"

// startSSOServer starts a server with SSO through a new Keycloak, as TestOIDC does.
func startSSOServer(t *testing.T) (*Keycloak, *Server) {
	t.Helper()
	kc := newKeycloak(t)
	s := StartServer(t, ServerOpts{Env: map[string]string{
		"TR_OIDC_ISSUER":        kc.Issuer,
		"TR_OIDC_CLIENT_ID":     kcClientID,
		"TR_OIDC_CLIENT_SECRET": kcClientSecret,
		"TR_OIDC_ADMIN_CLAIM":   "groups",
		"TR_OIDC_ADMIN_VALUES":  kcAdminGroup,
	}})
	redirect := s.URL + oidcCallbackPath
	kc.Start(t, redirect)
	s.MustAPI(t, http.MethodPatch, "/api/settings", map[string]any{"oidc.redirect_url": redirect}, s.Admin(), nil)
	return kc, s
}

// ssoApproval is the end of an SSO device approval: the address of the authorization request
// and the approval page address that the callback sent the browser to.
type ssoApproval struct {
	AuthURL *url.URL
	Code    string // the user code on the approval page
	Grant   string
	Error   string
}

// startDeviceSignIn asks the server, with the UI session in jar, to start the SSO sign-in that
// approves userCode, and returns the authorization request it sends the browser to.
func (kc *Keycloak) startDeviceSignIn(t testing.TB, s *Server, jar http.CookieJar, userCode string) *url.URL {
	t.Helper()
	c := kc.Client(jar)
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := c.Get(s.URL + oidcDevicePath + "?code=" + url.QueryEscape(userCode))
	if err != nil {
		t.Fatalf("GET %s: %v", oidcDevicePath, err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	loc, err := resp.Location()
	if resp.StatusCode != http.StatusFound || err != nil || loc.Hostname() != kc.Alias {
		t.Fatalf("GET %s: status %d Location %q, want 302 to Keycloak: %s", oidcDevicePath, resp.StatusCode, resp.Header.Get("Location"), b)
	}
	return loc
}

// approvalPage reads the callback's redirect to "/#/device?code=...&grant=..." or "&error=...".
func approvalPage(t testing.TB, res *oidcResult) ssoApproval {
	t.Helper()
	loc := res.Resp.Header.Get("Location")
	path, query, _ := strings.Cut(strings.TrimPrefix(loc, "/#"), "?")
	if res.Resp.StatusCode != http.StatusFound || path != "/device" {
		t.Fatalf("callback: status %d Location %q, want 302 /#/device?...: %s", res.Resp.StatusCode, loc, res.Body)
	}
	q, err := url.ParseQuery(query)
	if err != nil {
		t.Fatalf("callback Location %q: %v", loc, err)
	}
	return ssoApproval{Code: q.Get("code"), Grant: q.Get("grant"), Error: q.Get("error")}
}

// approveSSO signs in again at Keycloak as user to approve userCode, the way the approval page
// does, and returns where the callback sent the browser.
func (kc *Keycloak) approveSSO(t testing.TB, s *Server, jar http.CookieJar, userCode, user, pass string) ssoApproval {
	t.Helper()
	auth := kc.startDeviceSignIn(t, s, jar, userCode)
	c := kc.Client(jar)
	resp, err := c.Get(auth.String())
	if err != nil {
		t.Fatalf("GET the authorization request: %v", err)
	}
	a := approvalPage(t, kc.submitLogin(t, c, jar, resp, user, pass))
	a.AuthURL = auth
	return a
}

// approveWith sends POST /api/devices/approve with body as the SPA does, with the UI session in jar.
func approveWith(t testing.TB, s *Server, jar http.CookieJar, body map[string]string) (int, []byte) {
	t.Helper()
	return browserAPI(t, s, http.MethodPost, "/api/devices/approve", body, jar)
}

// TestOIDCDeviceApproval: an SSO admin approves a device after a new sign-in at Keycloak, then
// publishes with the device.
func TestOIDCDeviceApproval(t *testing.T) {
	kc, s := startSSOServer(t)
	alice := kc.SignIn(t, s, "alice", kcAlicePassword)
	repo := RandName("sso-dev")
	s.CreateRepo(t, repo, "raw", false, nil)

	en, key := s.StartEnrollment(t, "e2e alice laptop", "gradle")

	t.Run("no grant", func(t *testing.T) {
		for _, body := range []map[string]string{
			{"user_code": en.UserCode},
			{"user_code": en.UserCode, "totp_code": "000000"},
			{"user_code": en.UserCode, "grant": "not-a-grant"},
		} {
			if st, b := approveWith(t, s, alice.Jar, body); st != http.StatusUnauthorized {
				t.Fatalf("approve with %v: status %d, want 401: %s", body, st, b)
			}
		}
	})

	var grant string
	t.Run("new sign-in", func(t *testing.T) {
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

	t.Run("approve and publish", func(t *testing.T) {
		if st, b := approveWith(t, s, alice.Jar, map[string]string{"user_code": en.UserCode, "grant": grant}); st != http.StatusCreated {
			t.Fatalf("approve with the grant: status %d, want 201: %s", st, b)
		}
		st, poll := s.PollEnrollment(t, en.DeviceCode)
		if st != http.StatusOK || poll.DeviceID == 0 {
			t.Fatalf("poll after approval: status %d, answer %+v", st, poll)
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

	t.Run("grant works once and for one code", func(t *testing.T) {
		en2, _ := s.StartEnrollment(t, "e2e alice second laptop", "maven")
		if st, b := approveWith(t, s, alice.Jar, map[string]string{"user_code": en2.UserCode, "grant": grant}); st != http.StatusUnauthorized {
			t.Fatalf("approve another code with a used grant: status %d, want 401: %s", st, b)
		}
		en3, _ := s.StartEnrollment(t, "e2e alice third laptop", "maven")
		a := kc.approveSSO(t, s, alice.Jar, en2.UserCode, "alice", kcAlicePassword)
		if a.Grant == "" {
			t.Fatalf("callback for %s: error %q, want a grant", en2.UserCode, a.Error)
		}
		if st, b := approveWith(t, s, alice.Jar, map[string]string{"user_code": en3.UserCode, "grant": a.Grant}); st != http.StatusUnauthorized {
			t.Fatalf("approve %s with the grant of %s: status %d, want 401: %s", en3.UserCode, en2.UserCode, st, b)
		}
	})
}

// TestOIDCDeviceApprovalStale: a provider that ignores prompt=login and max_age returns the old
// auth_time of its own session, and the callback refuses the approval. The test signs in at
// Keycloak first, then waits until that sign-in is more than 5 minutes old. t.Parallel lets the
// sequential tests of the zone run during the wait.
func TestOIDCDeviceApprovalStale(t *testing.T) {
	kc, s := startSSOServer(t)
	signedIn := time.Now()
	alice := kc.SignIn(t, s, "alice", kcAlicePassword)

	t.Parallel()
	time.Sleep(time.Until(signedIn.Add(5*time.Minute + 15*time.Second)))

	en, _ := s.StartEnrollment(t, "e2e alice stale laptop", "gradle")
	auth := kc.startDeviceSignIn(t, s, alice.Jar, en.UserCode)
	q := auth.Query()
	q.Del("prompt")
	q.Del("max_age")
	auth.RawQuery = q.Encode()

	// Keycloak still has alice's session from the first sign-in, so without prompt=login it
	// sends the browser straight back to the callback, with that sign-in's auth_time.
	resp, err := kc.Client(alice.Jar).Get(auth.String())
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
	if st, b := browserAPI(t, s, http.MethodGet, "/api/devices/enroll?user_code="+url.QueryEscape(en.UserCode), nil, alice.Jar); st != http.StatusOK {
		t.Fatalf("look up the code after the refused approval: status %d, want 200 (still pending): %s", st, b)
	}
}
