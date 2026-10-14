//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// Folder upload (spec §13.1): POST /api/repos/{name}/upload/scan previews what a dropped folder
// holds and stores nothing; POST /api/repos/{name}/upload with the confirmed files stores them.
// A file's path inside the folder travels as its multipart filename.

type foundJSON struct {
	Kind     string   `json:"kind"`
	Title    string   `json:"title"`
	Files    []string `json:"files"`
	Problems []string `json:"problems"`
}

type scanJSON struct {
	Found   []foundJSON `json:"found"`
	Ignored []string    `json:"ignored"`
}

// scanFolder sends POST /api/repos/{repo}/upload/scan with parts (Name is the relative path).
func scanFolder(t *testing.T, s *Server, repo string, auth Auth, parts ...uploadPart) (int, scanJSON, []byte) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, p := range parts {
		w, err := mw.CreateFormFile("file", p.Name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(p.Body)
	}
	mw.Close()
	req := s.NewRequest(t, http.MethodPost, "/api/repos/"+repo+"/upload/scan", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, b := s.Do(t, req, auth)
	var out scanJSON
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("scan answer %s: %v", b, err)
		}
	}
	return resp.StatusCode, out, b
}

func mustScan(t *testing.T, s *Server, repo string, auth Auth, parts ...uploadPart) scanJSON {
	t.Helper()
	st, out, b := scanFolder(t, s, repo, auth, parts...)
	if st != http.StatusOK {
		t.Fatalf("scan in %s: status %d, want 200: %s", repo, st, b)
	}
	return out
}

func findTitle(t *testing.T, sc scanJSON, title string) foundJSON {
	t.Helper()
	for _, f := range sc.Found {
		if f.Title == title {
			return f
		}
	}
	t.Fatalf("scan found no %q: %+v", title, sc)
	return foundJSON{}
}

