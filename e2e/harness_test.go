//go:build e2e || import || scale || clients

package e2e

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	osexec "os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/moby/moby/api/types/container"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Shared named volumes for build-tool caches.
const (
	M2Volume     = "telesphorepo-e2e-m2"
	GradleVolume = "telesphorepo-e2e-gradle"
)

// ---------------------------------------------------------------- server

type ServerOpts struct {
	Alias      string            // network alias; default "repo-<random>"
	Env        map[string]string // overrides/extra TR_* vars (applied last)
	Storage    string            // "fs" (default) | "s3"
	S3         *S3Target         // required when Storage == "s3"
	Files      []ContainerFile   // extra files copied into the server container
	DataVolume string            // named volume mounted at /data; default: the container's own
	// NoBootstrapAdmin omits TR_BOOTSTRAP_ADMIN_*: the server creates "admin" with a random
	// password and prints it in its startup banner; StartServer reads it from the logs.
	NoBootstrapAdmin bool
	// Aliases are extra network aliases of the server on TestNetwork.
	Aliases []string
	// TLS makes the harness reach the server over HTTPS (the server needs TR_TLS_CERT_FILE and
	// TR_TLS_KEY_FILE in Env and the files in Files); it accepts any certificate.
	TLS bool
}

type Server struct {
	testcontainers.Container
	URL         string // host-mapped base URL, e.g. http://localhost:PORT
	InternalURL string // http://<alias>:8080, reachable from containers on TestNetwork
	Alias       string
	AdminUser   string
	AdminPass   string

	mu        sync.Mutex
	users     map[string]*harnessUser // what the harness knows about each user (see publish helpers)
	adminSess *Session                // see Admin
}

// StartServer starts a server container on TestNetwork and waits for /healthz.
func StartServer(t testing.TB, opts ServerOpts) *Server {
	t.Helper()
	ctx := context.Background()
	if opts.Alias == "" {
		opts.Alias = RandName("repo")
	}
	adminPass := randHex(16)
	scheme := "http"
	if opts.TLS {
		scheme = "https"
	}
	env := map[string]string{
		"TR_COOKIE_SECURE":   "false",
		"TR_INDEX_DEBOUNCE":  "300ms",
		"TR_INDEX_MAX_DELAY": "3s",
		"TR_LOG_LEVEL":       "debug",
		"TR_BASE_URL":        scheme + "://" + opts.Alias + ":8080",
	}
	if !opts.NoBootstrapAdmin {
		env["TR_BOOTSTRAP_ADMIN_USER"] = "admin"
		env["TR_BOOTSTRAP_ADMIN_PASSWORD"] = adminPass
	}
	var files []testcontainers.ContainerFile
	switch opts.Storage {
	case "", "fs":
	case "s3":
		if opts.S3 == nil {
			t.Fatal("StartServer: Storage s3 requires S3 target")
		}
		for k, v := range opts.S3.ServerEnv() {
			env[k] = v
		}
	default:
		t.Fatalf("StartServer: unknown storage %q", opts.Storage)
	}
	for k, v := range opts.Env {
		env[k] = v
	}
	for _, f := range opts.Files {
		files = append(files, f.tc())
	}
	var mounts []testcontainers.ContainerMount
	if opts.DataVolume != "" {
		mounts = append(mounts, testcontainers.VolumeMount(opts.DataVolume, "/data"))
	}

	c, err := testcontainers.Run(ctx, ServerImage,
		testcontainers.WithLabels(e2eLabels),
		testcontainers.WithEnv(env),
		testcontainers.WithExposedPorts("8080/tcp"),
		testcontainers.WithFiles(files...),
		testcontainers.WithMounts(mounts...),
		network.WithNetwork(append([]string{opts.Alias}, opts.Aliases...), TestNetwork),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/healthz").WithPort("8080/tcp").WithTLS(opts.TLS).WithAllowInsecure(opts.TLS).
			WithStartupTimeout(90*time.Second)),
	)
	registerCleanup(t, c, "server "+opts.Alias)
	if err != nil {
		t.Fatalf("start server: %v", err)
	}
	ep, err := c.PortEndpoint(ctx, "8080/tcp", scheme)
	if err != nil {
		t.Fatalf("server endpoint: %v", err)
	}
	srv := &Server{Container: c, URL: ep, InternalURL: scheme + "://" + opts.Alias + ":8080", Alias: opts.Alias, AdminUser: "admin", AdminPass: adminPass}
	if opts.NoBootstrapAdmin {
		srv.AdminPass = srv.BannerPassword(t)
	}
	return srv
}

// bannerPasswordRe matches the "password: <pw>" line of the first-start admin banner; the
// value stops at whitespace or a box-drawing border character.
var bannerPasswordRe = regexp.MustCompile(`(?m)password:[ \t]*([^\s│|║┃*#]+)`)

// BannerPassword returns the random admin password the server printed on first start
// (no TR_BOOTSTRAP_ADMIN_*). It polls the logs briefly, since log delivery may lag /healthz.
func (s *Server) BannerPassword(t testing.TB) string {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		logs := s.Logs(t)
		if m := bannerPasswordRe.FindAllStringSubmatch(logs, -1); len(m) > 0 {
			return m[len(m)-1][1]
		}
		if time.Now().After(deadline) {
			t.Fatalf("no \"password: ...\" bootstrap banner in server logs:\n%s", logs)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// Exec runs "<engine CLI> exec -i <container> args..." on the host with stdin fed from the
// given string, returning the exit code and combined output. It goes through the real CLI
// (not the API) so piped-stdin behaviour matches what an operator gets.
func (s *Server) Exec(t testing.TB, stdin string, args ...string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	argv := append([]string{"exec", "-i", s.GetContainerID()}, args...)
	cmd := osexec.CommandContext(ctx, Engine.CLI, argv...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		var ee *osexec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode(), string(out)
		}
		t.Fatalf("%s %s: %v\n%s", Engine.CLI, strings.Join(argv, " "), err, out)
	}
	return 0, string(out)
}

// registerCleanup terminates c at test end, dumping its logs first if the test failed.
func registerCleanup(t testing.TB, c *testcontainers.DockerContainer, label string) {
	if c == nil {
		return
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("---- logs of %s ----\n%s", label, containerLogs(c))
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_ = c.Terminate(ctx)
	})
}

