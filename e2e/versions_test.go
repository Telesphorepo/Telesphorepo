//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
)

// Versions, builds, artifact snippets and CI build records of Maven artifacts (SPEC.md "HTTP surface":
// GET /api/repos/{name}/versions, /builds, /snippets, /file and the build on /tree).

type buildLinkJSON struct {
	Rel   string `json:"rel"`
	Label string `json:"label"`
	URL   string `json:"url"`
}

// buildRecordJSON is a CI build record (the "build" of a file, a version or a build).
type buildRecordJSON struct {
	ID            int64           `json:"id"`
	Provider      string          `json:"provider"`
	ProviderTitle string          `json:"provider_title"`
	Repository    string          `json:"repository"`
	Revision      string          `json:"revision"`
	Ref           string          `json:"ref"`
	RefName       string          `json:"ref_name"`
	RefType       string          `json:"ref_type"`
	Run           string          `json:"run"`
	Workflow      string          `json:"workflow"`
	Actor         string          `json:"actor"`
	Links         []buildLinkJSON `json:"links"`
}

type versionJSON struct {
	Version     string           `json:"version"`
	Kind        string           `json:"kind"`
	Folder      string           `json:"folder"`
	LatestBuild string           `json:"latest_build"`
	LatestLabel string           `json:"latest_label"`
	BuildsCount int              `json:"builds_count"`
	PublishedAt string           `json:"published_at"`
	Build       *buildRecordJSON `json:"build"`
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
		Build string `json:"id"`
		Label string `json:"label"`
		Files []struct {
			Name       string `json:"name"`
			Path       string `json:"path"`
			Classifier string `json:"classifier"`
			Extension  string `json:"extension"`
		} `json:"files"`
		PublishedAt string           `json:"published_at"`
		CI          *buildRecordJSON `json:"build"`
	} `json:"items"`
	Next *string `json:"next"`
}

