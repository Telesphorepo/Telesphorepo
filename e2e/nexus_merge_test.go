//go:build import

package e2e

import (
	"bytes"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

// nexusMoved is the answer of POST .../repos/{repo}/move.
type nexusMoved struct {
	Name  string `json:"name"`
	Group string `json:"group"`
}

// nexusMergePlan is the part of GET /api/nexus/merges/plan that the test reads.
type nexusMergePlan struct {
	From          string `json:"from"`
	Into          string `json:"into"`
	Copying       bool   `json:"copying"`
	Copy          int64  `json:"copy"`
	Skip          int64  `json:"skip"`
	ConflictCount int64  `json:"conflict_count"`
	Conflicts     []struct {
		Path string `json:"path"`
	} `json:"conflicts"`
	BlockedCount int64  `json:"blocked_count"`
	Busy         string `json:"busy"`
}

// nexusMerge is the part of a Merge that the test reads.
type nexusMerge struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Redirect string `json:"redirect"`
	State    string `json:"state"`
	Step     string `json:"step"`
	Error    string `json:"error"`
	Copied   int64  `json:"copied"`
	Skipped  int64  `json:"skipped"`
	Kept     int64  `json:"kept"`
	Taken    int64  `json:"taken"`
}

// nexusWaitMerge polls merge id until it is no longer running and returns it.
func nexusWaitMerge(t *testing.T, s *Server, id int64) nexusMerge {
	t.Helper()
	deadline := time.Now().Add(nexusSyncWait)
	for {
		var m nexusMerge
		s.MustAPI(t, http.MethodGet, fmt.Sprintf("/api/nexus/merges/%d", id), nil, s.Admin(), &m)
		if m.State != "running" {
			return m
		}
		if time.Now().After(deadline) {
			t.Fatalf("merge %d still running after %s: %+v", id, nexusSyncWait, m)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// nexusMembers returns the names of the members of group name.
func nexusMembers(t *testing.T, s *Server, name string) []string {
	t.Helper()
	var out []string
	for _, m := range nexusRepoInfoOf(t, s, name).Members {
		out = append(out, m.Name)
	}
	return out
}

// nexusGetRaw requires that GET /raw/<repo>/<path> answers 200 with body.
func nexusGetRaw(t *testing.T, s *Server, repo, path string, body []byte) {
	t.Helper()
	resp, got := s.Do(t, s.NewRequest(t, http.MethodGet, "/raw/"+repo+"/"+path, nil), s.AdminBasic())
	if resp.StatusCode != http.StatusOK || !bytes.Equal(got, body) {
		t.Fatalf("GET %s from %s: status %d, body %q; want 200 with %q", path, repo, resp.StatusCode, got, body)
	}
}

// nexusSetOnline sets the Nexus raw repo name online or offline.
func nexusSetOnline(t *testing.T, n *Nexus, name string, online bool) {
	t.Helper()
	n.mustAPI(t, http.MethodPut, "/service/rest/v1/repositories/raw/hosted/"+name, map[string]any{
		"name": name, "online": online, "storage": nexusStorage,
		"raw": map[string]any{"contentDisposition": "ATTACHMENT"},
	}, nil)
}

// nexusGradualMove moves a copied raw repo project by project: the moving repo and the group
// <target>-all, a publish to the moving repo that the group reads, then a merge with a skipped
// file and a conflict taken from the moving repo, keeping the target's name (the moving repo's
// name redirects; a publish through it lands in the target). A second move merges while its
// Nexus repo is offline: the final sync fails, the merge fails before the switch-over, and a
// resume after the repo is online again finishes it, keeping the moving repo's name.
func nexusGradualMove(t *testing.T, n *Nexus, s *Server) {
	src, first := nexusRawRepo(t, n, "src-gm")
	same := nexusRawFile{"dist/same.txt", []byte("the same bytes " + randHex(8))}
	clash := nexusRawFile{"dist/clash.txt", []byte("published to nexus " + randHex(8))}
	n.uploadRaw(t, src, same, clash)
	n.waitAssets(t, src, same.path, clash.path)
	source := nexusCreateSource(t, s, n)
	target := RandName("nxgm")
	nexusSaveAndWait(t, s, source.ID, []map[string]any{{"source_repo": src, "target": target, "create": true, "anonymous_read": false}})

	pub := RandName("pub")
	s.CreateUser(t, pub, pub+"-password-1")
	s.SetPerms(t, target, pub, "repo.download", "repo.publish")

	// Move it project by project: the moving repo and the group of both.
	moving := target + "-new"
	var moved nexusMoved
	s.MustAPI(t, http.MethodPost, nexusSourcePath(source.ID)+"/repos/"+src+"/move", map[string]any{"name": moving, "group": true}, s.Admin(), &moved)
	group := target + "-all"
	if moved.Name != moving || moved.Group != group {
		t.Fatalf("POST move: %+v, want %s and the group %s", moved, moving, group)
	}
	var info struct {
		Format string `json:"format"`
		Fields struct {
			MovesInto string `json:"moves_into"`
		} `json:"fields"`
	}
	s.MustAPI(t, http.MethodGet, "/api/repos/"+moving, nil, s.Admin(), &info)
	if info.Format != "raw" || info.Fields.MovesInto != target {
		t.Fatalf("moving repo %s: %+v, want a raw repo that moves into %s", moving, info, target)
	}
	if got := nexusMembers(t, s, group); !slices.Equal(got, []string{moving, target}) {
		t.Fatalf("group %s reads %v, want [%s %s]", group, got, moving, target)
	}
	if r := nexusState(t, s, source.ID).repo(t, src); !slices.Contains(r.Moving, moving) {
		t.Fatalf("source repo %s: moving %v, want %s", src, r.Moving, moving)
	}
	if st, b := s.API(t, http.MethodPost, nexusSourcePath(source.ID)+"/repos/"+src+"/move", map[string]any{"name": moving}, s.Admin()); st != http.StatusConflict {
		t.Fatalf("a second move with the name %s: status %d, want 409: %s", moving, st, b)
	}

	// A project publishes to the moving repo; builds read both through the group.
	project := nexusRawFile{"dist/project.txt", []byte("published by a project " + randHex(8))}
	theirs := nexusRawFile{clash.path, []byte("published to the moving repo " + randHex(8))}
	for _, f := range []nexusRawFile{project, same, theirs} {
		if resp, b := rawPut(t, s, "/raw/"+moving+"/"+f.path, f.data, s.PublishAuth(t, pub, moving)); resp.StatusCode != http.StatusCreated {
			t.Fatalf("PUT %s to %s as %s: status %d, want 201: %s", f.path, moving, pub, resp.StatusCode, b)
		}
	}
	nexusGetRaw(t, s, group, project.path, project.data)
	nexusGetRaw(t, s, group, first.path, first.data)

	// The plan: one file to copy, one skipped, one conflict.
	var plan nexusMergePlan
	s.MustAPI(t, http.MethodGet, "/api/nexus/merges/plan?from="+url.QueryEscape(moving), nil, s.Admin(), &plan)
	if plan.Into != target || !plan.Copying || plan.Copy != 1 || plan.Skip != 1 || plan.ConflictCount != 1 || plan.BlockedCount != 0 || plan.Busy != "" ||
		len(plan.Conflicts) != 1 || plan.Conflicts[0].Path != clash.path {
		t.Fatalf("plan of %s: %+v, want into %s, copying, 1 to copy, 1 skipped and the conflict %s", moving, plan, target, clash.path)
	}
	start := func(body map[string]any) (int, []byte) {
		return s.API(t, http.MethodPost, "/api/nexus/merges", body, s.Admin())
	}
	if st, b := start(map[string]any{"from": moving, "into": target, "keep": "target"}); st != http.StatusBadRequest {
		t.Fatalf("a merge without a choice for its conflict: status %d, want 400: %s", st, b)
	}
	var m nexusMerge
	s.MustAPI(t, http.MethodPost, "/api/nexus/merges", map[string]any{"from": moving, "into": target, "keep": "target",
		"choices": map[string]string{clash.path: "take"}}, s.Admin(), &m)
	if m = nexusWaitMerge(t, s, m.ID); m.State != "done" {
		t.Fatalf("merge of %s into %s: %+v, want done", moving, target, m)
	}
	if m.Name != target || m.Redirect != moving || m.Copied != 1 || m.Skipped != 1 || m.Taken != 1 || m.Kept != 0 {
		t.Fatalf("merge of %s into %s: %+v, want %s with the redirect %s, 1 copied, 1 skipped, 1 taken", moving, target, m, target, moving)
	}
	nexusGetRaw(t, s, target, project.path, project.data)
	nexusGetRaw(t, s, target, clash.path, theirs.data)
	nexusGetRaw(t, s, target, first.path, first.data)

	// The moving repo's name answers for the target, and the target takes uploads.
	if r := nexusRepoInfoOf(t, s, moving); r.Format != "raw" {
		t.Fatalf("GET %s after the merge: %+v, want the raw repo %s", moving, r, target)
	}
	var byName struct {
		Name string `json:"name"`
	}
	s.MustAPI(t, http.MethodGet, "/api/repos/"+moving, nil, s.Admin(), &byName)
	if byName.Name != target {
		t.Fatalf("GET /api/repos/%s after the merge: name %q, want %s", moving, byName.Name, target)
	}
	after := nexusRawFile{"dist/after.txt", []byte("published through the old name " + randHex(8))}
	if resp, b := rawPut(t, s, "/raw/"+moving+"/"+after.path, after.data, s.PublishAuth(t, pub, moving)); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT through the redirect %s: status %d, want 201: %s", moving, resp.StatusCode, b)
	}
	nexusGetRaw(t, s, target, after.path, after.data)
	if r := nexusState(t, s, source.ID).repo(t, src); r.SwitchOver == nil {
		t.Fatalf("%s after the merge: %+v, want it switched over", src, r)
	}
	// The group keeps reading the target once the moving repo is deleted.
	deadline := time.Now().Add(2 * time.Minute)
	for got := nexusMembers(t, s, group); !slices.Equal(got, []string{target}); got = nexusMembers(t, s, group) {
		if time.Now().After(deadline) {
			t.Fatalf("group %s reads %v two minutes after the merge, want [%s]", group, got, target)
		}
		time.Sleep(500 * time.Millisecond)
	}
	nexusGetRaw(t, s, group, after.path, after.data)

	// A second move keeps the moving repo's name, and its merge fails while Nexus is offline.
	src2, first2 := nexusRawRepo(t, n, "src-gm2")
	source2 := nexusCreateSource(t, s, n)
	target2 := RandName("nxgm")
	nexusSaveAndWait(t, s, source2.ID, []map[string]any{{"source_repo": src2, "target": target2, "create": true, "anonymous_read": false}})
	moving2 := RandName("nxgmnew")
	s.MustAPI(t, http.MethodPost, nexusSourcePath(source2.ID)+"/repos/"+src2+"/move", map[string]any{"name": moving2, "group": false}, s.Admin(), &moved)
	if moved.Group != "" {
		t.Fatalf("POST move without a group: %+v, want no group", moved)
	}
	own := nexusRawFile{"dist/own.txt", []byte("published to " + moving2 + " " + randHex(8))}
	if resp, b := rawPut(t, s, "/raw/"+moving2+"/"+own.path, own.data, s.PublishAuth(t, s.AdminUser, moving2)); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT to %s: status %d, want 201: %s", moving2, resp.StatusCode, b)
	}
	nexusSetOnline(t, n, src2, false)
	s.MustAPI(t, http.MethodPost, "/api/nexus/merges", map[string]any{"from": moving2, "into": target2, "keep": "source"}, s.Admin(), &m)
	if m = nexusWaitMerge(t, s, m.ID); m.State != "failed" || m.Step != "sync" || !strings.Contains(m.Error, "final sync") {
		t.Fatalf("merge while %s is offline: %+v, want failed in its final sync", src2, m)
	}
	var opInfo nexusRepoOp
	s.MustAPI(t, http.MethodGet, "/api/repos/"+moving2, nil, s.Admin(), &opInfo)
	if opInfo.Operation == nil || opInfo.Operation.Type != "merge" || opInfo.Operation.State != "failed" {
		t.Fatalf("%s after the failed merge: operation %+v, want a failed merge", moving2, opInfo.Operation)
	}
	if r := nexusState(t, s, source2.ID).repo(t, src2); r.SwitchOver != nil {
		t.Fatalf("%s after the failed merge: %+v, want it still copying", src2, r)
	}
	nexusSetOnline(t, n, src2, true)
	if st, b := s.API(t, http.MethodPost, "/api/repos/"+moving2+"/operation/resume", nil, s.Admin()); st != http.StatusOK {
		t.Fatalf("resume the merge: status %d, want 200: %s", st, b)
	}
	if m = nexusWaitMerge(t, s, m.ID); m.State != "done" || m.Name != moving2 || m.Redirect != target2 {
		t.Fatalf("resumed merge: %+v, want done as %s with the redirect %s", m, moving2, target2)
	}
	s.MustAPI(t, http.MethodGet, "/api/repos/"+target2, nil, s.Admin(), &byName)
	if byName.Name != moving2 {
		t.Fatalf("GET /api/repos/%s after the merge: name %q, want %s", target2, byName.Name, moving2)
	}
	nexusGetRaw(t, s, moving2, first2.path, first2.data)
	nexusGetRaw(t, s, target2, own.path, own.data)
	if resp, b := rawPut(t, s, "/raw/"+target2+"/dist/later.txt", []byte("later"), s.PublishAuth(t, s.AdminUser, target2)); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT through the redirect %s: status %d, want 201: %s", target2, resp.StatusCode, b)
	}
	nexusGetRaw(t, s, moving2, "dist/later.txt", []byte("later"))
}
