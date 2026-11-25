//go:build import

package e2e

import (
	"net/http"
	"strings"
	"testing"
)

// nexusPyPI uploads two wheels to a Nexus pypi hosted repo, imports the repo into s, finds both in
// the simple index of the target, checks the rules of a held mirror, and syncs again with nothing
// new.
func nexusPyPI(t *testing.T, n *Nexus, s *Server) {
	src := RandName("src-pypi")
	n.mustAPI(t, http.MethodPost, "/service/rest/v1/repositories/pypi/hosted", map[string]any{
		"name": src, "online": true, "storage": nexusStorage}, nil)
	name, mod := pyNames()
	var files []string
	for _, v := range []string{"1.0", "1.1"} {
		file, whl := pyWheel(name, mod, v, "nexus "+name+" "+v)
		n.upload(t, src, nexusPart{field: "pypi.asset", value: file, data: whl})
		files = append(files, file)
	}
	n.waitAssets(t, src, files...)

	source := nexusCreateSource(t, s, n)
	if r := nexusGetSource(t, s, source.ID).repo(t, src); r.SourceFormat != "pypi" || r.Type != "hosted" || !r.Supported {
		t.Fatalf("the Nexus pypi repo: %+v, want a supported pypi hosted repo", r)
	}
	target := RandName("nxpy")
	first := nexusSaveAndWait(t, s, source.ID, []map[string]any{{"source_repo": src, "target": target, "create": true, "anonymous_read": true}})
	if r := first.repo(t, src); r.Imported != 2 || r.Failed != 0 {
		t.Fatalf("first sync of the pypi repo: %+v, want both wheels imported", r)
	}
	if info := nexusRepoInfoOf(t, s, target); info.Format != "pypi" || info.Kind != "hosted" || !info.AnonymousRead {
		t.Fatalf("the pypi target: %+v", info)
	}

	index := string(getFollowS3(t, s, nil, "/pypi/"+target+"/simple/"+name+"/"))
	for _, file := range files {
		if !strings.Contains(index, file) {
			t.Fatalf("the simple index of %s on the imported repo does not list %s:\n%s", name, file, index)
		}
	}

	// The target is a held mirror until its switch-over: a bulk delete that names it deletes
	// nothing, and its operation can be neither resumed nor cleared. A repo without an operation
	// has none to clear.
	var info struct {
		Operation *struct {
			Type  string `json:"type"`
			State string `json:"state"`
		} `json:"operation"`
	}
	s.MustAPI(t, http.MethodGet, "/api/repos/"+target, nil, s.Admin(), &info)
	if info.Operation == nil || info.Operation.Type != "mirror" || info.Operation.State != "held" {
		t.Fatalf("the pypi target: operation %+v, want a held mirror", info.Operation)
	}
	other := RandName("nxpyother")
	s.CreateRepo(t, other, "raw", false, nil)
	if st, b := s.API(t, http.MethodPost, "/api/repos/delete", map[string]any{"names": []string{other, target}}, s.Admin()); st != http.StatusConflict || !strings.Contains(string(b), target) {
		t.Fatalf("bulk delete of %s and the held mirror %s: status %d, want 409 that names %s: %s", other, target, st, target, b)
	}
	for _, r := range []string{target, other} {
		if st, b := s.API(t, http.MethodGet, "/api/repos/"+r, nil, s.Admin()); st != http.StatusOK {
			t.Fatalf("GET %s after the refused bulk delete: status %d, want 200: %s", r, st, b)
		}
	}
	for _, req := range []struct{ method, path string }{
		{http.MethodPost, "/api/repos/" + target + "/operation/resume"},
		{http.MethodDelete, "/api/repos/" + target + "/operation"},
		{http.MethodDelete, "/api/repos/" + other + "/operation"},
	} {
		if st, b := s.API(t, req.method, req.path, nil, s.Admin()); st != http.StatusConflict {
			t.Fatalf("%s %s: status %d, want 409: %s", req.method, req.path, st, b)
		}
	}
	again := nexusSyncNow(t, s, source.ID)
	if r := again.repo(t, src); r.Imported != 0 || r.Failed != 0 {
		t.Fatalf("second sync of the pypi repo: %+v, want nothing new", r)
	}
}
