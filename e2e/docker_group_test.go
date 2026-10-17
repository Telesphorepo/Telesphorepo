//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// TestDockerGroupBearer reads a private docker group with the bearer tokens of the token endpoint:
// a user who holds download on the group and on one member pulls through the group with the
// Docker CLI after docker login, with crane, and with a bearer token over HTTP. The group skips the
// member that the user may not read, the token reads no member directly, and every read answer
// carries Cache-Control: private.
func TestDockerGroupBearer(t *testing.T) {
	OnLocalDisk(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		host := registryHost(s)
		arch := hostArch()
		mem, hidden, grp := RandName("gmem"), RandName("ghid"), RandName("grp")
		s.CreateRepo(t, mem, "docker", false, nil)
		s.CreateRepo(t, hidden, "docker", false, nil)

		// v1 is in both members; the hidden member is first in read order, so a reader of both gets
		// the hidden image and a reader of mem only gets the image of mem.
		mine, secret := tarImage(t, arch, rawRandom(t, 4096)), tarImage(t, arch, rawRandom(t, 4096))
		memClient := registryClient{base: s.URL, auth: s.PublishAuth(t, s.AdminUser, mem).apply}
		hidClient := registryClient{base: s.URL, auth: s.PublishAuth(t, s.AdminUser, hidden).apply}
		memClient.pushImage(t, mem+"/app", "v1", mine)
		hidClient.pushImage(t, hidden+"/app", "v1", secret)
		hidClient.pushImage(t, hidden+"/app", "only-hidden", secret)
		s.CreateGroup(t, grp, "docker", false, []string{hidden, mem})

		const user, pass = "greader", "greader-pass-1234"
		s.CreateUser(t, user, pass)
		s.SetPerms(t, mem, user, "repo.download")
		s.SetPerms(t, grp, user, "repo.download")
		ref := host + "/" + grp + "/app"

		t.Run("docker CLI", func(t *testing.T) {
			d := StartDind(t, host)
			d.MustExec(t, "pull through the group", fmt.Sprintf(`set -e
printf '%%s' '%[1]s' | docker login %[2]s -u %[3]s --password-stdin
docker pull %[4]s:v1
`, pass, host, user, ref))
			out := d.MustExec(t, "the digest of the pulled image", fmt.Sprintf("docker image inspect --format '{{json .RepoDigests}}' %s:v1\n", ref))
			if !strings.Contains(out, "sha256:"+mine.Hex) {
				t.Fatalf("docker pull through the group: repo digests %s, want the image of %s (sha256:%s)", out, mem, mine.Hex)
			}
			if code, out := d.Exec(t, fmt.Sprintf("docker pull %s:only-hidden 2>&1", ref)); code == 0 {
				t.Fatalf("docker pull of a tag of the hidden member through the group: exit 0, want a failure:\n%s", out)
			}
		})

		t.Run("crane", func(t *testing.T) {
			code, out := RunClient(t, craneImage, ClientOpts{Cmd: []string{fmt.Sprintf(
				`set -e; crane auth login %[1]s -u %[2]s -p '%[3]s'; crane pull --insecure %[4]s:v1 /tmp/img.tar; echo "DIGEST $(crane digest --insecure %[4]s:v1)"; echo "TAGS $(crane ls --insecure %[4]s | tr '\n' ' ')"`,
				host, user, pass, ref)}})
			if code != 0 || !strings.Contains(out, "DIGEST sha256:"+mine.Hex) {
				t.Fatalf("crane through the group: exit %d, want the digest sha256:%s:\n%s", code, mine.Hex, out)
			}
			if !strings.Contains(out, "TAGS v1") || strings.Contains(out, "only-hidden") {
				t.Fatalf("crane ls through the group: want v1 and no tag of the hidden member:\n%s", out)
			}
		})

		accept := map[string]string{"Accept": "application/vnd.oci.image.manifest.v1+json"}
		tags := func(t *testing.T, auth Auth) []string {
			t.Helper()
			resp, body := ociDo(t, s, http.MethodGet, "/v2/"+grp+"/app/tags/list", nil, auth, nil)
			wantOCI(t, "tags list of the group", resp, body, http.StatusOK, "")
			var tl struct {
				Tags []string `json:"tags"`
			}
			if err := json.Unmarshal(body, &tl); err != nil {
				t.Fatal(err)
			}
			return tl.Tags
		}

		t.Run("bearer token", func(t *testing.T) {
			st, bearer, b := registryToken(t, s, Basic{user, pass}, "repository:"+grp+"/app:pull")
			wantAPIStatus(t, "token for a pull of the group", st, b, http.StatusOK)

			resp, body := ociDo(t, s, http.MethodGet, "/v2/"+grp+"/app/manifests/v1", nil, bearer, accept)
			wantOCI(t, "GET the manifest through the group", resp, body, http.StatusOK, "")
			if got := resp.Header.Get("Docker-Content-Digest"); got != "sha256:"+mine.Hex {
				t.Fatalf("the group serves %s for v1 to %s, want the image of %s (sha256:%s)", got, user, mem, mine.Hex)
			}
			if cc := resp.Header.Get("Cache-Control"); cc != "private" {
				t.Fatalf("GET a manifest of a private group: Cache-Control %q, want private", cc)
			}
			resp, body = ociDo(t, s, http.MethodHead, "/v2/"+grp+"/app/manifests/v1", nil, bearer, accept)
			wantOCI(t, "HEAD the manifest through the group", resp, body, http.StatusOK, "")
			if cc := resp.Header.Get("Cache-Control"); cc != "private" {
				t.Fatalf("HEAD a manifest of a private group: Cache-Control %q, want private", cc)
			}

			resp, body = ociDo(t, s, http.MethodGet, "/v2/"+grp+"/app/blobs/sha256:"+mine.Layers[0], nil, bearer, nil)
			switch resp.StatusCode {
			case http.StatusOK:
				if cc := resp.Header.Get("Cache-Control"); cc != "private" {
					t.Fatalf("GET a blob of a private group: Cache-Control %q, want private", cc)
				}
			case http.StatusFound, http.StatusTemporaryRedirect:
				if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
					t.Fatalf("a redirect of a blob of a private group: Cache-Control %q, want no-store", cc)
				}
			default:
				t.Fatalf("GET a blob through the group: status %d: %s", resp.StatusCode, body)
			}

			resp, body = ociDo(t, s, http.MethodGet, "/v2/"+grp+"/app/manifests/only-hidden", nil, bearer, accept)
			wantOCI(t, "a tag of the hidden member through the group", resp, body, http.StatusNotFound, "MANIFEST_UNKNOWN")
			if got := tags(t, bearer); !slices.Contains(got, "v1") || slices.Contains(got, "only-hidden") {
				t.Fatalf("tags list of the group for %s: %v, want v1 and no tag of the hidden member", user, got)
			}

			// The token reads the group only: a member is not in its scope.
			resp, body = ociDo(t, s, http.MethodGet, "/v2/"+mem+"/app/manifests/v1", nil, bearer, accept)
			wantOCI(t, "the group's token on a member", resp, body, http.StatusUnauthorized, "UNAUTHORIZED")
			if ch := resp.Header.Get("WWW-Authenticate"); !strings.Contains(ch, `error="insufficient_scope"`) {
				t.Fatalf("the group's token on a member: challenge %q, want insufficient_scope", ch)
			}
		})

		t.Run("admin and anonymous", func(t *testing.T) {
			st, bearer, b := registryToken(t, s, s.AdminBasic(), "repository:"+grp+"/app:pull")
			wantAPIStatus(t, "admin token for a pull of the group", st, b, http.StatusOK)
			if got := headDigest(t, s, bearer, grp+"/app", "v1"); got != "sha256:"+secret.Hex {
				t.Fatalf("the group serves %s for v1 to the admin, want the hidden member's sha256:%s", got, secret.Hex)
			}
			if got := tags(t, bearer); !slices.Contains(got, "only-hidden") {
				t.Fatalf("tags list of the group for the admin: %v, want only-hidden", got)
			}

			st, anon, b := registryToken(t, s, nil, "repository:"+grp+"/app:pull")
			wantAPIStatus(t, "anonymous token for the private group", st, b, http.StatusOK)
			resp, body := ociDo(t, s, http.MethodGet, "/v2/"+grp+"/app/manifests/v1", nil, anon, accept)
			wantOCI(t, "anonymous bearer on the private group", resp, body, http.StatusUnauthorized, "UNAUTHORIZED")

			// An anonymous bearer reads a public group's public member. (The Docker CLI with the
			// containerd image store sends no token when the anonymous GET succeeds, so only a
			// client that presents the token reaches this path.)
			pubm, pubg := RandName("gpub"), RandName("gpubg")
			s.CreateRepo(t, pubm, "docker", true, nil)
			registryClient{base: s.URL, auth: s.PublishAuth(t, s.AdminUser, pubm).apply}.pushImage(t, pubm+"/app", "v1", mine)
			s.CreateGroup(t, pubg, "docker", true, []string{pubm})
			st, anon, b = registryToken(t, s, nil, "repository:"+pubg+"/app:pull")
			wantAPIStatus(t, "anonymous token for the public group", st, b, http.StatusOK)
			if got := headDigest(t, s, anon, pubg+"/app", "v1"); got != "sha256:"+mine.Hex {
				t.Fatalf("an anonymous bearer through the public group: digest %s, want sha256:%s", got, mine.Hex)
			}
		})
	})
}
