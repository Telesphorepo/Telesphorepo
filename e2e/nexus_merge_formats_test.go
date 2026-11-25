//go:build import

package e2e

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"testing"
	"time"
)

// nexusMergePlanOf reads the plan of a merge of from into the repo it moves into.
func nexusMergePlanOf(t *testing.T, s *Server, from string) nexusMergePlan {
	t.Helper()
	var plan nexusMergePlan
	s.MustAPI(t, http.MethodGet, "/api/nexus/merges/plan?from="+url.QueryEscape(from), nil, s.Admin(), &plan)
	return plan
}

// nexusMergeDone starts a merge and requires that it ends done.
func nexusMergeDone(t *testing.T, s *Server, body map[string]any) nexusMerge {
	t.Helper()
	var m nexusMerge
	s.MustAPI(t, http.MethodPost, "/api/nexus/merges", body, s.Admin(), &m)
	if m = nexusWaitMerge(t, s, m.ID); m.State != "done" {
		t.Fatalf("merge %+v: %+v, want done", body, m)
	}
	return m
}

// nexusMergeSync requires that the final sync of merge m shows as part of the merge.
func nexusMergeSync(t *testing.T, s *Server, m nexusMerge, into string) {
	t.Helper()
	var full struct {
		SyncID *int64 `json:"sync_id"`
	}
	s.MustAPI(t, http.MethodGet, fmt.Sprintf("/api/nexus/merges/%d", m.ID), nil, s.Admin(), &full)
	if full.SyncID == nil {
		t.Fatalf("merge %d of a target that copies from Nexus: sync_id null, want its final sync", m.ID)
	}
	var sy struct {
		Trigger   string `json:"trigger"`
		MergeID   int64  `json:"merge_id"`
		MergeInto string `json:"merge_into"`
	}
	s.MustAPI(t, http.MethodGet, fmt.Sprintf("/api/nexus/syncs/%d", *full.SyncID), nil, s.Admin(), &sy)
	if sy.MergeID != m.ID || sy.MergeInto != into {
		t.Fatalf("final sync %d of merge %d: %+v, want it shown as the merge's into %s", *full.SyncID, m.ID, sy, into)
	}
}

// nexusMergeDocker merges a moving docker repo into a copied one: an image pushed to the moving
// repo goes in by its links and manifests, a tag that both hold with other digests is a conflict
// taken from the moving repo, and both names serve the merged tags and the old image by digest.
func nexusMergeDocker(t *testing.T, n *Nexus, s *Server) {
	port := 8082 + nexusDockerPort.Add(1) - 1
	u, err := url.Parse(n.InternalURL)
	if err != nil {
		t.Fatal(err)
	}
	nexusHost := fmt.Sprintf("%s:%d", u.Hostname(), port)
	src := RandName("src-dkm")
	n.mustAPI(t, http.MethodPost, "/service/rest/v1/repositories/docker/hosted", map[string]any{
		"name": src, "online": true,
		"storage": map[string]any{"blobStoreName": "default", "strictContentTypeValidation": true, "writePolicy": "allow"},
		"docker":  map[string]any{"v1Enabled": false, "forceBasicAuth": false, "httpPort": port},
	}, nil)
	oldFiles, oldHex := ociLayout(t, "/l1", rawRandom(t, 64<<10))
	script := fmt.Sprintf(`set -e
for i in 1 2 3 4 5 6 7 8 9 10; do skopeo copy --dest-tls-verify=false --dest-creds "$NX_CREDS" --preserve-digests oci:/l1:v1 docker://%s/team/app:v1 && exit 0; sleep 3; done; exit 1
`, nexusHost)
	if code, out := RunClient(t, skopeoImage, ClientOpts{Files: oldFiles, Cmd: []string{script},
		Env: map[string]string{"NX_CREDS": n.User + ":" + n.Pass}, Timeout: 10 * time.Minute}); code != 0 {
		t.Fatalf("skopeo push to the Nexus docker repo: exit %d:\n%s", code, out)
	}
	n.waitAssets(t, src, "team/app/manifests/v1")
	source := nexusCreateSource(t, s, n)
	target := RandName("nxdkm")
	nexusSaveAndWait(t, s, source.ID, []map[string]any{{"source_repo": src, "target": target, "create": true, "anonymous_read": true}})

	moving := RandName("nxdkmnew")
	var moved nexusMoved
	s.MustAPI(t, http.MethodPost, nexusSourcePath(source.ID)+"/repos/"+src+"/move", map[string]any{"name": moving, "group": false}, s.Admin(), &moved)

	// A project pushes v1 (another image than Nexus's v1) and v2 to the moving repo.
	auth := Basic{s.AdminUser, PublishToken(t, s, s.AdminUser, moving)}
	newHex, newManifest, _ := ociImage(t, s, auth, moving+"/team/app", "v1", nil, rawRandom(t, 64<<10))
	resp, body := ociDo(t, s, http.MethodPut, "/v2/"+moving+"/team/app/manifests/v2", newManifest, auth,
		map[string]string{"Content-Type": "application/vnd.oci.image.manifest.v1+json"})
	wantOCI(t, "put the manifest "+moving+"/team/app:v2", resp, body, http.StatusCreated, "")

	plan := nexusMergePlanOf(t, s, moving)
	tag := "team/app/_tags/v1"
	if plan.BlockedCount != 0 || plan.ConflictCount != 1 || len(plan.Conflicts) != 1 || plan.Conflicts[0].Path != tag {
		t.Fatalf("plan of %s: %+v, want no blocked file and the tag conflict %s", moving, plan, tag)
	}
	m := nexusMergeDone(t, s, map[string]any{"from": moving, "into": target, "keep": "target", "choices": map[string]string{tag: "take"}})
	if m.Taken != 1 || m.Copied == 0 {
		t.Fatalf("docker merge: %+v, want the tag taken and the objects copied", m)
	}
	nexusMergeSync(t, s, m, target)

	for _, m := range []struct{ name, ref, hex string }{
		{target, "v1", newHex}, {moving, "v2", newHex}, {target, "v2", newHex}, {moving, "v1", newHex},
		{target, "sha256:" + oldHex, oldHex},
	} {
		nexusWantManifest(t, s, m.name+"/team/app", m.ref, m.hex)
	}
}

