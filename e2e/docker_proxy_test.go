//go:build e2e

package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
	"golang.org/x/crypto/bcrypt"
)

// Docker proxy and group repos: a proxy of a second server
// (whose Bearer challenge the proxy answers at its token endpoint) and of the distribution
// registry with htpasswd (a Basic challenge), Docker pulls through them, the cache, the
// revalidation of a moved tag, a stopped remote, the read-only answers, and a group of a hosted
// repo and a proxy.

// distributionImage is the reference registry that a proxy test uses as its remote.
var distributionImage = floatingImage("docker.io/library/registry:latest")

// accessCount counts the requests of method whose path starts with prefix in the access log of s,
// through every request that returned before the call (Server.LogsThrough).
func accessCount(t testing.TB, s *Server, method, prefix string) int {
	t.Helper()
	n := 0
	sc := bufio.NewScanner(strings.NewReader(s.LogsThrough(t)))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.IndexByte(line, '{'); i > 0 {
			line = line[i:]
		}
		var e struct {
			Msg    string `json:"msg"`
			Method string `json:"method"`
			Path   string `json:"path"`
		}
		if json.Unmarshal([]byte(line), &e) == nil && e.Msg == "http" && e.Method == method && strings.HasPrefix(e.Path, prefix) {
			n++
		}
	}
	return n
}

// proxyRemote is the remote registry of a proxy test.
type proxyRemote struct {
	kind       string
	url        string // the remote_url of the proxy
	user, pass string
	// prefix is the name prefix of the images at the remote ("<repo>/" of a second server).
	prefix string
	// denied is the remote_url of a repo at the remote that user may not read ("": none), and
	// deniedPrefix the name prefix of its images.
	denied, deniedPrefix string
	client               registryClient
	// count counts the requests of method whose path starts with prefix in the remote's log.
	count func(t *testing.T, method, prefix string) int
	stop  func(t *testing.T)
}

// telesphorepoRemote starts a second server with a private docker repo that a user downloads.
func telesphorepoRemote(t *testing.T) *proxyRemote {
	t.Helper()
	remote := StartServer(t, ServerOpts{})
	repo := RandName("rpriv")
	remote.CreateRepo(t, repo, "docker", false, nil)
	remote.CreateUser(t, "remuser", "remuser-pass-1234")
	remote.SetPerms(t, repo, "remuser", "repo.download")
	hidden := RandName("rhid")
	remote.CreateRepo(t, hidden, "docker", false, nil)
	push := remote.PublishAuth(t, remote.AdminUser, repo)
	return &proxyRemote{
		kind: "telesphorepo", url: remote.InternalURL + "/" + repo + "/", user: "remuser", pass: "remuser-pass-1234", prefix: repo + "/",
		denied: remote.InternalURL + "/" + hidden + "/", deniedPrefix: hidden + "/",
		client: registryClient{base: remote.URL, auth: push.apply},
		count:  func(t *testing.T, method, prefix string) int { return accessCount(t, remote, method, prefix) },
		stop: func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			if err := remote.Container.Stop(ctx, nil); err != nil {
				t.Fatalf("stop the remote server: %v", err)
			}
		},
	}
}

