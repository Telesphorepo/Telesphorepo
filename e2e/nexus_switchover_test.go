//go:build import

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"
)

// nexusRawRepo creates a raw hosted repo in Nexus with one file, dist/<name>.txt, and returns its
// name and the file.
func nexusRawRepo(t *testing.T, n *Nexus, prefix string) (string, nexusRawFile) {
	t.Helper()
	name := RandName(prefix)
	n.mustAPI(t, http.MethodPost, "/service/rest/v1/repositories/raw/hosted", map[string]any{
		"name": name, "online": true, "storage": nexusStorage,
		"raw": map[string]any{"contentDisposition": "ATTACHMENT"},
	}, nil)
	f := nexusRawFile{"dist/" + name + ".txt", []byte("first file of " + name + " " + randHex(8) + "\n")}
	n.uploadRaw(t, name, f)
	n.waitAssets(t, name, f.path)
	return name, f
}

// nexusSwitchOverOf is the switch-over of a Nexus repo in the source detail.
type nexusSwitchOverOf struct {
	SyncID     int64     `json:"sync_id"`
	Failed     int64     `json:"failed"`
	SwitchedAt time.Time `json:"switched_at"`
}

// nexusRepoState is the part of a source repo that the switch-over test reads.
type nexusRepoState struct {
	Name       string             `json:"name"`
	Target     string             `json:"target"`
	SwitchOver *nexusSwitchOverOf `json:"switch_over"`
	Rehearsals []string           `json:"rehearsals"`
	Moving     []string           `json:"moving"`
}

// nexusSourceState is the part of GET /api/nexus/sources/{id} that the switch-over test reads.
type nexusSourceState struct {
	State        string           `json:"state"`
	Status       string           `json:"status"`
	Copying      int              `json:"copying"`
	SwitchedOver int              `json:"switched_over"`
	Repos        []nexusRepoState `json:"repos"`
}

func nexusState(t *testing.T, s *Server, id int64) nexusSourceState {
	t.Helper()
	var st nexusSourceState
	s.MustAPI(t, http.MethodGet, nexusSourcePath(id), nil, s.Admin(), &st)
	return st
}

func (st nexusSourceState) repo(t *testing.T, name string) nexusRepoState {
	t.Helper()
	i := slices.IndexFunc(st.Repos, func(r nexusRepoState) bool { return r.Name == name })
	if i < 0 {
		t.Fatalf("the source lists no repo %s: %+v", name, st.Repos)
	}
	return st.Repos[i]
}

// nexusRepoOp is the operation and the file count of GET /api/repos/{name}.
type nexusRepoOp struct {
	FileCount int64 `json:"file_count"`
	Fields    struct {
		RehearsalFor string `json:"rehearsal_for"`
	} `json:"fields"`
	Format        string `json:"format"`
	Kind          string `json:"kind"`
	AnonymousRead bool   `json:"anonymous_read"`
	Operation     *struct {
		Type  string `json:"type"`
		State string `json:"state"`
		Error string `json:"error"`
	} `json:"operation"`
}

