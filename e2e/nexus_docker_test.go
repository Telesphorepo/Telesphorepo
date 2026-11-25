//go:build import

package e2e

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The Nexus import of docker repos (part of TestNexus): a Nexus docker hosted repo behind its
// HTTP connector, with the Docker Bearer Token Realm on and forceBasicAuth off, so every fetch of
// the importer answers a Bearer challenge.

// skopeoImage pushes the test images to Nexus.
var skopeoImage = floatingImage("quay.io/skopeo/stable:latest")

// nexusDockerPort is the HTTP connector port of the next Nexus docker repo; each repo needs its
// own.
var nexusDockerPort atomic.Int32

// enableDockerToken turns on the Docker Bearer Token Realm of Nexus, keeping the active realms.
func (n *Nexus) enableDockerToken(t *testing.T) {
	t.Helper()
	var active []string
	n.mustAPI(t, http.MethodGet, "/service/rest/v1/security/realms/active", nil, &active)
	if !slices.Contains(active, "DockerToken") {
		n.mustAPI(t, http.MethodPut, "/service/rest/v1/security/realms/active", append(active, "DockerToken"), nil)
	}
}

// nexusDocker pushes an image and a multi-platform index to a Nexus docker hosted repo with
// skopeo, imports the repo into s, reads the manifests from s by tag and by digest, and syncs
// again with nothing new.
func nexusDocker(t *testing.T, n *Nexus, s *Server) {
	port := 8082 + nexusDockerPort.Add(1) - 1
	u, err := url.Parse(n.InternalURL)
	if err != nil {
		t.Fatal(err)
	}
	nexusHost := fmt.Sprintf("%s:%d", u.Hostname(), port)
	src := RandName("src-docker")
	n.mustAPI(t, http.MethodPost, "/service/rest/v1/repositories/docker/hosted", map[string]any{
		"name": src, "online": true,
		"storage": map[string]any{"blobStoreName": "default", "strictContentTypeValidation": true, "writePolicy": "allow"},
		"docker":  map[string]any{"v1Enabled": false, "forceBasicAuth": false, "httpPort": port},
	}, nil)

	appFiles, appHex := ociLayout(t, "/l1", rawRandom(t, 100<<10))
	idxFiles, idxHex, childHexes := ociIndexLayout(t, "/l2", "v1")
	script := fmt.Sprintf(`set -e
copy() { for i in 1 2 3 4 5 6 7 8 9 10; do skopeo copy --dest-tls-verify=false --dest-creds "$NX_CREDS" --preserve-digests "$@" && return 0; sleep 3; done; return 1; }
copy oci:/l1:v1 docker://%[1]s/team/app:v1
copy --all oci:/l2:v1 docker://%[1]s/team/multi:v1
`, nexusHost)
	code, out := RunClient(t, skopeoImage, ClientOpts{Files: append(appFiles, idxFiles...), Cmd: []string{script},
		Env: map[string]string{"NX_CREDS": n.User + ":" + n.Pass}, Timeout: 10 * time.Minute})
	if code != 0 {
		t.Fatalf("skopeo push to the Nexus docker repo: exit %d:\n%s", code, out)
	}
	paths := n.waitAssets(t, src, "team/app/manifests/v1", "team/multi/manifests/v1")
	if !slices.ContainsFunc(paths, func(p string) bool { return strings.HasPrefix(p, "v2/-/blobs/") }) {
		t.Fatalf("the Nexus docker repo lists no blob: %v", paths)
	}

	source := nexusCreateSource(t, s, n)
	if r := nexusGetSource(t, s, source.ID).repo(t, src); r.SourceFormat != "docker" || r.Type != "hosted" || !r.Supported {
		t.Fatalf("the Nexus docker repo: %+v, want a supported docker hosted repo", r)
	}
	target := RandName("nxdk")
	first := nexusSaveAndWait(t, s, source.ID, []map[string]any{{"source_repo": src, "target": target, "create": true, "anonymous_read": true}})
	if r := first.repo(t, src); r.Imported < 2 || r.Ignored == 0 || r.Failed != 0 {
		t.Fatalf("first sync of the docker repo: %+v, want both tags imported and the blobs ignored", r)
	}
	if info := nexusRepoInfoOf(t, s, target); info.Format != "docker" || info.Kind != "hosted" || !info.AnonymousRead {
		t.Fatalf("the docker target: %+v", info)
	}

	for _, m := range []struct{ name, ref, hex string }{
		{"team/app", "v1", appHex}, {"team/app", "sha256:" + appHex, appHex},
		{"team/multi", "v1", idxHex}, {"team/multi", "sha256:" + childHexes[0], childHexes[0]},
	} {
		nexusWantManifest(t, s, target+"/"+m.name, m.ref, m.hex)
	}

	again := nexusSyncNow(t, s, source.ID)
	if r := again.repo(t, src); r.Imported != 0 || r.Failed != 0 {
		t.Fatalf("second sync of the docker repo: %+v, want nothing new", r)
	}
}

// nexusWantManifest GETs the manifest ref of name on s anonymously and checks that its
// Docker-Content-Digest and the SHA-256 of its bytes are sha256:hex.
func nexusWantManifest(t *testing.T, s *Server, name, ref, hex string) {
	t.Helper()
	accept := strings.Join([]string{"application/vnd.oci.image.manifest.v1+json", "application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.v2+json", "application/vnd.docker.distribution.manifest.list.v2+json"}, ", ")
	resp, body := ociDo(t, s, http.MethodGet, "/v2/"+name+"/manifests/"+ref, nil, nil, map[string]string{"Accept": accept})
	wantOCI(t, "GET the manifest "+name+":"+ref, resp, body, http.StatusOK, "")
	if got := resp.Header.Get("Docker-Content-Digest"); got != "sha256:"+hex || sha256Hex(body) != hex {
		t.Fatalf("GET the manifest %s:%s: Docker-Content-Digest %q, body sha256:%s, want sha256:%s", name, ref, got, sha256Hex(body), hex)
	}
}
