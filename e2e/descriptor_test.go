//go:build e2e

package e2e

import (
	"net/http"
	"slices"
	"strings"
	"testing"
)

// The UI descriptors of the formats (GET /api/formats), the CI file route
// (GET /api/repos/{name}/ci) and the delete plan (POST /api/repos/{name}/files/delete/plan).

type descriptorJSON struct {
	Name   string `json:"name"`
	Kinds  []string
	Config struct {
		Fields []struct {
			Key        string `json:"key"`
			Type       string `json:"type"`
			ChangeNote string `json:"change_note"`
		} `json:"fields"`
	} `json:"config"`
	RepoTabs []string `json:"repo_tabs"`
	Upload   *struct {
		Single bool     `json:"single"`
		Folder bool     `json:"folder"`
		Keep   []string `json:"keep"`
		One    string   `json:"one"`
		Reads  []struct {
			Glob     string `json:"glob"`
			MaxBytes int64  `json:"max_bytes"`
		} `json:"reads"`
	} `json:"upload"`
	DeleteNotes []struct {
		Match string `json:"match"`
	} `json:"delete_notes"`
	FileDetails []struct {
		Match string `json:"match"`
		Part  string `json:"part"`
	} `json:"file_details"`
	Browse struct {
		Versions         bool `json:"versions"`
		ArtifactSnippets bool `json:"artifact_snippets"`
	} `json:"browse"`
	RemotePresets []struct {
		URL string `json:"url"`
	} `json:"remote_presets"`
	PublicKey string `json:"public_key"`
	Labels    struct {
		Tools map[string]string `json:"tools"`
	} `json:"labels"`
	ImportSources []string `json:"import_sources"`
	Tools         []struct {
		ID    string `json:"id"`
		Setup bool   `json:"setup"`
	} `json:"tools"`
	Setup bool `json:"setup"`
}

func TestFormatDescriptors(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	var list []descriptorJSON
	apiJSON(t, s, http.MethodGet, "/api/formats", nil, nil, http.StatusOK, &list)
	by := map[string]descriptorJSON{}
	for _, f := range list {
		by[f.Name] = f
	}
	mvn, deb, raw := by["maven"], by["debian"], by["raw"]
	if mvn.Upload == nil || !mvn.Upload.Folder || !mvn.Upload.Single || mvn.Upload.One != "artifact" || len(mvn.Upload.Reads) == 0 ||
		!mvn.Setup || len(mvn.Tools) != 2 || !mvn.Tools[0].Setup || !mvn.Browse.Versions || !mvn.Browse.ArtifactSnippets ||
		len(mvn.RemotePresets) == 0 || mvn.Labels.Tools["github-actions"] != "GitHub Actions" || mvn.Labels.Tools["gradle-kotlin"] == "" ||
		!slices.Contains(mvn.ImportSources, "maven2") || len(mvn.DeleteNotes) == 0 {
		t.Fatalf("maven descriptor: %+v", mvn)
	}
	if deb.Upload == nil || deb.Upload.Reads[0].Glob != "**/*.deb" || !slices.Contains(deb.RepoTabs, "signing") || deb.PublicKey != "key.asc" ||
		deb.Setup || len(deb.Tools) != 1 || deb.Tools[0].ID != "curl" || len(deb.FileDetails) != 1 || deb.FileDetails[0].Part != "deb-package" ||
		len(deb.Config.Fields) == 0 || deb.Config.Fields[0].Key != "architectures" || deb.Config.Fields[0].ChangeNote == "" {
		t.Fatalf("debian descriptor: %+v", deb)
	}
	if raw.Upload == nil || len(raw.Upload.Reads) != 0 || len(raw.Config.Fields) != 1 || raw.Config.Fields[0].Type != "enum" || raw.Browse.Versions {
		t.Fatalf("raw descriptor: %+v", raw)
	}
	dk := by["docker"]
	parts := map[string]string{}
	for _, d := range dk.FileDetails {
		parts[d.Match] = d.Part
	}
	notes := map[string]bool{}
	for _, n := range dk.DeleteNotes {
		notes[n.Match] = true
	}
	if !slices.Equal(dk.Kinds, []string{"hosted", "proxy", "group"}) || dk.Upload != nil || len(dk.Config.Fields) != 0 || len(dk.RepoTabs) != 0 ||
		len(dk.RemotePresets) != 3 || dk.RemotePresets[0].URL != "https://registry-1.docker.io/" ||
		!notes["**/_tags/*"] || !notes["**/_manifests/sha256/*"] || !notes["**/_blobs/sha256/*"] ||
		parts["**/_tags/*"] != "manifest" || parts["**/_manifests/sha256/*"] != "manifest" ||
		dk.Labels.Tools["docker"] != "Docker" || dk.Labels.Tools["helm"] != "Helm" || dk.Labels.Tools["oras"] != "ORAS" ||
		!slices.Contains(dk.ImportSources, "docker") || len(dk.Tools) != 1 || dk.Tools[0].ID != "docker" || dk.Tools[0].Setup ||
		!dk.Browse.Versions || !dk.Browse.ArtifactSnippets {
		t.Fatalf("docker descriptor: %+v", dk)
	}
}

