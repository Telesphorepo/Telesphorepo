//go:build e2e

package e2e

import (
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
// Docker CLI in Docker in Docker, the OCI conformance suite, and the removal of layers that no
// manifest uses.

const conformanceImage = "ghcr.io/opencontainers/distribution-spec/conformance:v1.1.1"

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

// TestDockerRegistry pushes and pulls with the real Docker CLI on both stores. On fs, the Docker
// CLI pushes a multi-platform index and pulls one platform of it, and HTTP requests check the
// repo names, the challenge, the token scopes, logins that cannot push, anonymous pulls, a tag
// move, a read IP rule and a repo delete.
func TestDockerRegistry(t *testing.T) {
	t.Parallel()
	ForEachStorage(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		host := registryHost(s)
		priv, pub := RandName("priv"), RandName("pub")
		s.CreateRepo(t, priv, "docker", false, nil)
		s.CreateRepo(t, pub, "docker", true, nil)
		dockerUsers(t, s, priv, pub)

		d := StartDind(t, host)
		img := host + "/" + priv + "/app"
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
		if opts.S3 != nil {
			return // the rest is the registry, not the store
		}

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

		t.Run("password and read token cannot push", func(t *testing.T) {
			readTok := s.CreateToken(t, "reader", "reader-pass-1234", "docker")
			for _, cred := range []Basic{{"reader", "reader-pass-1234"}, {"reader", readTok}} {
				st, bearer, b := registryToken(t, s, cred, "repository:"+priv+"/app:pull,push")
				wantAPIStatus(t, "token for a push with a password or read token", st, b, http.StatusOK)
				headDigest(t, s, bearer, priv+"/app", "v1")
				resp, body := ociDo(t, s, http.MethodPost, "/v2/"+priv+"/app/blobs/uploads/", nil, bearer, nil)
				if resp.StatusCode < 400 || !strings.Contains(string(body), "publish session") {
					t.Fatalf("push with a password or read token: status %d, want a refusal that names the publish session: %s", resp.StatusCode, body)
				}
			}
		})

		t.Run("anonymous pull", func(t *testing.T) {
			layer := rawRandom(t, 4096)
			mh, _, _ := ociImage(t, s, Basic{"pusher", PublishToken(t, s, "pusher", pub)}, pub+"/app", "v1", nil, layer)
			st, anon, b := registryToken(t, s, nil, "repository:"+pub+"/app:pull")
			wantAPIStatus(t, "anonymous token for the public repo", st, b, http.StatusOK)
			if got := headDigest(t, s, anon, pub+"/app", "v1"); got != "sha256:"+mh {
				t.Fatalf("anonymous HEAD of the public tag: %s, want sha256:%s", got, mh)
			}
			resp, body := ociDo(t, s, http.MethodGet, "/v2/"+pub+"/app/blobs/sha256:"+sha256Hex(layer), nil, anon, nil)
			if resp.StatusCode != http.StatusOK || !bytes.Equal(body, layer) {
				t.Fatalf("anonymous GET of the public layer: status %d, %d bytes, want the layer", resp.StatusCode, len(body))
			}
		})

		t.Run("tag move", func(t *testing.T) {
			old := headDigest(t, s, s.AdminBasic(), priv+"/app", "v1")
			ociImage(t, s, Basic{"pusher", tok}, priv+"/app", "v1", nil, rawRandom(t, 4096))
			if moved := headDigest(t, s, s.AdminBasic(), priv+"/app", "v1"); moved == old {
				t.Fatalf("the tag v1 still names %s after the push of another image", old)
			}
			resp, body := ociDo(t, s, http.MethodGet, "/v2/"+priv+"/app/manifests/"+old, nil, s.AdminBasic(), nil)
			wantOCI(t, "GET the old digest after the tag moved", resp, body, http.StatusOK, "")
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

		t.Run("allowed networks", func(t *testing.T) {
			// Every address is allowed: an anonymous token reads the restricted repo, through the
			// token endpoint and the registry routes.
			anonPull := func() (*http.Response, []byte) {
				t.Helper()
				st, anon, b := registryToken(t, s, nil, "repository:"+priv+"/app:pull")
				wantAPIStatus(t, "anonymous token for the restricted repo", st, b, http.StatusOK)
				return ociDo(t, s, http.MethodGet, "/v2/"+priv+"/app/manifests/v1", nil, anon, nil)
			}
			putIPRules(t, s, priv, ipRules{Read: []ipRule{{Kind: "cidr", Value: "0.0.0.0/0"}, {Kind: "cidr", Value: "::/0"}}})
			resp, body := anonPull()
			wantOCI(t, "anonymous pull through the allowed networks", resp, body, http.StatusOK, "")
			putIPRules(t, s, priv, ipRules{Read: []ipRule{}})
			resp, body = anonPull()
			wantOCI(t, "anonymous pull of a restricted repo without allowed networks", resp, body, http.StatusUnauthorized, "UNAUTHORIZED")
		})

		// The delete empties the docker rows of the repo in batches before the repo row goes.
		t.Run("repo delete", func(t *testing.T) {
			deleteRepos(t, s, pub)
			resp, body := ociDo(t, s, http.MethodGet, "/v2/"+pub+"/app/manifests/v1", nil, s.AdminBasic(), nil)
			wantOCI(t, "GET a manifest of a deleted repo", resp, body, http.StatusNotFound, "NAME_UNKNOWN")
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

// TestDockerRegistryAPI checks over HTTP what the Docker CLI and the conformance suite leave out:
// a mount across repos of the server (from a repo the token does not read it starts an upload
// instead), the referrers API with its filter and OCI-Subject, and the catalog of each kind of
// caller.
func TestDockerRegistryAPI(t *testing.T) {
	t.Parallel()
	OnLocalDisk(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		priv, pub, dst := RandName("priv"), RandName("pub"), RandName("dst")
		s.CreateRepo(t, priv, "docker", false, nil)
		s.CreateRepo(t, pub, "docker", true, nil)
		s.CreateRepo(t, dst, "docker", false, nil)
		dockerUsers(t, s, priv, pub)
		s.SetPerms(t, dst, "pusher", "repo.download", "repo.publish", "repo.delete")
		pubAuth := Basic{"pusher", PublishToken(t, s, "pusher", pub)}
		privAuth := Basic{"pusher", PublishToken(t, s, "pusher", priv)}
		dstAuth := Basic{"pusher", PublishToken(t, s, "pusher", dst)}
		layer := rawRandom(t, 200<<10)
		ociImage(t, s, pubAuth, pub+"/base", "v1", nil, layer)
		ociImage(t, s, privAuth, priv+"/secret", "v1", nil, rawRandom(t, 4096))

		t.Run("mount", func(t *testing.T) {
			st, bearer, b := registryToken(t, s, dstAuth, "repository:"+dst+"/base:pull,push", "repository:"+pub+"/base:pull")
			wantAPIStatus(t, "token for a mount from a public repo", st, b, http.StatusOK)
			resp, b := ociDo(t, s, http.MethodPost, "/v2/"+dst+"/base/blobs/uploads/?mount=sha256:"+sha256Hex(layer)+"&from="+pub+"/base", nil, bearer, nil)
			wantOCI(t, "mount from a public repo", resp, b, http.StatusCreated, "")
			resp, b = ociDo(t, s, http.MethodHead, "/v2/"+dst+"/base/blobs/sha256:"+sha256Hex(layer), nil, s.AdminBasic(), nil)
			wantOCI(t, "HEAD the mounted layer", resp, b, http.StatusOK, "")
			ociImage(t, s, dstAuth, dst+"/base", "v1", nil, layer)

			// A mount from a repo the token does not read starts an upload instead.
			st, bearer, b = registryToken(t, s, dstAuth, "repository:"+dst+"/copy:pull,push", "repository:"+priv+"/secret:pull")
			wantAPIStatus(t, "token for a mount from a private repo", st, b, http.StatusOK)
			resp, b = ociDo(t, s, http.MethodGet, "/v2/"+priv+"/secret/manifests/v1", nil, privAuth, nil)
			wantOCI(t, "GET the private manifest", resp, b, http.StatusOK, "")
			var m struct {
				Layers []struct {
					Digest string `json:"digest"`
				} `json:"layers"`
			}
			_ = json.Unmarshal(b, &m)
			resp, b = ociDo(t, s, http.MethodPost, "/v2/"+dst+"/copy/blobs/uploads/?mount="+m.Layers[0].Digest+"&from="+priv+"/secret", nil, bearer, nil)
			wantOCI(t, "mount from a private repo", resp, b, http.StatusAccepted, "")
			if !strings.HasPrefix(resp.Header.Get("Location"), "/v2/"+dst+"/copy/blobs/uploads/") {
				t.Fatalf("mount fallback: Location %q", resp.Header.Get("Location"))
			}
		})

		t.Run("referrers", func(t *testing.T) {
			subjectHex, subjectBytes, _ := ociImage(t, s, dstAuth, dst+"/art", "v1", nil, rawRandom(t, 1024))
			subject := "sha256:" + subjectHex
			desc := map[string]any{"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": subject, "size": len(subjectBytes)}
			// A manifest PUT with a subject answers OCI-Subject.
			ociBlob(t, s, dstAuth, dst+"/art", []byte("{}"))
			sig, _ := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
				"artifactType": "application/vnd.test.sig", "layers": []any{}, "subject": desc,
				"config": map[string]any{"mediaType": "application/vnd.oci.empty.v1+json", "digest": "sha256:" + sha256Hex([]byte("{}")), "size": 2}})
			resp, b := ociDo(t, s, http.MethodPut, "/v2/"+dst+"/art/manifests/sha256:"+sha256Hex(sig), sig, dstAuth, map[string]string{"Content-Type": "application/vnd.oci.image.manifest.v1+json"})
			wantOCI(t, "PUT a manifest with a subject", resp, b, http.StatusCreated, "")
			if resp.Header.Get("OCI-Subject") != subject {
				t.Fatalf("PUT a manifest with a subject: OCI-Subject %q, want %s", resp.Header.Get("OCI-Subject"), subject)
			}
			// A referrer of another type, by tag.
			other, _, _ := ociImage(t, s, dstAuth, dst+"/art", "signed", map[string]any{"subject": desc})

			resp, b = ociDo(t, s, http.MethodGet, "/v2/"+dst+"/art/referrers/"+subject, nil, s.AdminBasic(), nil)
			wantOCI(t, "referrers", resp, b, http.StatusOK, "")
			if !strings.Contains(string(b), sha256Hex(sig)) || !strings.Contains(string(b), other) {
				t.Fatalf("referrers: want both referrers: %s", b)
			}
			resp, b = ociDo(t, s, http.MethodGet, "/v2/"+dst+"/art/referrers/"+subject+"?artifactType=application/vnd.test.sig", nil, s.AdminBasic(), nil)
			wantOCI(t, "referrers with a filter", resp, b, http.StatusOK, "")
			if resp.Header.Get("OCI-Filters-Applied") != "artifactType" || !strings.Contains(string(b), sha256Hex(sig)) || strings.Contains(string(b), other) {
				t.Fatalf("referrers with a filter: OCI-Filters-Applied %q: %s", resp.Header.Get("OCI-Filters-Applied"), b)
			}
			resp, b = ociDo(t, s, http.MethodGet, "/v2/"+dst+"/art/referrers/sha256:"+strings.Repeat("0", 64), nil, s.AdminBasic(), nil)
			if resp.StatusCode != http.StatusOK || !strings.Contains(string(b), `"manifests":[]`) {
				t.Fatalf("referrers of a digest without any: status %d: %s", resp.StatusCode, b)
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
// layer and the other image stay. A layer that a manifest uses cannot be deleted on its own. On
// fs, a folder delete removes an image of more than 500 files with a multi-platform index.
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

		if opts.S3 != nil {
			return // the folder delete is the byte pipeline, not the store
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
