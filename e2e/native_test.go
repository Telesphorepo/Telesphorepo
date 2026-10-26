//go:build macos || scale

package e2e

// Helpers of the zones that run the server binary as a process of this computer, with no
// container engine: the macOS zone and the scale zone.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

// nativeServer is a server process on 127.0.0.1.
type nativeServer struct {
	base string
	log  *syncBuffer
	cmd  *exec.Cmd
	once sync.Once
}

// syncBuffer is the output of the server process.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// totpUser is a user with TOTP factors. Each factor gives one code per step of its period; last
// holds the last step used of each factor.
type totpUser struct {
	name, pass string
	keys       []*otp.Key
	last       []int64
}

// startNativeServer runs "bin serve" on the data folder dir on a free port of 127.0.0.1 with the
// extra environment env, and waits for /healthz. start starts the command (nil: cmd.Start). The
// server stops at the end of the test, or earlier with stop.
func startNativeServer(t *testing.T, bin, dir string, env []string, start func(*exec.Cmd) error) *nativeServer {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	s := &nativeServer{base: "http://" + addr, log: &syncBuffer{}}
	cmd := exec.Command(bin, "serve")
	cmd.Env = append(os.Environ(), "TR_DATA_DIR="+dir, "TR_LISTEN="+addr, "TR_BASE_URL="+s.base, "TR_COOKIE_SECURE=false")
	cmd.Env = append(cmd.Env, env...)
	cmd.Stdout, cmd.Stderr = s.log, s.log
	s.cmd = cmd
	if start == nil {
		start = (*exec.Cmd).Start
	}
	if err := start(cmd); err != nil {
		t.Fatalf("start %s: %v", bin, err)
	}
	t.Cleanup(func() {
		s.stop()
		if t.Failed() {
			t.Logf("server %s output:\n%s", s.base, s.log.String())
		}
	})
	for deadline := time.Now().Add(5 * time.Minute); ; {
		if resp, err := http.Get(s.base + "/healthz"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return s
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the server at %s did not answer /healthz:\n%s", s.base, s.log.String())
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// stop ends the server process and waits for it.
func (s *nativeServer) stop() {
	s.once.Do(func() {
		_ = s.cmd.Process.Kill()
		_ = s.cmd.Wait()
	})
}

// api sends a request as the web UI does, with the cookies of jar and the second-factor proof
// proof ("" for none). It returns the status and the body.
func (s *nativeServer) api(t *testing.T, jar http.CookieJar, proof, method, path string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, s.base+path, r)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("X-Telesphorepo-UI", "1")
	if proof != "" {
		req.Header.Set("X-Telesphorepo-Proof", proof)
	}
	c := &http.Client{Jar: jar, Timeout: time.Minute}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// mustAPI is api that fails the test unless the status is 2xx, and decodes the body into out.
func (s *nativeServer) mustAPI(t *testing.T, jar http.CookieJar, proof, method, path string, body, out any) {
	t.Helper()
	st, b := s.api(t, jar, proof, method, path, body)
	if st/100 != 2 {
		t.Fatalf("%s %s: status %d: %s", method, path, st, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s %s: decode %s: %v", method, path, b, err)
		}
	}
}

// signIn adds three TOTP factors to u with a password sign-in, then signs in with the password
// and, when the server asks for it, a TOTP code. It returns the cookies of the UI session.
func (s *nativeServer) signIn(t *testing.T, u *totpUser) http.CookieJar {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	creds := map[string]string{"username": u.name, "password": u.pass}
	s.mustAPI(t, jar, "", http.MethodPost, "/api/auth/login", creds, nil)
	for i := range 3 {
		var setup struct {
			SetupID    string `json:"setup_id"`
			OTPAuthURL string `json:"otpauth_url"`
		}
		s.mustAPI(t, jar, "", http.MethodPost, "/api/security/totp/begin", map[string]string{"name": fmt.Sprintf("factor %d", i+1)}, &setup)
		key, err := otp.NewKeyFromURL(setup.OTPAuthURL)
		if err != nil {
			t.Fatalf("otpauth_url %q of the TOTP setup: %v", setup.OTPAuthURL, err)
		}
		u.keys, u.last = append(u.keys, key), append(u.last, 0)
		s.mustAPI(t, jar, "", http.MethodPost, "/api/security/totp/confirm", map[string]string{"setup_id": setup.SetupID, "code": u.code(t, i)}, nil)
	}
	jar, _ = cookiejar.New(nil)
	var step struct {
		SecondFactor bool `json:"second_factor"`
	}
	s.mustAPI(t, jar, "", http.MethodPost, "/api/auth/login", creds, &step)
	if step.SecondFactor {
		s.mustAPI(t, jar, "", http.MethodPost, "/api/auth/login/second", map[string]string{"totp_code": u.freshCode(t)}, nil)
	}
	return jar
}

// freshCode returns a code of the factor of u that has gone longest without one.
func (u *totpUser) freshCode(t *testing.T) string {
	t.Helper()
	best := 0
	for i := range u.last {
		if u.last[i] < u.last[best] {
			best = i
		}
	}
	return u.code(t, best)
}

// code returns a code of factor i for a step after its last used one. The server accepts one step
// either side of its clock, so the code is for the current step or the next one; when both are
// used, it waits for the next step.
func (u *totpUser) code(t *testing.T, i int) string {
	t.Helper()
	k := u.keys[i]
	period := int64(k.Period())
	for {
		now := time.Now().Unix()
		cur := now / period
		step := max(u.last[i]+1, cur)
		if step <= cur+1 {
			u.last[i] = step
			c, err := totp.GenerateCodeCustom(k.Secret(), time.Unix(step*period, 0),
				totp.ValidateOpts{Period: uint(period), Digits: k.Digits(), Algorithm: k.Algorithm()})
			if err != nil {
				t.Fatal(err)
			}
			return c
		}
		time.Sleep(time.Until(time.Unix((step-1)*period+1, 0)))
	}
}