// nexusWaitEmpty waits until the empty operation of repo ended and the repo has no file.
func nexusWaitEmpty(t *testing.T, s *Server, repo string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		var info nexusRepoOp
		s.MustAPI(t, http.MethodGet, "/api/repos/"+repo, nil, s.Admin(), &info)
		if info.Operation != nil && info.Operation.State == "failed" {
			t.Fatalf("empty of %s failed: %+v", repo, info.Operation)
		}
		if info.Operation == nil && info.FileCount == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s not empty after 2 minutes: %+v", repo, info)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// nexusSwitchOver copies two raw repos from Nexus, rehearses publishing to the first (publish,
// empty, publish again, delete), switches over the first while the second keeps syncing, then
// switches over everything, which finishes the source.
func nexusSwitchOver(t *testing.T, n *Nexus, s *Server, tg *S3Target) {
	srcA, fileA := nexusRawRepo(t, n, "src-swa")
	srcB, fileB := nexusRawRepo(t, n, "src-swb")
	source := nexusCreateSource(t, s, n)
	tA, tB := RandName("nxswa"), RandName("nxswb")
	maps := []map[string]any{
		{"source_repo": srcA, "target": tA, "create": true, "anonymous_read": false},
		{"source_repo": srcB, "target": tB, "create": true, "anonymous_read": false},
	}
	first := nexusSaveAndWait(t, s, source.ID, maps)
	if first.repo(t, srcA).Imported != 1 || first.repo(t, srcB).Imported != 1 {
		t.Fatalf("first sync: %+v, want one file of each repo", first.Repos)
	}
	if st := nexusState(t, s, source.ID); st.Copying != 2 || st.SwitchedOver != 0 || st.repo(t, srcA).SwitchOver != nil {
		t.Fatalf("source after the first sync: %+v, want 2 repos copying and none switched over", st)
	}

	// A person who publishes to tA gets publish on its rehearsal repo.
	pub := RandName("pub")
	s.CreateUser(t, pub, pub+"-password-1")
	s.SetPerms(t, tA, pub, "repo.download", "repo.publish")

	// Rehearse publishing: a private raw repo named after tA, listed under srcA.
	rehearse := nexusSourcePath(source.ID) + "/repos/" + srcA + "/rehearsal"
	var created struct {
		Name string `json:"name"`
	}
	s.MustAPI(t, http.MethodPost, rehearse, nil, s.Admin(), &created)
	reh := tA + "-rehearsal"
	if created.Name != reh {
		t.Fatalf("POST rehearsal: name %q, want %s", created.Name, reh)
	}
	var info nexusRepoOp
	s.MustAPI(t, http.MethodGet, "/api/repos/"+reh, nil, s.Admin(), &info)
	if info.Format != "raw" || info.Kind != "hosted" || info.AnonymousRead || info.Fields.RehearsalFor != tA {
		t.Fatalf("rehearsal repo %s: %+v, want a private raw repo that rehearses %s", reh, info, tA)
	}
	if r := nexusState(t, s, source.ID).repo(t, srcA); !slices.Equal(r.Rehearsals, []string{reh}) {
		t.Fatalf("source repo %s: rehearsals %v, want [%s]", srcA, r.Rehearsals, reh)
	}
	// A second rehearsal names the one that exists.
	st, b := s.API(t, http.MethodPost, rehearse, nil, s.Admin())
	var taken struct {
		Repo      string `json:"repo"`
		Rehearsal bool   `json:"rehearsal"`
	}
	if st != http.StatusConflict || json.Unmarshal(b, &taken) != nil || taken.Repo != reh || !taken.Rehearsal {
		t.Fatalf("second rehearsal: status %d, want 409 naming %s as the rehearsal: %s", st, reh, b)
	}
	// The field is set only by Rehearse publishing.
	if st, b := s.API(t, http.MethodPatch, "/api/repos/"+tB, map[string]any{"fields": map[string]any{"rehearsal_for": tA}}, s.Admin()); st != http.StatusBadRequest {
		t.Fatalf("PATCH rehearsal_for: status %d, want 400: %s", st, b)
	}

	// Publish to the rehearsal, empty it, publish again: it keeps who can publish.
	for i := range 2 {
		resp, b := rawPut(t, s, fmt.Sprintf("/raw/%s/dist/try-%d.txt", reh, i), []byte("rehearsal"), s.PublishAuth(t, pub, reh))
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("PUT to the rehearsal as %s: status %d, want 201: %s", pub, resp.StatusCode, b)
		}
	}
	var emptied struct {
		Name  string `json:"name"`
		Files int64  `json:"files"`
	}
	s.MustAPI(t, http.MethodPost, "/api/nexus/rehearsals/"+reh+"/empty", nil, s.Admin(), &emptied)
	if emptied.Name != reh || emptied.Files < 2 {
		t.Fatalf("POST empty: %+v, want %s with at least 2 files", emptied, reh)
	}
	nexusWaitEmpty(t, s, reh)
	if resp, b := rawPut(t, s, "/raw/"+reh+"/dist/again.txt", []byte("again"), s.PublishAuth(t, pub, reh)); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT to the emptied rehearsal: status %d, want 201: %s", resp.StatusCode, b)
	}
	// Only a rehearsal repo empties here.
	if st, b := s.API(t, http.MethodPost, "/api/nexus/rehearsals/"+tB+"/empty", nil, s.Admin()); st != http.StatusBadRequest {
		t.Fatalf("POST empty of %s: status %d, want 400: %s", tB, st, b)
	}

	// Switch over tA alone: uploads go to tA, tB still refuses them.
	var sy nexusSync
	s.MustAPI(t, http.MethodPost, nexusSourcePath(source.ID)+"/repos/"+srcA+"/switch-over", nil, s.Admin(), &sy)
	if sy.Trigger != "cutover" {
		t.Fatalf("POST switch-over: trigger %q, want cutover", sy.Trigger)
	}
	final := nexusWaitSync(t, s, sy.ID)
	if len(final.Repos) != 1 || final.Repos[0].SourceRepo != srcA {
		t.Fatalf("final sync of %s: repos %+v, want %s only", srcA, final.Repos, srcA)
	}
	got := nexusState(t, s, source.ID)
	if a := got.repo(t, srcA); got.State != "mirroring" || got.Copying != 1 || got.SwitchedOver != 1 || a.SwitchOver == nil || a.SwitchOver.SyncID != sy.ID ||
		got.repo(t, srcB).SwitchOver != nil {
		t.Fatalf("source after the switch-over of %s: %+v, want it mirroring, %s switched over in sync %d, %s copying", srcA, got, srcA, sy.ID, srcB)
	}
	if resp, b := rawPut(t, s, "/raw/"+tA+"/dist/here.txt", []byte("published here"), s.PublishAuth(t, s.AdminUser, tA)); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT to %s after its switch-over: status %d, want 201: %s", tA, resp.StatusCode, b)
	}
	st, b = s.Mint(t, s.device(t, s.AdminUser), tB)
	nexusWantMirrorBlocked(t, s, "publish session for "+tB, tB, st, b)
	if st, b := s.API(t, http.MethodPost, nexusSourcePath(source.ID)+"/repos/"+srcA+"/switch-over", nil, s.Admin()); st != http.StatusConflict {
		t.Fatalf("second switch-over of %s: status %d, want 409: %s", srcA, st, b)
	}

	// A new file in Nexus reaches only the repo that still copies.
	newA := nexusRawFile{"dist/later-" + srcA + ".txt", []byte("later " + randHex(8))}
	newB := nexusRawFile{"dist/later-" + srcB + ".txt", []byte("later " + randHex(8))}
	n.uploadRaw(t, srcA, newA)
	n.uploadRaw(t, srcB, newB)
	n.waitAssets(t, srcA, newA.path)
	n.waitAssets(t, srcB, newB.path)
	later := nexusSyncNow(t, s, source.ID)
	if len(later.Repos) != 1 || later.repo(t, srcB).Imported != 1 {
		t.Fatalf("sync after the switch-over of %s: repos %+v, want %s alone with its new file", srcA, later.Repos, srcB)
	}
	nexusGetRaw(t, s, tg, tB, newB.path, newB.data)
	if resp, _ := s.Do(t, s.NewRequest(t, http.MethodGet, "/raw/"+tA+"/"+newA.path, nil), s.AdminBasic()); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET the new Nexus file of %s from %s after its switch-over: status %d, want 404", srcA, tA, resp.StatusCode)
	}
	for _, f := range []struct {
		repo string
		file nexusRawFile
	}{{tA, fileA}, {tB, fileB}} {
		nexusGetRaw(t, s, tg, f.repo, f.file.path, f.file.data)
	}

	// A switched-over repo cannot go back: a save without it keeps it, and a save that moves it
	// to another repo is refused.
	nexusSaveAndWait(t, s, source.ID, []map[string]any{{"source_repo": srcB, "target": tB, "create": false}})
	if a := nexusState(t, s, source.ID).repo(t, srcA); a.Target != tA || a.SwitchOver == nil {
		t.Fatalf("%s after a save without it: %+v, want it switched over into %s", srcA, a, tA)
	}
	if st, b := s.API(t, http.MethodPut, nexusSourcePath(source.ID)+"/repos", map[string]any{"repos": []map[string]any{
		{"source_repo": srcA, "target": RandName("nxother"), "create": true},
		{"source_repo": srcB, "target": tB, "create": false},
	}}, s.Admin()); st != http.StatusBadRequest {
		t.Fatalf("a save that moves the switched-over %s: status %d, want 400: %s", srcA, st, b)
	}

	// Switch over everything: tB switches over, and the source is done.
	nexusCutOver(t, s, source.ID)
	got = nexusState(t, s, source.ID)
	if got.Copying != 0 || got.SwitchedOver != 2 || got.repo(t, srcB).SwitchOver == nil {
		t.Fatalf("source after switching over everything: %+v, want both repos switched over", got)
	}
	if resp, b := rawPut(t, s, "/raw/"+tB+"/dist/here.txt", []byte("published here"), s.PublishAuth(t, s.AdminUser, tB)); resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT to %s after switching over everything: status %d, want 201: %s", tB, resp.StatusCode, b)
	}

	// Delete the rehearsal repo: the repository delete removes it, and the source lists it no more.
	if st, b := s.API(t, http.MethodPost, "/api/repos/delete", map[string]any{"names": []string{reh}}, s.Admin()); st != http.StatusAccepted {
		t.Fatalf("delete %s: status %d, want 202: %s", reh, st, b)
	}
	deadline := time.Now().Add(2 * time.Minute)
	for {
		st, _ := s.API(t, http.MethodGet, "/api/repos/"+reh, nil, s.Admin())
		if st == http.StatusNotFound {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s still answers %d two minutes after its delete", reh, st)
		}
		time.Sleep(500 * time.Millisecond)
	}
	if r := nexusState(t, s, source.ID).repo(t, srcA); len(r.Rehearsals) != 0 {
		t.Fatalf("%s after the delete of its rehearsal: rehearsals %v, want none", srcA, r.Rehearsals)
	}
}
