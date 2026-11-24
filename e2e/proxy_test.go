//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// CreateProxy creates a proxy repo of format with the remote settings px (remote_url, username,
// password, metadata_ttl, not_found_ttl). force skips the check of the remote.
func (s *Server) CreateProxy(t testing.TB, name, format string, anon bool, px map[string]any, force bool) {
	t.Helper()
	s.MustAPI(t, http.MethodPost, "/api/repos", map[string]any{
		"name": name, "format": format, "anonymous_read": anon, "kind": "proxy", "proxy": px, "force": force}, s.Admin(), nil)
}

// ProxyState is the proxy part of GET /api/repos/{name}.
type ProxyState struct {
	Kind  string `json:"kind"`
	Proxy *struct {
		RemoteURL      string  `json:"remote_url"`
		HasCredentials bool    `json:"has_credentials"`
		MetadataTTL    int64   `json:"metadata_ttl"`
		NotFoundTTL    int64   `json:"not_found_ttl"`
		LastOKAt       *string `json:"last_ok_at"`
		LastError      string  `json:"last_error"`
	} `json:"proxy"`
}

// remoteGets returns the status of every GET of path in the access log of s, in order, through
// every request that returned before the call (Server.LogsThrough).
func remoteGets(t testing.TB, s *Server, path string) []int {
	t.Helper()
	var out []int
	sc := bufio.NewScanner(strings.NewReader(s.LogsThrough(t)))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.IndexByte(line, '{'); i > 0 {
			line = line[i:] // a log line may carry a stream prefix
		}
		var e struct {
			Msg    string `json:"msg"`
			Method string `json:"method"`
			Path   string `json:"path"`
			Status int    `json:"status"`
		}
		if json.Unmarshal([]byte(line), &e) == nil && e.Msg == "http" && e.Method == http.MethodGet && e.Path == path {
			out = append(out, e.Status)
		}
	}
	return out
}

// remoteGetCount counts the GETs of paths under prefix in the access log of s, through every
// request that returned before the call (Server.LogsThrough).
func remoteGetCount(t testing.TB, s *Server, prefix string) map[string]int {
	t.Helper()
	out := map[string]int{}
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
		if json.Unmarshal([]byte(line), &e) == nil && e.Msg == "http" && e.Method == http.MethodGet && strings.HasPrefix(e.Path, prefix) {
			out[e.Path]++
		}
	}
	return out
}

// caddyRedirectConf answers every request on redir:8080 with a redirect to the host recorder
// (port 8081). The recorder serves the file only to a request without an Authorization header,
// and answers 403 to a request with one.
const caddyRedirectConf = `{
	admin off
	auto_https off
}
:8080 {
	redir http://recorder:8081{uri} 302
}
:8081 {
	@auth header Authorization *
	respond @auth "credentials arrived" 403
	respond ` + "`redirected file\n`" + ` 200
}
`

// startRedirector starts Caddy with the network aliases redir and recorder.
func startRedirector(t *testing.T) {
	t.Helper()
	startCaddy(t, caddyRedirectConf, "Caddy redirector", "redir", "recorder")
}

// proxyServesRemote checks that GET proxyPath on s answers the bytes of GET remotePath on remote.
func proxyServesRemote(t *testing.T, s, remote *Server, proxyPath, remotePath string) {
	t.Helper()
	st, want := debGet(t, remote, nil, remotePath, nil)
	if st != http.StatusOK || len(want) == 0 {
		t.Fatalf("GET %s on the remote: status %d", remotePath, st)
	}
	rawWantGet(t, s, nil, proxyPath, nil, want)
}

