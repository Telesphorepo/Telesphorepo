//go:build e2e

package e2e

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// Publisher credentials: the write gate, second-factor sign-in, the device flow,
// manual sessions and the OIDC exchange for CI.

// pubPut uploads a new file to the Maven repo with auth.
func pubPut(t *testing.T, s *Server, repo string, auth Auth) (*http.Response, []byte) {
	t.Helper()
	return authPut(t, s, "/maven/"+repo+"/com/example/pub/1.0/f-"+randHex(4)+".txt", "x", auth)
}

// wantPublishRequired checks the answer to a write without a publish session for the repo.
func wantPublishRequired(t *testing.T, resp *http.Response, body []byte) {
	t.Helper()
	wantStatus(t, resp, body, http.StatusUnauthorized)
	wantChallenge(t, resp)
	if !strings.Contains(string(body), "publish session") {
		t.Fatalf("write without a publish session: body %s does not name the publish session", body)
	}
}

func wantAPIStatus(t *testing.T, what string, st int, b []byte, want ...int) {
	t.Helper()
	if !slices.Contains(want, st) {
		t.Fatalf("%s: status %d, want %v: %s", what, st, want, b)
	}
}

// setSetting PATCHes one setting as the admin; nil resets it to its default.
func setSetting(t *testing.T, s *Server, key string, value any) {
	t.Helper()
	s.MustAPI(t, http.MethodPatch, "/api/settings", map[string]any{key: value}, s.Admin(), nil)
}

