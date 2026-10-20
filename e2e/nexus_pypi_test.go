//go:build import

package e2e

import (
	"net/http"
	"strings"
	"testing"
)

// nexusPyPI uploads two wheels to a Nexus pypi hosted repo, imports the repo into s, installs the
// newest with pip from the target, and syncs again with nothing new.
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

	code, out := RunClient(t, pythonImage, ClientOpts{Cmd: []string{`set -e
pip install --root-user-action=ignore --no-cache-dir --index-url "$INDEX" ` + name + `
python -c "import ` + mod + `; print(` + mod + `.MSG)"
`}, Env: map[string]string{"INDEX": pyIndex(s, target, "", "")}})
	if code != 0 || !strings.Contains(out, "nexus "+name+" 1.1") {
		t.Fatalf("pip install from the imported repo: exit %d\n%s", code, out)
	}

	again := nexusSyncNow(t, s, source.ID)
	if r := again.repo(t, src); r.Imported != 0 || r.Failed != 0 {
		t.Fatalf("second sync of the pypi repo: %+v, want nothing new", r)
	}
}