func TestProxy(t *testing.T) {
	t.Parallel()
	debs := buildDebPackages(t)
	rpms := buildRPMPackages(t)
	OnLocalDisk(t, func(t *testing.T, opts ServerOpts) {
		// The remote is a second server on the fs store, so its access log shows each request
		// the proxy makes.
		remote := StartServer(t, ServerOpts{})
		s := StartServer(t, opts)

		up := RandName("upstream")
		remote.CreateRepo(t, up, "maven", true, nil)
		uploader, uploaderPass := RandName("uploader"), randHex(12)
		remote.CreateUser(t, uploader, uploaderPass)
		remote.SetPerms(t, up, uploader, "repo.download", "repo.publish")

		group := "com.example.proxy" + randHex(4)
		groupPath := strings.ReplaceAll(group, ".", "/")
		upAuth := remote.PublishAuth(t, uploader, up)
		// A release on the remote: the proxy serves the remote's bytes. How mvn reads a Maven repo is
		// TestMaven's; here only what the proxy fetches and keeps matters.
		dir := groupPath + "/hello/1.0.0/"
		release := []string{dir + "hello-1.0.0.jar", dir + "hello-1.0.0.pom"}
		files := map[string][]byte{release[0]: rawRandom(t, 4096), release[1]: []byte("<project><groupId>" + group + "</groupId><artifactId>hello</artifactId><version>1.0.0</version></project>\n")}
		for _, p := range release {
			if resp, b := rawPut(t, remote, "/maven/"+up+"/"+p, files[p], upAuth); resp.StatusCode/100 != 2 {
				t.Fatalf("PUT %s on the remote: status %d: %s", p, resp.StatusCode, b)
			}
		}
		metadata := "<metadata><groupId>" + group + "</groupId><artifactId>hello</artifactId><versioning><release>1.0.0</release><versions><version>1.0.0</version></versions><lastUpdated>20260101000000</lastUpdated></versioning></metadata>\n"
		if resp, b := rawPut(t, remote, "/maven/"+up+"/"+groupPath+"/hello/maven-metadata.xml", []byte(metadata), upAuth); resp.StatusCode/100 != 2 {
			t.Fatalf("PUT maven-metadata.xml on the remote: status %d: %s", resp.StatusCode, b)
		}

		central := RandName("central")
		s.CreateProxy(t, central, "maven", true, map[string]any{"remote_url": remote.InternalURL + "/maven/" + up}, false)
		var ps ProxyState
		s.MustAPI(t, http.MethodGet, "/api/repos/"+central, nil, s.Admin(), &ps)
		if ps.Kind != "proxy" || ps.Proxy == nil || ps.Proxy.RemoteURL != remote.InternalURL+"/maven/"+up+"/" {
			t.Fatalf("GET proxy: %+v", ps.Proxy)
		}

		// The release reads through the proxy; a second read makes no remote request.
		resolve := func() {
			t.Helper()
			for _, p := range release {
				rawWantGet(t, s, opts.S3, "/maven/"+central+"/"+p, nil, files[p])
			}
		}
		// Count only what the reads through the proxy ask the remote for.
		prefix := "/maven/" + up + "/" + groupPath + "/"
		deployed := remoteGetCount(t, remote, prefix)
		resolve()
		first := remoteGetCount(t, remote, prefix)
		fetched := 0
		for p, n := range first {
			if n -= deployed[p]; n > 1 {
				t.Fatalf("the proxy sent %d GETs of %s to the remote, want at most 1", n, p)
			}
			fetched += n
		}
		if fetched == 0 {
			t.Fatalf("the remote got no request under %s from the proxy", prefix)
		}
		resolve()
		if second := remoteGetCount(t, remote, prefix); fmt.Sprint(second) != fmt.Sprint(first) {
			t.Fatalf("the second resolve made remote requests:\nbefore %v\nafter  %v", first, second)
		}

		// A changed maven-metadata.xml shows after metadata_ttl and not before; the recheck of an
		// unchanged file uses 304. The test uses 3 s: checked_at has a resolution of one second.
		s.MustAPI(t, http.MethodPatch, "/api/repos/"+central, map[string]any{"proxy": map[string]any{"metadata_ttl": 3, "not_found_ttl": 3}}, s.Admin(), nil)
		mdRel := groupPath + "/hello/maven-metadata.xml"
		mdURL := "/maven/" + central + "/" + mdRel
		// The proxy caches the file first (a fixed version needs no maven-metadata.xml, so the
		// resolves above may not have fetched it), then checks it again after the time.
		if st, b := rawGet(t, s, opts.S3, mdURL, nil); st != http.StatusOK {
			t.Fatalf("GET %s: status %d: %s", mdURL, st, b)
		}
		time.Sleep(4 * time.Second)
		st, before := rawGet(t, s, opts.S3, mdURL, nil)
		if st != http.StatusOK {
			t.Fatalf("GET %s: status %d", mdURL, st)
		}
		if got := remoteGets(t, remote, "/maven/"+up+"/"+mdRel); len(got) < 2 || got[len(got)-1] != http.StatusNotModified {
			t.Fatalf("the recheck of an unchanged maven-metadata.xml: remote statuses %v, want a 304 last", got)
		}
		changed := strings.Replace(string(before), "<versions>", "<versions>\n      <version>0.0.1-e2e</version>", 1)
		if resp, b := rawPut(t, remote, "/maven/"+up+"/"+mdRel, []byte(changed), upAuth); resp.StatusCode/100 != 2 {
			t.Fatalf("PUT the changed metadata on the remote: status %d: %s", resp.StatusCode, b)
		}
		if _, now := rawGet(t, s, opts.S3, mdURL, nil); strings.Contains(string(now), "0.0.1-e2e") {
			t.Fatalf("the changed metadata shows before metadata_ttl:\n%s", now)
		}
		time.Sleep(4 * time.Second)
		if _, now := rawGet(t, s, opts.S3, mdURL, nil); !strings.Contains(string(now), "0.0.1-e2e") {
			t.Fatalf("the changed metadata does not show after metadata_ttl:\n%s", now)
		}

		// A remote 404 is remembered for not_found_ttl.
		late := "com/e2e/late/1.0/late-1.0.txt"
		for range 2 {
			if st, b := rawGet(t, s, opts.S3, "/maven/"+central+"/"+late, nil); st != http.StatusNotFound {
				t.Fatalf("GET a file the remote does not have: status %d, want 404: %s", st, b)
			}
		}
		if got := remoteGets(t, remote, "/maven/"+up+"/"+late); len(got) != 1 {
			t.Fatalf("the remote got %v for a missing file, want one GET", got)
		}
		if resp, b := rawPut(t, remote, "/maven/"+up+"/"+late, []byte("late file"), upAuth); resp.StatusCode/100 != 2 {
			t.Fatalf("PUT %s on the remote: status %d: %s", late, resp.StatusCode, b)
		}
		if st, _ := rawGet(t, s, opts.S3, "/maven/"+central+"/"+late, nil); st != http.StatusNotFound {
			t.Fatalf("a remembered 404: status %d, want 404 until not_found_ttl", st)
		}
		time.Sleep(4 * time.Second)
		rawWantGet(t, s, opts.S3, "/maven/"+central+"/"+late, nil, []byte("late file"))

		// A proxy is read-only.
		for _, auth := range []Auth{nil, s.AdminBasic()} {
			resp, b := rawPut(t, s, "/maven/"+central+"/com/e2e/x/1.0/x-1.0.jar", []byte("x"), auth)
			if resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != "GET, HEAD" || RefusalCode(b) != "read_only_kind" {
				t.Fatalf("PUT to the proxy: status %d, Allow %q: %s, want 405, Allow GET, HEAD and code read_only_kind", resp.StatusCode, resp.Header.Get("Allow"), b)
			}
		}

		// Through a group: a hosted member's file wins over the proxy's; a file only upstream
		// comes through the proxy.
		both, only := "com/e2e/both/1.0/both-1.0.txt", "com/e2e/only/1.0/only-1.0.txt"
		for p, body := range map[string]string{both: "from the remote", only: "only at the remote"} {
			if resp, b := rawPut(t, remote, "/maven/"+up+"/"+p, []byte(body), upAuth); resp.StatusCode/100 != 2 {
				t.Fatalf("PUT %s on the remote: status %d: %s", p, resp.StatusCode, b)
			}
		}
		own := RandName("own")
		s.CreateRepo(t, own, "maven", true, nil)
		mavenPut(t, s, s.AdminUser, own, both, []byte("from our own repository"))
		grp := RandName("all")
		s.CreateGroup(t, grp, "maven", true, []string{own, central})
		rawWantGet(t, s, opts.S3, "/maven/"+grp+"/"+both, nil, []byte("from our own repository"))
		rawWantGet(t, s, opts.S3, "/maven/"+grp+"/"+only, nil, []byte("only at the remote"))
		if got := remoteGets(t, remote, "/maven/"+up+"/"+both); len(got) != 0 {
			t.Fatalf("the group asked the remote for a file of its hosted member: %v", got)
		}

		// Remote credentials: a private remote resolves with them and answers 502 without them.
		secret := RandName("secret")
		remote.CreateRepo(t, secret, "maven", false, nil)
		remote.SetPerms(t, secret, uploader, "repo.download", "repo.publish")
		file := "com/e2e/secret/1.0/secret-1.0.txt"
		if resp, b := rawPut(t, remote, "/maven/"+secret+"/"+file, []byte("secret file"), remote.PublishAuth(t, uploader, secret)); resp.StatusCode/100 != 2 {
			t.Fatalf("PUT %s on the remote: status %d: %s", file, resp.StatusCode, b)
		}
		readToken := remote.CreateToken(t, uploader, uploaderPass, "proxy-e2e")
		withCreds, noCreds := RandName("with-creds"), RandName("no-creds")
		s.CreateProxy(t, withCreds, "maven", true, map[string]any{"remote_url": remote.InternalURL + "/maven/" + secret, "username": uploader, "password": readToken}, false)
		s.CreateProxy(t, noCreds, "maven", true, map[string]any{"remote_url": remote.InternalURL + "/maven/" + secret}, true)
		rawWantGet(t, s, opts.S3, "/maven/"+withCreds+"/"+file, nil, []byte("secret file"))
		if st, b := rawGet(t, s, opts.S3, "/maven/"+noCreds+"/"+file, nil); st != http.StatusBadGateway {
			t.Fatalf("GET through a proxy without credentials: status %d, want 502: %s", st, b)
		}
		if st, b := s.API(t, http.MethodGet, "/api/repos/"+withCreds, nil, s.Admin()); st != http.StatusOK || strings.Contains(string(b), readToken) || !strings.Contains(string(b), `"has_credentials":true`) {
			t.Fatalf("GET a proxy with credentials: status %d: %s", st, b)
		}
		if st, b := s.API(t, http.MethodPost, "/api/repos/"+withCreds+"/proxy/test", nil, s.Admin()); st != http.StatusOK {
			t.Fatalf("test the remote of %s: status %d, want 200: %s", withCreds, st, b)
		}

		// A redirect to another host does not carry the credentials: the recorder answers 403 to a
		// request with an Authorization header.
		startRedirector(t)
		redirected := RandName("redirected")
		s.CreateProxy(t, redirected, "maven", true, map[string]any{"remote_url": "http://redir:8080/m2/", "username": "someone", "password": "s3cret-e2e"}, false)
		rawWantGet(t, s, opts.S3, "/maven/"+redirected+"/com/e2e/r/1.0/r-1.0.txt", nil, []byte("redirected file\n"))

		// Debian, Arch and RPM: the proxy serves the remote's signed indexes unchanged, so a client
		// checks them with the remote's key, and it has no key of its own. How apt, pacman and dnf
		// read a repo is the format tests'.
		upDeb := RandName("upstream-deb")
		remote.CreateRepo(t, upDeb, "debian", true, nil)
		remote.SetPerms(t, upDeb, uploader, "repo.download", "repo.publish")
		debAuth := remote.PublishAuth(t, uploader, upDeb)
		for _, deb := range [][]byte{debs.hello1, debs.native} {
			req := remote.NewRequest(t, http.MethodPut, "/debian/"+upDeb+"/upload/stable/main", strings.NewReader(string(deb)))
			if resp, b := remote.Do(t, req, debAuth); resp.StatusCode/100 != 2 {
				t.Fatalf("upload a .deb to the remote: status %d: %s", resp.StatusCode, b)
			}
		}
		remote.WaitDebianIndexed(t, upDeb)
		debProxy := RandName("deb-proxy")
		s.CreateProxy(t, debProxy, "debian", true, map[string]any{"remote_url": remote.InternalURL + "/debian/" + upDeb}, false)
		proxyServesRemote(t, s, remote, "/debian/"+debProxy+"/dists/stable/InRelease", "/debian/"+upDeb+"/dists/stable/InRelease")
		if resp, _ := s.Do(t, s.NewRequest(t, http.MethodGet, "/debian/"+debProxy+"/key.asc", nil), nil); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("GET the key of a Debian proxy: status %d, want 404", resp.StatusCode)
		}

		upArch := RandName("upstream-arch")
		remote.CreateRepo(t, upArch, "arch", true, nil)
		remote.SetPerms(t, upArch, uploader, "repo.download", "repo.publish")
		archMustUpload(t, remote, upArch, goArchPkg(t, "e2e-proxied", "1.0-1", "any", map[string]int64{"usr/share/e2e-proxied/data": 1}),
			remote.PublishAuth(t, uploader, upArch), "e2e-proxied", "1.0-1", "any")
		remote.WaitDebianIndexed(t, upArch)
		archProxy := RandName("arch-proxy")
		s.CreateProxy(t, archProxy, "arch", true, map[string]any{"remote_url": remote.InternalURL + "/arch/" + upArch}, false)
		for _, f := range []string{upArch + ".db", upArch + ".db.sig"} {
			proxyServesRemote(t, s, remote, "/arch/"+archProxy+"/x86_64/"+f, "/arch/"+upArch+"/x86_64/"+f)
		}
		if resp, _ := s.Do(t, s.NewRequest(t, http.MethodGet, "/arch/"+archProxy+"/key.asc", nil), nil); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("GET the key of an Arch proxy: status %d, want 404", resp.StatusCode)
		}

		upRPM := RandName("upstream-rpm")
		remote.CreateRepo(t, upRPM, "rpm", true, nil)
		remote.SetPerms(t, upRPM, uploader, "repo.download", "repo.publish")
		rpmMustUpload(t, remote, upRPM, rpms.data, remote.PublishAuth(t, uploader, upRPM), "e2e-data", "1.0.0-1", "noarch")
		remote.WaitDebianIndexed(t, upRPM)
		rpmProxy := RandName("rpm-proxy")
		s.CreateProxy(t, rpmProxy, "rpm", true, map[string]any{"remote_url": remote.InternalURL + "/rpm/" + upRPM + "/"}, false)
		for _, f := range []string{"repodata/repomd.xml", "repodata/repomd.xml.asc"} {
			proxyServesRemote(t, s, remote, "/rpm/"+rpmProxy+"/"+f, "/rpm/"+upRPM+"/"+f)
		}
		if resp, _ := s.Do(t, s.NewRequest(t, http.MethodGet, "/rpm/"+rpmProxy+"/key.asc", nil), nil); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("GET the key of an RPM proxy: status %d, want 404", resp.StatusCode)
		}

		// The remote stops: a cached file still serves, an uncached one answers 502, and the
		// proxy records the error.
		if err := remote.Stop(context.Background(), nil); err != nil {
			t.Fatalf("stop the remote: %v", err)
		}
		rawWantGet(t, s, opts.S3, "/maven/"+central+"/"+late, nil, []byte("late file"))
		if st, b := rawGet(t, s, opts.S3, "/maven/"+central+"/com/e2e/never/1.0/never-1.0.txt", nil); st != http.StatusBadGateway {
			t.Fatalf("GET an uncached file while the remote is down: status %d, want 502: %s", st, b)
		}
		s.MustAPI(t, http.MethodGet, "/api/repos/"+central, nil, s.Admin(), &ps)
		if ps.Proxy == nil || ps.Proxy.LastError == "" {
			t.Fatalf("the proxy has no last_error after the remote stopped: %+v", ps.Proxy)
		}

		// A stale changing file serves from the cache while the remote is down, and the failure
		// is remembered: a second request within 30 s does not ask the remote again.
		for i := range 2 {
			st, b := rawGet(t, s, opts.S3, mdURL, nil)
			if st != http.StatusOK || !strings.Contains(string(b), "0.0.1-e2e") {
				t.Fatalf("GET %d of a stale maven-metadata.xml while the remote is down: status %d: %s", i+1, st, b)
			}
		}
		if n := remoteFailures(t, s, central, mdRel); n != 1 {
			t.Fatalf("two GETs of a stale file while the remote is down asked the remote %d times, want 1", n)
		}
	})
}

