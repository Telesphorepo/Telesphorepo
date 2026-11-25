//go:build import

package e2e

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// npmTarball returns the tarball of a package whose index.js exports message, as npm pack
// writes it.
func npmTarball(t *testing.T, name, version, message string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	pj, _ := json.Marshal(map[string]string{"name": name, "version": version, "main": "index.js", "license": "MIT"})
	for _, f := range []struct{ name, body string }{
		{"package/package.json", string(pj)},
		{"package/index.js", fmt.Sprintf("module.exports = %q;\n", message)},
	} {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o644, Size: int64(len(f.body)), ModTime: time.Unix(0, 0), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(f.body))
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	gz.Close()
	return buf.Bytes()
}

// nexusNpm uploads two versions of a plain and of a scoped package to a Nexus npm hosted repo,
// imports the repo into s, reads both packuments from the target, and syncs again with nothing new.
func nexusNpm(t *testing.T, n *Nexus, s *Server) {
	src := RandName("src-npm")
	n.mustAPI(t, http.MethodPost, "/service/rest/v1/repositories/npm/hosted", map[string]any{
		"name": src, "online": true, "storage": nexusStorage}, nil)
	plain, scoped := RandName("nxplain"), "@nx/"+RandName("lib")
	for _, name := range []string{plain, scoped} {
		for _, v := range []string{"1.0.0", "1.1.0"} {
			n.upload(t, src, nexusPart{field: "npm.asset", value: "package.tgz", data: npmTarball(t, name, v, name+" "+v)})
		}
	}
	n.waitAssets(t, src, plain+"-1.1.0.tgz", strings.TrimPrefix(scoped, "@nx/")+"-1.1.0.tgz")

	source := nexusCreateSource(t, s, n)
	if r := nexusGetSource(t, s, source.ID).repo(t, src); r.SourceFormat != "npm" || r.Type != "hosted" || !r.Supported {
		t.Fatalf("the Nexus npm repo: %+v, want a supported npm hosted repo", r)
	}
	target := RandName("nxnpm")
	first := nexusSaveAndWait(t, s, source.ID, []map[string]any{{"source_repo": src, "target": target, "create": true, "anonymous_read": true}})
	if r := first.repo(t, src); r.Imported != 4 || r.Failed != 0 {
		t.Fatalf("first sync of the npm repo: %+v, want the four tarballs imported", r)
	}
	if info := nexusRepoInfoOf(t, s, target); info.Format != "npm" || info.Kind != "hosted" {
		t.Fatalf("the npm target: %+v", info)
	}

	for _, name := range []string{plain, scoped} {
		var packument struct {
			DistTags map[string]string `json:"dist-tags"`
			Versions map[string]any    `json:"versions"`
		}
		s.MustAPI(t, http.MethodGet, "/npm/"+target+"/"+name, nil, nil, &packument)
		if packument.DistTags["latest"] != "1.1.0" || len(packument.Versions) != 2 || packument.Versions["1.0.0"] == nil || packument.Versions["1.1.0"] == nil {
			t.Fatalf("packument of %s from the imported repo: dist-tags %v, versions %v; want 1.0.0 and 1.1.0, latest 1.1.0",
				name, packument.DistTags, slices.Sorted(maps.Keys(packument.Versions)))
		}
	}

	again := nexusSyncNow(t, s, source.ID)
	if r := again.repo(t, src); r.Imported != 0 || r.Failed != 0 {
		t.Fatalf("second sync of the npm repo: %+v, want nothing new", r)
	}
}
