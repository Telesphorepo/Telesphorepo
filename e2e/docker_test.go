//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

// The Docker format (OCI registry): the registry API at /v2/, the token endpoint, the real
// Docker CLI in Docker in Docker, the OCI conformance suite, crane, skopeo, ORAS and Helm, and
// the removal of layers that no manifest uses.

const conformanceImage = "ghcr.io/opencontainers/distribution-spec/conformance:v1.1.1"

// ociErrorCode returns the code of the first error of an OCI error body, or "".
func ociErrorCode(b []byte) string {
	var e struct {
		Errors []struct {
			Code string `json:"code"`
		} `json:"errors"`
	}
	if json.Unmarshal(b, &e) != nil || len(e.Errors) == 0 {
		return ""
	}
	return e.Errors[0].Code
}

// wantOCI fails the test unless the answer has the status and the OCI error code.
func wantOCI(t *testing.T, what string, resp *http.Response, body []byte, status int, code string) {
	t.Helper()
	if resp.StatusCode != status || (code != "" && ociErrorCode(body) != code) {
		t.Fatalf("%s: status %d %q, want %d %s: %s", what, resp.StatusCode, ociErrorCode(body), status, code, body)
	}
}

// ociDo sends method to the /v2/ path of s with auth, body and headers.
func ociDo(t *testing.T, s *Server, method, path string, body []byte, auth Auth, hdr map[string]string) (*http.Response, []byte) {
	t.Helper()
	req := s.NewRequest(t, method, path, bytes.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	return s.Do(t, req, auth)
}

// ociBlob uploads b to name in one request (POST with digest=) and returns its hex.
func ociBlob(t *testing.T, s *Server, auth Auth, name string, b []byte) string {
	t.Helper()
	h := sha256Hex(b)
	resp, body := ociDo(t, s, http.MethodPost, "/v2/"+name+"/blobs/uploads/?digest=sha256:"+h, b, auth, nil)
	wantOCI(t, "upload a blob to "+name, resp, body, http.StatusCreated, "")
	return h
}

// ociImage pushes an OCI image manifest of a fresh config and the layers to name:ref (a tag or
// a digest) and returns the manifest's hex, its bytes and the hex of its config.
func ociImage(t *testing.T, s *Server, auth Auth, name, ref string, extra map[string]any, layers ...[]byte) (string, []byte, string) {
	t.Helper()
	config := []byte(`{"architecture":"amd64","os":"linux","rootfs":{"type":"layers","diff_ids":[]},"created":"` + time.Now().UTC().Format(time.RFC3339Nano) + `","x":"` + randHex(8) + `"}`)
	ch := ociBlob(t, s, auth, name, config)
	var ls []map[string]any
	for _, l := range layers {
		ls = append(ls, map[string]any{"mediaType": "application/vnd.oci.image.layer.v1.tar", "digest": "sha256:" + ociBlob(t, s, auth, name, l), "size": len(l)})
	}
	m := map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
		"config": map[string]any{"mediaType": "application/vnd.oci.image.config.v1+json", "digest": "sha256:" + ch, "size": len(config)},
		"layers": ls}
	for k, v := range extra {
		m[k] = v
	}
	if ls == nil {
		m["layers"] = []any{}
	}
	mb, _ := json.Marshal(m)
	resp, body := ociDo(t, s, http.MethodPut, "/v2/"+name+"/manifests/"+ref, mb, auth, map[string]string{"Content-Type": "application/vnd.oci.image.manifest.v1+json"})
	wantOCI(t, "put the manifest "+name+":"+ref, resp, body, http.StatusCreated, "")
	mh := sha256Hex(mb)
	if got := resp.Header.Get("Docker-Content-Digest"); got != "sha256:"+mh {
		t.Fatalf("put the manifest %s:%s: Docker-Content-Digest %q, want sha256:%s", name, ref, got, mh)
	}
	return mh, mb, ch
}

// registryToken asks the token endpoint of s for a bearer token with the scopes, with auth (nil:
// anonymous). It returns the status, the token and the body.
func registryToken(t *testing.T, s *Server, auth Auth, scopes ...string) (int, Bearer, []byte) {
	t.Helper()
	q := url.Values{"service": {registryHost(s)}}
	for _, sc := range scopes {
		q.Add("scope", sc)
	}
	resp, b := s.Do(t, s.NewRequest(t, http.MethodGet, "/docker/token?"+q.Encode(), nil), auth)
	var out struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	_ = json.Unmarshal(b, &out)
	if resp.StatusCode == http.StatusOK && (out.Token == "" || out.Token != out.AccessToken || out.ExpiresIn < 60) {
		t.Fatalf("token answer %s: want token = access_token and expires_in >= 60", b)
	}
	return resp.StatusCode, Bearer{out.Token}, b
}

