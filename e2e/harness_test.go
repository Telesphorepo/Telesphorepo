//go:build e2e || import || scale

package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/moby/moby/api/types/container"
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
	Alias        string            // network alias; default "repo-<random>"
	Env          map[string]string // overrides/extra TR_* vars (applied last)
	Storage      string            // "fs" (default) | "s3"
	S3           *S3Target         // required when Storage == "s3"
	Files        []ContainerFile   // extra files copied into the server container
	DataVolume   string            // named volume mounted at /data; default: the container's own
	NoSigningKey bool              // do not configure TR_GPG_KEY_FILE (server auto-generates its key)
	// NoBootstrapAdmin omits TR_BOOTSTRAP_ADMIN_*: the server creates "admin" with a random
	// password and prints it in its startup banner; StartServer reads it from the logs.
	NoBootstrapAdmin bool
}

type Server struct {
	testcontainers.Container
	URL         string // host-mapped base URL, e.g. http://localhost:PORT
	InternalURL string // http://<alias>:8080, reachable from containers on TestNetwork
	Alias       string
	AdminUser   string
	AdminPass   string
}

// StartServer starts a server container on TestNetwork and waits for /healthz.
func StartServer(t testing.TB, opts ServerOpts) *Server {
	t.Helper()
	ctx := context.Background()
	if opts.Alias == "" {
		opts.Alias = RandName("repo")
	}
	adminPass := randHex(16)
	env := map[string]string{
		"TR_COOKIE_SECURE": "false",
		"TR_DEB_DEBOUNCE":  "300ms",
		"TR_DEB_MAX_DELAY": "3s",
		"TR_LOG_LEVEL":     "debug",
		"TR_BASE_URL":      "http://" + opts.Alias + ":8080",
	}
	if !opts.NoBootstrapAdmin {
		env["TR_BOOTSTRAP_ADMIN_USER"] = "admin"
		env["TR_BOOTSTRAP_ADMIN_PASSWORD"] = adminPass
	}
	var files []testcontainers.ContainerFile
	if !opts.NoSigningKey {
		env["TR_GPG_KEY_FILE"] = "/keys/signing.asc"
		files = append(files, testcontainers.ContainerFile{Reader: strings.NewReader(SigningPrivateKey), ContainerFilePath: "/keys/signing.asc", FileMode: 0o644})
	}
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
		testcontainers.WithEnv(env),
		testcontainers.WithExposedPorts("8080/tcp"),
		testcontainers.WithFiles(files...),
		testcontainers.WithMounts(mounts...),
		network.WithNetwork([]string{opts.Alias}, TestNetwork),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/healthz").WithPort("8080/tcp").WithStartupTimeout(90*time.Second)),
	)
	registerCleanup(t, c, "server "+opts.Alias)
	if err != nil {
		t.Fatalf("start server: %v", err)
	}
	ep, err := c.PortEndpoint(ctx, "8080/tcp", "http")
	if err != nil {
		t.Fatalf("server endpoint: %v", err)
	}
	srv := &Server{Container: c, URL: ep, InternalURL: "http://" + opts.Alias + ":8080", Alias: opts.Alias, AdminUser: "admin", AdminPass: adminPass}
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
func (s *Server) Restart(t testing.TB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := s.Stop(ctx, nil); err != nil {
		t.Fatalf("stop server %s: %v", s.Alias, err)
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

// Move is a storage move as the API returns it (spec §11).
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
	AssetID int64  `json:"asset_id"`
	Key     string `json:"key"`
	Reason  string `json:"reason"`
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

// Session carries a cookie jar holding tr_session (see Server.Login).
type Session struct{ Jar http.CookieJar }

func (s *Session) apply(*http.Request) {}

// Admin returns Basic credentials of the bootstrap admin.
func (s *Server) Admin() Basic { return Basic{s.AdminUser, s.AdminPass} }

// HTTPClient never follows redirects (S3 presigned redirects point at network-internal hosts).
func (s *Server) HTTPClient(auth Auth) *http.Client {
	c := &http.Client{
		Timeout:       2 * time.Minute,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	if sess, ok := auth.(*Session); ok && sess != nil {
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

func (s *Server) CreateUser(t testing.TB, name, pass string, admin bool) {
	t.Helper()
	s.MustAPI(t, http.MethodPost, "/api/users", map[string]any{"username": name, "password": pass, "is_admin": admin}, s.Admin(), nil)
}

func (s *Server) CreateRepo(t testing.TB, name, format string, anon bool, config map[string]any) {
	t.Helper()
	body := map[string]any{"name": name, "format": format, "anonymous_read": anon}
	if config != nil {
		body["config"] = config
	}
	s.MustAPI(t, http.MethodPost, "/api/repos", body, s.Admin(), nil)
}

// SetPermission grants level ("read" | "write") on repo to user.
func (s *Server) SetPermission(t testing.TB, repo, user, level string) {
	t.Helper()
	s.MustAPI(t, http.MethodPut, "/api/repos/"+repo+"/permissions/"+user, map[string]string{"level": level}, s.Admin(), nil)
}

// CreateToken creates an API token for user (authenticating with user/pass) and returns the secret.
func (s *Server) CreateToken(t testing.TB, user, pass, name string) string {
	t.Helper()
	var out struct {
		Token string `json:"token"`
	}
	s.MustAPI(t, http.MethodPost, "/api/tokens", map[string]any{"name": name}, Basic{user, pass}, &out)
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
		s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/status", nil, s.Admin(), &st)
		if st.Pending == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("debian repo %s: still %d pending after 60s", repo, st.Pending)
		}
		time.Sleep(250 * time.Millisecond)
	}
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
		testcontainers.WithCmd("server", "-s3", "-s3.port=8333", "-s3.config=/etc/seaweedfs/s3.json"),
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
	return tg
}

// ForEachStorage runs fn as subtests "fs" and "seaweedfs".
func ForEachStorage(t *testing.T, fn func(t *testing.T, opts ServerOpts)) {
	t.Run("fs", func(t *testing.T) { fn(t, ServerOpts{Storage: "fs"}) })
	t.Run("seaweedfs", func(t *testing.T) { fn(t, ServerOpts{Storage: "s3", S3: StartSeaweedFS(t)}) })
}

// ---------------------------------------------------------------- client containers

// ContainerFile is a file copied into a container before it starts.
type ContainerFile struct {
	Content string
	Path    string
	Mode    int64 // default 0644
}

func (f ContainerFile) tc() testcontainers.ContainerFile {
	mode := f.Mode
	if mode == 0 {
		mode = 0o644
	}
	return testcontainers.ContainerFile{Reader: strings.NewReader(f.Content), ContainerFilePath: f.Path, FileMode: mode}
}

// PublicKeyFile places the run's armored signing public key at path (e.g. "/keys/debian.asc").
func PublicKeyFile(path string) ContainerFile {
	return ContainerFile{Content: SigningPublicKey, Path: path}
}

type ClientOpts struct {
	Cmd        []string // joined with spaces and run via sh -c
	Files      []ContainerFile
	Env        map[string]string
	Mounts     map[string]string // named volume -> container path (e.g. M2Volume: "/root/.m2")
	WorkingDir string
	User       string
	Timeout    time.Duration // default 10m
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
	customizers := []testcontainers.ContainerCustomizer{
		testcontainers.WithEntrypoint("sh", "-c"),
		testcontainers.WithCmd(strings.Join(opts.Cmd, " ")),
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
	}
	c, err := testcontainers.Run(ctx, image, customizers...)
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
