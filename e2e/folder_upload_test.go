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

// Upload plans: POST /api/repos/{name}/upload/plan answers what an upload of the
// files would store and stores nothing; the browser sends the list of files and the first bytes
// of the files the format reads (poms, .debs). POST /api/repos/{name}/upload with the chosen
// files runs the same plan and stores them. A file's path inside the dropped folder travels as
// its multipart filename.

type planTargetJSON struct {
	Path   string `json:"path"`
	Target string `json:"target"`
}

type planItemJSON struct {
	Title    string            `json:"title"`
	Files    []planTargetJSON  `json:"files"`
	Facts    map[string]string `json:"facts"`
	Problems []string          `json:"problems"`
}

// paths returns the upload paths of the item's files.
func (it planItemJSON) paths() []string {
	out := make([]string, len(it.Files))
	for i, f := range it.Files {
		out[i] = f.Path
	}
	return out
}

// target returns the target of the file at path, or "".
func (it planItemJSON) target(path string) string {
	for _, f := range it.Files {
		if f.Path == path {
			return f.Target
		}
	}
	return ""
}

type planFieldJSON struct {
	Key     string `json:"key"`
	Value   string `json:"value"`
	Read    bool   `json:"read"`
	Problem string `json:"problem"`
}

type planJSON struct {
	Items   []planItemJSON  `json:"items"`
	Ignored []string        `json:"ignored"`
	Fields  []planFieldJSON `json:"fields"`
}

func (p planJSON) field(key string) planFieldJSON {
	for _, f := range p.Fields {
		if f.Key == key {
			return f
		}
	}
	return planFieldJSON{}
}

// planOpts are the extras of a plan request: the typed fields, the open folder, and which files
// the format reads (their whole body goes as the head; the test files are small).
type planOpts struct {
	fields map[string]string
	folder string
	reads  func(name string) bool
}

// readsPOMs and readsDebs are the read rules of the Maven and Debian formats.
func readsPOMs(name string) bool {
	return strings.HasSuffix(name, ".pom") || strings.HasSuffix(name, "pom.xml")
}
func readsDebs(name string) bool { return strings.HasSuffix(name, ".deb") }

