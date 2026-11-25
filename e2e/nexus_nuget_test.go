//go:build import

package e2e

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
	"time"
)

// nexusNuGet uploads a package to a Nexus nuget hosted repo, imports the repo into a new hosted
// repo of s, reads the package back from s, and syncs again with nothing new.
func nexusNuGet(t *testing.T, n *Nexus, s *Server) {
	src := RandName("src-nuget")
	n.mustAPI(t, http.MethodPost, "/service/rest/v1/repositories/nuget/hosted", map[string]any{
		"name": src, "online": true, "storage": nexusStorage,
	}, nil)

	id := "E2E.Nexus" + randHex(4)
	lid := strings.ToLower(id)
	nupkg := testNupkg(t, id, "1.2.3")
	n.upload(t, src, nexusPart{field: "nuget.asset", value: id + ".1.2.3.nupkg", data: nupkg})
	paths := n.assetPaths(t, src)
	for deadline := time.Now().Add(time.Minute); len(paths) == 0; paths = n.assetPaths(t, src) {
		if time.Now().After(deadline) {
			t.Fatalf("the Nexus nuget repo %s lists no asset after a minute", src)
		}
		time.Sleep(nexusPoll)
	}
	t.Logf("assets of the Nexus nuget repo: %v", paths)

	source := nexusCreateSource(t, s, n)
	if r := nexusGetSource(t, s, source.ID).repo(t, src); r.SourceFormat != "nuget" || r.Type != "hosted" || !r.Supported {
		t.Fatalf("the Nexus nuget repo: %+v, want a supported nuget hosted repo", r)
	}
	target := RandName("nxnu")
	first := nexusSaveAndWait(t, s, source.ID, []map[string]any{{"source_repo": src, "target": target, "create": true, "anonymous_read": true}})
	if r := first.repo(t, src); r.Imported != 1 || r.Failed != 0 {
		t.Fatalf("first sync of the nuget repo: %+v, want one package imported", r)
	}
	if info := nexusRepoInfoOf(t, s, target); info.Format != "nuget" || info.Kind != "hosted" || !info.AnonymousRead {
		t.Fatalf("the nuget target: %+v", info)
	}
	if b := getFollowS3(t, s, nil, "/nuget/"+target+"/v3-flatcontainer/"+lid+"/1.2.3/"+lid+".1.2.3.nupkg"); !bytes.Equal(b, nupkg) {
		t.Fatalf("GET the imported package: %d bytes, want the %d bytes of the upload", len(b), len(nupkg))
	}

	again := nexusSyncNow(t, s, source.ID)
	if r := again.repo(t, src); r.Imported != 0 || r.Failed != 0 {
		t.Fatalf("second sync of the nuget repo: %+v, want nothing new", r)
	}
}