func containerLogs(c testcontainers.Container) string {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rc, err := c.Logs(ctx)
	if err != nil {
		return "(logs unavailable: " + err.Error() + ")"
	}
	defer rc.Close()
	b, _ := io.ReadAll(rc)
	return string(b)
}

// Logs returns the server container's combined output so far.
func (s *Server) Logs(t testing.TB) string { t.Helper(); return containerLogs(s.Container) }

// Restart stops the server container gracefully (SIGTERM) and starts it again, then waits for
// /healthz. The container keeps its /data, env and network alias; URL follows the new host port.
func (s *Server) Restart(t testing.TB) { t.Helper(); s.RestartAfter(t, nil) }

// RestartAfter is Restart that runs whileStopped (when non-nil) between stop and start, e.g. to
// edit the data volume.
func (s *Server) RestartAfter(t testing.TB, whileStopped func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := s.Stop(ctx, nil); err != nil {
		t.Fatalf("stop server %s: %v", s.Alias, err)
	}
	if whileStopped != nil {
		whileStopped()
	}
	if err := s.Start(ctx); err != nil {
		t.Fatalf("start server %s again: %v\n%s", s.Alias, err, containerLogs(s.Container))
	}
	ep, err := s.PortEndpoint(ctx, "8080/tcp", "http")
	if err != nil {
		t.Fatalf("server endpoint after restart: %v", err)
	}
	s.URL = ep
}

// ---------------------------------------------------------------- data volumes

// volumeToolImage runs shell tools against a data volume. It is the curl client image, so the
// run pulls no extra image.
const volumeToolImage = "curlimages/curl:8.16.0"

// NewDataVolume returns the name of a new named volume for ServerOpts.DataVolume. The volume is
// removed at test end, after the containers that use it.
func NewDataVolume(t testing.TB) string {
	t.Helper()
	vol := RandName("telesphorepo-data")
	t.Cleanup(func() {
		if out, err := osexec.Command(Engine.CLI, "volume", "rm", "-f", vol).CombinedOutput(); err != nil {
			t.Errorf("remove volume %s: %v\n%s", vol, err, out)
		}
	})
	return vol
}

// VolumeExec runs script with sh as root in a throwaway container that mounts vol at /data,
// with files copied in first, and returns its output. A non-zero exit fails the test.
func VolumeExec(t testing.TB, vol, script string, files ...ContainerFile) string {
	t.Helper()
	files = append(files, ContainerFile{Path: "/volume.sh", Content: script})
	code, out := RunClient(t, volumeToolImage, ClientOpts{
		Cmd:     []string{"sh /volume.sh"},
		Files:   files,
		Mounts:  map[string]string{vol: "/data"},
		User:    "0",
		Timeout: 5 * time.Minute,
	})
	if code != 0 {
		t.Fatalf("volume %s: script exited %d:\n%s\n%s", vol, code, script, out)
	}
	return out
}

var sha256sumLineRe = regexp.MustCompile(`(?m)^([0-9a-f]{64})  \./(\S+)$`)

// FSBlobs returns the SHA-256 of every blob of the fs store at /data/blobs on vol (the default
// fs root), keyed by storage key.
func FSBlobs(t testing.TB, vol string) map[string]string {
	t.Helper()
	out := VolumeExec(t, vol, "set -e\nif [ -d /data/blobs ]; then cd /data/blobs && find . -type f ! -name '.tmp-*' -exec sha256sum {} +; fi\n")
	blobs := map[string]string{}
	for _, m := range sha256sumLineRe.FindAllStringSubmatch(out, -1) {
		blobs[m[2]] = m[1]
	}
	return blobs
}

// ---------------------------------------------------------------- storage moves

// Move is a storage move as the API returns it.
type Move struct {
	ID      int64          `json:"id"`
	State   string         `json:"state"`
	Total   int64          `json:"total"`
	Copied  int64          `json:"copied"`
	Skipped int64          `json:"skipped"`
	Failed  int64          `json:"failed"`
	Error   string         `json:"error"`
	Target  map[string]any `json:"target"`
}

// MoveFailure is one row of GET /api/storage/moves/{id}/failures.
type MoveFailure struct {
	BlobID int64  `json:"blob_id"`
	Key    string `json:"key"`
	Reason string `json:"reason"`
}

// StartMove PATCHes /api/settings with a storage change and requires 202 {move}.
func (s *Server) StartMove(t testing.TB, patch map[string]any) Move {
	t.Helper()
	st, b := s.API(t, http.MethodPatch, "/api/settings", patch, s.Admin())
	if st != http.StatusAccepted {
		t.Fatalf("PATCH /api/settings (storage change): status %d, want 202: %s", st, b)
	}
	var out struct {
		Move Move `json:"move"`
	}
	if err := json.Unmarshal(b, &out); err != nil || out.Move.ID == 0 {
		t.Fatalf("PATCH /api/settings: no move in 202 body %s (err %v)", b, err)
	}
	return out.Move
}

// GetMove fetches GET /api/storage/moves/{id}.
func (s *Server) GetMove(t testing.TB, id int64) Move {
	t.Helper()
	var m Move
	s.MustAPI(t, http.MethodGet, fmt.Sprintf("/api/storage/moves/%d", id), nil, s.Admin(), &m)
	return m
}