// headDigest returns the Docker-Content-Digest of a HEAD of the manifest ref of name.
func headDigest(t *testing.T, s *Server, auth Auth, name, ref string) string {
	t.Helper()
	resp, b := ociDo(t, s, http.MethodHead, "/v2/"+name+"/manifests/"+ref, nil, auth, nil)
	wantOCI(t, "HEAD "+name+":"+ref, resp, b, http.StatusOK, "")
	return resp.Header.Get("Docker-Content-Digest")
}

// dockerUsers creates the users of the registry tests: pusher (download, publish and delete on
// priv; download and publish on pub) and reader (download on priv).
func dockerUsers(t *testing.T, s *Server, priv, pub string) {
	t.Helper()
	s.CreateUser(t, "pusher", "pusher-pass-1234")
	s.CreateUser(t, "reader", "reader-pass-1234")
	s.SetPerms(t, priv, "pusher", "repo.download", "repo.publish", "repo.delete")
	s.SetPerms(t, pub, "pusher", "repo.download", "repo.publish")
	s.SetPerms(t, priv, "reader", "repo.download")
}

// dockerSetting returns the effective value of the setting key (GET /api/settings).
func dockerSetting(t *testing.T, s *Server, key string) any {
	t.Helper()
	var set struct {
		Groups []struct {
			Settings []struct {
				Key   string `json:"key"`
				Value any    `json:"value"`
			} `json:"settings"`
		} `json:"groups"`
	}
	s.MustAPI(t, http.MethodGet, "/api/settings", nil, s.Admin(), &set)
	for _, g := range set.Groups {
		for _, it := range g.Settings {
			if it.Key == key {
				return it.Value
			}
		}
	}
	t.Fatalf("GET /api/settings has no %s", key)
	return nil
}

// TestDockerChallengeScheme: without base_url, the realm of the challenge has the scheme that the
// client used. A trusted proxy gives it with X-Forwarded-Proto or Forwarded; any other peer can
// only make it https.
func TestDockerChallengeScheme(t *testing.T) {
	t.Parallel()
	OnLocalDisk(t, func(t *testing.T, opts ServerOpts) {
		env := map[string]string{"TR_BASE_URL": ""} // an empty override leaves base_url unset
		for k, v := range opts.Env {
			env[k] = v
		}
		opts.Env = env
		s := StartServer(t, opts)
		u, err := url.Parse(s.URL)
		if err != nil {
			t.Fatal(err)
		}
		realm := func(name string, hdr map[string]string, want string) {
			t.Helper()
			resp, b := ociDo(t, s, http.MethodGet, "/v2/", nil, nil, hdr)
			wantOCI(t, "GET /v2/ "+name, resp, b, http.StatusUnauthorized, "UNAUTHORIZED")
			if ch := resp.Header.Get("WWW-Authenticate"); !strings.HasPrefix(ch, `Bearer realm="`+want+"://"+u.Host+`/docker/token"`) {
				t.Fatalf("GET /v2/ %s: challenge %q, want the realm %s://%s/docker/token", name, ch, want, u.Host)
			}
		}

		// With no trusted proxy, the forwarded proto can only make the realm https.
		setTrustedProxies(t, s, []string{})
		realm("over plain HTTP", nil, "http")
		realm("from an untrusted peer with X-Forwarded-Proto https", map[string]string{"X-Forwarded-Proto": "https"}, "https")
		realm("from an untrusted peer with Forwarded proto=https", map[string]string{"Forwarded": `for=203.0.113.9;proto=https`}, "https")
		realm("from an untrusted peer with X-Forwarded-Proto http", map[string]string{"X-Forwarded-Proto": "http"}, "http")

		// A trusted proxy gives the scheme; the first entry is the scheme that the first proxy saw.
		setTrustedProxies(t, s, []string{"private"})
		realm("through a trusted proxy with X-Forwarded-Proto https", map[string]string{"X-Forwarded-Proto": "https, http"}, "https")
		realm("through a trusted proxy with Forwarded proto=https", map[string]string{"Forwarded": `for="[2001:db8::1]";proto="HTTPS", for=10.0.0.1;proto=http`}, "https")
		realm("through a trusted proxy with X-Forwarded-Proto http", map[string]string{"X-Forwarded-Proto": "http"}, "http")
		realm("through a trusted proxy without a forwarded proto", nil, "http")
	})
}

