//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
)

// Versions, builds, artifact snippets and build provenance of Maven artifacts (spec "Browse":
// GET /api/repos/{name}/versions, /builds, /snippets, /file and the provenance on /tree).

type provenanceJSON struct {
	Repository    string `json:"repository"`
	RepositoryURL string `json:"repository_url"`
	SHA           string `json:"sha"`
	CommitURL     string `json:"commit_url"`
	Ref           string `json:"ref"`
	RefName       string `json:"ref_name"`
	RefType       string `json:"ref_type"`
	RunID         string `json:"run_id"`
	RunAttempt    string `json:"run_attempt"`
	RunURL        string `json:"run_url"`
	Workflow      string `json:"workflow"`
	WorkflowPath  string `json:"workflow_path"`
	WorkflowRef   string `json:"workflow_ref"`
	Actor         string `json:"actor"`
}

type versionJSON struct {
	Version     string          `json:"version"`
	Kind        string          `json:"kind"`
	LatestBuild string          `json:"latest_build"`
	BuildsCount int             `json:"builds_count"`
	PublishedAt string          `json:"published_at"`
	Provenance  *provenanceJSON `json:"provenance"`
}

type artifactJSON struct {
	Path  string `json:"path"`
	Title string `json:"title"`
	Kind  string `json:"kind"`
}

type versionsJSON struct {
	Artifact *artifactJSON `json:"artifact"`
	Items    []versionJSON `json:"items"`
	Next     *string       `json:"next"`
}

type buildsJSON struct {
	Artifact *artifactJSON `json:"artifact"`
	Version  *versionJSON  `json:"version"`
	Items    []struct {
		Build string `json:"build"`
		Files []struct {
			Name       string `json:"name"`
			Path       string `json:"path"`
			Classifier string `json:"classifier"`
			Extension  string `json:"extension"`
		} `json:"files"`
		PublishedAt string          `json:"published_at"`
		Provenance  *provenanceJSON `json:"provenance"`
	} `json:"items"`
	Next *string `json:"next"`
}

type snippetJSON struct {
	Client  string `json:"client"`
	Purpose string `json:"purpose"`
	Context string `json:"context"`
	Via     string `json:"via"`
	Title   string `json:"title"`
	Lang    string `json:"lang"`
	Code    string `json:"code"`
	Note    string `json:"note"`
}

type artifactSnippetsJSON struct {
	BaseURL  string        `json:"base_url"`
	Artifact *artifactJSON `json:"artifact"`
	Version  string        `json:"version"`
	Mode     string        `json:"version_mode"`
	Snippets []snippetJSON `json:"snippets"`
}

// codeOf joins the code of the snippets of client.
func codeOf(snips []snippetJSON, client string) string {
	var b strings.Builder
	for _, s := range snips {
		if s.Client == client {
			b.WriteString(s.Code)
		}
	}
	return b.String()
}

func mavenPOM(group, id, version, packaging, extra string) string {
	return fmt.Sprintf(`<project><modelVersion>4.0.0</modelVersion><groupId>%s</groupId><artifactId>%s</artifactId>`+
		`<version>%s</version><packaging>%s</packaging>%s</project>`, group, id, version, packaging, extra)
}