// WaitMove polls the move until it leaves the copying state and returns it. It fails the test
// when the move still copies after timeout.
func (s *Server) WaitMove(t testing.TB, id int64, timeout time.Duration) Move {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		m := s.GetMove(t, id)
		if m.State != "copying" {
			return m
		}
		if time.Now().After(deadline) {
			t.Fatalf("move %d still copying after %s: %+v", id, timeout, m)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// MoveFailures lists every failure row of the move, following the keyset pages.
func (s *Server) MoveFailures(t testing.TB, id int64) []MoveFailure {
	t.Helper()
	var all []MoveFailure
	after := ""
	for {
		var page struct {
			Items []MoveFailure `json:"items"`
			Next  *string       `json:"next"`
		}
		p := fmt.Sprintf("/api/storage/moves/%d/failures?limit=100", id)
		if after != "" {
			p += "&after=" + url.QueryEscape(after)
		}
		s.MustAPI(t, http.MethodGet, p, nil, s.Admin(), &page)
		all = append(all, page.Items...)
		if page.Next == nil || *page.Next == "" {
			return all
		}
		after = *page.Next
	}
}

// ---------------------------------------------------------------- HTTP / API

// Auth decorates a request with credentials. nil means anonymous.
type Auth interface{ apply(*http.Request) }

// Basic is HTTP Basic auth (use Basic{User: "x", Pass: token} for API tokens).
type Basic struct{ User, Pass string }

func (b Basic) apply(r *http.Request) { r.SetBasicAuth(b.User, b.Pass) }

// Session carries a cookie jar holding tr_session (see Server.Login). It sends the
// Sec-Fetch-Site header of a browser on the same origin, so http.CrossOriginProtection treats
// it like the web UI.
type Session struct {
	Jar http.CookieJar

	mu     sync.Mutex
	signIn func(t testing.TB) http.CookieJar // signs in on first use (Server.Admin); nil after
}

func (s *Session) apply(r *http.Request) {
	if r.Header.Get("Sec-Fetch-Site") == "" {
		r.Header.Set("Sec-Fetch-Site", "same-origin")
	}
	r.Header.Set("X-Telesphorepo-UI", "1") // the web UI's API client sends it
}

// Proof is the UI session Sess with a fresh second factor in the header X-Telesphorepo-Proof:
// "totp <code>", "passkey <base64url assertion>" or "grant <grant>" (the routes with the
// FreshFactor policy: device approval, manual publish sessions).
type Proof struct {
	Sess  *Session
	Value string
}

func (p Proof) apply(r *http.Request) {
	p.Sess.apply(r)
	r.Header.Set("X-Telesphorepo-Proof", p.Value)
}

// TOTPProof is sess with a fresh TOTP code of user as the proof.
func (s *Server) TOTPProof(t testing.TB, sess *Session, user string) Proof {
	t.Helper()
	return Proof{sess, "totp " + s.TOTPCode(t, user)}
}

// sessionOf returns the UI session that auth carries, or nil.
func sessionOf(auth Auth) *Session {
	switch a := auth.(type) {
	case *Session:
		return a
	case Proof:
		return a.Sess
	}
	return nil
}

// ready signs in a lazy session on its first use.
func (s *Session) ready(t testing.TB) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.signIn != nil {
		s.Jar = s.signIn(t)
		s.signIn = nil
	}
}

// Admin returns the UI session of the bootstrap admin, signed in with the password and a TOTP
// code. Every API route that changes state, and every admin route, needs a UI session (spec
// "Auth model"). The first request signs in: the harness enrolls TOTP factors for the admin
// through the setup-only session, then signs in with both steps. The session is kept per server.
func (s *Server) Admin() *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.adminSess == nil {
		s.adminSess = &Session{signIn: func(t testing.TB) http.CookieJar {
			return s.LoginTOTP(t, s.AdminUser).Jar
		}}
	}
	return s.adminSess
}

// AdminBasic returns HTTP Basic credentials of the bootstrap admin: for format reads and the
// GET routes of the API that are not admin routes.
func (s *Server) AdminBasic() Basic { return Basic{s.AdminUser, s.AdminPass} }

// HTTPClient never follows redirects (S3 presigned redirects point at network-internal hosts).
func (s *Server) HTTPClient(auth Auth) *http.Client {
	c := &http.Client{
		Timeout:       2 * time.Minute,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	if sess := sessionOf(auth); sess != nil {
		c.Jar = sess.Jar
	}
	return c
}

// NewRequest builds a request against s.URL+path.
func (s *Server) NewRequest(t testing.TB, method, path string, body io.Reader) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, s.URL+path, body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	return req
}

// Do sends req with auth and returns the response (body already read and closed) plus body bytes.
func (s *Server) Do(t testing.TB, req *http.Request, auth Auth) (*http.Response, []byte) {
	t.Helper()
	if sess := sessionOf(auth); sess != nil {
		sess.ready(t)
	}
	if auth != nil {
		auth.apply(req)
	}
	resp, err := s.HTTPClient(auth).Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL.Path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s %s: read body: %v", req.Method, req.URL.Path, err)
	}
	return resp, b
}