func TestPublishGate(t *testing.T) {
	t.Parallel()
	s := StartServer(t, ServerOpts{})
	repo, other := RandName("pub"), RandName("other")
	s.CreateRepo(t, repo, "maven", false, nil)
	s.CreateRepo(t, other, "raw", false, nil)
	w, r := RandName("writer"), RandName("reader")
	s.CreateUser(t, w, "writer-pass-1234")
	s.CreateUser(t, r, "reader-pass-1234")
	s.SetPerms(t, repo, w, "repo.download", "repo.publish")
	s.SetPerms(t, other, w, "repo.download", "repo.publish")
	s.SetPerms(t, repo, r, "repo.download")

	t.Run("password and read token cannot write", func(t *testing.T) {
		readToken := s.CreateToken(t, w, "writer-pass-1234", "read")
		for _, a := range []Basic{{w, "writer-pass-1234"}, {"ci", readToken}, s.AdminBasic()} {
			resp, body := pubPut(t, s, repo, a)
			wantPublishRequired(t, resp, body)
		}
	})

	t.Run("a session writes and reads its repo only", func(t *testing.T) {
		ps := s.MintSession(t, w, repo)
		p := "/maven/" + repo + "/com/example/pub/1.0/session.txt"
		resp, body := authPut(t, s, p, "from a session", ps.Auth())
		wantStatus(t, resp, body, http.StatusCreated)
		resp, body = authGet(t, s, p, ps.Auth())
		wantStatus(t, resp, body, http.StatusOK)

		resp, body = rawPut(t, s, "/raw/"+other+"/x.bin", []byte("x"), ps.Auth())
		wantPublishRequired(t, resp, body)
		// A session token reaches no JSON API route outside /api/publish/.
		st, b := s.API(t, http.MethodGet, "/api/me", nil, ps.Auth())
		wantAPIStatus(t, "GET /api/me with a session token", st, b, http.StatusForbidden)
	})

	t.Run("read-only user cannot mint", func(t *testing.T) {
		st, b := s.Mint(t, s.EnrollDevice(t, r), repo)
		wantAPIStatus(t, "mint as a read-only user", st, b, http.StatusForbidden)
	})

	t.Run("lost permission", func(t *testing.T) {
		ps := s.MintSession(t, w, repo)
		s.SetPerms(t, repo, w, "repo.download")
		resp, body := pubPut(t, s, repo, ps.Auth())
		wantStatus(t, resp, body, http.StatusForbidden)
		s.SetPerms(t, repo, w, "repo.download", "repo.publish")
		resp, body = pubPut(t, s, repo, ps.Auth())
		wantStatus(t, resp, body, http.StatusCreated)
	})

	t.Run("closed session", func(t *testing.T) {
		ps, ps2 := s.MintSession(t, w, repo), s.MintSession(t, w, repo)
		resp, body := pubPut(t, s, repo, ps.Auth())
		wantStatus(t, resp, body, http.StatusCreated)
		if st := s.CloseSession(t, ps); st/100 != 2 {
			t.Fatalf("close session: status %d", st)
		}
		resp, body = pubPut(t, s, repo, ps.Auth())
		wantPublishRequired(t, resp, body)
		// Only a session's own token ends it.
		ps3 := s.MintSession(t, w, repo)
		st, b := s.API(t, http.MethodDelete, "/api/publish/sessions/"+ps2.ID, nil, ps3.Auth())
		wantAPIStatus(t, "close a session with another session's token", st, b, http.StatusUnauthorized)
		st, b = s.API(t, http.MethodDelete, "/api/publish/sessions/"+ps2.ID, nil, nil)
		wantAPIStatus(t, "close a session without a token", st, b, http.StatusUnauthorized)
		resp, body = pubPut(t, s, repo, ps2.Auth())
		wantStatus(t, resp, body, http.StatusCreated)
	})

	t.Run("expired session", func(t *testing.T) {
		setSetting(t, s, "publish.session_ttl", "2s")
		ps := s.MintSession(t, w, repo)
		setSetting(t, s, "publish.session_ttl", nil)
		time.Sleep(3 * time.Second)
		resp, body := pubPut(t, s, repo, ps.Auth())
		wantPublishRequired(t, resp, body)
	})

	t.Run("replayed nonce and bad signature", func(t *testing.T) {
		d := s.device(t, w)
		nonce := s.Challenge(t, d.ID)
		sig := d.Sign(nonce, repo)
		mustSession(t, "mint")(s.MintWith(t, d.ID, repo, nonce, sig))
		st, b := s.MintWith(t, d.ID, repo, nonce, sig)
		wantAPIStatus(t, "mint with a used nonce", st, b, http.StatusUnauthorized)

		_, wrong, _ := ed25519.GenerateKey(rand.Reader)
		nonce = s.Challenge(t, d.ID)
		forged := (&Device{ID: d.ID, Key: wrong}).Sign(nonce, repo)
		st, b = s.MintWith(t, d.ID, repo, nonce, forged)
		wantAPIStatus(t, "mint with a signature of another key", st, b, http.StatusUnauthorized)
		// A signature over another repo does not mint for this one.
		nonce = s.Challenge(t, d.ID)
		st, b = s.MintWith(t, d.ID, repo, nonce, d.Sign(nonce, other))
		wantAPIStatus(t, "mint with a signature over another repo", st, b, http.StatusUnauthorized)
	})

	t.Run("revoked device", func(t *testing.T) {
		d := s.EnrollDevice(t, w)
		ps := mustSession(t, "mint")(s.Mint(t, d, repo))
		resp, body := pubPut(t, s, repo, ps.Auth())
		wantStatus(t, resp, body, http.StatusCreated)

		type deviceRow struct {
			ID    int64  `json:"id"`
			State string `json:"state"`
		}
		var page struct {
			Items []deviceRow `json:"items"`
		}
		sess := s.LoginTOTP(t, w)
		s.MustAPI(t, http.MethodGet, "/api/devices", nil, sess, &page)
		if !slices.Contains(page.Items, deviceRow{d.ID, "active"}) {
			t.Fatalf("GET /api/devices: device %d is not listed as active: %+v", d.ID, page.Items)
		}

		// Another user cannot revoke the device.
		st, b := s.API(t, http.MethodDelete, fmt.Sprintf("/api/devices/%d", d.ID), nil, s.SignIn(t, r, "reader-pass-1234"))
		wantAPIStatus(t, "revoke another user's device", st, b, http.StatusNotFound)

		st, b = s.API(t, http.MethodDelete, fmt.Sprintf("/api/devices/%d", d.ID), nil, sess)
		wantAPIStatus(t, "revoke own device", st, b, http.StatusOK, http.StatusNoContent)
		// Revoking ends the device's open sessions at once.
		resp, body = pubPut(t, s, repo, ps.Auth())
		wantPublishRequired(t, resp, body)
		st, b = s.API(t, http.MethodPost, "/api/publish/challenge", map[string]any{"device_id": d.ID}, nil)
		wantAPIStatus(t, "challenge for a revoked device", st, b, http.StatusUnauthorized)

		// An admin revokes any user's device.
		d2 := s.EnrollDevice(t, w)
		st, b = s.API(t, http.MethodDelete, fmt.Sprintf("/api/devices/%d", d2.ID), nil, s.Admin())
		wantAPIStatus(t, "admin revokes a device", st, b, http.StatusOK, http.StatusNoContent)
		st, b = s.API(t, http.MethodPost, "/api/publish/challenge", map[string]any{"device_id": d2.ID}, nil)
		wantAPIStatus(t, "challenge for a device the admin revoked", st, b, http.StatusUnauthorized)
	})

	t.Run("expired device", func(t *testing.T) {
		setSetting(t, s, "publish.device_ttl", "8s")
		d := s.EnrollDevice(t, w)
		setSetting(t, s, "publish.device_ttl", nil)
		ps := mustSession(t, "mint")(s.Mint(t, d, repo))
		time.Sleep(9 * time.Second)
		resp, body := pubPut(t, s, repo, ps.Auth())
		wantPublishRequired(t, resp, body)
		st, b := s.API(t, http.MethodPost, "/api/publish/challenge", map[string]any{"device_id": d.ID}, nil)
		wantAPIStatus(t, "challenge for an expired device", st, b, http.StatusUnauthorized)
	})

	t.Run("manual session", func(t *testing.T) {
		sess := s.LoginTOTP(t, w)
		st, b := s.API(t, http.MethodPost, "/api/publish/sessions/manual", map[string]string{"repo": repo}, Proof{sess, "totp 000000"})
		wantAPIStatus(t, "manual session with a wrong code", st, b, http.StatusUnauthorized)
		// The fresh factor comes in the header X-Telesphorepo-Proof; a body field is not a proof.
		st, b = s.API(t, http.MethodPost, "/api/publish/sessions/manual", map[string]string{"repo": repo, "totp_code": s.TOTPCode(t, w)}, sess)
		wantAPIStatus(t, "manual session without the proof header", st, b, http.StatusBadRequest)
		ps := mustSession(t, "manual session")(s.ManualSession(t, sess, w, repo))
		resp, body := pubPut(t, s, repo, ps.Auth())
		wantStatus(t, resp, body, http.StatusCreated)

		// A user without a second factor signs in with the password but cannot mint.
		u := RandName("nofactor")
		s.CreateUser(t, u, "nofactor-pass-1234")
		s.SetPerms(t, repo, u, "repo.download", "repo.publish")
		plain := s.Login(t, u, "nofactor-pass-1234")
		st, b = s.API(t, http.MethodPost, "/api/publish/sessions/manual", map[string]string{"repo": repo}, Proof{plain, "totp 000000"})
		wantAPIStatus(t, "manual session of a user without a second factor", st, b, http.StatusForbidden)
		if !strings.Contains(string(b), "second_factor_required") {
			t.Fatalf("manual session of a user without a second factor: body %s, want second_factor_required", b)
		}
	})
}

