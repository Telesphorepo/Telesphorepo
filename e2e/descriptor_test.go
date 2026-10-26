//go:build e2e

package e2e

import (
	"net/http"
	"strings"
	"testing"
)

// The UI descriptors of the formats (GET /api/formats), the CI file route
// (GET /api/repos/{name}/ci) and the delete plan (POST /api/repos/{name}/files/delete/plan).

type descriptorJSON struct {
	Name   string `json:"name"`
	Config struct {
		Fields []struct {
			Key     string   `json:"key"`
			Type    string   `json:"type"`
			Options  []string `json:"options"`
			ValueKey string   `json:"value_key"`
		} `json:"fields"`
	} `json:"config"`
	DeleteNotes []struct {
		One  string `json:"one"`
		Many string `json:"many"`
		Text string `json:"text"`
	} `json:"delete_notes"`
	RemotePresets []struct {
		Label string `json:"label"`
		URL   string `json:"url"`
	} `json:"remote_presets"`
	Labels struct {
		Tools map[string]string `json:"tools"`
	} `json:"labels"`
	Tools []struct {
		ID    string `json:"id"`
		Label string `json:"label"`
	} `json:"tools"`
}

// formatDescriptors reads GET /api/formats by format name.
func formatDescriptors(t *testing.T, s *Server) map[string]descriptorJSON {
	t.Helper()
	var list []descriptorJSON
	apiJSON(t, s, http.MethodGet, "/api/formats", nil, nil, http.StatusOK, &list)
	by := map[string]descriptorJSON{}
	for _, f := range list {
		by[f.Name] = f
	}
	return by
}

// wellFormed reports what is missing from the words and choices of descriptor f, or "": every
// remote preset has a label and an http(s) URL, every tool an ID and a label, every tool label
// words, the CI system github-actions a label, every config field a key and a type, every enum
// field options, and every rules field options and the key of its value.
func wellFormed(f descriptorJSON) string {
	for _, p := range f.RemotePresets {
		if p.Label == "" || !strings.HasPrefix(p.URL, "https://") && !strings.HasPrefix(p.URL, "http://") {
			return "a remote preset without a label or an http(s) URL"
		}
	}
	for _, tl := range f.Tools {
		if tl.ID == "" || tl.Label == "" {
			return "a tool without an ID or a label"
		}
	}
	for id, l := range f.Labels.Tools {
		if l == "" {
			return "no label for the snippet tool " + id
		}
	}
	if f.Labels.Tools["github-actions"] == "" {
		return "no label for the CI system github-actions"
	}
	for _, fd := range f.Config.Fields {
		if fd.Key == "" || fd.Type == "" || fd.Type == "enum" && len(fd.Options) == 0 {
			return "a config field without a key, a type or the options of an enum"
		}
		if fd.Type == "rules" && (len(fd.Options) == 0 || fd.ValueKey == "") {
			return "a rules field without the options or the key of its value"
		}
	}
	return ""
}

