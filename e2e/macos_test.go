//go:build macos

package e2e

// The macOS zone (tag macos): the release job of release.yml on macos-latest runs it. It needs no
// container engine. It starts the darwin server binary of "go tool task build" (E2E_SERVER_BIN),
// installs the CLI with install.sh, and runs setup and publish for a Maven and a raw project. It
// needs curl, Java and Maven on the PATH.

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
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

// macServer is a server process on 127.0.0.1.
type macServer struct {
	base string
	log  *syncBuffer
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

// macUser is a user with TOTP factors. Each factor gives one code per step of its period; last
// holds the last step used of each factor.
type macUser struct {
	name, pass string
	keys       []*otp.Key
	last       []int64
}

// TestMacOS: on macOS, install.sh installs the CLI of the server, setup enrolls the computer
// and writes telesphorepo.toml, and publish runs mvn deploy for a Maven project and uploads a
// file to a raw repo.
func TestMacOS(t *testing.T) {
	t.Parallel()
	bin := os.Getenv("E2E_SERVER_BIN")
	if bin == "" {
		t.Fatal("set E2E_SERVER_BIN to the server binary of go tool task build")
	}
	s := startMacServer(t, bin)
	admin := &macUser{name: "admin", pass: "admin-pass-macos-1234"}
	adminJar := s.signIn(t, admin)

	mvn, raw := "releases", "files"
	for _, r := range [][2]string{{mvn, "maven"}, {raw, "raw"}} {
		s.mustAPI(t, adminJar, "", http.MethodPost, "/api/repos", map[string]any{"name": r[0], "format": r[1], "anonymous_read": false}, nil)
	}
	dev := &macUser{name: "dev", pass: "dev-pass-macos-1234"}
	s.mustAPI(t, adminJar, "", http.MethodPost, "/api/users", map[string]any{"username": dev.name, "password": dev.pass, "permissions": []string{}}, nil)
	s.mustAPI(t, adminJar, "", http.MethodPut, "/api/grants", map[string]any{"users": []string{dev.name}, "repos": []string{mvn, raw},
		"perms": []string{"repo.download", "repo.publish"}}, nil)
	devJar := s.signIn(t, dev)

	home := t.TempDir()
	app := filepath.Join(t.TempDir(), "app")
	files := filepath.Join(t.TempDir(), "files")
	for _, d := range []string{filepath.Join(app, "src", "main", "java", "com", "example"), files} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(app, "pom.xml"), `<project xmlns="http://maven.apache.org/POM/4.0.0">
  <modelVersion>4.0.0</modelVersion>
  <groupId>com.example</groupId>
  <artifactId>macos-demo</artifactId>
  <version>1.0</version>
  <properties>
    <maven.compiler.release>17</maven.compiler.release>
    <project.build.sourceEncoding>UTF-8</project.build.sourceEncoding>
  </properties>
</project>
`)
	writeFile(t, filepath.Join(app, "src", "main", "java", "com", "example", "Hello.java"),
		"package com.example;\n\npublic class Hello {\n  public static String hello() { return \"hello\"; }\n}\n")
	writeFile(t, filepath.Join(files, "hello.txt"), "hello from macOS\n")

	install := func(link string) string {
		return "curl -fsSL " + s.base + "/install.sh | sh -s -- setup '" + link + "'"
	}

	run(t, home, app, install(s.setupLink(t, devJar, dev, mvn)))
	wantProjectRepo(t, app, mvn)
	run(t, home, app, "telesphorepo publish")
	s.wantFile(t, admin, "/maven/"+mvn+"/com/example/macos-demo/1.0/macos-demo-1.0.jar", nil)

	run(t, home, files, install(s.setupLink(t, devJar, dev, raw)))
	wantProjectRepo(t, files, raw)
	run(t, home, files, "telesphorepo publish hello.txt")
	s.wantFile(t, admin, "/raw/"+raw+"/hello.txt", []byte("hello from macOS\n"))

	for _, name := range []string{"telesphorepo", "docker-credential-telesphorepo"} {
		if _, err := os.Stat(filepath.Join(home, ".local", "bin", name)); err != nil {
			t.Fatalf("install.sh did not make %s: %v", name, err)
		}
	}
}