// API sends a request to path (e.g. "/api/me"). body: nil, []byte, string, io.Reader, or any JSON-marshalable value.
func (s *Server) API(t testing.TB, method, path string, body any, auth Auth) (int, []byte) {
	t.Helper()
	var r io.Reader
	isJSON := false
	switch b := body.(type) {
	case nil:
	case []byte:
		r = bytes.NewReader(b)
	case string:
		r = strings.NewReader(b)
	case io.Reader:
		r = b
	default:
		j, err := json.Marshal(b)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		r, isJSON = bytes.NewReader(j), true
	}
	req := s.NewRequest(t, method, path, r)
	if isJSON {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, b := s.Do(t, req, auth)
	return resp.StatusCode, b
}

// MustAPI is API that fails the test unless the status is 2xx; decodes JSON into out when non-nil.
func (s *Server) MustAPI(t testing.TB, method, path string, body any, auth Auth, out any) {
	t.Helper()
	st, b := s.API(t, method, path, body, auth)
	if st < 200 || st > 299 {
		t.Fatalf("%s %s: status %d: %s", method, path, st, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s %s: decode %s: %v", method, path, b, err)
		}
	}
}

// Login performs POST /api/auth/login and returns a Session holding the cookie.
func (s *Server) Login(t testing.TB, user, pass string) *Session {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	sess := &Session{Jar: jar}
	s.MustAPI(t, http.MethodPost, "/api/auth/login", map[string]string{"username": user, "password": pass}, sess, nil)
	return sess
}

// SignIn returns a full UI session of user, the way the web UI signs in. A user without a
// second factor signs in with the password alone; a person who needs the second factor (a
// server permission, or a repository permission other than download and publish) and has none
// gets a setup-only session, so the harness then enrolls TOTP factors and signs in with both
// steps (LoginTOTP).
// A user with factors that the harness enrolled signs in with LoginTOTP. pass is remembered
// for later sign-ins.
func (s *Server) SignIn(t testing.TB, user, pass string) *Session {
	t.Helper()
	u := s.user(user)
	u.pass = pass
	if len(u.factors) > 0 {
		return s.LoginTOTP(t, user)
	}
	jar, _ := cookiejar.New(nil)
	sess := &Session{Jar: jar}
	var a struct {
		SecondFactor bool `json:"second_factor"`
		Session      *struct {
			SecondFactorRequired bool `json:"second_factor_required"`
		} `json:"session"`
	}
	s.MustAPI(t, http.MethodPost, "/api/auth/login", map[string]string{"username": user, "password": pass}, sess, &a)
	switch {
	case a.SecondFactor:
		t.Fatalf("harness: %s has a second factor that the harness did not enroll", user)
	case a.Session != nil && a.Session.SecondFactorRequired:
		return s.LoginTOTP(t, user)
	}
	return sess
}

// AllServerPerms holds every server permission, in registry order (module start order). A person
// with all of them is an administrator.
var AllServerPerms = []string{"repositories.all", "people.manage", "settings.general", "activity.read", "storage.manage", "network.manage", "signing.manage", "signin.manage", "nexus.manage"}

// CreateUser creates a local user with the server permissions perms (none for a plain user).
// The harness keeps the password, so the publish helpers can sign in as the user.
func (s *Server) CreateUser(t testing.TB, name, pass string, perms ...string) {
	t.Helper()
	s.MustAPI(t, http.MethodPost, "/api/users", map[string]any{"username": name, "password": pass, "permissions": nonNil(perms)}, s.Admin(), nil)
	s.user(name).pass = pass
}

// SetServerPerms replaces the server permissions of user with perms, as the bootstrap admin.
func (s *Server) SetServerPerms(t testing.TB, user string, perms ...string) {
	t.Helper()
	s.MustAPI(t, http.MethodPut, "/api/users/"+user+"/permissions", map[string]any{"permissions": nonNil(perms)}, s.Admin(), nil)
}

// nonNil returns xs, or an empty list for nil, so that JSON gets [] and not null.
func nonNil(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

func (s *Server) CreateRepo(t testing.TB, name, format string, anon bool, config map[string]any) {
	t.Helper()
	body := map[string]any{"name": name, "format": format, "anonymous_read": anon}
	if config != nil {
		body["config"] = config
	}
	s.MustAPI(t, http.MethodPost, "/api/repos", body, s.Admin(), nil)
}

// SetPerms sets the repository permissions of user on repo to perms (for example "repo.download",
// "repo.publish"), as the bootstrap admin. No perms removes every permission of user on repo.
func (s *Server) SetPerms(t testing.TB, repo, user string, perms ...string) {
	t.Helper()
	s.MustAPI(t, http.MethodPut, "/api/grants", map[string]any{"users": []string{user}, "repos": []string{repo}, "perms": nonNil(perms)}, s.Admin(), nil)
}

// CreateToken creates a read token for user (signed in to the UI with user/pass, see SignIn)
// and returns the secret.
func (s *Server) CreateToken(t testing.TB, user, pass, name string) string {
	t.Helper()
	var out struct {
		Token string `json:"token"`
	}
	s.MustAPI(t, http.MethodPost, "/api/tokens", map[string]any{"name": name}, s.SignIn(t, user, pass), &out)
	if out.Token == "" {
		t.Fatal("CreateToken: empty token in response")
	}
	return out.Token
}

// WaitDebianIndexed polls /api/repos/{repo}/status until pending == 0 (60s timeout).
func (s *Server) WaitDebianIndexed(t testing.TB, repo string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		var st struct {
			Pending int64 `json:"pending"`
		}
		s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/status", nil, s.AdminBasic(), &st)
		if st.Pending == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("debian repo %s: still %d pending after 60s", repo, st.Pending)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// DebianKey returns the armored public signing key of Debian repo at /debian/{repo}/key.asc,
// read with auth (nil for an anonymous-read repo).
func (s *Server) DebianKey(t testing.TB, repo string, auth Auth) string {
	t.Helper()
	resp, b := s.Do(t, s.NewRequest(t, http.MethodGet, "/debian/"+repo+"/key.asc", nil), auth)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(b), "BEGIN PGP PUBLIC KEY BLOCK") {
		t.Fatalf("GET /debian/%s/key.asc: status %d: %s", repo, resp.StatusCode, b)
	}
	if strings.Contains(string(b), "PRIVATE KEY") {
		t.Fatalf("GET /debian/%s/key.asc exposes private key material", repo)
	}
	return string(b)
}

// RandName returns prefix-<8 hex>, safe for repo, user and alias names.
func RandName(prefix string) string { return prefix + "-" + randHex(4) }

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ---------------------------------------------------------------- S3

type S3Target struct {
	Endpoint     string // reachable from containers on TestNetwork
	HostEndpoint string // reachable from the test process
	Region       string
	Bucket       string
	AccessKey    string
	SecretKey    string
	Prefix       string // unique per target, no trailing slash
	PathStyle    bool
}

// ServerEnv returns the TR_* variables pointing the server at this target.
func (s *S3Target) ServerEnv() map[string]string {
	return map[string]string{
		"TR_STORAGE":              "s3",
		"TR_S3_ENDPOINT":          s.Endpoint,
		"TR_S3_REGION":            s.Region,
		"TR_S3_BUCKET":            s.Bucket,
		"TR_S3_ACCESS_KEY_ID":     s.AccessKey,
		"TR_S3_SECRET_ACCESS_KEY": s.SecretKey,
		"TR_S3_PREFIX":            s.Prefix,
		"TR_S3_PATH_STYLE":        fmt.Sprint(s.PathStyle),
	}
}

// Client returns an S3 client for test-side checks, using HostEndpoint.
func (s *S3Target) Client() *s3.Client {
	cfg := aws.Config{Region: s.Region, Credentials: credentials.NewStaticCredentialsProvider(s.AccessKey, s.SecretKey, "")}
	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(s.HostEndpoint)
		o.UsePathStyle = s.PathStyle
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
	})
}