// remoteFailures counts the log lines of s for a failed remote request of path in the proxy repo,
// through every request that returned before the call (Server.LogsThrough): a message that
// starts with "remote failed", and the repo and path attributes.
func remoteFailures(t testing.TB, s *Server, repo, path string) int {
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
			Msg  string `json:"msg"`
			Repo string `json:"repo"`
			Path string `json:"path"`
		}
		if json.Unmarshal([]byte(line), &e) == nil && strings.HasPrefix(e.Msg, "remote failed") && e.Repo == repo && e.Path == path {
			n++
		}
	}
	return n
}

// TestProxyStream: the first GET of a large file that a proxy has not cached gets the remote's
// bytes while the proxy stores them (no ETag yet, the remote's length), on both stores. Requests
// that come at the same time wait for that fetch; the remote sees one GET, and the stored file
// serves the same bytes afterwards.
func TestProxyStream(t *testing.T) {
	t.Parallel()
	ForEachStorage(t, func(t *testing.T, opts ServerOpts) {
		remote := StartServer(t, ServerOpts{})
		s := StartServer(t, opts)
		up := RandName("upstream")
		remote.CreateRepo(t, up, "maven", true, nil)
		single, together := "com/e2e/big/1.0/big-1.0.jar", "com/e2e/big/2.0/big-2.0.jar"
		files := map[string][]byte{single: rawRandom(t, 24<<20), together: rawRandom(t, 16<<20)}
		for p, b := range files {
			mavenPut(t, remote, remote.AdminUser, up, p, b)
		}
		central := RandName("central")
		s.CreateProxy(t, central, "maven", true, map[string]any{"remote_url": remote.InternalURL + "/maven/" + up}, false)

		resp, body := s.Do(t, s.NewRequest(t, http.MethodGet, "/maven/"+central+"/"+single, nil), nil)
		switch {
		case resp.StatusCode != http.StatusOK:
			t.Fatalf("streamed GET through the proxy: status %d: %.200s", resp.StatusCode, body)
		case !bytes.Equal(body, files[single]):
			t.Fatalf("streamed GET through the proxy: got %d bytes, want %d (content differs)", len(body), len(files[single]))
		case resp.ContentLength != int64(len(files[single])):
			t.Fatalf("streamed GET through the proxy: Content-Length %d, want %d", resp.ContentLength, len(files[single]))
		case resp.Header.Get("ETag") != "":
			t.Fatalf("the first GET of an uncached file did not stream (it has the ETag %s of a stored file)", resp.Header.Get("ETag"))
		case resp.Header.Get("Content-Type") != "application/java-archive":
			t.Fatalf("streamed GET through the proxy: Content-Type %q", resp.Header.Get("Content-Type"))
		}
		rawWantGet(t, s, opts.S3, "/maven/"+central+"/"+single, nil, files[single])

		var wg sync.WaitGroup
		errs := make([]string, 3)
		for i := range errs {
			wg.Go(func() {
				st, b := rawGet(t, s, opts.S3, "/maven/"+central+"/"+together, nil)
				if st != http.StatusOK || !bytes.Equal(b, files[together]) {
					errs[i] = fmt.Sprintf("status %d, %d bytes", st, len(b))
				}
			})
		}
		wg.Wait()
		for i, e := range errs {
			if e != "" {
				t.Fatalf("concurrent GET %d of an uncached file: %s, want 200 with the remote's bytes", i+1, e)
			}
		}
		for _, p := range []string{single, together} {
			if got := remoteGets(t, remote, "/maven/"+up+"/"+p); len(got) != 1 {
				t.Fatalf("the remote got %v for %s, want one GET", got, p)
			}
		}
	})
}