// TestDockerRegistry drives the registry with HTTP requests and the real Docker CLI: the
// challenge, the token scopes, push and pull, logins that cannot push, anonymous pulls, a tag
// move, a multi-platform index and a read IP rule.
func TestDockerRegistry(t *testing.T) {
	t.Parallel()
	ForEachStorage(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		host := registryHost(s)
		priv, pub := RandName("priv"), RandName("pub")
		s.CreateRepo(t, priv, "docker", false, nil)
		s.CreateRepo(t, pub, "docker", true, nil)
		dockerUsers(t, s, priv, pub)

		t.Run("repo names", func(t *testing.T) {
			for _, bad := range []string{"a..b", "a-", "a_"} {
				st, b := s.API(t, http.MethodPost, "/api/repos", map[string]any{"name": bad, "format": "docker"}, s.Admin())
				wantAPIStatus(t, "create the docker repo "+bad, st, b, http.StatusBadRequest)
			}
		})

		t.Run("challenge and tokens", func(t *testing.T) {
			resp, b := ociDo(t, s, http.MethodGet, "/v2/", nil, nil, nil)
			wantOCI(t, "GET /v2/ anonymous", resp, b, http.StatusUnauthorized, "UNAUTHORIZED")
			ch := resp.Header.Get("WWW-Authenticate")
			if !strings.HasPrefix(ch, `Bearer realm="http://`+host+`/docker/token"`) || !strings.Contains(ch, `service="`+host+`"`) {
				t.Fatalf("GET /v2/: challenge %q", ch)
			}
			if v := resp.Header.Get("Docker-Distribution-API-Version"); v != "registry/2.0" {
				t.Fatalf("GET /v2/: API version header %q", v)
			}
			resp, b = ociDo(t, s, http.MethodGet, "/v2/", nil, s.AdminBasic(), nil)
			wantOCI(t, "GET /v2/ with a password", resp, b, http.StatusOK, "")

			// Anonymous: the subset, pull on the public repo only.
			st, anon, b := registryToken(t, s, nil, "repository:"+pub+"/app:pull", "repository:"+priv+"/app:pull")
			wantAPIStatus(t, "anonymous token", st, b, http.StatusOK)
			resp, b = ociDo(t, s, http.MethodGet, "/v2/"+pub+"/app/manifests/none", nil, anon, nil)
			wantOCI(t, "anonymous bearer on the public repo", resp, b, http.StatusNotFound, "MANIFEST_UNKNOWN")
			resp, b = ociDo(t, s, http.MethodGet, "/v2/"+priv+"/app/manifests/none", nil, anon, nil)
			wantOCI(t, "anonymous bearer on the private repo", resp, b, http.StatusUnauthorized, "UNAUTHORIZED")
			ch = resp.Header.Get("WWW-Authenticate")
			if !strings.Contains(ch, `error="insufficient_scope"`) || !strings.Contains(ch, `scope="repository:`+priv+`/app:pull"`) {
				t.Fatalf("bearer without the scope: challenge %q", ch)
			}

			st, _, b = registryToken(t, s, Basic{"reader", "wrong-password"}, "repository:"+priv+"/app:pull")
			wantAPIStatus(t, "token with a bad password", st, b, http.StatusUnauthorized)
			if ociErrorCode(b) != "UNAUTHORIZED" {
				t.Fatalf("token with a bad password: %s", b)
			}

			// A single-segment name names no image.
			resp, b = ociDo(t, s, http.MethodGet, "/v2/"+pub+"/manifests/v1", nil, nil, nil)
			wantOCI(t, "a name without an image", resp, b, http.StatusNotFound, "NAME_UNKNOWN")
		})

		d := StartDind(t, host)
		img, pubImg := host+"/"+priv+"/app", host+"/"+pub+"/app"
		tok := PublishToken(t, s, "pusher", priv)
		login := func(user, pass string) string {
			return fmt.Sprintf("docker logout %s >/dev/null 2>&1 || true\nprintf '%%s' '%s' | docker login %s -u %s --password-stdin\n", host, pass, host, user)
		}

		t.Run("push and pull", func(t *testing.T) {
			d.MustExec(t, "push and pull", "set -e\n"+DockerScratchImage("/ctx1", 20<<20, 1024, 100<<10)+"cd /ctx1 && sha256sum f0 f1 f2 > /sums && cd /\n"+
				login("pusher", tok)+fmt.Sprintf(`docker build -q -t %[1]s:v1 /ctx1
docker push %[1]s:v1
docker rmi %[1]s:v1
docker pull %[1]s:v1
docker create --name c1 %[1]s:v1 /f0
mkdir -p /out && docker cp c1:/f0 /out/f0 && docker cp c1:/f1 /out/f1 && docker cp c1:/f2 /out/f2
docker rm c1
cd /out && sha256sum -c /sums
`, img))
			idx := headDigest(t, s, s.AdminBasic(), priv+"/app", "v1")
			if !strings.HasPrefix(idx, "sha256:") {
				t.Fatalf("HEAD of the pushed tag: Docker-Content-Digest %q", idx)
			}
		})

		t.Run("password and read token cannot push", func(t *testing.T) {
			readTok := s.CreateToken(t, "reader", "reader-pass-1234", "docker")
			for _, cred := range [][2]string{{"reader", "reader-pass-1234"}, {"reader", readTok}} {
				d.MustExec(t, "pull with "+cred[0], "set -e\n"+login(cred[0], cred[1])+fmt.Sprintf("docker rmi %[1]s:v1 >/dev/null 2>&1 || true\ndocker pull %[1]s:v1\ndocker tag %[1]s:v1 %[1]s:nope\n", img))
				code, out := d.Exec(t, fmt.Sprintf("docker push %s:nope 2>&1", img))
				if code == 0 || !strings.Contains(out, "publish session") {
					t.Fatalf("push with a password or read token: exit %d, want a refusal that names the publish session:\n%s", code, out)
				}
			}
		})

		t.Run("anonymous pull", func(t *testing.T) {
			pubTok := PublishToken(t, s, "pusher", pub)
			d.MustExec(t, "anonymous pull", "set -e\n"+login("pusher", pubTok)+fmt.Sprintf(`docker tag %[1]s:v1 %[2]s:v1
docker push %[2]s:v1
docker logout %[3]s
docker rmi %[2]s:v1
docker pull %[2]s:v1
`, img, pubImg, host))
		})

		t.Run("tag move", func(t *testing.T) {
			old := headDigest(t, s, s.AdminBasic(), priv+"/app", "v1")
			d.MustExec(t, "tag move", "set -e\n"+DockerScratchImage("/ctx2", 4096, 2048)+login("pusher", tok)+fmt.Sprintf(`docker build -q -t %[1]s:v1 /ctx2
docker push %[1]s:v1
`, img))
			moved := headDigest(t, s, s.AdminBasic(), priv+"/app", "v1")
			if moved == old {
				t.Fatalf("the tag v1 still names %s after the push of another image", old)
			}
			d.MustExec(t, "pull the old digest", fmt.Sprintf("set -e\ndocker pull %s@%s\n", img, old))
		})

		t.Run("multi-platform index", func(t *testing.T) {
			d.MustExec(t, "multi-platform push", "set -e\n"+DockerScratchImage("/ctx3", 3000, 5000)+login("pusher", tok)+fmt.Sprintf(
				"docker build -q --platform linux/amd64,linux/arm64 --provenance=false --sbom=false -t %[1]s:multi /ctx3\ndocker push %[1]s:multi\n", img))
			resp, b := ociDo(t, s, http.MethodGet, "/v2/"+priv+"/app/manifests/multi", nil, s.AdminBasic(), nil)
			wantOCI(t, "GET the index", resp, b, http.StatusOK, "")
			var idx struct {
				MediaType string `json:"mediaType"`
				Manifests []struct {
					Platform struct {
						Architecture string `json:"architecture"`
					} `json:"platform"`
				} `json:"manifests"`
			}
			if err := json.Unmarshal(b, &idx); err != nil {
				t.Fatal(err)
			}
			var archs []string
			for _, m := range idx.Manifests {
				archs = append(archs, m.Platform.Architecture)
			}
			slices.Sort(archs)
			if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "index") && !strings.Contains(ct, "list") || !slices.Equal(archs, []string{"amd64", "arm64"}) {
				t.Fatalf("index: Content-Type %q, platforms %v: %s", ct, archs, b)
			}
			d.MustExec(t, "pull one platform", fmt.Sprintf("set -e\ndocker pull --platform linux/arm64 %s:multi\n", img))
		})

		t.Run("read IP rule", func(t *testing.T) {
			putIPRules(t, s, pub, ipRules{Read: []ipRule{{Kind: "cidr", Value: "203.0.113.0/24"}}})
			code, out := d.Exec(t, fmt.Sprintf("docker logout %s >/dev/null 2>&1; docker rmi %s:v1 >/dev/null 2>&1; docker pull %s:v1 2>&1", host, pubImg, pubImg))
			if code == 0 {
				t.Fatalf("pull from an address the read rule refuses: exit %d:\n%s", code, out)
			}
			if len(auditEvents(t, s, "network.denied", pub)) == 0 {
				t.Fatal("no network.denied event for the refused pull")
			}
			putIPRules(t, s, pub, ipRules{Read: []ipRule{}, Publish: []ipRule{}})
		})

		// The delete empties the docker rows of the repo in batches before the repo row goes.
		t.Run("repo delete", func(t *testing.T) {
			deleteRepos(t, s, pub)
			if code, out := d.Exec(t, fmt.Sprintf("docker rmi %s:v1 >/dev/null 2>&1; docker pull %s:v1 2>&1", pubImg, pubImg)); code == 0 {
				t.Fatalf("pull from a deleted repo worked:\n%s", out)
			}
		})
	})
}