func TestVersionsAndSnippets(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	repo := RandName("vers")
	s.CreateRepo(t, repo, "maven", false, nil)
	pub := s.PublishAuth(t, s.AdminUser, repo)
	put := func(p, body string) {
		t.Helper()
		resp, b := authPut(t, s, "/maven/"+repo+"/"+p, body, pub)
		if resp.StatusCode/100 != 2 {
			t.Fatalf("PUT %s: status %d: %s", p, resp.StatusCode, b)
		}
	}
	lib := "com/acme/lib/"
	for _, v := range []string{"1.0.0", "1.1.0"} {
		put(lib+v+"/lib-"+v+".pom", mavenPOM("com.acme", "lib", v, "jar", ""))
		put(lib+v+"/lib-"+v+".jar", "jar "+v)
		put(lib+v+"/lib-"+v+".jar.sha1", "0000")
	}
	put(lib+"1.1.0/lib-1.1.0-sources.jar", "sources")
	for i, day := range []string{"20261001", "20261002", "20261003"} {
		base := fmt.Sprintf("%s1.2.0-SNAPSHOT/lib-1.2.0-%s.101010-%d", lib, day, i+1)
		put(base+".pom", mavenPOM("com.acme", "lib", "1.2.0-SNAPSHOT", "jar", ""))
		put(base+".jar", "snapshot "+day)
		put(base+".jar.md5", "0000")
	}
	put(lib+"1.2.0-SNAPSHOT/lib-1.2.0-20261003.101010-3-sources.jar", "sources")
	put(lib+"1.2.0-SNAPSHOT/maven-metadata.xml", "<metadata/>")
	put(lib+"maven-metadata.xml", "<metadata/>")
	put("com/acme/bom/2.0/bom-2.0.pom", mavenPOM("com.acme", "bom", "2.0", "pom",
		"<dependencyManagement><dependencies/></dependencyManagement>"))
	put("com/acme/hello/com.acme.hello.gradle.plugin/1.0/com.acme.hello.gradle.plugin-1.0.pom",
		mavenPOM("com.acme.hello", "com.acme.hello.gradle.plugin", "1.0", "pom", ""))
	read := s.AdminBasic()

	t.Run("versions", func(t *testing.T) {
		var v versionsJSON
		s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/versions?path=com/acme/lib", nil, read, &v)
		if v.Artifact == nil || v.Artifact.Title != "com.acme:lib" || v.Artifact.Path != lib || v.Artifact.Kind != "library" {
			t.Fatalf("artifact = %+v", v.Artifact)
		}
		var got []string
		for _, it := range v.Items {
			got = append(got, it.Version+"/"+it.Kind)
		}
		if !slices.Equal(got, []string{"1.2.0-SNAPSHOT/snapshot", "1.1.0/release", "1.0.0/release"}) {
			t.Fatalf("versions = %v", got)
		}
		snap := v.Items[0]
		if snap.BuildsCount != 3 || snap.LatestBuild != "20261003.101010-3" ||
			snap.PublishedAt == "" || snap.Provenance != nil {
			t.Fatalf("snapshot version = %+v", snap)
		}
		if v.Items[1].BuildsCount != 1 || v.Items[1].LatestBuild != "" {
			t.Fatalf("release version = %+v", v.Items[1])
		}

		var page versionsJSON
		s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/versions?path=com/acme/lib&limit=1", nil, read, &page)
		if len(page.Items) != 1 || page.Next == nil || *page.Next != "1.2.0-SNAPSHOT" {
			t.Fatalf("first page = %+v", page)
		}
		s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/versions?path=com/acme/lib&limit=1&after="+url.QueryEscape(*page.Next), nil, read, &page)
		if len(page.Items) != 1 || page.Items[0].Version != "1.1.0" {
			t.Fatalf("second page = %+v", page)
		}

		var none versionsJSON
		s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/versions?path=com/acme", nil, read, &none)
		if none.Artifact != nil || len(none.Items) != 0 {
			t.Fatalf("a group folder is no artifact: %+v", none)
		}
	})

	t.Run("builds", func(t *testing.T) {
		var b buildsJSON
		s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/builds?path=com/acme/lib/&version=1.2.0-SNAPSHOT", nil, read, &b)
		if b.Artifact == nil || b.Version == nil || b.Version.BuildsCount != 3 {
			t.Fatalf("builds: %+v", b)
		}
		var ids []string
		for _, it := range b.Items {
			ids = append(ids, it.Build)
		}
		if !slices.Equal(ids, []string{"20261003.101010-3", "20261002.101010-2", "20261001.101010-1"}) {
			t.Fatalf("builds = %v", ids)
		}
		var files []string
		for _, f := range b.Items[0].Files {
			files = append(files, f.Classifier+"."+f.Extension)
		}
		// The pom, the main file, then the classified ones; no checksum files.
		if !slices.Equal(files, []string{".pom", ".jar", "sources.jar"}) {
			t.Fatalf("files of the newest build = %v", files)
		}

		var page buildsJSON
		s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/builds?path=com/acme/lib/&version=1.2.0-SNAPSHOT&limit=2", nil, read, &page)
		if len(page.Items) != 2 || page.Next == nil || *page.Next != "20261002.101010-2" {
			t.Fatalf("first page = %+v", page)
		}
		s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/builds?path=com/acme/lib/&version=1.2.0-SNAPSHOT&limit=2&after="+*page.Next, nil, read, &page)
		if len(page.Items) != 1 || page.Items[0].Build != "20261001.101010-1" || page.Next != nil {
			t.Fatalf("second page = %+v", page)
		}

		var rel buildsJSON
		s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/builds?path=com/acme/lib/&version=1.1.0", nil, read, &rel)
		if len(rel.Items) != 1 || rel.Items[0].Build != "1.1.0" || len(rel.Items[0].Files) != 3 {
			t.Fatalf("release builds = %+v", rel)
		}

		var missing buildsJSON
		s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/builds?path=com/acme/lib/&version=9.9.9", nil, read, &missing)
		if missing.Artifact != nil || missing.Version != nil || len(missing.Items) != 0 {
			t.Fatalf("a version that does not exist: %+v", missing)
		}
		st, body := s.API(t, http.MethodGet, "/api/repos/"+repo+"/builds?path=com/acme/lib/", nil, read)
		wantAPIStatus(t, "builds without a version", st, body, http.StatusBadRequest)
	})

	snippets := func(t *testing.T, path, mode string) artifactSnippetsJSON {
		t.Helper()
		var out artifactSnippetsJSON
		q := "/api/repos/" + repo + "/snippets?path=" + url.QueryEscape(path)
		if mode != "" {
			q += "&version_mode=" + mode
		}
		s.MustAPI(t, http.MethodGet, q, nil, read, &out)
		return out
	}

	t.Run("artifact snippets", func(t *testing.T) {
		pinned := snippets(t, "com/acme/lib", "")
		if pinned.Artifact == nil || pinned.Version != "1.1.0" || pinned.Mode != "pinned" {
			t.Fatalf("pinned: %+v", pinned)
		}
		for _, sn := range pinned.Snippets {
			if sn.Purpose != "use" || sn.Context != "artifact" || sn.Code == "" || sn.Title == "" {
				t.Fatalf("snippet %+v", sn)
			}
		}
		kts := codeOf(pinned.Snippets, "gradle-kotlin")
		if !strings.Contains(kts, `implementation("com.acme:lib:1.1.0")`) || !strings.Contains(kts, "dependencyResolutionManagement") ||
			!strings.Contains(kts, "/maven/"+repo) || !strings.Contains(kts, "credentials(PasswordCredentials::class)") ||
			!strings.Contains(kts, "Username=YOUR_USERNAME") {
			t.Fatalf("gradle-kotlin snippets of a private repo:\n%s", kts)
		}
		if g := codeOf(pinned.Snippets, "gradle-groovy"); !strings.Contains(g, "implementation 'com.acme:lib:1.1.0'") {
			t.Fatalf("gradle-groovy snippets:\n%s", g)
		}
		if m := codeOf(pinned.Snippets, "maven"); !strings.Contains(m, "<version>1.1.0</version>") || !strings.Contains(m, "<id>"+repo+"</id>") ||
			!strings.Contains(m, "<servers>") {
			t.Fatalf("maven snippets:\n%s", m)
		}

		latest := snippets(t, "com/acme/lib", "latest")
		if latest.Mode != "latest" || !strings.Contains(codeOf(latest.Snippets, "gradle-kotlin"), `"com.acme:lib:latest.release"`) ||
			!strings.Contains(codeOf(latest.Snippets, "maven"), "<version>RELEASE</version>") {
			t.Fatalf("latest: %+v", latest)
		}
		noted := false
		for _, sn := range latest.Snippets {
			noted = noted || strings.Contains(sn.Note, "reproducible")
		}
		if !noted {
			t.Fatalf("latest mode: no snippet notes that builds stop being reproducible")
		}

		var named artifactSnippetsJSON
		s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/snippets?path=com/acme/lib&version=1.0.0", nil, read, &named)
		if named.Version != "1.0.0" || !strings.Contains(codeOf(named.Snippets, "maven"), "<version>1.0.0</version>") {
			t.Fatalf("artifact folder with a version: %+v", named)
		}
		if v := snippets(t, "com/acme/lib/1.0.0", "").Version; v != "1.0.0" {
			t.Fatalf("version folder: version %q, want 1.0.0", v)
		}
		if v := snippets(t, "com/acme/lib/1.0.0/lib-1.0.0.jar", "").Version; v != "1.0.0" {
			t.Fatalf("a file: version %q, want 1.0.0", v)
		}
		snap := snippets(t, "com/acme/lib/1.2.0-SNAPSHOT", "latest")
		if snap.Version != "1.2.0-SNAPSHOT" || !strings.Contains(codeOf(snap.Snippets, "gradle-kotlin"), "latest.integration") {
			t.Fatalf("snapshot latest: %+v", snap)
		}

		bom := snippets(t, "com/acme/bom", "")
		if bom.Artifact == nil || bom.Artifact.Kind != "bom" || !strings.Contains(codeOf(bom.Snippets, "gradle-kotlin"), `platform("com.acme:bom:2.0")`) ||
			!strings.Contains(codeOf(bom.Snippets, "maven"), "<scope>import</scope>") {
			t.Fatalf("bom: %+v", bom)
		}

		plugin := snippets(t, "com/acme/hello/com.acme.hello.gradle.plugin", "")
		pk := codeOf(plugin.Snippets, "gradle-kotlin")
		if plugin.Artifact == nil || plugin.Artifact.Kind != "gradle-plugin" || plugin.Artifact.Title != "com.acme.hello" ||
			!strings.Contains(pk, `id("com.acme.hello") version "1.0"`) || !strings.Contains(pk, "pluginManagement") ||
			strings.Contains(pk, "implementation") || codeOf(plugin.Snippets, "maven") != "" {
			t.Fatalf("gradle plugin: %+v", plugin)
		}

		none := snippets(t, "com/acme", "")
		if none.Artifact != nil || len(none.Snippets) != 0 {
			t.Fatalf("not an artifact: %+v", none)
		}
		st, b := s.API(t, http.MethodGet, "/api/repos/"+repo+"/snippets?path=com/acme/lib&version_mode=newest", nil, read)
		wantAPIStatus(t, "an unknown version_mode", st, b, http.StatusBadRequest)
	})

	t.Run("repo snippets", func(t *testing.T) {
		var u struct {
			BaseURL  string        `json:"base_url"`
			Snippets []snippetJSON `json:"snippets"`
		}
		s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/usage", nil, read, &u)
		seen := map[string]bool{}
		for _, sn := range u.Snippets {
			if sn.Context != "repo" || sn.Client == "" || sn.Code == "" {
				t.Fatalf("snippet %+v", sn)
			}
			seen[sn.Purpose+"/"+sn.Via+"/"+sn.Client] = true
		}
		for _, want := range []string{"use//gradle-kotlin", "use//gradle-groovy", "use//maven", "publish/computer/gradle-kotlin",
			"publish/computer/maven", "publish/ci/github-actions", "publish/ci/jenkins", "publish/hand/curl"} {
			if !seen[want] {
				t.Fatalf("usage: no %s snippet; got %v", want, seen)
			}
		}
	})
}

