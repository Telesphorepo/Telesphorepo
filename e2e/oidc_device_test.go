//go:build e2e

package e2e

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// oidcDevicePath starts the new SSO sign-in that approves a device.
const oidcDevicePath = "/api/auth/oidc/device"

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

// approveWith sends POST /api/devices/approve for userCode as the SPA does, with the UI session in
// jar and proof in the header X-Telesphorepo-Proof ("" for none).
func approveWith(t testing.TB, s *Server, jar http.CookieJar, userCode, proof string) (int, []byte) {
	t.Helper()
	var auth Auth = &Session{Jar: jar}
	if proof != "" {
		auth = Proof{&Session{Jar: jar}, proof}
	}
	return s.API(t, http.MethodPost, "/api/devices/approve", map[string]string{"user_code": userCode}, auth)
}