// planUpload sends POST /api/repos/{repo}/upload/plan for parts (Name is the relative path).
func planUpload(t *testing.T, s *Server, repo string, auth Auth, o planOpts, parts ...uploadPart) (int, planJSON, []byte) {
	t.Helper()
	type file struct {
		Path string `json:"path"`
		Size int    `json:"size"`
	}
	req := struct {
		Fields map[string]string `json:"fields"`
		Folder string            `json:"folder"`
		Files  []file            `json:"files"`
	}{o.fields, o.folder, nil}
	for _, p := range parts {
		req.Files = append(req.Files, file{p.Name, len(p.Body)})
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	pw, err := mw.CreateFormField("plan")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(pw).Encode(req); err != nil {
		t.Fatal(err)
	}
	for _, p := range parts {
		if o.reads == nil || !o.reads(p.Name) {
			continue
		}
		w, err := mw.CreateFormFile("head", p.Name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(p.Body)
	}
	mw.Close()
	r := s.NewRequest(t, http.MethodPost, "/api/repos/"+repo+"/upload/plan", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	resp, b := s.Do(t, r, auth)
	var out planJSON
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("plan answer %s: %v", b, err)
		}
	}
	return resp.StatusCode, out, b
}

func mustPlan(t *testing.T, s *Server, repo string, auth Auth, o planOpts, parts ...uploadPart) planJSON {
	t.Helper()
	st, out, b := planUpload(t, s, repo, auth, o, parts...)
	if st != http.StatusOK {
		t.Fatalf("plan in %s: status %d, want 200: %s", repo, st, b)
	}
	return out
}

func findTitle(t *testing.T, p planJSON, title string) planItemJSON {
	t.Helper()
	for _, it := range p.Items {
		if it.Title == title {
			return it
		}
	}
	t.Fatalf("the plan has no %q: %+v", title, p)
	return planItemJSON{}
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
	s.CreateUser(t, w, "writer-pass-1234")
	s.SetPerms(t, repo, w, "repo.download", "repo.publish")
	ps := s.MintSession(t, w, repo)
	poms := planOpts{reads: readsPOMs}

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
	if st, _, b := planUpload(t, s, repo, Basic{w, "writer-pass-1234"}, poms, layout...); st != http.StatusUnauthorized {
		t.Fatalf("plan with a password: status %d, want 401: %s", st, b)
	}
	// A head of a file the format does not read is refused.
	if st, _, b := planUpload(t, s, repo, ps.Auth(), planOpts{reads: func(string) bool { return true }}, layout...); st != http.StatusBadRequest {
		t.Fatalf("plan with the head of a jar: status %d, want 400: %s", st, b)
	}
	sc := mustPlan(t, s, repo, ps.Auth(), poms, layout...)
	api := findTitle(t, sc, "com.prism:api:1.2")
	wantFiles := []string{d + "api-1.2.pom", d + "api-1.2.jar", d + "api-1.2-sources.jar", d + "api-1.2.module"}
	if len(sc.Items) != 1 || len(api.Problems) != 0 || !sameSet(api.paths(), wantFiles) || len(sc.Fields) != 0 ||
		api.target(d+"api-1.2-sources.jar") != "com/prism/api/1.2/api-1.2-sources.jar" || api.target(d+"api-1.2.pom") != "com/prism/api/1.2/api-1.2.pom" {
		t.Fatalf("plan of the Maven layout: %+v, want one clean artifact with %v and its targets", sc, wantFiles)
	}
	if !sameSet(sc.Ignored, []string{d + "api-1.2.jar.sha1", d + "api-1.2.jar.asc", "repo/com/prism/api/maven-metadata.xml", "repo/README.txt"}) {
		t.Fatalf("plan ignored %v", sc.Ignored)
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
	sc = mustPlan(t, s, repo, ps.Auth(), poms, flat...)
	core, util := findTitle(t, sc, "com.prism:core:2.0"), findTitle(t, sc, "com.prism:util:2.0")
	if len(sc.Items) != 2 || len(core.Problems)+len(util.Problems) != 0 ||
		!sameSet(core.paths(), []string{"libs/core-2.0.pom", "libs/core-2.0.jar", "libs/core-2.0-javadoc.jar"}) ||
		!sameSet(util.paths(), []string{"libs/util-2.0.pom", "libs/util-2.0.jar"}) ||
		!sameSet(sc.Ignored, []string{"libs/notes.txt"}) {
		t.Fatalf("plan of the flat folder: %+v", sc)
	}

	// A bad tree: coordinates that do not match the path.
	bad := []uploadPart{
		{"bad/com/prism/api/1.3/api-1.3.pom", testPOM("com.prism", "api", "1.4")},
		{"bad/com/prism/api/1.3/api-1.3.jar", tinyJar(t, "api-13")},
		{"bad/org/other/lib/1.0/lib-1.0.pom", testPOM("com.prism", "lib", "1.0")},
	}
	sc = mustPlan(t, s, repo, ps.Auth(), poms, bad...)
	v14, lib := findTitle(t, sc, "com.prism:api:1.4"), findTitle(t, sc, "com.prism:lib:1.0")
	if len(v14.Problems) == 0 || !strings.Contains(strings.Join(v14.Problems, " "), "version") ||
		!slices.Contains(v14.paths(), "bad/com/prism/api/1.3/api-1.3.jar") {
		t.Fatalf("plan of a pom whose version does not match its folder: %+v", v14)
	}
	if len(lib.Problems) == 0 || !strings.Contains(strings.Join(lib.Problems, " "), "groupId") {
		t.Fatalf("plan of a pom whose groupId does not match its folder: %+v", lib)
	}
	if st, b := manualUpload(t, s, repo, ps.Auth(), nil, bad...); st != http.StatusBadRequest {
		t.Fatalf("upload of the bad tree: status %d, want 400: %s", st, b)
	}

	// A lone jar: the plan asks for the coordinates, then gives the target.
	lone := []uploadPart{{"tool.jar", tinyJar(t, "tool")}}
	sc = mustPlan(t, s, repo, ps.Auth(), poms, lone...)
	if len(sc.Items) != 1 || sc.field("groupId").Problem == "" || sc.items0Target() != "" {
		t.Fatalf("plan of a lone jar without coordinates: %+v", sc)
	}
	typed := planOpts{fields: map[string]string{"groupId": "com.prism", "artifactId": "tool", "version": "0.9"}, reads: readsPOMs}
	sc = mustPlan(t, s, repo, ps.Auth(), typed, lone...)
	if len(sc.Items) != 1 || len(sc.Items[0].Problems) != 0 || sc.field("groupId").Problem != "" || sc.items0Target() != "com/prism/tool/0.9/tool-0.9.jar" {
		t.Fatalf("plan of a lone jar with coordinates: %+v", sc)
	}
	// A pom gives the coordinates: the plan says it read them.
	single := []uploadPart{{"api-3.0.pom", testPOM("com.prism", "api", "3.0")}, {"api-3.0.jar", tinyJar(t, "api-30")}}
	sc = mustPlan(t, s, repo, ps.Auth(), poms, single...)
	if g := sc.field("groupId"); !g.Read || g.Value != "com.prism" || len(sc.Items) != 1 || sc.Items[0].Title != "com.prism:api:3.0" {
		t.Fatalf("plan of a pom and its jar: %+v", sc)
	}

	// Plans stored nothing.
	if items, _ := treePage(t, s, repo, "", "", 50, s.AdminBasic()); len(items) != 0 {
		t.Fatalf("the repo after plans: %v, want empty", nodeNames(items))
	}

	// The chosen files, sent again, are stored with checksums and metadata.
	got := mustUpload(t, s, repo, ps.Auth(), nil, pick(layout, api.paths())...)
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
	confirmed := append(core.paths(), util.paths()...)
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

// items0Target is the target of the first file of the first item, or "".
func (p planJSON) items0Target() string {
	if len(p.Items) == 0 || len(p.Items[0].Files) == 0 {
		return ""
	}
	return p.Items[0].Files[0].Target
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
	s.CreateUser(t, w, "writer-pass-1234")
	s.SetPerms(t, deb, w, "repo.download", "repo.publish")
	s.SetPerms(t, raw, w, "repo.download", "repo.publish")

	// A folder of .debs, one of them broken. The open folder dists/trixie/ gives the distribution.
	ps := s.MintSession(t, w, deb)
	debs := []uploadPart{
		{"debs/native.deb", pkgs.native},
		{"debs/hello.deb", pkgs.hello1},
		{"debs/broken.deb", []byte("not an ar archive")},
		{"debs/notes.txt", []byte("x")},
	}
	sc := mustPlan(t, s, deb, ps.Auth(), planOpts{folder: "dists/trixie/", reads: readsDebs}, debs...)
	native := findTitle(t, sc, "e2e-native 1.0 ("+pkgs.arch+")")
	hello := findTitle(t, sc, "e2e-hello 1.0 (all)")
	broken := findTitle(t, sc, "debs/broken.deb")
	if len(sc.Items) != 3 || len(native.Problems)+len(hello.Problems) != 0 || len(broken.Problems) == 0 ||
		!sameSet(sc.Ignored, []string{"debs/notes.txt"}) || sc.field("distribution").Value != "trixie" || sc.field("component").Value != "main" ||
		!strings.HasPrefix(hello.target("debs/hello.deb"), "pool/main/") {
		t.Fatalf("plan of the .deb folder: %+v", sc)
	}
	// A bad distribution is a problem of its field.
	sc = mustPlan(t, s, deb, ps.Auth(), planOpts{fields: map[string]string{"distribution": "Bad Name"}, reads: readsDebs}, debs...)
	if sc.field("distribution").Problem == "" {
		t.Fatalf("plan with a bad distribution: %+v", sc)
	}
	got := mustUpload(t, s, deb, ps.Auth(), map[string]string{"distribution": "trixie"}, pick(debs, append(native.paths(), hello.paths()...))...)
	if len(got) != 2 || got[0].Status != "created" || got[1].Status != "created" {
		t.Fatalf("upload of the chosen .debs: %+v", got)
	}
	s.WaitDebianIndexed(t, deb)

	// A raw folder: every file, stored at its path under the target folder.
	rs := s.MintSession(t, w, raw)
	site := []uploadPart{{"site/index.html", []byte("<h1>hi</h1>")}, {"site/css/a.css", []byte("a{}")}}
	sc = mustPlan(t, s, raw, rs.Auth(), planOpts{fields: map[string]string{"folder": "web"}}, site...)
	if len(sc.Items) != 2 || len(sc.Ignored) != 0 || findTitle(t, sc, "site/css/a.css").target("site/css/a.css") != "web/site/css/a.css" {
		t.Fatalf("plan of the raw folder: %+v", sc)
	}
	got = mustUpload(t, s, raw, rs.Auth(), map[string]string{"folder": "web"}, site...)
	if len(got) != 2 || got[0].Path != "web/site/index.html" || got[1].Path != "web/site/css/a.css" {
		t.Fatalf("raw folder upload: %+v", got)
	}
	rawWantGet(t, s, nil, "/raw/"+raw+"/web/site/css/a.css", s.AdminBasic(), []byte("a{}"))
}