type snippetJSON struct {
	ID      string `json:"id"`
	Tool    string `json:"tool"`
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
		if s.Tool == client {
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
	t.Parallel()
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
		if snap.BuildsCount != 3 || snap.LatestBuild != "20261003.101010-3" || snap.LatestLabel != "Build 3" ||
			snap.PublishedAt == "" || snap.Build != nil {
			t.Fatalf("snapshot version = %+v", snap)
		}
		if v.Items[1].BuildsCount != 1 || v.Items[1].LatestBuild != "" || v.Items[1].LatestLabel != "" {
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
		var ids, labels []string
		for _, it := range b.Items {
			ids = append(ids, it.Build)
			labels = append(labels, it.Label)
		}
		if !slices.Equal(ids, []string{"20261003.101010-3", "20261002.101010-2", "20261001.101010-1"}) {
			t.Fatalf("builds = %v", ids)
		}
		if !slices.Equal(labels, []string{"Build 3", "Build 2", "Build 1"}) {
			t.Fatalf("build labels = %v", labels)
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
		if len(rel.Items) != 1 || rel.Items[0].Build != "1.1.0" || rel.Items[0].Label != "" || len(rel.Items[0].Files) != 3 {
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

	t.Run("build by id", func(t *testing.T) {
		snap := "/api/repos/" + repo + "/builds?path=com/acme/lib/&version=1.2.0-SNAPSHOT"
		// The oldest build, with after and limit, which the lookup does not use.
		var one buildsJSON
		s.MustAPI(t, http.MethodGet, snap+"&build=20261001.101010-1&limit=50&after=20261003.101010-3", nil, read, &one)
		if one.Artifact == nil || one.Version == nil || one.Version.LatestBuild != "20261003.101010-3" || one.Version.BuildsCount != 3 ||
			one.Version.Folder != "com/acme/lib/1.2.0-SNAPSHOT/" || one.Next != nil {
			t.Fatalf("by id: %+v", one)
		}
		if len(one.Items) != 1 || one.Items[0].Build != "20261001.101010-1" || one.Items[0].Label != "Build 1" {
			t.Fatalf("by id items = %+v", one.Items)
		}
		var files []string
		for _, f := range one.Items[0].Files {
			files = append(files, f.Path)
		}
		if !slices.Equal(files, []string{"com/acme/lib/1.2.0-SNAPSHOT/lib-1.2.0-20261001.101010-1.pom", "com/acme/lib/1.2.0-SNAPSHOT/lib-1.2.0-20261001.101010-1.jar"}) {
			t.Fatalf("files of build 1 = %v", files)
		}

		var gone buildsJSON
		s.MustAPI(t, http.MethodGet, snap+"&build=20261009.101010-9", nil, read, &gone)
		if gone.Artifact == nil || gone.Version == nil || len(gone.Items) != 0 || gone.Next != nil {
			t.Fatalf("a build that does not exist: %+v", gone)
		}

		var rel buildsJSON
		s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/builds?path=com/acme/lib/&version=1.1.0&build=1.1.0", nil, read, &rel)
		if len(rel.Items) != 1 || rel.Items[0].Build != "1.1.0" || rel.Version == nil || rel.Version.Folder != "com/acme/lib/1.1.0/" {
			t.Fatalf("release by id: %+v", rel)
		}
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
		if !strings.Contains(kts, `implementation("com.acme:lib:1.1.0")`) || !strings.Contains(kts, "repositories {") ||
			strings.Contains(kts, "dependencyResolutionManagement") ||
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
		// A snippet of the latest mode carries a note where the same snippet pinned has none.
		pinnedNote := map[string]string{}
		for _, sn := range pinned.Snippets {
			pinnedNote[sn.ID] = sn.Note
		}
		noted := false
		for _, sn := range latest.Snippets {
			if note, ok := pinnedNote[sn.ID]; ok && note == "" && sn.Note != "" {
				noted = true
			}
		}
		if !noted {
			t.Fatalf("latest mode: no snippet carries a note that the same pinned snippet lacks: pinned %+v, latest %+v", pinned.Snippets, latest.Snippets)
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
			BaseURL string `json:"base_url"`
			Tools   []struct {
				ID    string `json:"id"`
				Label string `json:"label"`
			} `json:"tools"`
			Snippets []snippetJSON `json:"snippets"`
		}
		s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/usage", nil, read, &u)
		seen := map[string]bool{}
		for _, sn := range u.Snippets {
			if sn.Context != "repo" || sn.Tool == "" || sn.ID == "" || sn.Code == "" {
				t.Fatalf("snippet %+v", sn)
			}
			seen[sn.Purpose+"/"+sn.Via+"/"+sn.Tool] = true
		}
		labels := map[string]string{}
		for _, tl := range u.Tools {
			labels[tl.ID] = tl.Label
		}
		for _, id := range []string{"github-actions", "gradle-kotlin", "telesphorepo"} {
			if labels[id] == "" {
				t.Fatalf("usage tools: no label for %s: %v", id, u.Tools)
			}
		}
		for _, want := range []string{"use//gradle-kotlin", "use//gradle-groovy", "use//maven", "publish/computer/telesphorepo",
			"publish/ci/github-actions", "publish/ci/jenkins"} {
			if !seen[want] {
				t.Fatalf("usage: no %s snippet; got %v", want, seen)
			}
		}
	})

	// A proxy of Maven Central and a group that holds it tell the build to use only them, with no
	// mavenCentral() line; a hosted repo keeps the line.
	t.Run("proxy snippets", func(t *testing.T) {
		proxy, group := RandName("central"), RandName("all")
		s.MustAPI(t, http.MethodPost, "/api/repos", map[string]any{"name": proxy, "format": "maven", "kind": "proxy",
			"anonymous_read": true, "force": true, "proxy": map[string]any{"remote_url": "https://repo1.maven.org/maven2/"}}, s.Admin(), nil)
		s.MustAPI(t, http.MethodPost, "/api/repos", map[string]any{"name": group, "format": "maven", "kind": "group",
			"anonymous_read": false, "members": []string{repo, proxy}}, s.Admin(), nil)
		for r, want := range map[string]bool{repo: true, proxy: false, group: false} {
			st, body := s.API(t, http.MethodGet, "/api/repos/"+r+"/usage", nil, s.Admin())
			if st != http.StatusOK {
				t.Fatalf("usage of %s: status %d: %s", r, st, body)
			}
			if got := strings.Contains(string(body), "mavenCentral()"); got != want {
				t.Fatalf("usage of %s names mavenCentral(): %v, want %v:\n%s", r, got, want, body)
			}
		}
	})
}

// TestDockerVersions checks the versions (tags), builds (the manifest with its config and layers,
// or the platforms of an index), artifact kinds, artifact snippets and manifest details of a
// docker repo.
func TestDockerVersions(t *testing.T) {
	t.Parallel()
	OnLocalDisk(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		repo := RandName("dkv")
		s.CreateRepo(t, repo, "docker", false, nil)
		reader := RandName("reader")
		s.CreateUser(t, reader, "reader-pass-1234")
		s.SetPerms(t, repo, reader, "repo.download")
		read := Basic{reader, "reader-pass-1234"}
		reg := registryClient{base: s.URL, auth: s.PublishAuth(t, s.AdminUser, repo).apply}

		v1 := tarImage(t, "amd64", rawRandom(t, 2048))
		reg.pushImage(t, repo+"/app", "1.0", v1)
		v2 := tarImage(t, "amd64", rawRandom(t, 2048), rawRandom(t, 1024))
		reg.pushImage(t, repo+"/app", "2.0", v2)

		var children []map[string]any
		for _, arch := range []string{"amd64", "arm64"} {
			img := tarImage(t, arch, rawRandom(t, 1024))
			reg.pushImage(t, repo+"/multi", "sha256:"+img.Hex, img)
			children = append(children, map[string]any{"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": "sha256:" + img.Hex,
				"size": len(img.Manifest), "platform": map[string]string{"os": "linux", "architecture": arch}})
		}
		idx, _ := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json", "manifests": children})
		reg.pushManifest(t, repo+"/multi", "latest", "application/vnd.oci.image.index.v1+json", idx)

		chartCfg := []byte(`{"name":"demo","version":"0.1.0","apiVersion":"v2"}`)
		chart := rawRandom(t, 3000)
		reg.pushBlob(t, repo+"/charts/demo", chartCfg)
		reg.pushBlob(t, repo+"/charts/demo", chart)
		cm, _ := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
			"config": map[string]any{"mediaType": "application/vnd.cncf.helm.config.v1+json", "digest": "sha256:" + sha256Hex(chartCfg), "size": len(chartCfg)},
			"layers": []map[string]any{{"mediaType": "application/vnd.cncf.helm.chart.content.v1.tar+gzip", "digest": "sha256:" + sha256Hex(chart), "size": len(chart)}}})
		reg.pushManifest(t, repo+"/charts/demo", "0.1.0", "application/vnd.oci.image.manifest.v1+json", cm)

		t.Run("versions", func(t *testing.T) {
			var v versionsJSON
			s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/versions?path=app", nil, read, &v)
			if v.Artifact == nil || v.Artifact.Kind != "image" || v.Artifact.Path != "app/" || len(v.Items) != 2 ||
				v.Items[0].Version != "2.0" || v.Items[1].Version != "1.0" || v.Items[0].Kind != "release" || v.Items[0].BuildsCount != 1 ||
				v.Items[0].LatestBuild == "" || !strings.HasPrefix(v2.Hex, v.Items[0].LatestBuild) {
				t.Fatalf("versions of app: %+v", v)
			}
			var page versionsJSON
			s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/versions?path=app&limit=1", nil, read, &page)
			if len(page.Items) != 1 || page.Next == nil || *page.Next != "2.0" {
				t.Fatalf("first page of the versions of app: %+v", page)
			}
			s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/versions?path=app&limit=1&after=2.0", nil, read, &page)
			if len(page.Items) != 1 || page.Items[0].Version != "1.0" {
				t.Fatalf("second page of the versions of app: %+v", page)
			}
			s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/versions?path=charts/demo", nil, read, &v)
			if v.Artifact == nil || v.Artifact.Kind != "helm-chart" || len(v.Items) != 1 || v.Items[0].Version != "0.1.0" {
				t.Fatalf("versions of the chart: %+v", v)
			}
			var none versionsJSON
			s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/versions?path=charts", nil, read, &none)
			if none.Artifact != nil {
				t.Fatalf("a folder that is no image has versions: %+v", none)
			}
		})

		t.Run("builds", func(t *testing.T) {
			var b buildsJSON
			s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/builds?path=app/&version=2.0", nil, read, &b)
			// The build ID is a prefix of the manifest digest, the same as the latest build of the version.
			if len(b.Items) != 1 || b.Version == nil || b.Items[0].Build == "" || !strings.HasPrefix(v2.Hex, b.Items[0].Build) ||
				b.Items[0].Build != b.Version.LatestBuild {
				t.Fatalf("builds of app 2.0: %+v", b)
			}
			var exts []string
			for _, f := range b.Items[0].Files {
				exts = append(exts, f.Extension)
			}
			if !slices.Equal(exts, []string{"manifest", "config", "layer", "layer"}) || b.Items[0].Files[0].Path != "app/_manifests/sha256/"+v2.Hex {
				t.Fatalf("files of app 2.0: %+v", b.Items[0].Files)
			}
			s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/builds?path=multi/&version=latest", nil, read, &b)
			var platforms []string
			for _, f := range b.Items[0].Files[1:] {
				platforms = append(platforms, f.Classifier)
			}
			if !slices.Equal(platforms, []string{"linux/amd64", "linux/arm64"}) {
				t.Fatalf("files of the index: %+v", b.Items[0].Files)
			}
		})

		t.Run("snippets and details", func(t *testing.T) {
			var a artifactSnippetsJSON
			s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/snippets?path=app", nil, read, &a)
			if a.Version != "2.0" || a.Mode != "pinned" || !strings.Contains(codeOf(a.Snippets, "docker"), "@sha256:"+v2.Hex) {
				t.Fatalf("pinned snippets of app: %+v", a)
			}
			s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/snippets?path=app&version_mode=latest", nil, read, &a)
			if !strings.Contains(codeOf(a.Snippets, "docker"), "/"+repo+"/app:2.0") {
				t.Fatalf("latest snippets of app: %+v", a)
			}
			s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/snippets?path="+url.QueryEscape("app/_tags/1.0"), nil, read, &a)
			if a.Version != "1.0" || !strings.Contains(codeOf(a.Snippets, "docker"), "@sha256:"+v1.Hex) {
				t.Fatalf("snippets of the tag 1.0: %+v", a)
			}
			s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/snippets?path=charts/demo", nil, read, &a)
			if !strings.Contains(codeOf(a.Snippets, "helm"), "helm pull oci://") || !strings.Contains(codeOf(a.Snippets, "helm"), "--version 0.1.0") {
				t.Fatalf("snippets of the chart: %+v", a)
			}

			var m struct {
				Digest    string   `json:"digest"`
				MediaType string   `json:"media_type"`
				Layers    []any    `json:"layers"`
				Platforms []any    `json:"platforms"`
				Tags      []string `json:"tags"`
				Config    *struct {
					Digest string `json:"digest"`
				} `json:"config"`
			}
			s.MustAPI(t, http.MethodGet, "/api/docker/"+repo+"/manifest?path="+url.QueryEscape("app/_tags/2.0"), nil, read, &m)
			if m.Digest != "sha256:"+v2.Hex || len(m.Layers) != 2 || len(m.Platforms) != 0 || !slices.Equal(m.Tags, []string{"2.0"}) || m.Config == nil {
				t.Fatalf("details of app 2.0: %+v", m)
			}
			s.MustAPI(t, http.MethodGet, "/api/docker/"+repo+"/manifest?path="+url.QueryEscape("multi/_tags/latest"), nil, read, &m)
			if len(m.Platforms) != 2 || m.Config != nil || !strings.Contains(m.MediaType, "index") {
				t.Fatalf("details of the index: %+v", m)
			}
			if st, b := s.API(t, http.MethodGet, "/api/docker/"+repo+"/manifest?path=app/_blobs/sha256/"+v1.Layers[0], nil, read); st != http.StatusBadRequest {
				t.Fatalf("details of a layer: status %d, want 400: %s", st, b)
			}
		})
	})
}
