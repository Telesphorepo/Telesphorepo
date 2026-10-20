//go:build edge

package e2e

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	osexec "os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	mobynetwork "github.com/moby/moby/api/types/network"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/Telesphorepo/Telesphorepo/internal/storage"
)

// The fixture of the edge zone: the edge Worker of edge/ in wrangler dev (edge/test/Containerfile)
// with local R2 and Queues, in front of a real server whose S3 storage is the local R2 of that
// Worker (its S3 endpoint /cdn-cgi/local/r2/s3). The server writes the same buckets that the
// Worker reads. Each test starts its own Worker and server on TestNetwork.

const (
	edgeImage      = "telesphorepo-edge-e2e:latest"
	edgeAccessKey  = "edgee2eaccess" // local_dev.experimental_s3_credentials of edge/test/wrangler.jsonc
	edgeSecretKey  = "edgee2esecret0123456789"
	edgeHomeBucket = "edge-home"
	edgePrefix     = "tr" // s3.prefix of the server and the var PREFIX of the Worker
	edgeHomeHint   = "enam"
	edgeHitHeader  = "Telesphorepo-Edge"
	edgeCron       = "*/10 * * * *"
)

// edgeRegionBuckets are the regional buckets that edge/test/wrangler.jsonc binds, by hint. The
// client of every test is in London (edge/test/run.sh), so the Worker reads weur when it is ready.
var edgeRegionBuckets = map[string]string{"weur": "edge-weur", "apac": "edge-apac"}

// edgeBothRegions is edge.regions with both regional buckets.
var edgeBothRegions = []string{"weur=edge-weur", "apac=edge-apac"}

var edgeImageBuild struct {
	once sync.Once
	err  error
}