// TestSecondFactorLogin covers the sign-in rule: a person with server permissions
// signs in with a second factor (without one the session reaches only the setup routes); a
// person who only downloads and publishes signs in with the password and needs a factor only to
// approve a device or create a manual publish session.
func TestSecondFactorLogin(t *testing.T) {
	t.Parallel()
	s := StartServer(t, ServerOpts{})
	wantSetupOnly := func(t *testing.T, sess *Session, method, path string) {
		t.Helper()
		st, b := s.API(t, method, path, nil, sess)
		if st != http.StatusForbidden || !strings.Contains(string(b), "second_factor_required") {
			t.Fatalf("%s %s with a setup-only session: status %d, want 403 second_factor_required: %s", method, path, st, b)
		}
	}
	// The bootstrap admin follows the rule too. This runs before s.Admin() enrolls its factors.
	wantSetupOnly(t, s.Login(t, s.AdminUser, s.AdminPass), http.MethodGet, "/api/settings")

	u, pw := RandName("twofa"), "twofa-pass-1234"
	s.CreateUser(t, u, pw, s.ServerPerms(t)...)
	pub, pubPw := RandName("publisher"), "publisher-pass-1234"
	s.CreateUser(t, pub, pubPw)
	type me struct {
		SecondFactorRequired bool `json:"second_factor_required"`
	}

	t.Run("an admin without a second factor reaches setup only", func(t *testing.T) {
		sess := s.Login(t, u, pw)
		var m me
		s.MustAPI(t, http.MethodGet, "/api/me", nil, sess, &m)
		if !m.SecondFactorRequired {
			t.Fatal("GET /api/me of an admin without a second factor: second_factor_required is false")
		}
		wantSetupOnly(t, sess, http.MethodGet, "/api/repos")
		wantSetupOnly(t, sess, http.MethodGet, "/api/settings")
		wantSetupOnly(t, sess, http.MethodGet, "/api/audit")
		s.MustAPI(t, http.MethodGet, "/api/security", nil, sess, nil)
	})

	t.Run("other users sign in with the password", func(t *testing.T) {
		sess := s.Login(t, pub, pubPw)
		var m me
		s.MustAPI(t, http.MethodGet, "/api/me", nil, sess, &m)
		if m.SecondFactorRequired {
			t.Fatal("GET /api/me of a publisher signed in with a password: second_factor_required is true")
		}
		s.MustAPI(t, http.MethodGet, "/api/repos", nil, sess, nil)
		s.MustAPI(t, http.MethodGet, "/api/tokens", nil, sess, nil)

		// A device approval still needs a second factor.
		en, _ := s.StartEnrollment(t, "laptop")
		st, b := s.API(t, http.MethodPost, "/api/devices/approve", map[string]string{"user_code": en.UserCode}, Proof{sess, "totp 000000"})
		wantAPIStatus(t, "approve without a second factor", st, b, http.StatusForbidden)
		if !strings.Contains(string(b), "second_factor_required") {
			t.Fatalf("approve without a second factor: body %s, want second_factor_required", b)
		}

		// With a factor the password still signs in, in one step; the approval takes the code.
		s.EnrollTOTP(t, pub)
		var step struct {
			SecondFactor bool `json:"second_factor"`
		}
		jar, _ := cookiejar.New(nil)
		sess = &Session{Jar: jar}
		s.MustAPI(t, http.MethodPost, "/api/auth/login", map[string]string{"username": pub, "password": pubPw}, sess, &step)
		if step.SecondFactor {
			t.Fatal("password sign-in of a publisher with a factor asks for the second factor")
		}
		s.MustAPI(t, http.MethodGet, "/api/repos", nil, sess, nil)
		st, b = s.ApproveEnrollment(t, sess, pub, en.UserCode)
		wantAPIStatus(t, "approve with a second factor", st, b, http.StatusCreated)
	})

	s.EnrollTOTP(t, u)

	// step1 sends the password step and requires the answer to ask for the second factor.
	step1 := func(t *testing.T) *Session {
		t.Helper()
		jar, _ := cookiejar.New(nil)
		sess := &Session{Jar: jar}
		var a struct {
			SecondFactor bool     `json:"second_factor"`
			Methods      []string `json:"methods"`
		}
		s.MustAPI(t, http.MethodPost, "/api/auth/login", map[string]string{"username": u, "password": pw}, sess, &a)
		if !a.SecondFactor || !slices.Contains(a.Methods, "totp") {
			t.Fatalf("password step: answer %+v, want second_factor with method totp", a)
		}
		return sess
	}
	second := func(t *testing.T, sess *Session, code string) (int, []byte) {
		t.Helper()
		return s.API(t, http.MethodPost, "/api/auth/login/second", map[string]string{"totp_code": code}, sess)
	}

	t.Run("the password alone gives no session", func(t *testing.T) {
		sess := step1(t)
		st, b := s.API(t, http.MethodGet, "/api/me", nil, sess)
		wantAPIStatus(t, "GET /api/me after the password step", st, b, http.StatusUnauthorized)
	})

	t.Run("wrong code, then the right one", func(t *testing.T) {
		st, b := second(t, step1(t), "000000")
		wantAPIStatus(t, "second step with a wrong code", st, b, http.StatusUnauthorized)
		sess := step1(t)
		code := s.TOTPCode(t, u)
		st, b = second(t, sess, code)
		wantAPIStatus(t, "second step with the right code", st, b, http.StatusOK)
		s.MustAPI(t, http.MethodGet, "/api/repos", nil, sess, nil)

		// A code of a step that was used once is refused.
		st, b = second(t, step1(t), code)
		wantAPIStatus(t, "second step with a used code", st, b, http.StatusUnauthorized)
	})

	t.Run("second step without the password step", func(t *testing.T) {
		jar, _ := cookiejar.New(nil)
		st, b := second(t, &Session{Jar: jar}, s.TOTPCode(t, u))
		wantAPIStatus(t, "second step without a pending sign-in", st, b, http.StatusUnauthorized)
	})
}