// ListKeys returns all object keys under the target's Prefix (full keys, prefix included).
func (s *S3Target) ListKeys(t testing.TB) []string {
	t.Helper()
	keys, err := s.listKeys(context.Background())
	if err != nil {
		t.Fatalf("list s3 keys: %v", err)
	}
	return keys
}

func (s *S3Target) listKeys(ctx context.Context) ([]string, error) {
	var keys []string
	p := s3.NewListObjectsV2Paginator(s.Client(), &s3.ListObjectsV2Input{Bucket: &s.Bucket, Prefix: aws.String(s.Prefix)})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, o := range page.Contents {
			keys = append(keys, aws.ToString(o.Key))
		}
	}
	return keys, nil
}

// Blobs returns the SHA-256 of every object under the target's Prefix, keyed by storage key
// (the object key without Prefix).
func (s *S3Target) Blobs(t testing.TB) map[string]string {
	t.Helper()
	ctx := context.Background()
	c := s.Client()
	blobs := map[string]string{}
	for _, k := range s.ListKeys(t) {
		obj, err := c.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.Bucket), Key: aws.String(k)})
		if err != nil {
			t.Fatalf("get s3 object %s: %v", k, err)
		}
		h := sha256.New()
		_, err = io.Copy(h, obj.Body)
		obj.Body.Close()
		if err != nil {
			t.Fatalf("read s3 object %s: %v", k, err)
		}
		blobs[strings.TrimPrefix(k, s.Prefix+"/")] = hex.EncodeToString(h.Sum(nil))
	}
	return blobs
}

const (
	seaweedAlias  = "seaweedfs"
	seaweedAccess = "e2eaccesskey"
	seaweedSecret = "e2esecretkey0123456789"
)

var seaweed struct {
	once sync.Once
	c    testcontainers.Container
	host string
	err  error
}

func stopSharedSeaweed() {
	if seaweed.c != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_ = seaweed.c.Terminate(ctx)
	}
}

func startSharedSeaweed() {
	ctx := context.Background()
	img := os.Getenv("E2E_SEAWEEDFS_IMAGE")
	if img == "" {
		img = "chrislusf/seaweedfs:4.48"
	}
	s3conf := fmt.Sprintf(`{"identities":[{"name":"e2e","credentials":[{"accessKey":%q,"secretKey":%q}],"actions":["Admin","Read","Write","List","Tagging"]}]}`, seaweedAccess, seaweedSecret)
	c, err := testcontainers.Run(ctx, img,
		testcontainers.WithLabels(e2eLabels),
		// Each test bucket is a SeaweedFS collection, and a collection grows 7 volumes on its first
		// write. The default of 8 volumes (of 30 GB each) runs out after a few buckets, and later
		// writes answer 500 "no free volumes left". Small volumes and a high limit fit a whole run.
		testcontainers.WithCmd("server", "-s3", "-s3.port=8333", "-s3.config=/etc/seaweedfs/s3.json",
			"-master.volumeSizeLimitMB=32", "-volume.max=2000"),
		testcontainers.WithExposedPorts("8333/tcp"),
		testcontainers.WithFiles(testcontainers.ContainerFile{Reader: strings.NewReader(s3conf), ContainerFilePath: "/etc/seaweedfs/s3.json", FileMode: 0o644}),
		network.WithNetwork([]string{seaweedAlias}, TestNetwork),
		testcontainers.WithWaitStrategy(wait.ForListeningPort("8333/tcp").WithStartupTimeout(2*time.Minute)),
	)
	seaweed.c = c
	if err != nil {
		seaweed.err = fmt.Errorf("start seaweedfs: %w", err)
		if c != nil {
			seaweed.err = fmt.Errorf("%w\n%s", seaweed.err, containerLogs(c))
		}
		return
	}
	seaweed.host, seaweed.err = c.PortEndpoint(ctx, "8333/tcp", "http")
}

// StartSeaweedFS returns a target on the run-wide SeaweedFS S3 gateway (started on first use)
// with a fresh bucket and a unique prefix.
func StartSeaweedFS(t testing.TB) *S3Target {
	t.Helper()
	seaweed.once.Do(startSharedSeaweed)
	if seaweed.err != nil {
		t.Fatal(seaweed.err)
	}
	tg := &S3Target{
		Endpoint:     "http://" + seaweedAlias + ":8333",
		HostEndpoint: seaweed.host,
		Region:       "us-east-1",
		Bucket:       RandName("e2e"),
		AccessKey:    seaweedAccess,
		SecretKey:    seaweedSecret,
		Prefix:       "e2e/" + RunID + "/" + randHex(4),
		PathStyle:    true,
	}
	// The gateway accepts connections before the filer is ready; retry bucket creation.
	deadline := time.Now().Add(90 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, err := tg.Client().CreateBucket(ctx, &s3.CreateBucketInput{Bucket: &tg.Bucket})
		cancel()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("seaweedfs create bucket %s: %v", tg.Bucket, err)
		}
		time.Sleep(time.Second)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("---- last lines of the SeaweedFS log ----\n%s", tail(containerLogs(seaweed.c), 100))
		}
	})
	return tg
}