// distributionRemote starts the distribution registry with an htpasswd user.
func distributionRemote(t *testing.T) *proxyRemote {
	t.Helper()
	ctx := context.Background()
	user, pass := "reguser", "reg-pass-"+randHex(6)
	hash, err := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	alias := RandName("reg")
	c, err := runContainer(ctx, distributionImage,
		e2eContainer,
		testcontainers.WithEnv(map[string]string{
			"REGISTRY_AUTH": "htpasswd", "REGISTRY_AUTH_HTPASSWD_REALM": "e2e", "REGISTRY_AUTH_HTPASSWD_PATH": "/auth/htpasswd",
		}),
		testcontainers.WithFiles(testcontainers.ContainerFile{Reader: strings.NewReader(user + ":" + string(hash) + "\n"),
			ContainerFilePath: "/auth/htpasswd", FileMode: 0o644}),
		testcontainers.WithExposedPorts("5000/tcp"),
		network.WithNetwork([]string{alias}, TestNetwork),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/v2/").WithPort("5000/tcp").
			WithStatusCodeMatcher(func(st int) bool { return st == http.StatusUnauthorized }).WithStartupTimeout(time.Minute)),
	)
	registerCleanup(t, c, "registry "+alias)
	if err != nil {
		t.Fatalf("start the registry: %v", err)
	}
	ep, err := c.PortEndpoint(ctx, "5000/tcp", "http")
	if err != nil {
		t.Fatal(err)
	}
	client := registryClient{base: ep, auth: func(r *http.Request) { r.SetBasicAuth(user, pass) }}
	return &proxyRemote{
		kind: "distribution", url: "http://" + alias + ":5000/", user: user, pass: pass,
		client: client,
		// The access log only (`... [time] "GET /v2/..."`): the registry also logs trace spans
		// whose names hold the method and the path, in a batch some seconds later. The engine
		// delivers the output late, so the count first sends a marker request and waits for its
		// line: the logs then hold the line of every request that returned before.
		count: func(t *testing.T, method, prefix string) int {
			t.Helper()
			mark := "/v2/e2e-log-mark/" + RandName("m") + "/manifests/x"
			client.do(t, http.MethodGet, mark, nil, nil)
			logs := containerLogs(c)
			for deadline := time.Now().Add(30 * time.Second); !strings.Contains(logs, `] "GET `+mark+" "); logs = containerLogs(c) {
				if time.Now().After(deadline) {
					t.Fatalf("the access log line of %s did not appear in the registry log in 30 s", mark)
				}
				time.Sleep(50 * time.Millisecond)
			}
			return strings.Count(logs, `] "`+method+" "+prefix)
		},
		stop: func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			if err := c.Stop(ctx, nil); err != nil {
				t.Fatalf("stop the registry: %v", err)
			}
		},
	}
}

// TestDockerProxy proxies a second server and the distribution registry: the check of the
// remote, Docker pulls through the proxy, the cache of blobs, the revalidation of a moved tag, a
// missing tag, a mount that never asks the remote, a stopped remote, the read-only answer to a
// push, wrong credentials, and a group of a hosted repo and the proxy.
func TestDockerProxy(t *testing.T) {
	t.Parallel()
	OnLocalDisk(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		host := registryHost(s)
		d := StartDind(t, host)
		for _, start := range []func(*testing.T) *proxyRemote{telesphorepoRemote, distributionRemote} {
			rm := start(t)
			t.Run(rm.kind, func(t *testing.T) { dockerProxyCase(t, s, d, rm) })
		}
	})
}