var userCodeRe = regexp.MustCompile(`^[A-Z]{4}-[A-Z]{4}$`)

func TestDeviceEnrollment(t *testing.T) {
	t.Parallel()
	s := StartServer(t, ServerOpts{})
	repo := RandName("dev")
	s.CreateRepo(t, repo, "maven", false, nil)
	u, pw := RandName("dev"), "dev-pass-1234"
	s.CreateUser(t, u, pw)
	s.SetPerms(t, repo, u, "repo.download", "repo.publish")

	// The enrollment takes the name and the public key only.
	st0, b0 := s.API(t, http.MethodPost, "/api/devices/enroll", map[string]string{"name": "x", "client": "ant", "public_key": b64(make([]byte, 32))}, nil)
	wantAPIStatus(t, "enroll with an unknown field", st0, b0, http.StatusBadRequest)

	en, key := s.StartEnrollment(t, "laptop")
	if !userCodeRe.MatchString(en.UserCode) || en.VerificationURL != s.InternalURL+"/#/device" || en.ExpiresIn <= 0 || en.Interval < 1 {
		t.Fatalf("enrollment %+v: want user code ABCD-EFGH, verification_url %s/#/device, expires_in > 0, interval >= 1", en, s.InternalURL)
	}

	st, a := s.PollEnrollment(t, en.DeviceCode)
	if st != http.StatusBadRequest || a.Error != "authorization_pending" {
		t.Fatalf("poll before approval: status %d, answer %+v, want 400 authorization_pending", st, a)
	}
	st, a = s.PollEnrollment(t, en.DeviceCode)
	if st != http.StatusBadRequest || a.Error != "slow_down" {
		t.Fatalf("second poll at once: status %d, answer %+v, want 400 slow_down", st, a)
	}

	// A sign-in without a second factor cannot approve.
	setup := s.Login(t, u, pw)
	st2, b := s.API(t, http.MethodPost, "/api/devices/approve", map[string]string{"user_code": en.UserCode}, Proof{setup, "totp 000000"})
	wantAPIStatus(t, "approve from a setup-only sign-in", st2, b, http.StatusForbidden)

	sess := s.LoginTOTP(t, u)
	st2, b = s.API(t, http.MethodPost, "/api/devices/approve", map[string]string{"user_code": en.UserCode}, Proof{sess, "totp 000000"})
	wantAPIStatus(t, "approve with a wrong code", st2, b, http.StatusUnauthorized)
	st2, b = s.API(t, http.MethodPost, "/api/devices/approve", map[string]string{"user_code": "BCDF-GHJK"}, s.TOTPProof(t, sess, u))
	wantAPIStatus(t, "approve an unknown code", st2, b, http.StatusNotFound)
	st2, b = s.ApproveEnrollment(t, sess, u, en.UserCode)
	wantAPIStatus(t, "approve", st2, b, http.StatusOK, http.StatusCreated)
	st2, b = s.ApproveEnrollment(t, sess, u, en.UserCode)
	wantAPIStatus(t, "approve the same code again", st2, b, http.StatusConflict)

	time.Sleep(time.Duration(en.Interval) * time.Second)
	st, a = s.PollEnrollment(t, en.DeviceCode)
	if st != http.StatusOK || a.DeviceID == 0 {
		t.Fatalf("poll after approval: status %d, answer %+v, want 200 with device_id", st, a)
	}
	// The device code answers once.
	time.Sleep(time.Duration(en.Interval) * time.Second)
	st, a2 := s.PollEnrollment(t, en.DeviceCode)
	if st != http.StatusBadRequest || a2.Error != "expired_token" {
		t.Fatalf("poll after the device id was taken: status %d, answer %+v, want 400 expired_token", st, a2)
	}

	// The device has the name it enrolled with.
	var devices struct {
		Items []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"items"`
	}
	s.MustAPI(t, http.MethodGet, "/api/devices", nil, sess, &devices)
	if len(devices.Items) != 1 || devices.Items[0].ID != a.DeviceID || devices.Items[0].Name != "laptop" {
		t.Fatalf("devices: %+v, want the laptop device", devices.Items)
	}

	d := &Device{ID: a.DeviceID, User: u, Key: key}
	ps := mustSession(t, "mint with the new device")(s.Mint(t, d, repo))
	resp, body := pubPut(t, s, repo, ps.Auth())
	wantStatus(t, resp, body, http.StatusCreated)
}

// TestDeviceEnrollmentExpiry moves the expiry of an enrollment into the past in the database
// while the server is stopped: the poll answers expired_token and the code can no longer be approved.
func TestDeviceEnrollmentExpiry(t *testing.T) {
	t.Parallel()
	vol := NewDataVolume(t)
	s := StartServer(t, ServerOpts{DataVolume: vol})
	u := RandName("dev")
	s.CreateUser(t, u, "dev-pass-1234")
	en, _ := s.StartEnrollment(t, "old laptop")

	s.RestartAfter(t, func() {
		editDB(t, vol, "UPDATE device_enrollments SET expires_at = 1 WHERE user_code = ?", en.UserCode)
	})

	st, a := s.PollEnrollment(t, en.DeviceCode)
	if st != http.StatusBadRequest || a.Error != "expired_token" {
		t.Fatalf("poll of an expired enrollment: status %d, answer %+v, want 400 expired_token", st, a)
	}
	st, b := s.ApproveEnrollment(t, s.LoginTOTP(t, u), u, en.UserCode)
	wantAPIStatus(t, "approve an expired code", st, b, http.StatusNotFound)
}

// trustPolicy is the body of POST /api/publish/trusts and PATCH /api/publish/trusts/{id}.
type trustPolicy struct {
	Name     string              `json:"name"`
	Issuer   string              `json:"issuer"`
	Audience string              `json:"audience"`
	Claims   map[string][]string `json:"claims"`
	JWKS     string              `json:"jwks"`
	Repos    []string            `json:"repos"`
	Disabled bool                `json:"disabled"`
}

// createTrust creates the policy as the admin and returns its id.
func createTrust(t *testing.T, s *Server, p trustPolicy) int64 {
	t.Helper()
	var out struct {
		ID int64 `json:"id"`
	}
	s.MustAPI(t, http.MethodPost, "/api/publish/trusts", p, s.Admin(), &out)
	if out.ID == 0 {
		t.Fatalf("POST /api/publish/trusts %s: no id in the answer", p.Name)
	}
	return out.ID
}