// TestDockerConformance runs the OCI distribution conformance suite (pull, push, content
// discovery, content management) against a docker repo, with the publish session token of a user
// who publishes and deletes.
func TestDockerConformance(t *testing.T) {
	t.Parallel()
	OnLocalDisk(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		repo := RandName("conf")
		s.CreateRepo(t, repo, "docker", false, nil)
		s.CreateUser(t, "pusher", "pusher-pass-1234")
		s.SetPerms(t, repo, "pusher", "repo.download", "repo.publish", "repo.delete")
		tok := PublishToken(t, s, "pusher", repo)
		code, out := RunClient(t, conformanceImage, ClientOpts{NoShell: true, Timeout: 15 * time.Minute, Env: map[string]string{
			"OCI_ROOT_URL":                "http://" + registryHost(s),
			"OCI_NAMESPACE":               repo + "/conformance",
			"OCI_CROSSMOUNT_NAMESPACE":    repo + "/other",
			"OCI_USERNAME":                "pusher",
			"OCI_PASSWORD":                tok,
			"OCI_TEST_PULL":               "1",
			"OCI_TEST_PUSH":               "1",
			"OCI_TEST_CONTENT_DISCOVERY":  "1",
			"OCI_TEST_CONTENT_MANAGEMENT": "1",
			"OCI_HIDE_SKIPPED_WORKFLOWS":  "0",
			"OCI_DEBUG":                   "0",
			"OCI_REPORT_DIR":              "none",
		}})
		if code != 0 {
			t.Fatalf("OCI conformance suite: exit %d:\n%s", code, out)
		}
	})
}