// tail returns the last n lines of s.
func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return strings.Join(lines[max(0, len(lines)-n):], "\n")
}

// ForEachStorage runs fn as subtests "fs" and "seaweedfs".
func ForEachStorage(t *testing.T, fn func(t *testing.T, opts ServerOpts)) {
	t.Run("fs", func(t *testing.T) { fn(t, ServerOpts{Storage: "fs"}) })
	t.Run("seaweedfs", func(t *testing.T) { fn(t, ServerOpts{Storage: "s3", S3: StartSeaweedFS(t)}) })
}

// ---------------------------------------------------------------- client containers

// ContainerFile is a file copied into a container before it starts.
type ContainerFile struct {
	Content  string
	Path     string
	Mode     int64  // default 0644
	HostPath string // a file of the host to copy, in place of Content
}

func (f ContainerFile) tc() testcontainers.ContainerFile {
	mode := f.Mode
	if mode == 0 {
		mode = 0o644
	}
	if f.HostPath != "" {
		return testcontainers.ContainerFile{HostFilePath: f.HostPath, ContainerFilePath: f.Path, FileMode: mode}
	}
	return testcontainers.ContainerFile{Reader: strings.NewReader(f.Content), ContainerFilePath: f.Path, FileMode: mode}
}

type ClientOpts struct {
	Cmd        []string // joined with spaces and run via sh -c
	Files      []ContainerFile
	Env        map[string]string
	Mounts     map[string]string // named volume -> container path (e.g. M2Volume: "/root/.m2")
	WorkingDir string
	User       string
	Timeout    time.Duration // default 10m
	Privileged bool          // run the container privileged (e.g. Docker in Docker)
	// NoShell runs the image's own entrypoint and command; Cmd is ignored.
	NoShell bool
}

// ClientResult is a finished client container; it is removed at test end.
type ClientResult struct {
	ExitCode  int
	Output    string
	container testcontainers.Container
}