// startMacServer starts the server binary with a new data folder on a free port of 127.0.0.1
// and waits for /healthz. The test stops it at the end.
func startMacServer(t *testing.T, bin string) *macServer {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	s := &macServer{base: "http://" + addr, log: &syncBuffer{}}
	cmd := exec.Command(bin, "serve")
	cmd.Env = append(os.Environ(), "TR_DATA_DIR="+t.TempDir(), "TR_LISTEN="+addr, "TR_BASE_URL="+s.base, "TR_COOKIE_SECURE=false",
		"TR_BOOTSTRAP_ADMIN_USER=admin", "TR_BOOTSTRAP_ADMIN_PASSWORD=admin-pass-macos-1234")
	cmd.Stdout, cmd.Stderr = s.log, s.log
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", bin, err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if t.Failed() {
			t.Logf("server output:\n%s", s.log.String())
		}
	})
	for deadline := time.Now().Add(90 * time.Second); ; {
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

// api sends a request as the web UI does, with the cookies of jar and the second-factor proof
// proof ("" for none). It returns the status and the body.
func (s *macServer) api(t *testing.T, jar http.CookieJar, proof, method, path string, body any) (int, []byte) {
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
func (s *macServer) mustAPI(t *testing.T, jar http.CookieJar, proof, method, path string, body, out any) {
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
func (s *macServer) signIn(t *testing.T, u *macUser) http.CookieJar {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	creds := map[string]string{"username": u.name, "password": u.pass}
	s.mustAPI(t, jar, "", http.MethodPost, "/api/auth/login", creds, nil)
	for i := range 3 {
		var setup struct {
			SetupID    string `json:"setup_id"`
			OTPAuthURL string `json:"otpauth_url"`
		}
		s.mustAPI(t, jar, "", http.MethodPost, "/api/security/totp/begin", map[string]string{"name": fmt.Sprintf("macos %d", i+1)}, &setup)
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
func (u *macUser) freshCode(t *testing.T) string {
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
func (u *macUser) code(t *testing.T, i int) string {
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

// setupLink makes a setup code of u for repo, as the Publish tab does, and returns its link.
func (s *macServer) setupLink(t *testing.T, jar http.CookieJar, u *macUser, repo string) string {
	t.Helper()
	var c struct {
		Link string `json:"link"`
	}
	s.mustAPI(t, jar, "totp "+u.freshCode(t), http.MethodPost, "/api/publish/setup-codes", map[string]string{"repo": repo}, &c)
	if !strings.HasPrefix(c.Link, s.base+"/") {
		t.Fatalf("POST /api/publish/setup-codes for %s: link %q", repo, c.Link)
	}
	return c.Link
}

// wantFile fails the test unless u reads path with status 200, and the content want when it is
// not nil.
func (s *macServer) wantFile(t *testing.T, u *macUser, path string, want []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, s.base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(u.name, u.pass)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || (want != nil && !bytes.Equal(b, want)) {
		t.Fatalf("GET %s: status %d, %d bytes", path, resp.StatusCode, len(b))
	}
}

// run runs script with sh in dir, as a person runs it on a Mac: HOME is home, ~/.local/bin comes
// first on the PATH, and no CI variable is set. It fails the test on a non-zero exit and returns
// the output.
func run(t *testing.T, home, dir, script string) string {
	t.Helper()
	cmd := exec.Command("sh", "-c", script)
	cmd.Dir = dir
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch {
		case k == "CI", k == "HOME", k == "PATH", strings.HasPrefix(k, "GITHUB_"), strings.HasPrefix(k, "ACTIONS_"), strings.HasPrefix(k, "JENKINS_"):
			continue
		}
		cmd.Env = append(cmd.Env, kv)
	}
	cmd.Env = append(cmd.Env, "HOME="+home, "PATH="+filepath.Join(home, ".local", "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	t.Logf("$ %s\n%s", script, out)
	if err != nil {
		t.Fatalf("%s: %v", script, err)
	}
	return string(out)
}

func writeFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// wantProjectRepo fails the test unless the telesphorepo.toml that setup wrote in dir names repo.
func wantProjectRepo(t *testing.T, dir, repo string) {
	t.Helper()
	name := filepath.Join(dir, "telesphorepo.toml")
	var p struct {
		Repo string `toml:"repo"`
	}
	if _, err := toml.DecodeFile(name, &p); err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	if p.Repo != repo {
		t.Fatalf("%s names repo %q, want %q", name, p.Repo, repo)
	}
}