func dockerProxyCase(t *testing.T, s *Server, d *Dind, rm *proxyRemote) {
	host := registryHost(s)
	arch := hostArch()
	v1 := tarImage(t, arch, rawRandom(t, 30<<20), rawRandom(t, 4096))
	rm.client.pushImage(t, rm.prefix+"app", "v1", v1)
	other := tarImage(t, arch, rawRandom(t, 8192))
	rm.client.pushImage(t, rm.prefix+"other", "v1", other)

	// The check passes with the right credentials and fails with wrong ones.
	prx := RandName("prx")
	s.CreateProxy(t, prx, "docker", true, map[string]any{"remote_url": rm.url, "username": rm.user, "password": rm.pass, "metadata_ttl": 1}, false)
	st, b := s.API(t, http.MethodPost, "/api/repos", map[string]any{"name": RandName("bad"), "format": "docker", "kind": "proxy",
		"proxy": map[string]any{"remote_url": rm.url, "username": rm.user, "password": "wrong-password"}}, s.Admin())
	if st != http.StatusBadGateway || !strings.Contains(string(b), "refused the username and password") {
		t.Fatalf("a proxy with a wrong password: status %d, want 502 naming the refused credentials: %s", st, b)
	}
	if rm.kind == "telesphorepo" && rm.count(t, http.MethodGet, "/docker/token") == 0 {
		t.Fatal("the proxy check asked no token of the remote: the Bearer challenge did not run")
	}

	ref := host + "/" + prx + "/app:v1"
	blobGets := func() int { return rm.count(t, http.MethodGet, "/v2/"+rm.prefix+"app/blobs/") }
	t.Run("pull", func(t *testing.T) {
		d.MustExec(t, "pull through the proxy", fmt.Sprintf("set -e\ndocker rmi -f %[1]s >/dev/null 2>&1 || true\ndocker pull %[1]s\n", ref))
		for _, h := range v1.Layers {
			st, b := s.API(t, http.MethodGet, "/api/repos/"+prx+"/file?path=app/_blobs/sha256/"+h, nil, s.Admin())
			wantAPIStatus(t, "the cached layer "+h[:12], st, b, http.StatusOK)
		}
		if got := headDigest(t, s, nil, prx+"/app", "v1"); got != "sha256:"+v1.Hex {
			t.Fatalf("the proxy serves %s for v1, want sha256:%s", got, v1.Hex)
		}
		before := blobGets()
		if before == 0 {
			t.Fatal("the remote log has no blob GET of the first pull")
		}
		d.MustExec(t, "pull again", fmt.Sprintf("set -e\ndocker rmi -f %[1]s\ndocker pull %[1]s\n", ref))
		if after := blobGets(); after != before {
			t.Fatalf("the second pull sent %d blob GETs to the remote, want none", after-before)
		}
	})

	t.Run("revalidate a moved tag", func(t *testing.T) {
		heads := rm.count(t, http.MethodHead, "/v2/"+rm.prefix+"app/manifests/v1")
		moved := tarImage(t, arch, rawRandom(t, 2048))
		rm.client.pushImage(t, rm.prefix+"app", "v1", moved)
		// metadata_ttl is 1 s: the proxy asks the remote again once it passed.
		for deadline := time.Now().Add(30 * time.Second); ; {
			got := headDigest(t, s, nil, prx+"/app", "v1")
			if got == "sha256:"+moved.Hex {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("30 s after the tag moved the proxy serves %s for it, want sha256:%s", got, moved.Hex)
			}
			time.Sleep(200 * time.Millisecond)
		}
		if rm.count(t, http.MethodHead, "/v2/"+rm.prefix+"app/manifests/v1") <= heads {
			t.Fatal("the proxy checked the tag again without a HEAD at the remote")
		}

		// No tag names the old manifest any more: the proxy drops it, and after docker.link_grace
		// its layers leave the cache.
		cached := func(path string) bool {
			st, b := s.API(t, http.MethodGet, "/api/repos/"+prx+"/file?path="+path, nil, s.Admin())
			wantAPIStatus(t, "read the cached file "+path, st, b, http.StatusOK, http.StatusNotFound)
			return st == http.StatusOK
		}
		if cached("app/_manifests/sha256/" + v1.Hex) {
			t.Fatal("the proxy keeps the manifest of the moved tag")
		}
		grace := dockerSetting(t, s, "docker.link_grace")
		s.MustAPI(t, http.MethodPatch, "/api/settings", map[string]any{"docker.link_grace": "1s"}, s.Admin(), nil)
		defer s.MustAPI(t, http.MethodPatch, "/api/settings", map[string]any{"docker.link_grace": grace}, s.Admin(), nil)
		deadline := time.Now().Add(time.Minute)
		for _, h := range v1.Order {
			for cached("app/_blobs/sha256/" + h) {
				if time.Now().After(deadline) {
					t.Fatalf("the layer %s of the moved tag is still in the proxy cache after a minute", h[:12])
				}
				time.Sleep(500 * time.Millisecond)
			}
		}
		if !cached("app/_manifests/sha256/" + moved.Hex) {
			t.Fatal("the proxy does not keep the manifest that the moved tag names")
		}
		resp, body := ociDo(t, s, http.MethodGet, "/v2/"+prx+"/app/manifests/sha256:"+v1.Hex, nil, nil, nil)
		wantOCI(t, "the old digest after the tag moved", resp, body, http.StatusOK, "")
		resp, body = ociDo(t, s, http.MethodGet, "/v2/"+prx+"/app/manifests/nope", nil, nil, nil)
		wantOCI(t, "a tag that the remote does not have", resp, body, http.StatusNotFound, "MANIFEST_UNKNOWN")
	})

	if rm.denied != "" {
		t.Run("denied name", func(t *testing.T) {
			den := RandName("den")
			s.CreateProxy(t, den, "docker", true, map[string]any{"remote_url": rm.denied, "username": rm.user, "password": rm.pass, "not_found_ttl": 3600}, false)
			remoteGets := func() int { return rm.count(t, http.MethodGet, "/v2/"+rm.deniedPrefix+"app/manifests/v1") }
			resp, body := ociDo(t, s, http.MethodGet, "/v2/"+den+"/app/manifests/v1", nil, nil, nil)
			wantOCI(t, "a tag of a name that the remote denies", resp, body, http.StatusNotFound, "MANIFEST_UNKNOWN")
			gets := remoteGets()
			if gets == 0 {
				t.Fatal("the remote log has no GET of the denied tag")
			}
			resp, body = ociDo(t, s, http.MethodGet, "/v2/"+den+"/app/manifests/v1", nil, nil, nil)
			wantOCI(t, "the denied tag again", resp, body, http.StatusNotFound, "MANIFEST_UNKNOWN")
			if n := remoteGets(); n != gets {
				t.Fatalf("the second GET of the denied tag sent %d GETs to the remote, want none (not_found_ttl)", n-gets)
			}
			resp, body = ociDo(t, s, http.MethodGet, "/v2/"+den+"/app/tags/list", nil, nil, nil)
			wantOCI(t, "the tags list of a name that the remote denies", resp, body, http.StatusNotFound, "NAME_UNKNOWN")
			var ps ProxyState
			s.MustAPI(t, http.MethodGet, "/api/repos/"+den, nil, s.Admin(), &ps)
			if ps.Proxy == nil || ps.Proxy.LastError != "" || ps.Proxy.LastOKAt == nil {
				t.Fatalf("a denied name marks the proxy as failing: %+v", ps.Proxy)
			}
		})
	}

	t.Run("mount from the proxy", func(t *testing.T) {
		local := RandName("local")
		s.CreateRepo(t, local, "docker", false, nil)
		auth := s.PublishAuth(t, s.AdminUser, local)
		layer := other.Layers[0]
		resp, body := ociDo(t, s, http.MethodPost, "/v2/"+local+"/app/blobs/uploads/?mount=sha256:"+layer+"&from="+prx+"/other", nil, auth, nil)
		wantOCI(t, "a mount of a layer the proxy has not cached", resp, body, http.StatusAccepted, "")
		for _, m := range []string{http.MethodGet, http.MethodHead} {
			if n := rm.count(t, m, "/v2/"+rm.prefix+"other/blobs/sha256:"+layer); n != 0 {
				t.Fatalf("the mount sent %d %s requests to the remote, want none", n, m)
			}
		}
	})

	t.Run("read-only", func(t *testing.T) {
		st, bearer, b := registryToken(t, s, s.AdminBasic(), "repository:"+prx+"/app:pull,push")
		wantAPIStatus(t, "token for a push to the proxy", st, b, http.StatusOK)
		resp, body := ociDo(t, s, http.MethodPost, "/v2/"+prx+"/app/blobs/uploads/", nil, bearer, nil)
		wantOCI(t, "a push to the proxy", resp, body, http.StatusMethodNotAllowed, "UNSUPPORTED")
	})

	t.Run("group", func(t *testing.T) {
		hosted, grp := RandName("ghost"), RandName("grp")
		s.CreateRepo(t, hosted, "docker", true, nil)
		local := registryClient{base: s.URL, auth: s.PublishAuth(t, s.AdminUser, hosted).apply}
		mine := tarImage(t, arch, rawRandom(t, 4096))
		local.pushImage(t, hosted+"/app", "v1", mine)
		local.pushImage(t, hosted+"/app", "only-hosted", mine)
		v2 := tarImage(t, arch, rawRandom(t, 4096))
		rm.client.pushImage(t, rm.prefix+"app", "v2", v2)
		if got := headDigest(t, s, nil, prx+"/app", "v2"); got != "sha256:"+v2.Hex {
			t.Fatalf("the proxy serves %s for v2, want sha256:%s", got, v2.Hex)
		}
		s.CreateGroup(t, grp, "docker", true, []string{hosted, prx})

		d.MustExec(t, "pull through the group", fmt.Sprintf("set -e\ndocker pull %s/%s/app:v1\n", host, grp))
		if got := headDigest(t, s, nil, grp+"/app", "v1"); got != "sha256:"+mine.Hex {
			t.Fatalf("the group serves %s for v1, want the hosted sha256:%s", got, mine.Hex)
		}
		if got := headDigest(t, s, nil, grp+"/app", "v2"); got != "sha256:"+v2.Hex {
			t.Fatalf("the group serves %s for v2, want the proxy's sha256:%s", got, v2.Hex)
		}
		resp, body := ociDo(t, s, http.MethodGet, "/v2/"+grp+"/app/tags/list", nil, nil, nil)
		wantOCI(t, "tags list of the group", resp, body, http.StatusOK, "")
		var tl struct {
			Name string   `json:"name"`
			Tags []string `json:"tags"`
		}
		if err := json.Unmarshal(body, &tl); err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"only-hosted", "v1", "v2"} {
			if !slices.Contains(tl.Tags, want) {
				t.Fatalf("tags list of the group %v: no %s", tl.Tags, want)
			}
		}
		if tl.Name != grp+"/app" || !slices.IsSorted(tl.Tags) {
			t.Fatalf("tags list of the group: %+v", tl)
		}
		st, bearer, b := registryToken(t, s, s.AdminBasic(), "repository:"+grp+"/app:pull,push")
		wantAPIStatus(t, "token for a push to the group", st, b, http.StatusOK)
		resp, body = ociDo(t, s, http.MethodPost, "/v2/"+grp+"/app/blobs/uploads/", nil, bearer, nil)
		wantOCI(t, "a push to the group", resp, body, http.StatusMethodNotAllowed, "UNSUPPORTED")

		resp, body = ociDo(t, s, http.MethodGet, "/v2/"+prx+"/app/tags/list", nil, nil, nil)
		wantOCI(t, "tags list of the proxy", resp, body, http.StatusOK, "")
		if err := json.Unmarshal(body, &tl); err != nil || !slices.Contains(tl.Tags, "v1") || !slices.Contains(tl.Tags, "v2") || tl.Name != prx+"/app" {
			t.Fatalf("tags list of the proxy (the remote's tags): %s", body)
		}
	})

	t.Run("stopped remote", func(t *testing.T) {
		rm.stop(t)
		time.Sleep(1500 * time.Millisecond) // the cached tag is stale now
		resp, body := ociDo(t, s, http.MethodGet, "/v2/"+prx+"/app/manifests/v2", nil, nil, nil)
		wantOCI(t, "a cached tag while the remote is down", resp, body, http.StatusOK, "")
		if got := resp.Header.Get("Docker-Content-Digest"); got == "" {
			t.Fatal("a cached tag while the remote is down: no Docker-Content-Digest")
		}
		resp, body = ociDo(t, s, http.MethodGet, "/v2/"+prx+"/app/manifests/never-cached", nil, nil, nil)
		wantOCI(t, "an uncached tag while the remote is down", resp, body, http.StatusBadGateway, "UNKNOWN")
	})
}