// ReadFile copies a file out of the (stopped) client container.
func (r *ClientResult) ReadFile(t testing.TB, path string) []byte {
	t.Helper()
	rc, err := r.container.CopyFileFromContainer(context.Background(), path)
	if err != nil {
		t.Fatalf("copy %s from client container: %v", path, err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read %s from client container: %v", path, err)
	}
	return b
}

// RunClient runs image with opts.Cmd on TestNetwork, waits for exit and returns exit code and combined output.
func RunClient(t testing.TB, image string, opts ClientOpts) (int, string) {
	t.Helper()
	r := RunClientResult(t, image, opts)
	return r.ExitCode, r.Output
}

// RunClientResult is RunClient that also allows copying files out of the finished container.
func RunClientResult(t testing.TB, image string, opts ClientOpts) *ClientResult {
	t.Helper()
	ctx := context.Background()
	if opts.Timeout == 0 {
		opts.Timeout = 10 * time.Minute
	}
	files := make([]testcontainers.ContainerFile, len(opts.Files))
	for i, f := range opts.Files {
		files[i] = f.tc()
	}
	var mounts []testcontainers.ContainerMount
	for vol, target := range opts.Mounts {
		mounts = append(mounts, testcontainers.VolumeMount(vol, testcontainers.ContainerMountTarget(target)))
	}
	var customizers []testcontainers.ContainerCustomizer
	if !opts.NoShell {
		customizers = append(customizers, testcontainers.WithEntrypoint("sh", "-c"), testcontainers.WithCmd(strings.Join(opts.Cmd, " ")))
	}
	if opts.Privileged {
		customizers = append(customizers, testcontainers.WithHostConfigModifier(func(hc *container.HostConfig) { hc.Privileged = true }))
	}
	customizers = append(customizers,
		testcontainers.WithEnv(opts.Env),
		testcontainers.WithFiles(files...),
		testcontainers.WithMounts(mounts...),
		network.WithNetwork(nil, TestNetwork),
		testcontainers.WithConfigModifier(func(c *container.Config) {
			if opts.WorkingDir != "" {
				c.WorkingDir = opts.WorkingDir
			}
			if opts.User != "" {
				c.User = opts.User
			}
		}),
		testcontainers.WithWaitStrategyAndDeadline(opts.Timeout, wait.ForExit()),
	)
	c, err := testcontainers.Run(ctx, image, append(customizers, testcontainers.WithLabels(e2eLabels))...)
	registerCleanup(t, c, "client "+image)
	if err != nil {
		out := ""
		if c != nil {
			out = containerLogs(c)
		}
		t.Fatalf("run client %s: %v\n%s", image, err, out)
	}
	st, err := c.State(ctx)
	if err != nil {
		t.Fatalf("client state: %v", err)
	}
	return &ClientResult{ExitCode: st.ExitCode, Output: containerLogs(c), container: c}
}

// ---------------------------------------------------------------- publisher credentials

// Every write on a format route needs a publish session. The harness gets one the way a
// developer does: it enrolls a TOTP factor for the user (it computes the codes itself), signs in
// with both steps, enrolls an Ed25519 device key through the device flow, approves it, and then
// mints a session per publish with a signed challenge. Reads keep using passwords and read
// tokens.

// harnessUser is what the harness knows about one user of one server.
type harnessUser struct {
	pass    string
	factors []*totpFactor // TOTP factors the harness enrolled; empty until EnrollTOTP
	device  *Device
}

// totpFactor is one enrolled TOTP factor. The server refuses a step that was used once with the
// same factor, so the harness tracks the last step it used.
type totpFactor struct {
	secret   string // base32
	lastStep int64
}

// totpFactors is how many TOTP factors EnrollTOTP enrolls per user. Each factor gives one fresh
// code per 30 s step, so several factors let a test sign in, approve and mint without waiting
// for the next step.
const totpFactors = 3

// user returns the harness record of name, creating an empty one. The bootstrap admin's
// record starts with the admin password.
func (s *Server) user(name string) *harnessUser {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.users == nil {
		s.users = map[string]*harnessUser{}
	}
	u := s.users[name]
	if u == nil {
		u = &harnessUser{}
		if name == s.AdminUser {
			u.pass = s.AdminPass
		}
		s.users[name] = u
	}
	return u
}

// Password returns the password the harness created user with.
func (s *Server) Password(t testing.TB, user string) string {
	t.Helper()
	pass := s.user(user).pass
	if pass == "" {
		t.Fatalf("harness: no password known for user %q (create the user with Server.CreateUser)", user)
	}
	return pass
}

// EnrollTOTP adds totpFactors TOTP factors to user through the API. It signs in with the
// password only, so a person who needs the second factor must not have one yet (the session of
// such a person without one reaches the setup routes); the factors are added one after the
// other on it.
func (s *Server) EnrollTOTP(t testing.TB, user string) {
	t.Helper()
	u := s.user(user)
	sess := s.Login(t, user, s.Password(t, user))
	for i := range totpFactors {
		var setup struct {
			SetupID string `json:"setup_id"`
			Secret  string `json:"secret"`
		}
		s.MustAPI(t, http.MethodPost, "/api/security/totp/begin", map[string]string{"name": fmt.Sprintf("e2e %d", i+1)}, sess, &setup)
		if setup.Secret == "" || setup.SetupID == "" {
			t.Fatalf("POST /api/security/totp/begin for %s: no setup_id or secret in the answer", user)
		}
		f := &totpFactor{secret: setup.Secret}
		s.MustAPI(t, http.MethodPost, "/api/security/totp/confirm", map[string]string{"setup_id": setup.SetupID, "code": totpCodeOf(t, f)}, sess, nil)
		u.factors = append(u.factors, f)
	}
}

// TOTPCode returns a TOTP code of user for a step that the harness has not used with the
// chosen factor yet.
func (s *Server) TOTPCode(t testing.TB, user string) string {
	t.Helper()
	u := s.user(user)
	if len(u.factors) == 0 {
		t.Fatalf("harness: user %q has no TOTP factor (use Server.EnrollTOTP)", user)
	}
	best := u.factors[0]
	for _, f := range u.factors[1:] {
		if f.lastStep < best.lastStep {
			best = f
		}
	}
	return totpCodeOf(t, best)
}

// totpCodeOf returns a code of f for a step after its last used one and marks that step used.
// The server accepts one step either side of its clock, so the harness uses the current step or
// the next one, and waits when both are used. It keeps 3 s of margin at step boundaries for
// clock skew between the host and the server container.
func totpCodeOf(t testing.TB, f *totpFactor) string {
	t.Helper()
	const margin = 3
	for {
		now := time.Now().Unix()
		cur := now / 30
		step := max(f.lastStep+1, cur)
		if step == cur || (step == cur+1 && now%30 >= margin) {
			f.lastStep = step
			code, err := totp.GenerateCodeCustom(f.secret, time.Unix(step*30, 0), totp.ValidateOpts{
				Period: 30, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1,
			})
			if err != nil {
				t.Fatalf("TOTP code: %v", err)
			}
			return code
		}
		time.Sleep(time.Until(time.Unix((step-1)*30+margin, 0)))
	}
}

// LoginTOTP enrolls TOTP factors for user when the harness has none, signs in and returns the
// full session. A person who needs the second factor signs in with the password and then a TOTP
// code; any other user signs in with the password alone and keeps the factors for
// approvals and manual sessions.
func (s *Server) LoginTOTP(t testing.TB, user string) *Session {
	t.Helper()
	if len(s.user(user).factors) == 0 {
		s.EnrollTOTP(t, user)
	}
	jar, _ := cookiejar.New(nil)
	sess := &Session{Jar: jar}
	var step struct {
		SecondFactor bool `json:"second_factor"`
	}
	s.MustAPI(t, http.MethodPost, "/api/auth/login", map[string]string{"username": user, "password": s.Password(t, user)}, sess, &step)
	if step.SecondFactor {
		s.MustAPI(t, http.MethodPost, "/api/auth/login/second", map[string]string{"totp_code": s.TOTPCode(t, user)}, sess, nil)
	}
	return sess
}

// Device is an enrolled device key.
type Device struct {
	ID   int64
	User string
	Key  ed25519.PrivateKey
}

// Enrollment is the answer of POST /api/devices/enroll.
type Enrollment struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURL string `json:"verification_url"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// b64 is the encoding of device keys, nonces and signatures on the wire.
func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// StartEnrollment generates an Ed25519 key and starts the device flow for it from publisher
// client client ("gradle" or "maven"), without auth.
func (s *Server) StartEnrollment(t testing.TB, name, client string) (Enrollment, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var en Enrollment
	s.MustAPI(t, http.MethodPost, "/api/devices/enroll", map[string]string{"name": name, "client": client, "public_key": b64(pub)}, nil, &en)
	if en.DeviceCode == "" || en.UserCode == "" {
		t.Fatalf("POST /api/devices/enroll: incomplete answer %+v", en)
	}
	return en, priv
}

// PollAnswer is the answer of POST /api/devices/enroll/poll: a device id, or an RFC 8628 error.
type PollAnswer struct {
	DeviceID int64  `json:"device_id"`
	Error    string `json:"error"`
}

// PollEnrollment polls the device flow once.
func (s *Server) PollEnrollment(t testing.TB, deviceCode string) (int, PollAnswer) {
	t.Helper()
	st, b := s.API(t, http.MethodPost, "/api/devices/enroll/poll", map[string]string{"device_code": deviceCode}, nil)
	var a PollAnswer
	if err := json.Unmarshal(b, &a); err != nil {
		t.Fatalf("POST /api/devices/enroll/poll: status %d, decode %s: %v", st, b, err)
	}
	return st, a
}

// ApproveEnrollment approves userCode as user with a fresh TOTP code.
func (s *Server) ApproveEnrollment(t testing.TB, sess *Session, user, userCode string) (int, []byte) {
	t.Helper()
	return s.API(t, http.MethodPost, "/api/devices/approve", map[string]string{"user_code": userCode}, s.TOTPProof(t, sess, user))
}

// EnrollDevice enrolls a new device key for user the real way: start the flow, sign in with
// both factors, approve with a fresh code, poll for the device id.
func (s *Server) EnrollDevice(t testing.TB, user string) *Device {
	t.Helper()
	en, key := s.StartEnrollment(t, "e2e "+user, "gradle")
	sess := s.LoginTOTP(t, user)
	if st, b := s.ApproveEnrollment(t, sess, user, en.UserCode); st/100 != 2 {
		t.Fatalf("approve device of %s: status %d: %s", user, st, b)
	}
	st, a := s.PollEnrollment(t, en.DeviceCode)
	if st != http.StatusOK || a.DeviceID == 0 {
		t.Fatalf("poll after approval: status %d, answer %+v", st, a)
	}
	return &Device{ID: a.DeviceID, User: user, Key: key}
}

// device returns the cached device of user, enrolling it on first use.
func (s *Server) device(t testing.TB, user string) *Device {
	t.Helper()
	u := s.user(user)
	if u.device == nil {
		u.device = s.EnrollDevice(t, user)
	}
	return u.device
}

// Challenge asks for a publish nonce for device id.
func (s *Server) Challenge(t testing.TB, deviceID int64) string {
	t.Helper()
	var c struct {
		Nonce string `json:"nonce"`
	}
	s.MustAPI(t, http.MethodPost, "/api/publish/challenge", map[string]any{"device_id": deviceID}, nil, &c)
	if c.Nonce == "" {
		t.Fatal("POST /api/publish/challenge: empty nonce")
	}
	return c.Nonce
}

// Sign signs the mint message of nonce and repo with the device key.
func (d *Device) Sign(nonce, repo string) string {
	msg := "telesphorepo publish v1\n" + nonce + "\n" + strconv.FormatInt(d.ID, 10) + "\n" + repo
	return b64(ed25519.Sign(d.Key, []byte(msg)))
}

// MintWith sends POST /api/publish/sessions with the given proof.
func (s *Server) MintWith(t testing.TB, deviceID int64, repo, nonce, signature string) (int, []byte) {
	t.Helper()
	return s.API(t, http.MethodPost, "/api/publish/sessions",
		map[string]any{"device_id": deviceID, "repo": repo, "nonce": nonce, "signature": signature}, nil)
}

// Mint asks for a challenge and mints a session for repo with device d.
func (s *Server) Mint(t testing.TB, d *Device, repo string) (int, []byte) {
	t.Helper()
	nonce := s.Challenge(t, d.ID)
	return s.MintWith(t, d.ID, repo, nonce, d.Sign(nonce, repo))
}

// PublishSession is a minted session as the API returns it.
type PublishSession struct {
	ID    string // the {id} of DELETE /api/publish/sessions/{id}
	Token string
	Repo  string
}

// Auth returns Basic credentials that carry the session token.
func (p PublishSession) Auth() Basic { return Basic{User: "publisher", Pass: p.Token} }

// ParseSession decodes a mint answer {id, token, repo, expires_at}.
func ParseSession(t testing.TB, b []byte) PublishSession {
	t.Helper()
	var raw struct {
		ID    json.RawMessage `json:"id"`
		Token string          `json:"token"`
		Repo  string          `json:"repo"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("decode publish session %s: %v", b, err)
	}
	if !strings.HasPrefix(raw.Token, "trs_") || len(raw.ID) == 0 {
		t.Fatalf("publish session answer %s: want an id and a trs_ token", b)
	}
	return PublishSession{ID: strings.Trim(string(raw.ID), `"`), Token: raw.Token, Repo: raw.Repo}
}