func sameSet(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

// pick returns the parts whose names are in names.
func pick(parts []uploadPart, names []string) []uploadPart {
	var out []uploadPart
	for _, p := range parts {
		if slices.Contains(names, p.Name) {
			out = append(out, p)
		}
	}
	return out
}

func testPOM(group, artifact, version string) []byte {
	return []byte(fmt.Sprintf(`<?xml version="1.0"?>
<project xmlns="http://maven.apache.org/POM/4.0.0">
  <modelVersion>4.0.0</modelVersion>
  <groupId>%s</groupId>
  <artifactId>%s</artifactId>
  <version>%s</version>
</project>
`, group, artifact, version))
}

func TestFolderUploadMaven(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	repo := RandName("mvn")
	s.CreateRepo(t, repo, "maven", false, nil)
	w := RandName("writer")
	s.CreateUser(t, w, "writer-pass-1234", false)
	s.SetPermission(t, repo, w, "write")
	ps := s.MintSession(t, w, repo)

	// A Maven-layout tree, as the browser sends a dropped "repo" folder.
	d := "repo/com/prism/api/1.2/"
	layout := []uploadPart{
		{d + "api-1.2.pom", testPOM("com.prism", "api", "1.2")},
		{d + "api-1.2.jar", tinyJar(t, "api-12")},
		{d + "api-1.2-sources.jar", tinyJar(t, "api-12-sources")},
		{d + "api-1.2.module", []byte(`{"formatVersion":"1.1"}`)},
		{d + "api-1.2.jar.sha1", []byte("0000")},
		{d + "api-1.2.jar.asc", []byte("sig")},
		{"repo/com/prism/api/maven-metadata.xml", []byte("<metadata/>")},
		{"repo/README.txt", []byte("hello")},
	}
	if st, _, b := scanFolder(t, s, repo, Basic{w, "writer-pass-1234"}, layout...); st != http.StatusUnauthorized {
		t.Fatalf("scan with a password: status %d, want 401: %s", st, b)
	}
	sc := mustScan(t, s, repo, ps.Auth(), layout...)
	api := findTitle(t, sc, "com.prism:api:1.2")
	wantFiles := []string{d + "api-1.2.pom", d + "api-1.2.jar", d + "api-1.2-sources.jar", d + "api-1.2.module"}
	if len(sc.Found) != 1 || api.Kind != "maven" || len(api.Problems) != 0 || !sameSet(api.Files, wantFiles) {
		t.Fatalf("scan of the Maven layout: %+v, want one clean artifact with %v", sc, wantFiles)
	}
	if !sameSet(sc.Ignored, []string{d + "api-1.2.jar.sha1", d + "api-1.2.jar.asc", "repo/com/prism/api/maven-metadata.xml", "repo/README.txt"}) {
		t.Fatalf("scan ignored %v", sc.Ignored)
	}

	// A flat build folder with two artifacts.
	flat := []uploadPart{
		{"libs/core-2.0.pom", testPOM("com.prism", "core", "2.0")},
		{"libs/core-2.0.jar", tinyJar(t, "core-20")},
		{"libs/core-2.0-javadoc.jar", tinyJar(t, "core-20-javadoc")},
		{"libs/util-2.0.pom", testPOM("com.prism", "util", "2.0")},
		{"libs/util-2.0.jar", tinyJar(t, "util-20")},
		{"libs/notes.txt", []byte("not an artifact")},
	}
	sc = mustScan(t, s, repo, ps.Auth(), flat...)
	core, util := findTitle(t, sc, "com.prism:core:2.0"), findTitle(t, sc, "com.prism:util:2.0")
	if len(sc.Found) != 2 || len(core.Problems)+len(util.Problems) != 0 ||
		!sameSet(core.Files, []string{"libs/core-2.0.pom", "libs/core-2.0.jar", "libs/core-2.0-javadoc.jar"}) ||
		!sameSet(util.Files, []string{"libs/util-2.0.pom", "libs/util-2.0.jar"}) ||
		!sameSet(sc.Ignored, []string{"libs/notes.txt"}) {
		t.Fatalf("scan of the flat folder: %+v", sc)
	}

	// A bad tree: coordinates that do not match the path.
	bad := []uploadPart{
		{"bad/com/prism/api/1.3/api-1.3.pom", testPOM("com.prism", "api", "1.4")},
		{"bad/com/prism/api/1.3/api-1.3.jar", tinyJar(t, "api-13")},
		{"bad/org/other/lib/1.0/lib-1.0.pom", testPOM("com.prism", "lib", "1.0")},
	}
	sc = mustScan(t, s, repo, ps.Auth(), bad...)
	v14, lib := findTitle(t, sc, "com.prism:api:1.4"), findTitle(t, sc, "com.prism:lib:1.0")
	if len(v14.Problems) == 0 || !strings.Contains(strings.Join(v14.Problems, " "), "version") ||
		!slices.Contains(v14.Files, "bad/com/prism/api/1.3/api-1.3.jar") {
		t.Fatalf("scan of a pom whose version does not match its folder: %+v", v14)
	}
	if len(lib.Problems) == 0 || !strings.Contains(strings.Join(lib.Problems, " "), "groupId") {
		t.Fatalf("scan of a pom whose groupId does not match its folder: %+v", lib)
	}
	if st, b := manualUpload(t, s, repo, ps.Auth(), nil, bad...); st != http.StatusBadRequest {
		t.Fatalf("upload of the bad tree: status %d, want 400: %s", st, b)
	}

	// Scans stored nothing.
	if items, _ := treePage(t, s, repo, "", "", 50, s.AdminBasic()); len(items) != 0 {
		t.Fatalf("the repo after scans: %v, want empty", nodeNames(items))
	}

	// The confirmed files, sent again, are stored with checksums and metadata.
	got := mustUpload(t, s, repo, ps.Auth(), nil, pick(layout, api.Files)...)
	have := map[string]string{}
	for _, u := range got {
		have[u.Path] = u.Status
	}
	for _, p := range []string{"com/prism/api/1.2/api-1.2.jar", "com/prism/api/1.2/api-1.2.jar.sha1", "com/prism/api/1.2/api-1.2-sources.jar",
		"com/prism/api/1.2/api-1.2.module", "com/prism/api/1.2/api-1.2.pom", "com/prism/api/maven-metadata.xml"} {
		if have[p] != "created" {
			t.Fatalf("folder upload: %s is %q, want created (answer %+v)", p, have[p], got)
		}
	}
	confirmed := append(slices.Clone(core.Files), util.Files...)
	got = mustUpload(t, s, repo, ps.Auth(), nil, pick(flat, confirmed)...)
	have = map[string]string{}
	for _, u := range got {
		have[u.Path] = u.Status
	}
	for _, p := range []string{"com/prism/core/2.0/core-2.0-javadoc.jar", "com/prism/util/2.0/util-2.0.jar", "com/prism/util/maven-metadata.xml"} {
		if have[p] != "created" {
			t.Fatalf("flat folder upload: %s is %q, want created (answer %+v)", p, have[p], got)
		}
	}
	if resp, _ := authGet(t, s, "/maven/"+repo+"/com/prism/util/2.0/util-2.0.jar", s.AdminBasic()); resp.StatusCode != http.StatusOK {
		t.Fatalf("GET the uploaded util jar: status %d", resp.StatusCode)
	}
}

func TestFolderUploadDebianAndRaw(t *testing.T) {
	pkgs := buildDebPackages(t)
	archs := []string{"amd64", "arm64"}
	if !slices.Contains(archs, pkgs.arch) {
		archs = append(archs, pkgs.arch)
	}
	s := StartServer(t, ServerOpts{})
	deb, raw := RandName("deb"), RandName("raw")
	s.CreateRepo(t, deb, "debian", false, map[string]any{"architectures": archs})
	s.CreateRepo(t, raw, "raw", false, nil)
	w := RandName("writer")
	s.CreateUser(t, w, "writer-pass-1234", false)
	s.SetPermission(t, deb, w, "write")
	s.SetPermission(t, raw, w, "write")

	// A folder of .debs, one of them broken.
	ps := s.MintSession(t, w, deb)
	debs := []uploadPart{
		{"debs/native.deb", pkgs.native},
		{"debs/hello.deb", pkgs.hello1},
		{"debs/broken.deb", []byte("not an ar archive")},
		{"debs/notes.txt", []byte("x")},
	}
	sc := mustScan(t, s, deb, ps.Auth(), debs...)
	native := findTitle(t, sc, "e2e-native 1.0 ("+pkgs.arch+")")
	hello := findTitle(t, sc, "e2e-hello 1.0 (all)")
	broken := findTitle(t, sc, "debs/broken.deb")
	if len(sc.Found) != 3 || native.Kind != "debian" || len(native.Problems)+len(hello.Problems) != 0 || len(broken.Problems) == 0 ||
		!sameSet(sc.Ignored, []string{"debs/notes.txt"}) {
		t.Fatalf("scan of the .deb folder: %+v", sc)
	}
	got := mustUpload(t, s, deb, ps.Auth(), map[string]string{"distribution": "trixie"}, pick(debs, append(native.Files, hello.Files...))...)
	if len(got) != 2 || got[0].Status != "created" || got[1].Status != "created" {
		t.Fatalf("upload of the confirmed .debs: %+v", got)
	}
	s.WaitDebianIndexed(t, deb)

	// A raw folder: every file, stored at its path under the target folder.
	rs := s.MintSession(t, w, raw)
	site := []uploadPart{{"site/index.html", []byte("<h1>hi</h1>")}, {"site/css/a.css", []byte("a{}")}}
	sc = mustScan(t, s, raw, rs.Auth(), site...)
	if len(sc.Found) != 2 || sc.Found[0].Kind != "file" || len(sc.Ignored) != 0 {
		t.Fatalf("scan of the raw folder: %+v", sc)
	}
	got = mustUpload(t, s, raw, rs.Auth(), map[string]string{"folder": "web"}, site...)
	if len(got) != 2 || got[0].Path != "web/site/index.html" || got[1].Path != "web/site/css/a.css" {
		t.Fatalf("raw folder upload: %+v", got)
	}
	rawWantGet(t, s, nil, "/raw/"+raw+"/web/site/css/a.css", s.AdminBasic(), []byte("a{}"))
}