func TestCIWorkflowRoute(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	mvn, raw, prx := RandName("mvn"), RandName("raw"), RandName("prx")
	s.CreateRepo(t, mvn, "maven", false, nil)
	s.CreateRepo(t, raw, "raw", false, nil)
	s.MustAPI(t, http.MethodPost, "/api/repos", map[string]any{"name": prx, "format": "maven", "kind": "proxy", "anonymous_read": true,
		"proxy": map[string]any{"remote_url": "http://127.0.0.1:9/"}, "force": true}, s.Admin(), nil)
	reader := RandName("reader")
	s.CreateUser(t, reader, "reader-pass-1234")
	s.SetPerms(t, mvn, reader, "repo.download")

	type snippet struct {
		ID, Tool, Purpose, Via, File, Title, Code, Note string
	}
	get := func(t *testing.T, repo, query string, auth Auth) snippet {
		t.Helper()
		var out snippet
		s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/ci?"+query, nil, auth, &out)
		return out
	}
	rauth := Basic{reader, "reader-pass-1234"}

	gh := get(t, mvn, "system=github-actions&tool=maven&branches=main,release/*&tags=v*", rauth)
	if gh.Tool != "github-actions" || gh.Purpose != "publish" || gh.Via != "ci" || gh.File != ".github/workflows/publish.yml" ||
		!strings.Contains(gh.Code, `branches: ["main", "release/*"]`) || !strings.Contains(gh.Code, `tags: ["v*"]`) ||
		!strings.Contains(gh.Code, "mvn -B deploy") || !strings.Contains(gh.Code, "actions/setup-java@v6") {
		t.Fatalf("GitHub Actions workflow for Maven: %+v", gh)
	}
	def := get(t, mvn, "system=github-actions", rauth)
	if !strings.Contains(def.Code, `branches: ["main"]`) || strings.Contains(def.Code, "tags:") || !strings.Contains(def.Code, "./gradlew publish") {
		t.Fatalf("default workflow (first tool, push to main): %+v", def)
	}
	jk := get(t, mvn, "system=jenkins&tool=gradle&tags=v*", rauth)
	if jk.File != "Jenkinsfile" || !strings.Contains(jk.Code, "tag 'v*'") || !strings.Contains(jk.Code, "./gradlew build") ||
		!strings.Contains(jk.Code, "telesphorepo-id-token") {
		t.Fatalf("Jenkinsfile for Gradle: %+v", jk)
	}
	if plain := get(t, mvn, "system=jenkins", rauth); strings.Contains(plain.Code, "when {") {
		t.Fatalf("Jenkinsfile without a trigger has a when block: %s", plain.Code)
	}
	curl := get(t, raw, "system=github-actions", s.AdminBasic())
	if !strings.Contains(curl.Code, "ACTIONS_ID_TOKEN_REQUEST_URL") || !strings.Contains(curl.Code, "/api/publish/sessions/oidc") ||
		!strings.Contains(curl.Code, "/raw/"+raw+"/") || !strings.Contains(curl.Code, "trap close_session EXIT") {
		t.Fatalf("curl workflow of a raw repo: %+v", curl)
	}

	for _, q := range []string{"system=travis", "system=github-actions&tool=ant", "system=github-actions&branches=a%20b", "system=jenkins&tags=it's"} {
		if st, b := s.API(t, http.MethodGet, "/api/repos/"+mvn+"/ci?"+q, nil, rauth); st != http.StatusBadRequest {
			t.Fatalf("ci?%s: status %d, want 400: %s", q, st, b)
		}
	}
	if st, b := s.API(t, http.MethodGet, "/api/repos/"+prx+"/ci?system=github-actions", nil, s.AdminBasic()); st != http.StatusBadRequest {
		t.Fatalf("ci of a proxy: status %d, want 400: %s", st, b)
	}
	if st, b := s.API(t, http.MethodGet, "/api/repos/"+raw+"/ci?system=github-actions", nil, rauth); st != http.StatusForbidden {
		t.Fatalf("ci of a repo without download: status %d, want 403: %s", st, b)
	}

	// Docker: build, then log in with the job's identity token right before the push.
	dk := RandName("dk")
	s.CreateRepo(t, dk, "docker", false, nil)
	host := s.Alias + ":8080"
	for _, c := range []struct{ system, tag string }{{"github-actions", "$GITHUB_SHA"}, {"jenkins", "${GIT_COMMIT:-$BUILD_NUMBER}"}} {
		f := get(t, dk, "system="+c.system+"&tool=docker", s.AdminBasic())
		build := strings.Index(f.Code, `docker build -t "`+host+"/"+dk+"/app:"+c.tag+`" .`)
		login := strings.Index(f.Code, `printf '%s' "$OIDC_TOKEN" | docker login `+host+" -u oidc --password-stdin")
		push := strings.Index(f.Code, `docker push "`+host+"/"+dk+"/app:"+c.tag+`"`)
		logout := strings.Index(f.Code, "docker logout "+host)
		if build < 0 || login < build || push < login || logout < push || !strings.Contains(f.Note, "Change app to the name of your image") {
			t.Fatalf("%s file for Docker: build, login, push and logout out of order or missing:\n%s\nnote: %s", c.system, f.Code, f.Note)
		}
	}
	var u struct {
		Snippets []snippetJSON `json:"snippets"`
	}
	s.MustAPI(t, http.MethodGet, "/api/repos/"+dk+"/usage", nil, s.AdminBasic(), &u)
	seen := map[string]string{}
	for _, sn := range u.Snippets {
		seen[sn.ID] = sn.Code + sn.Note
	}
	if !strings.Contains(seen["docker.login"], "docker login "+host) || !strings.Contains(seen["docker.pull"], "docker pull "+host+"/"+dk+"/IMAGE:TAG") ||
		!strings.Contains(seen["helm.pull"], "helm pull oci://"+host+"/"+dk+"/") ||
		!strings.Contains(seen["docker.helper"], "docker-credential-telesphorepo login "+s.InternalURL) ||
		!strings.Contains(seen["docker.publish"], "docker login "+host+" -u publish --password-stdin") ||
		!strings.Contains(seen["docker.pull"], "insecure-registries") || !strings.Contains(seen["helm.push"], "helm push") {
		t.Fatalf("docker usage snippets: %v", seen)
	}
}