// mustSession returns a function that fails the test unless st is 2xx and decodes the session,
// so that a mint call's results pass straight in: mustSession(t, "mint")(s.Mint(t, d, repo)).
func mustSession(t testing.TB, what string) func(st int, b []byte) PublishSession {
	return func(st int, b []byte) PublishSession {
		t.Helper()
		if st/100 != 2 {
			t.Fatalf("%s: status %d: %s", what, st, b)
		}
		return ParseSession(t, b)
	}
}

// MintSession mints a session for repo with the user's cached device.
func (s *Server) MintSession(t testing.TB, user, repo string) PublishSession {
	t.Helper()
	return mustSession(t, "mint a publish session for "+user+" on "+repo)(s.Mint(t, s.device(t, user), repo))
}

// PublishToken returns a new publish-session token of user for repo: the password for one
// publish (any username). The user's TOTP factor and device are set up on first use and
// cached per server; every call mints a new session.
func PublishToken(t testing.TB, s *Server, user, repo string) string {
	t.Helper()
	return s.MintSession(t, user, repo).Token
}

// PublishAuth is PublishToken as Basic credentials with the user's name.
func (s *Server) PublishAuth(t testing.TB, user, repo string) Basic {
	t.Helper()
	return Basic{User: user, Pass: PublishToken(t, s, user, repo)}
}

// ManualSession asks for a manual publish session of user for repo with a UI session and a
// fresh TOTP code.
func (s *Server) ManualSession(t testing.TB, sess *Session, user, repo string) (int, []byte) {
	t.Helper()
	return s.API(t, http.MethodPost, "/api/publish/sessions/manual", map[string]string{"repo": repo}, s.TOTPProof(t, sess, user))
}

// OIDCSession exchanges a CI identity token for a publish session for repo.
func (s *Server) OIDCSession(t testing.TB, token, repo string) (int, []byte) {
	t.Helper()
	return s.API(t, http.MethodPost, "/api/publish/sessions/oidc", map[string]string{"token": token, "repo": repo}, nil)
}

// CloseSession ends p with its own token and returns the status.
func (s *Server) CloseSession(t testing.TB, p PublishSession) int {
	t.Helper()
	st, _ := s.API(t, http.MethodDelete, "/api/publish/sessions/"+p.ID, nil, p.Auth())
	return st
}