// TestDockerClients pushes and pulls with crane (cross-repo mount), skopeo (a streamed upload of
// an OCI layout), ORAS (an artifact with a subject, the referrers API) and Helm (OCI charts), and
// checks the catalog of each kind of caller.
func TestDockerClients(t *testing.T) {
	t.Parallel()
	OnLocalDisk(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		host := registryHost(s)
		priv, pub, dst := RandName("priv"), RandName("pub"), RandName("dst")
		s.CreateRepo(t, priv, "docker", false, nil)
		s.CreateRepo(t, pub, "docker", true, nil)
		s.CreateRepo(t, dst, "docker", false, nil)
		dockerUsers(t, s, priv, pub)
		s.SetPerms(t, dst, "pusher", "repo.download", "repo.publish", "repo.delete")
		pubAuth := Basic{"pusher", PublishToken(t, s, "pusher", pub)}
		privAuth := Basic{"pusher", PublishToken(t, s, "pusher", priv)}
		dstTok := PublishToken(t, s, "pusher", dst)
		dstAuth := Basic{"pusher", dstTok}
		layer := rawRandom(t, 200<<10)
		ociImage(t, s, pubAuth, pub+"/base", "v1", nil, layer)
		ociImage(t, s, privAuth, priv+"/secret", "v1", nil, rawRandom(t, 4096))

		t.Run("crane mount", func(t *testing.T) {
			code, out := RunClient(t, craneImage, ClientOpts{Cmd: []string{fmt.Sprintf(
				"crane auth login %[1]s -u pusher -p '%[2]s' && crane copy --insecure -v %[1]s/%[3]s/base:v1 %[1]s/%[4]s/base:v1 2>&1", host, dstTok, pub, dst)}})
			if code != 0 {
				t.Fatalf("crane copy: exit %d:\n%s", code, out)
			}
			if !strings.Contains(out, "mount=sha256%3A"+sha256Hex(layer)) && !strings.Contains(out, "mount=sha256:"+sha256Hex(layer)) || !strings.Contains(out, "201 Created") {
				t.Fatalf("crane copy: no mount answered 201:\n%s", out)
			}
			resp, b := ociDo(t, s, http.MethodHead, "/v2/"+dst+"/base/blobs/sha256:"+sha256Hex(layer), nil, s.AdminBasic(), nil)
			wantOCI(t, "HEAD the mounted layer", resp, b, http.StatusOK, "")

			// A mount from a repo the token does not read starts an upload instead.
			st, bearer, b := registryToken(t, s, dstAuth, "repository:"+dst+"/copy:pull,push", "repository:"+priv+"/secret:pull")
			wantAPIStatus(t, "token for a mount from a private repo", st, b, http.StatusOK)
			secretLayer := ""
			resp, b = ociDo(t, s, http.MethodGet, "/v2/"+priv+"/secret/manifests/v1", nil, privAuth, nil)
			wantOCI(t, "GET the private manifest", resp, b, http.StatusOK, "")
			var m struct {
				Layers []struct {
					Digest string `json:"digest"`
				} `json:"layers"`
			}
			_ = json.Unmarshal(b, &m)
			secretLayer = m.Layers[0].Digest
			resp, b = ociDo(t, s, http.MethodPost, "/v2/"+dst+"/copy/blobs/uploads/?mount="+secretLayer+"&from="+priv+"/secret", nil, bearer, nil)
			wantOCI(t, "mount from a private repo", resp, b, http.StatusAccepted, "")
			if !strings.HasPrefix(resp.Header.Get("Location"), "/v2/"+dst+"/copy/blobs/uploads/") {
				t.Fatalf("mount fallback: Location %q", resp.Header.Get("Location"))
			}
		})

		t.Run("skopeo", func(t *testing.T) {
			// The layout layer is uncompressed. Without --preserve-digests, skopeo compresses it
			// and writes a new manifest.
			files, manifestHex := ociLayout(t, "/layout", rawRandom(t, 300<<10))
			code, out := RunClient(t, skopeoImage, ClientOpts{Files: files, Cmd: []string{fmt.Sprintf(
				"skopeo copy --dest-tls-verify=false --dest-creds 'pusher:%s' --preserve-digests oci:/layout:v1 docker://%s/%s/sk:v1 2>&1", dstTok, host, dst)}})
			if code != 0 {
				t.Fatalf("skopeo copy: exit %d:\n%s", code, out)
			}
			if got := headDigest(t, s, s.AdminBasic(), dst+"/sk", "v1"); got != "sha256:"+manifestHex {
				t.Fatalf("skopeo push: tag v1 names %s, want sha256:%s", got, manifestHex)
			}
		})

		t.Run("oras", func(t *testing.T) {
			ref := host + "/" + dst + "/art:v1"
			code, out := RunClient(t, orasImage, ClientOpts{WorkingDir: "/tmp", Cmd: []string{fmt.Sprintf(`set -e
cd /tmp && echo hello > a.txt && echo signature > sig.txt
oras push --plain-http -u pusher -p '%[1]s' --artifact-type application/vnd.test.art %[2]s a.txt
oras attach --plain-http -u pusher -p '%[1]s' --artifact-type application/vnd.test.sig %[2]s sig.txt
oras discover --plain-http -u pusher -p '%[1]s' --format json %[2]s
oras discover --plain-http -u pusher -p '%[1]s' --artifact-type application/vnd.test.sig --format json %[2]s
oras pull --plain-http -u pusher -p '%[1]s' -o /tmp/out %[2]s
cmp /tmp/out/a.txt a.txt
`, dstTok, ref)}})
			if code != 0 || !strings.Contains(out, "application/vnd.test.sig") {
				t.Fatalf("oras: exit %d:\n%s", code, out)
			}
			subject := headDigest(t, s, s.AdminBasic(), dst+"/art", "v1")
			resp, b := ociDo(t, s, http.MethodGet, "/v2/"+dst+"/art/referrers/"+subject+"?artifactType=application/vnd.test.sig", nil, s.AdminBasic(), nil)
			wantOCI(t, "referrers with a filter", resp, b, http.StatusOK, "")
			if resp.Header.Get("OCI-Filters-Applied") != "artifactType" || !strings.Contains(string(b), "application/vnd.test.sig") {
				t.Fatalf("referrers with a filter: OCI-Filters-Applied %q: %s", resp.Header.Get("OCI-Filters-Applied"), b)
			}
			resp, b = ociDo(t, s, http.MethodGet, "/v2/"+dst+"/art/referrers/sha256:"+strings.Repeat("0", 64), nil, s.AdminBasic(), nil)
			if resp.StatusCode != http.StatusOK || !strings.Contains(string(b), `"manifests":[]`) {
				t.Fatalf("referrers of a digest without any: status %d: %s", resp.StatusCode, b)
			}
			// A manifest PUT with a subject answers OCI-Subject.
			_, _, _ = ociImage(t, s, dstAuth, dst+"/art", "signed", map[string]any{"subject": map[string]any{
				"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": subject, "size": 10}})
			mb := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.empty.v1+json","digest":"sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a","size":2},"layers":[],"subject":{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"` + subject + `","size":10}}`)
			ociBlob(t, s, dstAuth, dst+"/art", []byte("{}"))
			resp, b = ociDo(t, s, http.MethodPut, "/v2/"+dst+"/art/manifests/sha256:"+sha256Hex(mb), mb, dstAuth, map[string]string{"Content-Type": "application/vnd.oci.image.manifest.v1+json"})
			wantOCI(t, "PUT a manifest with a subject", resp, b, http.StatusCreated, "")
			if resp.Header.Get("OCI-Subject") != subject {
				t.Fatalf("PUT a manifest with a subject: OCI-Subject %q, want %s", resp.Header.Get("OCI-Subject"), subject)
			}
		})

		t.Run("helm", func(t *testing.T) {
			code, out := RunClient(t, helmImage, ClientOpts{WorkingDir: "/tmp", Cmd: []string{fmt.Sprintf(`set -e
cd /tmp
helm create mychart >/dev/null
helm package mychart
printf '%%s' '%[1]s' | helm registry login %[2]s -u pusher --password-stdin --plain-http
helm push mychart-0.1.0.tgz oci://%[2]s/%[3]s --plain-http
mkdir -p /tmp/pulled
helm pull oci://%[2]s/%[3]s/mychart --version 0.1.0 --plain-http -d /tmp/pulled
cmp mychart-0.1.0.tgz /tmp/pulled/mychart-0.1.0.tgz
`, dstTok, host, dst)}})
			if code != 0 {
				t.Fatalf("helm: exit %d:\n%s", code, out)
			}
		})

		t.Run("catalog", func(t *testing.T) {
			catalog := func(auth Auth) []string {
				t.Helper()
				resp, b := ociDo(t, s, http.MethodGet, "/v2/_catalog", nil, auth, nil)
				wantOCI(t, "GET /v2/_catalog", resp, b, http.StatusOK, "")
				var c struct {
					Repositories []string `json:"repositories"`
				}
				if err := json.Unmarshal(b, &c); err != nil {
					t.Fatal(err)
				}
				return c.Repositories
			}
			has := func(names []string, name string) bool { return slices.Contains(names, name) }
			user := catalog(Basic{"reader", "reader-pass-1234"})
			if !has(user, priv+"/secret") || !has(user, pub+"/base") || has(user, dst+"/base") {
				t.Fatalf("catalog of reader: %v", user)
			}
			st, bearer, b := registryToken(t, s, dstAuth, "registry:catalog:*")
			wantAPIStatus(t, "catalog token", st, b, http.StatusOK)
			session := catalog(bearer)
			if !has(session, dst+"/base") || !has(session, pub+"/base") || has(session, priv+"/secret") {
				t.Fatalf("catalog of a publish session: %v", session)
			}
			anon := catalog(nil)
			if !has(anon, pub+"/base") || has(anon, dst+"/base") || has(anon, priv+"/secret") {
				t.Fatalf("anonymous catalog: %v", anon)
			}
			resp, b := ociDo(t, s, http.MethodGet, "/v2/_catalog?n=1", nil, s.AdminBasic(), nil)
			wantOCI(t, "catalog page of 1", resp, b, http.StatusOK, "")
			if !strings.Contains(resp.Header.Get("Link"), `rel="next"`) {
				t.Fatalf("a full catalog page has no Link: %q", resp.Header.Get("Link"))
			}
			resp, b = ociDo(t, s, http.MethodGet, "/v2/_catalog?n=-1", nil, s.AdminBasic(), nil)
			wantOCI(t, "catalog with a bad n", resp, b, http.StatusBadRequest, "PAGINATION_NUMBER_INVALID")
		})
	})
}