func TestDeletePlan(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	repo := RandName("mvn")
	s.CreateRepo(t, repo, "maven", false, nil)
	ps := s.MintSession(t, s.AdminUser, repo)
	for _, v := range []string{"1.0", "2.0"} {
		mustUpload(t, s, repo, ps.Auth(), map[string]string{"groupId": "com.acme", "artifactId": "lib", "version": v},
			uploadPart{"lib.jar", tinyJar(t, "lib-"+v)})
	}
	type plan struct {
		Files int  `json:"files"`
		More  bool `json:"more"`
		Notes []struct {
			Text  string `json:"text"`
			Count int    `json:"count"`
			One   string `json:"one"`
			Many  string `json:"many"`
		} `json:"notes"`
	}
	var folder plan
	s.MustAPI(t, http.MethodPost, "/api/repos/"+repo+"/files/delete/plan", map[string]any{"prefixes": []string{"com/acme/lib/"}}, s.Admin(), &folder)
	if folder.Files < 4 || folder.More || len(folder.Notes) != 1 || folder.Notes[0].Count != 2 || folder.Notes[0].Many != "versions" ||
		!strings.Contains(folder.Notes[0].Text, "maven-metadata.xml") {
		t.Fatalf("delete plan of the artifact folder: %+v", folder)
	}
	var one plan
	s.MustAPI(t, http.MethodPost, "/api/repos/"+repo+"/files/delete/plan",
		map[string]any{"paths": []string{"com/acme/lib/1.0/lib-1.0.pom", "com/acme/lib/1.0/missing.jar"}}, s.Admin(), &one)
	if one.Files != 1 || len(one.Notes) != 1 || one.Notes[0].Count != 1 {
		t.Fatalf("delete plan of one pom and a missing file: %+v", one)
	}
	if st, b := s.API(t, http.MethodPost, "/api/repos/"+repo+"/files/delete/plan", map[string]any{"prefixes": []string{"../"}}, s.Admin()); st != http.StatusBadRequest {
		t.Fatalf("delete plan of a bad prefix: status %d, want 400: %s", st, b)
	}
	// The plan deleted nothing.
	if resp, _ := authGet(t, s, "/maven/"+repo+"/com/acme/lib/1.0/lib-1.0.jar", s.AdminBasic()); resp.StatusCode != http.StatusOK {
		t.Fatalf("GET after a delete plan: status %d, want 200", resp.StatusCode)
	}
}
