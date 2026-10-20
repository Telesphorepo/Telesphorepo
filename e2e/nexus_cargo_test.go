//go:build import

package e2e

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The Nexus import of cargo repos (part of TestNexus): cargo publishes two versions of a crate to
// a Nexus cargo hosted repo, the sync imports them with the entries of their Cargo.toml, cargo
// builds from the target, and a second sync finds nothing new.

// nexusCargo runs the cargo part of TestNexus against n and s.
func nexusCargo(t *testing.T, n *Nexus, s *Server) {
	src := RandName("src-cargo")
	n.mustAPI(t, http.MethodPost, "/service/rest/v1/repositories/cargo/hosted", map[string]any{
		"name": src, "online": true, "storage": nexusStorage,
	}, nil)
	crate := "nx_" + randHex(4)
	files := []ContainerFile{cargoRegistry("/work", "nx", n.InternalURL+"/repository/"+src+"/", false)}
	files = append(files, cargoLib("/work/lib", crate, "1.0.0", "", "hello 1.0.0 of "+crate+" mirrored from nexus")...)
	token := "Basic " + base64.StdEncoding.EncodeToString([]byte(n.User+":"+n.Pass))
	code, out := RunClient(t, rustImage, ClientOpts{Cmd: []string{`set -e
cd /work/lib
cargo publish --registry nx --no-verify
sed -i 's/^version = "1.0.0"/version = "1.1.0"/; s/hello 1.0.0/hello 1.1.0/' Cargo.toml src/lib.rs
cargo publish --registry nx --no-verify
`}, Files: files, Env: cargoEnv(map[string]string{cargoTokenEnv("nx"): token}), Timeout: 15 * time.Minute})
	if code != 0 {
		t.Fatalf("cargo publish to the Nexus cargo repo: exit %d\n%s", code, out)
	}
	var paths []string
	deadline := time.Now().Add(2 * time.Minute)
	for {
		paths = n.assetPaths(t, src)
		if len(paths) >= 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(2 * time.Second)
	}
	t.Logf("the assets of the Nexus cargo repo: %v", paths)

	source := nexusCreateSource(t, s, n)
	if r := nexusGetSource(t, s, source.ID).repo(t, src); r.SourceFormat != "cargo" || r.Type != "hosted" || !r.Supported {
		t.Fatalf("the Nexus cargo repo: %+v, want a supported cargo hosted repo", r)
	}
	target := RandName("nxcargo")
	first := nexusSaveAndWait(t, s, source.ID, []map[string]any{{"source_repo": src, "target": target, "create": true, "anonymous_read": true}})
	if r := first.repo(t, src); r.Imported != 2 || r.Failed != 0 {
		t.Fatalf("first sync of the cargo repo: %+v, want both crates imported (Nexus assets %v)", r, paths)
	}
	if info := nexusRepoInfoOf(t, s, target); info.Format != "cargo" || info.Kind != "hosted" || !info.AnonymousRead {
		t.Fatalf("the cargo target: %+v", info)
	}
	requireCargoAssets(t, s, target, "crates/"+crate+"/"+crate+"-1.0.0.crate", "crates/"+crate+"/"+crate+"-1.1.0.crate")

	code, out = RunClient(t, rustImage, ClientOpts{Cmd: []string{"cd /work/app && cargo run -q"},
		Files: append(cargoBin("/work/app", crate+" = { version = \"1\", registry = \"tr\" }\n", crate),
			cargoRegistry("/work", "tr", s.InternalURL+"/cargo/"+target+"/index/", false)),
		Env: cargoEnv(nil), Timeout: 15 * time.Minute})
	if code != 0 || !strings.Contains(out, "hello 1.1.0 of "+crate+" mirrored from nexus") {
		t.Fatalf("cargo run from the imported repo: exit %d\n%s", code, out)
	}

	again := nexusSyncNow(t, s, source.ID)
	if r := again.repo(t, src); r.Imported != 0 || r.Failed != 0 {
		t.Fatalf("second sync of the cargo repo: %+v, want nothing new", r)
	}
}

// requireCargoAssets fails unless every path of want is an asset of repo.
func requireCargoAssets(t *testing.T, s *Server, repo string, want ...string) {
	t.Helper()
	var page struct {
		Items []struct {
			Path string `json:"path"`
		} `json:"items"`
	}
	s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/assets?limit=200&prefix=crates/", nil, s.Admin(), &page)
	have := map[string]bool{}
	for _, it := range page.Items {
		have[strings.TrimPrefix(it.Path, "/")] = true
	}
	for _, w := range want {
		if !have[w] {
			t.Fatalf("%s has no %s: %+v", repo, w, page.Items)
		}
	}
}