// TestCIWorkflowRoute: every format descriptor is well formed; the CI file of each system and
// tool, its branches and tags, the default tool, the refusals; the Docker files and usage
// snippets.
func TestCIWorkflowRoute(t *testing.T) {
	t.Parallel()
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
		!strings.Contains(gh.Code, "curl -fsSL "+s.InternalURL+"/install.sh | sh") || !strings.Contains(gh.Code, "telesphorepo publish") ||
		!strings.Contains(gh.Code, "id-token: write") || !strings.Contains(gh.Code, "actions/setup-java@") {
		t.Fatalf("GitHub Actions workflow for Maven: %+v", gh)
	}
	by := formatDescriptors(t, s)
	for name, f := range by {
		if why := wellFormed(f); why != "" {
			t.Fatalf("%s descriptor: %s: %+v", name, why, f)
		}
	}
	// Without tool, the file is the one of the first tool of the descriptor.
	tools := by["maven"].Tools
	if len(tools) == 0 {
		t.Fatal("the maven descriptor has no tools")
	}
	def := get(t, mvn, "system=github-actions", rauth)
	if first := get(t, mvn, "system=github-actions&tool="+tools[0].ID, rauth); def.Code != first.Code ||
		!strings.Contains(def.Code, `branches: ["main"]`) || strings.Contains(def.Code, "tags:") || !strings.Contains(def.Code, "telesphorepo publish") {
		t.Fatalf("default workflow (the first tool %s, push to main): %+v", tools[0].ID, def)
	}
	jk := get(t, mvn, "system=jenkins&tool=gradle&tags=v*", rauth)
	if jk.File != "Jenkinsfile" || !strings.Contains(jk.Code, "tag 'v*'") || !strings.Contains(jk.Code, "./gradlew build") ||
		!strings.Contains(jk.Code, "telesphorepo-id-token") || !strings.Contains(jk.Code, "isUnix()") ||
		!strings.Contains(jk.Code, s.InternalURL+"/install.ps1") || !strings.Contains(jk.Code, "telesphorepo publish") ||
		!strings.Contains(jk.Code, `powershell '.\\gradlew.bat build'`) {
		t.Fatalf("Jenkinsfile for Gradle: %+v", jk)
	}
	fs := get(t, mvn, "system=jenkins-freestyle&tool=maven", rauth)
	if fs.Tool != "jenkins" || !strings.Contains(fs.Code, "mvn -B verify") || !strings.Contains(fs.Code, "/install.sh | sh") ||
		!strings.Contains(fs.Code, "telesphorepo publish") || !strings.Contains(fs.Note, "OIDC_TOKEN") {
		t.Fatalf("Jenkins freestyle step for Maven: %+v", fs)
	}
	if plain := get(t, mvn, "system=jenkins", rauth); strings.Contains(plain.Code, "when {") {
		t.Fatalf("Jenkinsfile without a trigger has a when block: %s", plain.Code)
	}
	cli := get(t, raw, "system=github-actions", s.AdminBasic())
	if !strings.Contains(cli.Code, "/install.sh | sh") || !strings.Contains(cli.Code, "telesphorepo publish file.tar.gz") ||
		strings.Contains(cli.Code, "OIDC_TOKEN") {
		t.Fatalf("CLI workflow of a raw repo: %+v", cli)
	}
	// A publish session of the repo reads its CI file (telesphorepo setup).
	if st, b := s.API(t, http.MethodGet, "/api/repos/"+raw+"/ci?system=github-actions", nil, s.MintSession(t, s.AdminUser, raw).Auth()); st != http.StatusOK {
		t.Fatalf("ci with a publish session of the repo: status %d, want 200: %s", st, b)
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

	// Docker: build, install the CLI, name its credential helper for the registry host, push.
	dk := RandName("dk")
	s.CreateRepo(t, dk, "docker", false, nil)
	host := s.Alias + ":8080"
	for _, c := range []struct{ system, tag string }{{"github-actions", "$GITHUB_SHA"}, {"jenkins", "${GIT_COMMIT:-$BUILD_NUMBER}"}} {
		f := get(t, dk, "system="+c.system+"&tool=docker", s.AdminBasic())
		build := strings.Index(f.Code, `docker build -t "`+host+"/"+dk+"/app:"+c.tag+`" .`)
		install := strings.Index(f.Code, "/install.sh | sh")
		helper := strings.Index(f.Code, `{"credHelpers":{"`+host+`":"telesphorepo"}}`)
		push := strings.Index(f.Code, `docker push "`+host+"/"+dk+"/app:"+c.tag+`"`)
		if build < 0 || install < build || helper < install || push < helper || strings.Contains(f.Code, "docker login") ||
			f.Note == "" || !strings.Contains(f.Code, "TELESPHOREPO_SERVER") ||
			(c.system == "jenkins" && !strings.Contains(f.Code, `powershell 'docker build -t "`+host+"/"+dk+"/app:$(if ($env:GIT_COMMIT)")) {
			t.Fatalf("%s file for Docker: build, install, helper and push out of order or missing:\n%s\nnote: %s", c.system, f.Code, f.Note)
		}
	}
	var u struct {
		Snippets []snippetJSON `json:"snippets"`
	}
	s.MustAPI(t, http.MethodGet, "/api/repos/"+dk+"/usage", nil, s.AdminBasic(), &u)
	seen, notes := map[string]string{}, map[string]string{}
	for _, sn := range u.Snippets {
		seen[sn.ID] = sn.Code + sn.Note
		notes[sn.ID] = sn.Note
	}
	if !strings.Contains(seen["docker.login"], "docker login "+host) || !strings.Contains(seen["docker.pull"], "docker pull "+host+"/"+dk+"/IMAGE:TAG") ||
		!strings.Contains(seen["helm.pull"], "helm pull oci://"+host+"/"+dk+"/") ||
		!strings.Contains(seen["docker.helper"], "docker push "+host+"/"+dk+"/IMAGE:TAG") || notes["docker.helper"] == "" ||
		!strings.Contains(seen["docker.publish"], "docker login "+host+" -u publish --password-stdin") ||
		!strings.Contains(seen["docker.pull"], "insecure-registries") || !strings.Contains(seen["helm.push"], "helm push") {
		t.Fatalf("docker usage snippets: %v", seen)
	}
}

func TestDeletePlan(t *testing.T) {
	t.Parallel()
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
	// The note of the plan is a delete note of the maven descriptor, counted.
	described := false
	if len(folder.Notes) == 1 {
		for _, n := range formatDescriptors(t, s)["maven"].DeleteNotes {
			described = described || n.Text == folder.Notes[0].Text && n.One == folder.Notes[0].One && n.Many == folder.Notes[0].Many
		}
	}
	if folder.Files < 4 || folder.More || !described || folder.Notes[0].Count != 2 || folder.Notes[0].Text == "" {
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