// nexusMergeNpm merges a moving npm repo into a copied one: a prerelease published with
// npm publish --tag beta keeps its dist-tag, and latest, which both repos hold with other
// versions, is a conflict that keeps the copy's latest.
func nexusMergeNpm(t *testing.T, n *Nexus, s *Server) {
	src := RandName("src-npmm")
	n.mustAPI(t, http.MethodPost, "/service/rest/v1/repositories/npm/hosted", map[string]any{
		"name": src, "online": true, "storage": nexusStorage}, nil)
	pkg := RandName("nxmerge")
	n.upload(t, src, nexusPart{field: "npm.asset", value: "package.tgz", data: npmTarball(t, pkg, "1.0.0", pkg+" 1.0.0")})
	n.waitAssets(t, src, pkg+"-1.0.0.tgz")
	source := nexusCreateSource(t, s, n)
	target := RandName("nxnpmm")
	nexusSaveAndWait(t, s, source.ID, []map[string]any{{"source_repo": src, "target": target, "create": true, "anonymous_read": false}})
	moving := RandName("nxnpmmnew")
	var moved nexusMoved
	s.MustAPI(t, http.MethodPost, nexusSourcePath(source.ID)+"/repos/"+src+"/move", map[string]any{"name": moving, "group": false}, s.Admin(), &moved)

	beta := "2.0.0-beta.1"
	registry := s.InternalURL + "/npm/" + moving + "/"
	pj, _ := json.Marshal(map[string]string{"name": pkg, "version": beta, "main": "index.js", "license": "MIT"})
	code, out := RunClient(t, nodeImage, ClientOpts{Cmd: []string{"set -e\ncd /work/app && npm publish --tag beta --registry " + registry},
		Files: []ContainerFile{
			{Path: "/work/app/package.json", Content: string(pj)},
			{Path: "/work/app/index.js", Content: fmt.Sprintf("module.exports = %q;\n", pkg+" "+beta)},
			{Path: "/work/app/.npmrc", Content: npmrc(registry, PublishToken(t, s, s.AdminUser, moving))},
		}})
	if code != 0 {
		t.Fatalf("npm publish --tag beta to %s: exit %d:\n%s", moving, code, out)
	}

	plan := nexusMergePlanOf(t, s, moving)
	latest := "label:" + pkg + " latest"
	if plan.BlockedCount != 0 || plan.Copy != 1 || plan.ConflictCount != 1 || len(plan.Conflicts) != 1 || plan.Conflicts[0].Path != latest {
		t.Fatalf("plan of %s: %+v, want the tarball to copy and the conflict %s", moving, plan, latest)
	}
	m := nexusMergeDone(t, s, map[string]any{"from": moving, "into": target, "keep": "target", "choices": map[string]string{latest: "keep"}})
	nexusMergeSync(t, s, m, target)

	var packument struct {
		DistTags map[string]string `json:"dist-tags"`
		Versions map[string]any    `json:"versions"`
	}
	for _, repo := range []string{target, moving} {
		s.MustAPI(t, http.MethodGet, "/npm/"+repo+"/"+pkg, nil, s.AdminBasic(), &packument)
		if packument.DistTags["beta"] != beta || packument.DistTags["latest"] != "1.0.0" || packument.Versions[beta] == nil {
			t.Fatalf("packument of %s from %s after the merge: dist-tags %v, versions %v; want beta %s and latest 1.0.0",
				pkg, repo, packument.DistTags, slices.Sorted(maps.Keys(packument.Versions)), beta)
		}
	}
}