// buildEdgeImage builds the fixture image once per run. E2E_SKIP_BUILD=1 reuses an image that
// exists.
func buildEdgeImage(t testing.TB) {
	t.Helper()
	edgeImageBuild.once.Do(func() {
		if os.Getenv("E2E_SKIP_BUILD") == "1" && osexec.Command(Engine.CLI, "image", "inspect", edgeImage).Run() == nil {
			return
		}
		dir, err := filepath.Abs(filepath.Join("..", "edge"))
		if err != nil {
			edgeImageBuild.err = err
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()
		out, err := osexec.CommandContext(ctx, Engine.CLI, "build", "-t", edgeImage, "-f", filepath.Join(dir, "test", "Containerfile"), dir).CombinedOutput()
		if err != nil {
			edgeImageBuild.err = fmt.Errorf("build %s: %w\n%s", edgeImage, err, out)
		}
	})
	if edgeImageBuild.err != nil {
		t.Fatal(edgeImageBuild.err)
	}
}

// edgeOpts changes the fixture of one test.
type edgeOpts struct {
	Regions  []string // edge.regions when edge delivery starts; nil: both regions
	TTL      string   // edge.access_ttl; "": 1s
	Worker   string   // the version the Worker is built as; "": dev, the version of the server image
	Redirect bool     // s3.redirect of the server
	Off      bool     // leave edge delivery off
}

// Edge is a running Worker with its server.
type Edge struct {
	S        *Server
	URL      string // the Worker, from the test process
	Internal string // the Worker, from containers on TestNetwork
	Host     string // <alias>:8787: the host of the base URL, so the registry host too
	S3       *S3Target
	stores   map[string]storage.Store // "home" and each regional hint
}

// startEdge starts the Worker, then the server with the local R2 of the Worker as its S3 storage
// and the Worker as its base URL, and turns edge delivery on.
func startEdge(t *testing.T, o edgeOpts) *Edge {
	t.Helper()
	buildEdgeImage(t)
	ctx := context.Background()
	serverAlias, alias := RandName("repo"), RandName("edge")
	env := map[string]string{"ORIGIN": "http://" + serverAlias + ":8080", "PREFIX": edgePrefix}
	if o.Worker != "" {
		env["WORKER_VERSION"] = o.Worker
	}
	c, err := runContainer(ctx, edgeImage,
		testcontainers.WithLabels(e2eLabels),
		testcontainers.WithEnv(env),
		testcontainers.WithExposedPorts("8787/tcp"),
		network.WithNetwork([]string{alias}, TestNetwork),
		testcontainers.WithWaitStrategy(wait.ForLog("Ready on").WithStartupTimeout(3*time.Minute)),
	)
	registerCleanup(t, c, "edge "+alias)
	if err != nil {
		t.Fatalf("start the edge Worker: %v", err)
	}
	ep, err := c.PortEndpoint(ctx, "8787/tcp", "http")
	if err != nil {
		t.Fatalf("edge Worker endpoint: %v", err)
	}
	host := alias + ":8787"
	// The server reaches the local S3 endpoint through startForwarder at 127.0.0.1:8787: wrangler
	// dev answers /cdn-cgi/ only for a loopback Host.
	tg := &S3Target{
		Endpoint: "http://127.0.0.1:8787/cdn-cgi/local/r2/s3", HostEndpoint: ep + "/cdn-cgi/local/r2/s3",
		Region: "auto", Bucket: edgeHomeBucket, AccessKey: edgeAccessKey, SecretKey: edgeSecretKey,
		Prefix: edgePrefix, PathStyle: true,
	}
	s := StartServer(t, ServerOpts{Alias: serverAlias, Storage: "s3", S3: tg, Env: map[string]string{
		"TR_BASE_URL":       "http://" + host,
		"TR_S3_REDIRECT":    strconv.FormatBool(o.Redirect),
		"TR_BLOBS_GC_GRACE": "1s",
	}})
	startForwarder(t, s, host)
	e := &Edge{S: s, URL: ep, Internal: "http://" + host, Host: host, S3: tg, stores: map[string]storage.Store{}}
	e.stores["home"] = e.openStore(t, edgeHomeBucket)
	for hint, b := range edgeRegionBuckets {
		e.stores[hint] = e.openStore(t, b)
	}
	// The Worker is the proxy in front of the server, on the private addresses of TestNetwork.
	s.MustAPI(t, http.MethodPatch, "/api/settings", map[string]any{"trusted_proxies": []string{"private"}}, s.Admin(), nil)
	if !o.Off {
		regions := o.Regions
		if regions == nil {
			regions = edgeBothRegions
		}
		ttl := o.TTL
		if ttl == "" {
			ttl = "1s"
		}
		e.enable(t, regions, ttl)
	}
	return e
}

// startForwarder runs edge/test/forward.mjs in the network namespace of the server, so the server
// reaches the Worker at target as 127.0.0.1:8787. Miniflare answers a request under /cdn-cgi/ only
// when its Host is a loopback name, and the S3 signature covers that Host. The server does not
// touch its store at boot, so the forwarder starts after the server.
func startForwarder(t testing.TB, s *Server, target string) {
	t.Helper()
	c, err := testcontainers.Run(context.Background(), edgeImage,
		testcontainers.WithLabels(e2eLabels),
		testcontainers.WithEnv(map[string]string{"TARGET": target}),
		testcontainers.WithEntrypoint("node", "/edge/test/forward.mjs"),
		testcontainers.WithHostConfigModifier(func(hc *container.HostConfig) {
			hc.NetworkMode = container.NetworkMode("container:" + s.GetContainerID())
		}),
		testcontainers.WithEndpointSettingsModifier(func(m map[string]*mobynetwork.EndpointSettings) { clear(m) }),
		testcontainers.WithWaitStrategy(wait.ForLog("forwarding").WithStartupTimeout(time.Minute)),
	)
	registerCleanup(t, c, "edge forwarder of "+s.Alias)
	if err != nil {
		t.Fatalf("start the forwarder to the edge Worker: %v", err)
	}
}

// openStore opens a bucket of the Worker's local R2 from the test process, with the prefix of the
// server.
func (e *Edge) openStore(t testing.TB, bucket string) storage.Store {
	t.Helper()
	st, err := storage.NewS3(context.Background(), storage.Config{Backend: "s3", S3Endpoint: e.S3.HostEndpoint, S3Region: "auto",
		S3Bucket: bucket, S3AccessKeyID: edgeAccessKey, S3SecretAccessKey: edgeSecretKey, S3Prefix: edgePrefix, S3PathStyle: true})
	if err != nil {
		t.Fatalf("open the local bucket %s: %v", bucket, err)
	}
	return st
}

// enable turns edge delivery on with the regions and the access TTL and waits until it is on
// and every region is ready.
func (e *Edge) enable(t testing.TB, regions []string, ttl string) {
	t.Helper()
	e.S.MustAPI(t, http.MethodPatch, "/api/settings", map[string]any{"edge.enabled": true, "edge.home_hint": edgeHomeHint,
		"edge.regions": regions, "edge.access_ttl": ttl}, e.S.Admin(), nil)
	e.waitStatus(t, "edge delivery on with every region ready", 2*time.Minute, func(st edgeStatus) bool {
		return st.Mode == "on" && !st.Backfill.Running && st.readyExactly(edgeHints(regions))
	})
}

// edgeHints returns the hints of edge.regions entries.
func edgeHints(regions []string) []string {
	var out []string
	for _, r := range regions {
		h, _, _ := strings.Cut(r, "=")
		out = append(out, h)
	}
	return out
}

// edgeStatus is GET /api/edge/status.
type edgeStatus struct {
	Enabled       bool    `json:"enabled"`
	Mode          string  `json:"mode"`
	WorkerVersion *string `json:"worker_version"`
	DeployNeeded  bool    `json:"deploy_needed"`
	Backfill      struct {
		Running bool `json:"running"`
	} `json:"backfill"`
	Regions []struct {
		Hint   string `json:"hint"`
		Bucket string `json:"bucket"`
		State  string `json:"state"`
	} `json:"regions"`
}

// readyExactly reports whether the regions are exactly hints, each ready.
func (st edgeStatus) readyExactly(hints []string) bool {
	if len(st.Regions) != len(hints) {
		return false
	}
	for _, r := range st.Regions {
		if r.State != "ready" || !containsString(hints, r.Hint) {
			return false
		}
	}
	return true
}

// state returns the state of the region hint, "" when the status does not list it.
func (st edgeStatus) state(hint string) string {
	for _, r := range st.Regions {
		if r.Hint == hint {
			return r.State
		}
	}
	return ""
}

func containsString(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func (e *Edge) status(t testing.TB) edgeStatus {
	t.Helper()
	var st edgeStatus
	e.S.MustAPI(t, http.MethodGet, "/api/edge/status", nil, e.S.Admin(), &st)
	return st
}

// waitStatus polls the edge status until ok, for at most timeout.
func (e *Edge) waitStatus(t testing.TB, what string, timeout time.Duration, ok func(edgeStatus) bool) edgeStatus {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		st := e.status(t)
		if ok(st) {
			return st
		}
		if time.Now().After(deadline) {
			b, _ := json.Marshal(st)
			t.Fatalf("%s: not after %s; the edge status is %s", what, timeout, b)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// gap runs the Worker's scheduled gap check once, the way the cron does. A pass runs when the
// server (or the test) asked for one in gap/request.json since the last pass.
func (e *Edge) gap(t testing.TB) {
	t.Helper()
	resp, b := e.send(t, e.URL, http.MethodGet, "/__scheduled?cron="+url.QueryEscape(edgeCron), nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("run the scheduled gap check: status %d: %s", resp.StatusCode, b)
	}
}

// requestGap writes gap/request.json, as the server does when it adds a region, so the next gap
// check runs a pass.
func (e *Edge) requestGap(t testing.TB) {
	t.Helper()
	time.Sleep(1100 * time.Millisecond) // the Worker compares whole seconds with the start of the last pass
	body := []byte(fmt.Sprintf(`{"at":%d,"regions":[]}`, time.Now().Unix()))
	if err := e.stores["home"].Put(context.Background(), "edge/v1/gap/request.json", bytes.NewReader(body), int64(len(body)), "application/json"); err != nil {
		t.Fatalf("write gap/request.json: %v", err)
	}
}

// edgeBlobKey is the storage key of the blob with the hex digest.
func edgeBlobKey(hex string) string { return "blobs/sha256/" + hex[:2] + "/" + hex }

// has reports whether bucket ("home" or a hint) holds key.
func (e *Edge) has(t testing.TB, bucket, key string) bool {
	t.Helper()
	_, err := e.stores[bucket].Head(context.Background(), key)
	if errors.Is(err, storage.ErrNotFound) {
		return false
	}
	if err != nil {
		t.Fatalf("HEAD %s in %s: %v", key, bucket, err)
	}
	return true
}

// count returns how many objects bucket holds under prefix.
func (e *Edge) count(t testing.TB, bucket, prefix string) int {
	t.Helper()
	n, after := 0, ""
	for {
		objs, err := e.stores[bucket].List(context.Background(), prefix, after, 1000)
		if err != nil {
			t.Fatalf("list %s of %s: %v", prefix, bucket, err)
		}
		n += len(objs)
		if len(objs) < 1000 {
			return n
		}
		after = objs[len(objs)-1].Key
	}
}

// waitBlobs runs gap passes until every bucket of want holds (or, with present false, lacks) the
// blob of hex.
func (e *Edge) waitBlobs(t testing.TB, what, hex string, present bool, buckets ...string) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for {
		done := true
		for _, b := range buckets {
			if e.has(t, b, edgeBlobKey(hex)) != present {
				done = false
			}
		}
		if done {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: the blob %s is not %s in %v after 90s", what, hex[:12], map[bool]string{true: "copied", false: "deleted"}[present], buckets)
		}
		e.requestGap(t)
		e.gap(t)
		time.Sleep(2 * time.Second) // the local queue delivers in batches of up to 1 s
	}
}

// pointerKey finds the key of the pointer of path in the home bucket: the pointer names its path.
func (e *Edge) pointerKey(t testing.TB, path string) string {
	t.Helper()
	ctx := context.Background()
	after := ""
	for {
		objs, err := e.stores["home"].List(ctx, "edge/v1/p/", after, 1000)
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range objs {
			rc, _, err := e.stores["home"].Get(ctx, o.Key)
			if err != nil {
				continue
			}
			b, _ := io.ReadAll(rc)
			rc.Close()
			var p struct {
				Path string `json:"path"`
			}
			if json.Unmarshal(b, &p) == nil && p.Path == path {
				return o.Key
			}
		}
		if len(objs) < 1000 {
			t.Fatalf("no pointer of %s in the home bucket", path)
		}
		after = objs[len(objs)-1].Key
	}
}

// ---------------------------------------------------------------- requests

// edgeBearer is a Bearer token.
type edgeBearer string

func (b edgeBearer) apply(r *http.Request) { r.Header.Set("Authorization", "Bearer "+string(b)) }

// send sends method path to base (the Worker or the server) with auth and the headers hdr. The
// test asks for no encoding, so both answers carry their bytes as they are.
func (e *Edge) send(t testing.TB, base, method, path string, auth Auth, hdr map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept-Encoding", "identity")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	return e.S.Do(t, req, auth)
}

// get sends a GET of path to the Worker.
func (e *Edge) get(t testing.TB, path string, auth Auth) (*http.Response, []byte) {
	t.Helper()
	return e.send(t, e.URL, http.MethodGet, path, auth, nil)
}

func hitOf(resp *http.Response) string { return resp.Header.Get(edgeHitHeader) }

// edgeHeaders are the headers that the Worker's answer and the server's own answer share. Date
// and the transport headers are left out; Content-Length is compared on the answers that the
// Worker gives itself.
var edgeHeaders = []string{"Content-Type", "Content-Range", "Content-Disposition", "Content-Security-Policy",
	"X-Content-Type-Options", "ETag", "Last-Modified", "Cache-Control", "Accept-Ranges", "X-Checksum-SHA256",
	"X-Checksum-SHA1", "X-Checksum-MD5", "Telesphorepo-Version", "Docker-Content-Digest",
	"Docker-Distribution-API-Version", "WWW-Authenticate", "Location"}

// edge304Headers are the headers that a 304 shares: Go's server drops the content headers of a
// 304, a Worker keeps them.
var edge304Headers = []string{"ETag", "Last-Modified", "Cache-Control", "X-Checksum-SHA256", "Telesphorepo-Version",
	"Docker-Content-Digest"}

// compare sends the same request to the Worker and to the server and fails the test unless the
// status, the body bytes and edgeHeaders are the same, and the Worker answered itself exactly
// when hit is true. It returns the Worker's answer.
func (e *Edge) compare(t testing.TB, what, method, path string, auth Auth, hdr map[string]string, hit bool) (*http.Response, []byte) {
	t.Helper()
	er, eb := e.send(t, e.URL, method, path, auth, hdr)
	or, ob := e.send(t, e.S.URL, method, path, auth, hdr)
	if er.StatusCode != or.StatusCode {
		t.Errorf("%s: the edge answers %d (%s), the server %d: %.300s", what, er.StatusCode, hitOf(er), or.StatusCode, eb)
		return er, eb
	}
	if !bytes.Equal(eb, ob) {
		t.Errorf("%s: the edge answers %d bytes (%s), the server %d bytes", what, len(eb), hitOf(er), len(ob))
	}
	headers := edgeHeaders
	if er.StatusCode == http.StatusNotModified {
		headers = edge304Headers
	}
	for _, h := range headers {
		if a, b := strings.Join(er.Header.Values(h), ", "), strings.Join(or.Header.Values(h), ", "); a != b {
			t.Errorf("%s: header %s is %q at the edge (%s), %q at the server", what, h, a, hitOf(er), b)
		}
	}
	got := hitOf(er)
	if (got != "") != hit {
		t.Errorf("%s: %s %s answered at the edge: %q, want an edge answer %v (status %d)", what, method, path, got, hit, er.StatusCode)
	}
	if got != "" && er.StatusCode != http.StatusNotModified && er.Header.Get("Content-Length") != or.Header.Get("Content-Length") {
		t.Errorf("%s: Content-Length %q at the edge, %q at the server", what, er.Header.Get("Content-Length"), or.Header.Get("Content-Length"))
	}
	return er, eb
}

// waitHit sends GETs of path to the Worker until it answers itself (the outbox wrote the pointer
// and the access objects) and returns that answer.
func (e *Edge) waitHit(t testing.TB, path string, auth Auth) (*http.Response, []byte) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		resp, b := e.get(t, path, auth)
		if hitOf(resp) != "" {
			return resp, b
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET %s: no edge answer after 20s; the last answer is %d: %.300s", path, resp.StatusCode, b)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// waitMiss sends GETs of path to the Worker until the server answers them, within d.
func (e *Edge) waitMiss(t testing.TB, what, path string, auth Auth, d time.Duration) (*http.Response, []byte) {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		resp, b := e.get(t, path, auth)
		if hitOf(resp) == "" {
			return resp, b
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: GET %s still answered at the edge (%s) after %s", what, path, hitOf(resp), d)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// ---------------------------------------------------------------- content

// put stores body at path (/maven/..., /raw/..., ...) through the server with a publish session
// of the admin for repo.
func (e *Edge) put(t testing.TB, repo, path string, body []byte) {
	t.Helper()
	s := e.S
	resp, b := s.Do(t, s.NewRequest(t, http.MethodPut, path, bytes.NewReader(body)), s.PublishAuth(t, s.AdminUser, repo))
	if resp.StatusCode/100 != 2 {
		t.Fatalf("PUT %s: status %d: %s", path, resp.StatusCode, b)
	}
}

// deleteFile deletes path of repo through the API.
func (e *Edge) deleteFile(t testing.TB, repo, path string) {
	t.Helper()
	var out struct {
		Deleted int `json:"deleted"`
	}
	e.S.MustAPI(t, http.MethodPost, "/api/repos/"+repo+"/files/delete", map[string][]string{"paths": {path}}, e.S.Admin(), &out)
	if out.Deleted != 1 {
		t.Fatalf("delete %s of %s: deleted %d, want 1", path, repo, out.Deleted)
	}
}

// createRepo creates a hosted repo; createProxy and createGroup the other kinds.
func (e *Edge) createRepo(t testing.TB, name, format string, anon bool) {
	t.Helper()
	e.S.CreateRepo(t, name, format, anon, nil)
}

// createProxy creates an anonymous proxy of remote; a non-nil config is the repo config (the
// Debian check of the remote reads the InRelease of its "distribution").
func (e *Edge) createProxy(t testing.TB, name, format, remote string, config map[string]any) {
	t.Helper()
	body := map[string]any{"name": name, "format": format, "anonymous_read": true,
		"kind": "proxy", "proxy": map[string]any{"remote_url": remote}}
	if config != nil {
		body["config"] = config
	}
	e.S.MustAPI(t, http.MethodPost, "/api/repos", body, e.S.Admin(), nil)
}

func (e *Edge) createGroup(t testing.TB, name, format string, members ...string) {
	t.Helper()
	e.S.MustAPI(t, http.MethodPost, "/api/repos", map[string]any{"name": name, "format": format, "anonymous_read": true,
		"kind": "group", "members": members}, e.S.Admin(), nil)
}

// guard gives repo a read IP rule that every address passes: the repo is guarded, so the edge
// sends its reads to the server.
func (e *Edge) guard(t testing.TB, repo string) {
	t.Helper()
	rule := func(v string) map[string]string { return map[string]string{"kind": "cidr", "value": v} }
	e.S.MustAPI(t, http.MethodPut, "/api/repos/"+repo+"/ip-rules", map[string]any{
		"read": []map[string]string{rule("0.0.0.0/0"), rule("::/0")}, "publish": []map[string]string{}}, e.S.Admin(), nil)
}

// edgeToken is a read token with its row ID.
type edgeToken struct {
	ID    int64
	Token string
}

// publicID is the public ID of the token: the key of its access object.
func (k edgeToken) publicID() string { return strings.Split(k.Token, "_")[1] }

// createToken creates a read token of user (signed in to the UI).
func (e *Edge) createToken(t testing.TB, user string) edgeToken {
	t.Helper()
	var out struct {
		ID    int64  `json:"id"`
		Token string `json:"token"`
	}
	e.S.MustAPI(t, http.MethodPost, "/api/tokens", map[string]any{"name": RandName("edge")}, e.S.SignIn(t, user, e.S.Password(t, user)), &out)
	if !strings.HasPrefix(out.Token, "trp_") {
		t.Fatalf("create a read token of %s: %+v", user, out)
	}
	return edgeToken{out.ID, out.Token}
}

// deviceProof enrolls a device of user and returns its proof of a token exchange nonce
// ("trx.<device id>.<nonce>.<signature>") and the device.
func (e *Edge) deviceProof(t testing.TB, user string) (string, *Device) {
	t.Helper()
	d := e.S.EnrollDevice(t, user)
	now := time.Now().Unix()
	msg := fmt.Sprintf("telesphorepo challenge v1\n%d\n%d", d.ID, now)
	var c struct {
		Nonce string `json:"nonce"`
	}
	e.S.MustAPI(t, http.MethodPost, "/api/publish/challenge", map[string]any{"device_id": d.ID, "exchange": true, "ts": now,
		"signature": b64(ed25519.Sign(d.Key, []byte(msg)))}, nil, &c)
	if c.Nonce == "" {
		t.Fatal("token exchange nonce: empty")
	}
	return fmt.Sprintf("trx.%d.%s.%s", d.ID, c.Nonce, d.Sign(c.Nonce, "*")), d
}

// registryToken asks the token endpoint of the server for a pull token of image (repo/name) with
// auth (nil: anonymous).
func (e *Edge) registryToken(t testing.TB, auth Auth, image string) edgeBearer {
	t.Helper()
	q := url.Values{"service": {e.Host}, "scope": {"repository:" + image + ":pull"}}
	resp, b := e.send(t, e.S.URL, http.MethodGet, "/docker/token?"+q.Encode(), auth, nil)
	var out struct {
		Token string `json:"token"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(b, &out) != nil || !strings.HasPrefix(out.Token, "trb.") {
		t.Fatalf("token for %s: status %d: %s", image, resp.StatusCode, b)
	}
	if n := strings.Count(out.Token, "."); n != 3 {
		t.Fatalf("token for %s has %d parts, want trb.<payload>.<hmac>.<ed25519>", image, n+1)
	}
	return edgeBearer(out.Token)
}

// edgeOCI is an OCI image of uncompressed tar layers: its blobs by hex, in push order, and its
// manifest.
type edgeOCI struct {
	Blobs    map[string][]byte
	Order    []string // config, then layers
	Layers   []string
	Manifest []byte
	Hex      string
}

// newOCI builds an image of the host architecture with one layer per file.
func newOCI(t testing.TB, files ...[]byte) edgeOCI {
	t.Helper()
	img := edgeOCI{Blobs: map[string][]byte{}}
	var layers []map[string]any
	var diffIDs []string
	for i, content := range files {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		if err := tw.WriteHeader(&tar.Header{Name: fmt.Sprintf("f%d", i), Mode: 0o644, Size: int64(len(content)), ModTime: time.Unix(0, 0)}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(content); err != nil {
			t.Fatal(err)
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		l := buf.Bytes()
		h := hexSHA256(l)
		img.Blobs[h] = l
		img.Layers = append(img.Layers, h)
		diffIDs = append(diffIDs, "sha256:"+h)
		layers = append(layers, map[string]any{"mediaType": "application/vnd.oci.image.layer.v1.tar", "digest": "sha256:" + h, "size": len(l)})
	}
	config, _ := json.Marshal(map[string]any{"architecture": runtime.GOARCH, "os": "linux", "created": time.Now().UTC().Format(time.RFC3339Nano),
		"config": map[string]any{"Cmd": []string{"/f0"}, "Labels": map[string]string{"e2e": randHex(8)}},
		"rootfs": map[string]any{"type": "layers", "diff_ids": diffIDs}})
	ch := hexSHA256(config)
	img.Blobs[ch] = config
	img.Order = append([]string{ch}, img.Layers...)
	img.Manifest, _ = json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
		"config": map[string]any{"mediaType": "application/vnd.oci.image.config.v1+json", "digest": "sha256:" + ch, "size": len(config)},
		"layers": layers})
	img.Hex = hexSHA256(img.Manifest)
	return img
}

func hexSHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// pushOCI pushes img to name:tag (name is repo/image) through the server with a publish session.
func (e *Edge) pushOCI(t testing.TB, name, tag string, img edgeOCI) {
	t.Helper()
	s := e.S
	repo, _, _ := strings.Cut(name, "/")
	auth := s.PublishAuth(t, s.AdminUser, repo)
	for _, h := range img.Order {
		resp, b := s.Do(t, s.NewRequest(t, http.MethodPost, "/v2/"+name+"/blobs/uploads/?digest=sha256:"+h, bytes.NewReader(img.Blobs[h])), auth)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("upload the blob %s to %s: status %d: %s", h[:12], name, resp.StatusCode, b)
		}
	}
	req := s.NewRequest(t, http.MethodPut, "/v2/"+name+"/manifests/"+tag, bytes.NewReader(img.Manifest))
	req.Header.Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
	resp, b := s.Do(t, req, auth)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("put the manifest %s:%s: status %d: %s", name, tag, resp.StatusCode, b)
	}
}

// edgeDeb is a .deb with one control file.
func edgeDeb(t testing.TB, pkg, version string) []byte {
	t.Helper()
	targz := func(files map[string][]byte) []byte {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		for name, data := range files {
			if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
				t.Fatal(err)
			}
			if _, err := tw.Write(data); err != nil {
				t.Fatal(err)
			}
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	control := fmt.Sprintf("Package: %s\nVersion: %s\nArchitecture: all\nMaintainer: E2E <e2e@telesphorepo.test>\nDescription: edge e2e package %s\n", pkg, version, randHex(4))
	var ar bytes.Buffer
	ar.WriteString("!<arch>\n")
	members := []struct {
		name string
		data []byte
	}{{"debian-binary", []byte("2.0\n")}, {"control.tar.gz", targz(map[string][]byte{"control": []byte(control)})}, {"data.tar.gz", targz(nil)}}
	for _, m := range members {
		fmt.Fprintf(&ar, "%-16s%-12d%-6d%-6d%-8s%-10d`\n", m.name, 0, 0, 0, "100644", len(m.data))
		ar.Write(m.data)
		if len(m.data)%2 == 1 {
			ar.WriteByte('\n')
		}
	}
	return ar.Bytes()
}

// uploadDeb uploads a .deb to repo (suite trixie, component main) and waits for the indexes.
func (e *Edge) uploadDeb(t testing.TB, repo string, deb []byte) {
	t.Helper()
	s := e.S
	req := s.NewRequest(t, http.MethodPut, "/debian/"+repo+"/upload/trixie/main", bytes.NewReader(deb))
	req.Header.Set("Content-Type", "application/vnd.debian.binary-package")
	resp, b := s.Do(t, req, s.PublishAuth(t, s.AdminUser, repo))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload a .deb to %s: status %d: %s", repo, resp.StatusCode, b)
	}
	s.WaitDebianIndexed(t, repo)
}