// TestBuildProvenance publishes from a CI token with GitHub Actions claims (Keycloak plays
// GitHub, see keycloak_test.go) and from a device, and checks the provenance on the tree, the
// file details and the versions.
func TestBuildProvenance(t *testing.T) {
	kc := newKeycloak(t)
	s := StartServer(t, ServerOpts{})
	kc.CIAudience = s.InternalURL
	kc.Start(t, s.URL+oidcCallbackPath)

	repo := RandName("prov")
	s.CreateRepo(t, repo, "maven", true, nil)
	createTrust(t, s, trustPolicy{Name: "acme-app", Issuer: kc.Issuer, Claims: map[string][]string{"repository": {kcCIRepository}},
		Repos: []string{repo}})
	ci := mustSession(t, "exchange a CI token")(s.OIDCSession(t, kc.CIToken(t), repo))
	put := func(p, body string, auth Auth) {
		t.Helper()
		resp, b := authPut(t, s, "/maven/"+repo+"/"+p, body, auth)
		if resp.StatusCode/100 != 2 {
			t.Fatalf("PUT %s: status %d: %s", p, resp.StatusCode, b)
		}
	}
	dir := "com/acme/app/"
	put(dir+"1.0.0/app-1.0.0.pom", mavenPOM("com.acme", "app", "1.0.0", "jar", ""), ci.Auth())
	put(dir+"1.0.0/app-1.0.0.jar", "jar", ci.Auth())
	if st := s.CloseSession(t, ci); st/100 != 2 {
		t.Fatalf("close the CI session: status %d", st)
	}
	device := s.PublishAuth(t, s.AdminUser, repo)
	put(dir+"0.9.0/app-0.9.0.pom", mavenPOM("com.acme", "app", "0.9.0", "jar", ""), device)

	origin := fmt.Sprintf("http://%s:%d", kc.Alias, kc.Port)
	want := provenanceJSON{
		Repository: kcCIRepository, RepositoryURL: origin + "/" + kcCIRepository, SHA: kcCISHA,
		CommitURL: origin + "/" + kcCIRepository + "/commit/" + kcCISHA, Ref: kcCIRef, RefName: "v1.0.0", RefType: "tag",
		RunID: kcCIRunID, RunAttempt: kcCIRunAttempt,
		RunURL:   origin + "/" + kcCIRepository + "/actions/runs/" + kcCIRunID + "/attempts/" + kcCIRunAttempt,
		Workflow: kcCIWorkflow, WorkflowPath: ".github/workflows/release.yml", WorkflowRef: kcCIWorkflowRef, Actor: kcCIActor,
	}
	read := Auth(nil)

	t.Run("tree", func(t *testing.T) {
		var tree struct {
			Items []struct {
				Name       string          `json:"name"`
				UploadedBy string          `json:"uploaded_by"`
				Provenance *provenanceJSON `json:"provenance"`
			} `json:"items"`
		}
		s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/tree?path="+dir+"1.0.0", nil, read, &tree)
		if len(tree.Items) != 2 {
			t.Fatalf("tree: %+v", tree)
		}
		for _, it := range tree.Items {
			if it.Provenance == nil || *it.Provenance != want || it.UploadedBy != "" {
				t.Fatalf("%s: provenance %+v uploaded_by %q, want %+v and no user", it.Name, it.Provenance, it.UploadedBy, want)
			}
		}
		// Decode into a fresh value: json keeps a field that the reply omits, so a reused
		// item would keep the provenance of the first reply.
		tree.Items = nil
		s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/tree?path="+dir+"0.9.0", nil, read, &tree)
		if len(tree.Items) != 1 || tree.Items[0].Provenance != nil || tree.Items[0].UploadedBy != s.AdminUser {
			t.Fatalf("a device upload records no provenance: %+v", tree)
		}
	})

	t.Run("file details", func(t *testing.T) {
		var f struct {
			Path       string          `json:"path"`
			Size       int64           `json:"size"`
			Kind       string          `json:"kind"`
			Provenance *provenanceJSON `json:"provenance"`
		}
		s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/file?path="+dir+"1.0.0/app-1.0.0.jar", nil, read, &f)
		if f.Path != dir+"1.0.0/app-1.0.0.jar" || f.Size != 3 || f.Kind != "artifact" || f.Provenance == nil || *f.Provenance != want {
			t.Fatalf("file details: %+v", f)
		}
		s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/file?path="+dir+"0.9.0/app-0.9.0.pom", nil, read, &f)
		if f.Provenance != nil {
			t.Fatalf("device upload: provenance %+v", f.Provenance)
		}
		st, b := s.API(t, http.MethodGet, "/api/repos/"+repo+"/file?path="+dir+"nope.jar", nil, read)
		wantAPIStatus(t, "details of a missing file", st, b, http.StatusNotFound)
	})

	t.Run("versions", func(t *testing.T) {
		var v versionsJSON
		s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/versions?path="+dir, nil, read, &v)
		if len(v.Items) != 2 || v.Items[0].Version != "1.0.0" || v.Items[0].Provenance == nil || *v.Items[0].Provenance != want ||
			v.Items[1].Provenance != nil {
			t.Fatalf("versions: %+v", v)
		}
		var b buildsJSON
		s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/builds?path="+dir+"&version=1.0.0", nil, read, &b)
		if len(b.Items) != 1 || b.Items[0].Provenance == nil || *b.Items[0].Provenance != want {
			t.Fatalf("builds: %+v", b)
		}
	})
}