// TestDockerGC deletes an image that shares a layer with another one: after docker.link_grace
// and blobs.gc_grace the layers that only the deleted image used leave the store; the shared
// layer and the other image stay. A layer that a manifest uses cannot be deleted on its own. A
// folder delete removes an image of more than 500 files with a multi-platform index.
func TestDockerGC(t *testing.T) {
	t.Parallel()
	ForEachStorage(t, func(t *testing.T, opts ServerOpts) {
		if opts.Env == nil {
			opts.Env = map[string]string{}
		}
		opts.Env["TR_BLOBS_GC_GRACE"] = "1s"
		vol := ""
		if opts.Storage != "s3" {
			vol = NewDataVolume(t)
			opts.DataVolume = vol
		}
		s := StartServer(t, opts)
		repo := RandName("gc")
		s.CreateRepo(t, repo, "docker", false, nil)
		s.CreateUser(t, "pusher", "pusher-pass-1234")
		s.SetPerms(t, repo, "pusher", "repo.download", "repo.publish", "repo.delete")
		auth := Basic{"pusher", PublishToken(t, s, "pusher", repo)}
		shared, onlyA, onlyB := rawRandom(t, 10<<10), rawRandom(t, 20<<10), rawRandom(t, 30<<10)
		aHex, aBytes, aConfig := ociImage(t, s, auth, repo+"/a", "v1", nil, shared, onlyA)
		bHex, _, _ := ociImage(t, s, auth, repo+"/b", "v1", nil, shared, onlyB)

		stored := func() map[string]bool {
			t.Helper()
			var keys map[string]string
			if vol != "" {
				keys = FSBlobs(t, vol)
			} else {
				keys = opts.S3.Blobs(t)
			}
			out := map[string]bool{}
			for _, h := range keys {
				out[h] = true
			}
			return out
		}
		for _, h := range []string{sha256Hex(onlyA), aConfig, aHex} {
			if !stored()[h] {
				t.Fatalf("the blob %s of image a is not in the store", h)
			}
		}

		// A layer that b uses cannot be deleted on its own.
		st, b := s.API(t, http.MethodPost, "/api/repos/"+repo+"/files/delete",
			map[string]any{"paths": []string{"b/_blobs/sha256/" + sha256Hex(onlyB)}}, s.Admin())
		wantAPIStatus(t, "delete a used layer", st, b, http.StatusConflict)

		resp, body := ociDo(t, s, http.MethodDelete, "/v2/"+repo+"/a/manifests/sha256:"+aHex, nil, auth, nil)
		wantOCI(t, "delete image a by digest", resp, body, http.StatusAccepted, "")
		resp, body = ociDo(t, s, http.MethodGet, "/v2/"+repo+"/a/manifests/v1", nil, auth, nil)
		wantOCI(t, "the tag of the deleted image", resp, body, http.StatusNotFound, "MANIFEST_UNKNOWN")
		grace := dockerSetting(t, s, "docker.link_grace")
		s.MustAPI(t, http.MethodPatch, "/api/settings", map[string]any{"docker.link_grace": "1s"}, s.Admin(), nil)

		deadline := time.Now().Add(2 * time.Minute)
		for {
			now := stored()
			if !now[sha256Hex(onlyA)] && !now[aConfig] && !now[sha256Hex(aBytes)] {
				if !now[sha256Hex(shared)] || !now[sha256Hex(onlyB)] || !now[bHex] {
					t.Fatal("the blob GC deleted a blob that image b uses")
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("the blobs of the deleted image a are still in the store after 2 minutes")
			}
			time.Sleep(time.Second)
		}
		resp, body = ociDo(t, s, http.MethodGet, "/v2/"+repo+"/b/manifests/v1", nil, auth, nil)
		wantOCI(t, "GET image b after the GC", resp, body, http.StatusOK, "")
		for _, l := range [][]byte{shared, onlyB} {
			resp, body = ociDo(t, s, http.MethodGet, "/v2/"+repo+"/b/blobs/sha256:"+sha256Hex(l), nil, auth, nil)
			if resp.StatusCode == http.StatusFound {
				continue // the store presigns downloads
			}
			if resp.StatusCode != http.StatusOK || sha256Hex(body) != sha256Hex(l) {
				t.Fatalf("GET a layer of image b after the GC: status %d", resp.StatusCode)
			}
		}

		// The grace from before the test again: the layers of the next image wait for their
		// manifests longer than 1 s when the host is busy.
		s.MustAPI(t, http.MethodPatch, "/api/settings", map[string]any{"docker.link_grace": grace}, s.Admin(), nil)

		// A folder delete of an image of more than 500 files. The pipeline removes 500 files per
		// batch in path order (removeBatch in internal/assets/remove.go; the server does not
		// expose it), so the first batch holds the 499 links (_blobs/ sorts first) and a child
		// manifest of the index, and the other manifests and the tags come later.
		big := repo + "/big"
		var layers []map[string]any
		for range 497 {
			l := rawRandom(t, 64)
			layers = append(layers, map[string]any{"mediaType": "application/vnd.oci.image.layer.v1.tar", "digest": "sha256:" + ociBlob(t, s, auth, big, l), "size": len(l)})
		}
		var configs, children [2][]byte
		var index []byte
		for {
			for i := range children {
				configs[i] = []byte(`{"architecture":"amd64","os":"linux","rootfs":{"type":"layers","diff_ids":[]},"x":"` + randHex(8) + `"}`)
				children[i], _ = json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
					"config": map[string]any{"mediaType": "application/vnd.oci.image.config.v1+json", "digest": "sha256:" + sha256Hex(configs[i]), "size": len(configs[i])},
					"layers": layers})
			}
			var ms []map[string]any
			for i, arch := range []string{"amd64", "arm64"} {
				ms = append(ms, map[string]any{"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": "sha256:" + sha256Hex(children[i]),
					"size": len(children[i]), "platform": map[string]string{"os": "linux", "architecture": arch}})
			}
			index, _ = json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json", "manifests": ms})
			// The lowest manifest digest must be a child, so the first batch holds a child but not
			// its index.
			if min(sha256Hex(children[0]), sha256Hex(children[1])) < sha256Hex(index) {
				break
			}
		}
		for i := range children {
			ociBlob(t, s, auth, big, configs[i])
			resp, body := ociDo(t, s, http.MethodPut, "/v2/"+big+"/manifests/sha256:"+sha256Hex(children[i]), children[i], auth,
				map[string]string{"Content-Type": "application/vnd.oci.image.manifest.v1+json"})
			wantOCI(t, "put a child manifest", resp, body, http.StatusCreated, "")
		}
		const bigTags = 20
		for i := range bigTags {
			resp, body := ociDo(t, s, http.MethodPut, fmt.Sprintf("/v2/%s/manifests/t%02d", big, i), index, auth,
				map[string]string{"Content-Type": "application/vnd.oci.image.index.v1+json"})
			wantOCI(t, "tag the index", resp, body, http.StatusCreated, "")
		}
		var out struct {
			Deleted int `json:"deleted"`
		}
		s.MustAPI(t, http.MethodPost, "/api/repos/"+repo+"/files/delete", map[string]any{"prefixes": []string{"big/"}}, s.Admin(), &out)
		if want := 499 + 3 + bigTags; out.Deleted != want {
			t.Fatalf("delete the folder of image big: deleted %d files, want %d", out.Deleted, want)
		}
		resp, body = ociDo(t, s, http.MethodGet, "/v2/"+big+"/tags/list", nil, auth, nil)
		wantOCI(t, "the tags of the deleted image big", resp, body, http.StatusNotFound, "NAME_UNKNOWN")
		resp, body = ociDo(t, s, http.MethodGet, "/v2/"+repo+"/b/manifests/v1", nil, auth, nil)
		wantOCI(t, "GET image b after the delete of image big", resp, body, http.StatusOK, "")
	})
}
