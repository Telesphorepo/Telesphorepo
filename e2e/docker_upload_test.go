//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// The upload host (docker.upload_url): a reverse proxy with a small body limit sits in front of
// the main host, as Cloudflare does with 100 MB. A push starts on the main host, and the server
// sends the layer uploads to the upload host with an upload token in the URL. The clients name
// only the main host.

// caddyBodyLimitConf is a reverse proxy on :8080 to the server at upstream that refuses a request
// body above 1 MB with 413. Like Cloudflare, Caddy keeps the Host header and the Location headers
// of the server.
const caddyBodyLimitConf = `{
	admin off
	auto_https off
}
:8080 {
	request_body {
		max_size 1MB
	}
	reverse_proxy %s:8080
}
`

// startBodyLimitProxy starts Caddy with the network alias alias in front of the server at
// upstream (a network alias), with a body limit of 1 MB.
func startBodyLimitProxy(t *testing.T, alias, upstream string) {
	t.Helper()
	startCaddy(t, fmt.Sprintf(caddyBodyLimitConf, upstream), "Caddy body limit", alias)
}

// TestDockerUploadHost puts Caddy with a body limit of 1 MB in front of the main host (base_url)
// and points docker.upload_url at the server itself. With docker.direct_uploads off, a crane push of
// a 5 MB layer through the proxy fails with 413. With it on, POST on the main host answers an absolute
// Location on the upload host with an upload token, which writes to that upload only; and the
// Docker CLI (both push paths of the engine), crane and skopeo push a 5 MB layer through the proxy
// hostname and pull it back. ORAS and Helm 4 (oras-go v2.6.1 and later) refuse an upload Location
// on another host, so they push to the upload host by name and pull through the proxy.
func TestDockerUploadHost(t *testing.T) {
	t.Parallel()
	OnLocalDisk(t, func(t *testing.T, opts ServerOpts) {
		srvAlias, mainAlias := RandName("origin"), RandName("main")
		opts.Alias = srvAlias
		env := map[string]string{"TR_BASE_URL": "http://" + mainAlias + ":8080"}
		for k, v := range opts.Env {
			env[k] = v
		}
		opts.Env = env
		s := StartServer(t, opts)
		startBodyLimitProxy(t, mainAlias, srvAlias)
		mainHost, uploadURL := mainAlias+":8080", "http://"+srvAlias+":8080"
		r := RandName("r")
		s.CreateRepo(t, r, "docker", false, nil)
		s.CreateUser(t, "pusher", "pusher-pass-1234")
		s.SetPerms(t, r, "pusher", "repo.download", "repo.publish")
		// Each push gets its own publish session, so no session ends within the test.
		token := func() string { return PublishToken(t, s, "pusher", r) }
		files, manifestHex := ociLayout(t, "/layout", rawRandom(t, 5<<20))

		var hosts map[string]string
		s.MustAPI(t, http.MethodGet, "/docker/hosts", nil, nil, &hosts)
		if hosts["registry"] != mainHost || len(hosts) != 1 {
			t.Fatalf("GET /docker/hosts with direct uploads off: %v, want {registry: %s}", hosts, mainHost)
		}

		st, b := s.API(t, http.MethodPatch, "/api/settings", map[string]any{"docker.direct_uploads": true}, s.Admin())
		wantAPIStatus(t, "direct uploads without an upload address", st, b, http.StatusBadRequest)
		s.MustAPI(t, http.MethodPatch, "/api/settings", map[string]any{"docker.upload_url": uploadURL}, s.Admin(), nil)
		t.Run("direct uploads off: the proxy refuses the layer", func(t *testing.T) {
			code, out := RunClient(t, craneImage, ClientOpts{Files: files, Cmd: []string{fmt.Sprintf(
				"crane auth login %[1]s -u pusher -p '%[2]s' && crane push --insecure /layout %[1]s/%[3]s/control:v1 2>&1", mainHost, token(), r)}})
			if code == 0 || !strings.Contains(out, "413") {
				t.Fatalf("crane push through the proxy without the upload host: exit %d, want a 413:\n%s", code, out)
			}
		})

		s.MustAPI(t, http.MethodPatch, "/api/settings", map[string]any{"docker.direct_uploads": true}, s.Admin(), nil)

		t.Run("upload token", func(t *testing.T) {
			auth := Basic{"pusher", token()}
			start := func() *url.URL {
				resp, b := ociDo(t, s, http.MethodPost, "/v2/"+r+"/raw/blobs/uploads/", nil, auth, nil)
				wantOCI(t, "start an upload", resp, b, http.StatusAccepted, "")
				loc, err := url.Parse(resp.Header.Get("Location"))
				if err != nil || loc.Scheme+"://"+loc.Host != uploadURL || !strings.HasPrefix(loc.Path, "/v2/"+r+"/raw/blobs/uploads/") || loc.Query().Get("_state") == "" {
					t.Fatalf("upload Location %q: want %s/v2/%s/raw/blobs/uploads/<id>?_state=<token>", resp.Header.Get("Location"), uploadURL, r)
				}
				return loc
			}
			// The test reaches the server at s.URL: it keeps the path and the query of the Location.
			at := func(loc *url.URL, q url.Values) string { return loc.Path + "?" + q.Encode() }
			one, other := start(), start()
			blob := rawRandom(t, 4096)

			resp, b := ociDo(t, s, http.MethodPatch, one.Path, blob, nil, nil)
			wantOCI(t, "PATCH without a credential", resp, b, http.StatusUnauthorized, "")
			resp, b = ociDo(t, s, http.MethodPatch, at(one, other.Query()), blob, nil, nil)
			wantOCI(t, "PATCH with the token of another upload", resp, b, http.StatusUnauthorized, "")
			q := one.Query()
			q.Set("_state", q.Get("_state")+"x")
			resp, b = ociDo(t, s, http.MethodPatch, at(one, q), blob, nil, nil)
			wantOCI(t, "PATCH with a changed token", resp, b, http.StatusUnauthorized, "")
			resp, b = ociDo(t, s, http.MethodGet, "/v2/"+r+"/raw/manifests/v1?_state="+url.QueryEscape(one.Query().Get("_state")), nil, nil, nil)
			wantOCI(t, "the token on another path", resp, b, http.StatusUnauthorized, "")

			resp, b = ociDo(t, s, http.MethodPatch, at(one, one.Query()), blob, nil, nil)
			wantOCI(t, "PATCH with the upload token", resp, b, http.StatusAccepted, "")
			next, err := url.Parse(resp.Header.Get("Location"))
			if err != nil || next.Scheme+"://"+next.Host != uploadURL || next.Path != one.Path || next.Query().Get("_state") == "" {
				t.Fatalf("PATCH Location %q: want the upload on %s with a token", resp.Header.Get("Location"), uploadURL)
			}
			q = next.Query()
			q.Set("digest", "sha256:"+sha256Hex(blob))
			resp, b = ociDo(t, s, http.MethodPut, at(next, q), nil, nil, nil)
			wantOCI(t, "PUT with the upload token", resp, b, http.StatusCreated, "")
			resp, b = ociDo(t, s, http.MethodHead, "/v2/"+r+"/raw/blobs/sha256:"+sha256Hex(blob), nil, s.AdminBasic(), nil)
			wantOCI(t, "HEAD the uploaded blob", resp, b, http.StatusOK, "")
		})

		// A new Docker engine keeps images in containerd, which pushes with the containerd pusher.
		// With a graph driver set (vfs), it pushes with the distribution client.
		for _, eng := range []struct{ name, driver string }{{"docker containerd store", ""}, {"docker graph driver", "vfs"}} {
			t.Run(eng.name, func(t *testing.T) {
				var args []string
				if eng.driver != "" {
					args = append(args, "--storage-driver="+eng.driver)
				}
				d := StartDindArgs(t, append(args, "--insecure-registry="+mainHost)...)
				img := mainHost + "/" + r + "/" + strings.ReplaceAll(eng.name, " ", "-")
				d.MustExec(t, "push and pull through the proxy", "set -e\n"+DockerScratchImage("/ctx", 5<<20)+fmt.Sprintf(`printf '%%s' '%[1]s' | docker login %[2]s -u pusher --password-stdin
docker build -q -t %[3]s:v1 /ctx
docker push %[3]s:v1
docker rmi %[3]s:v1
docker pull %[3]s:v1
`, token(), mainHost, img))
			})
		}

		t.Run("crane", func(t *testing.T) {
			code, out := RunClient(t, craneImage, ClientOpts{Files: files, Cmd: []string{fmt.Sprintf(
				"set -e; crane auth login %[1]s -u pusher -p '%[2]s'; crane push --insecure /layout %[1]s/%[3]s/crane:v1; crane pull --insecure %[1]s/%[3]s/crane:v1 /tmp/img.tar 2>&1", mainHost, token(), r)}})
			if code != 0 {
				t.Fatalf("crane through the proxy: exit %d:\n%s", code, out)
			}
			if got := headDigest(t, s, s.AdminBasic(), r+"/crane", "v1"); got != "sha256:"+manifestHex {
				t.Fatalf("crane push: tag v1 names %s, want sha256:%s", got, manifestHex)
			}
		})

		t.Run("skopeo", func(t *testing.T) {
			code, out := RunClient(t, skopeoImage, ClientOpts{Files: files, Cmd: []string{fmt.Sprintf(
				"set -e; skopeo copy --dest-tls-verify=false --dest-creds 'pusher:%[2]s' --preserve-digests oci:/layout:v1 docker://%[1]s/%[3]s/skopeo:v1 2>&1; "+
					"skopeo copy --src-tls-verify=false --src-creds 'pusher:%[2]s' docker://%[1]s/%[3]s/skopeo:v1 oci:/out:v1 2>&1", mainHost, token(), r)}})
			if code != 0 {
				t.Fatalf("skopeo through the proxy: exit %d:\n%s", code, out)
			}
			if got := headDigest(t, s, s.AdminBasic(), r+"/skopeo", "v1"); got != "sha256:"+manifestHex {
				t.Fatalf("skopeo push: tag v1 names %s, want sha256:%s", got, manifestHex)
			}
		})

		// ORAS and Helm 4 refuse an upload Location on another host, so they push to the upload
		// host by name: there the upload Location stays on that host. They pull through the proxy.
		uploadHost := srvAlias + ":8080"
		t.Run("upload host answers on its own host", func(t *testing.T) {
			s.MustAPI(t, http.MethodGet, "/docker/hosts", nil, nil, &hosts)
			if hosts["registry"] != mainHost || hosts["upload"] != uploadHost {
				t.Fatalf("GET /docker/hosts with direct uploads on: %v, want registry %s and upload %s", hosts, mainHost, uploadHost)
			}
			req := s.NewRequest(t, http.MethodPost, "/v2/"+r+"/raw/blobs/uploads/", nil)
			req.Host = uploadHost
			resp, b := s.Do(t, req, Basic{"pusher", token()})
			wantOCI(t, "start an upload on the upload host", resp, b, http.StatusAccepted, "")
			if loc := resp.Header.Get("Location"); !strings.HasPrefix(loc, "/v2/"+r+"/raw/blobs/uploads/") || strings.Contains(loc, "_state") {
				t.Fatalf("upload Location on the upload host %q: want a path without a token", loc)
			}
			req = s.NewRequest(t, http.MethodGet, "/v2/", nil)
			req.Host = uploadHost
			resp, b = s.Do(t, req, nil)
			wantOCI(t, "GET /v2/ on the upload host", resp, b, http.StatusUnauthorized, "UNAUTHORIZED")
			if ch := resp.Header.Get("WWW-Authenticate"); !strings.Contains(ch, `realm="http://`+uploadHost+`/docker/token"`) {
				t.Fatalf("the challenge on the upload host names another realm: %q", ch)
			}
		})

		t.Run("oras", func(t *testing.T) {
			code, out := RunClient(t, orasImage, ClientOpts{WorkingDir: "/tmp", Cmd: []string{fmt.Sprintf(`set -e
cd /tmp && head -c 5242880 /dev/urandom > big.bin
oras push --plain-http -u pusher -p '%[1]s' %[2]s/%[4]s/oras:v1 big.bin
oras pull --plain-http -u pusher -p '%[1]s' -o /tmp/out %[3]s/%[4]s/oras:v1
cmp /tmp/out/big.bin big.bin
`, token(), uploadHost, mainHost, r)}})
			if code != 0 {
				t.Fatalf("oras push to the upload host, pull through the proxy: exit %d:\n%s", code, out)
			}
		})

		t.Run("helm", func(t *testing.T) {
			tk := token()
			code, out := RunClient(t, helmImage, ClientOpts{WorkingDir: "/tmp", Cmd: []string{fmt.Sprintf(`set -e
cd /tmp
helm create mychart >/dev/null
helm package mychart
printf '%%s' '%[1]s' | helm registry login %[2]s -u pusher --password-stdin --plain-http
printf '%%s' '%[1]s' | helm registry login %[3]s -u pusher --password-stdin --plain-http
helm push mychart-0.1.0.tgz oci://%[2]s/%[4]s --plain-http
mkdir -p /tmp/pulled
helm pull oci://%[3]s/%[4]s/mychart --version 0.1.0 --plain-http -d /tmp/pulled
cmp mychart-0.1.0.tgz /tmp/pulled/mychart-0.1.0.tgz
`, tk, uploadHost, mainHost, r)}})
			if code != 0 {
				t.Fatalf("helm push to the upload host, pull through the proxy: exit %d:\n%s", code, out)
			}
		})
	})
}
