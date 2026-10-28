//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// redirectJSON is an entry of GET /api/repos/{name}/redirects.
type redirectJSON struct {
	Name       string  `json:"name"`
	CreatedAt  string  `json:"created_at"`
	LastUsedAt *string `json:"last_used_at"`
}

// renameRepo renames repo from to to as the admin and checks the answer.
func renameRepo(t *testing.T, s *Server, from, to string) {
	t.Helper()
	var out struct {
		Name string `json:"name"`
	}
	s.MustAPI(t, http.MethodPost, "/api/repos/"+from+"/rename", map[string]any{"name": to}, s.Admin(), &out)
	if out.Name != to {
		t.Fatalf("rename %s to %s: the answer names %q", from, to, out.Name)
	}
}

func redirectsOf(t *testing.T, s *Server, repo string) []redirectJSON {
	t.Helper()
	var out []redirectJSON
	s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/redirects", nil, s.Admin(), &out)
	return out
}

// TestRepoRedirects renames a Maven, an npm and a Docker repository and publishes through each
// old name with the real client (mvn deploy, npm publish, docker push), minting the publish token
// through the old name too: every file lands in the renamed repository, and Activity records the
// old name. A redirect's name is reserved, a repository takes its own old name back, and after
// Remove the redirect the old name answers 404 and a new repository can take it. A redirect added
// without a rename serves downloads at once, and the name of a repo or a redirect is refused.
func TestRepoRedirects(t *testing.T) {
	t.Parallel()
	OnLocalDisk(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		writer, pass := RandName("writer"), randHex(12)
		s.CreateUser(t, writer, pass)

		t.Run("maven", func(t *testing.T) {
			old, renamed := RandName("mvn-old"), RandName("mvn-new")
			s.CreateRepo(t, old, "maven", false, nil)
			s.SetPerms(t, old, writer, "repo.download", "repo.publish")
			renameRepo(t, s, old, renamed)

			group := "com.example.redirect" + randHex(4)
			groupPath := strings.ReplaceAll(group, ".", "/")
			oldURL := s.InternalURL + "/maven/" + old
			msg := "hello through the old name " + group
			files := append(mavenLibProject(group, "1.0.0", oldURL, msg),
				ContainerFile{Path: "/work/settings.xml", Content: mavenSettingsXML(writer, PublishToken(t, s, writer, old))})
			code, out := RunClient(t, mavenImage, ClientOpts{
				Cmd:     []string{"cd /work/lib && mvn -B -ntp -s /work/settings.xml deploy"},
				Files:   files,
				Mounts:  map[string]string{M2Volume: "/root/.m2/repository"},
				Timeout: 15 * time.Minute,
			})
			if code != 0 {
				t.Fatalf("mvn deploy to the old name: exit %d\n%s", code, out)
			}
			jar := groupPath + "/hello/1.0.0/hello-1.0.0.jar"
			if !slices.Contains(mavenAssetPaths(t, s, renamed, groupPath+"/", s.Admin()), jar) {
				t.Fatalf("the deploy through %s did not land in %s", old, renamed)
			}
			for _, name := range []string{old, renamed} {
				if resp := mavenHead(t, s, "/maven/"+name+"/"+jar, Basic{writer, pass}); resp.StatusCode != http.StatusOK {
					t.Fatalf("HEAD the jar through %s: status %d", name, resp.StatusCode)
				}
			}

			// Activity records the name that the deploy used.
			var through *auditEntry
			for _, e := range auditAll(t, s, "action=file.upload&repo="+renamed) {
				if e.Target == jar {
					through = &e
					break
				}
			}
			if through == nil || through.Repo != old || through.Detail["redirect_to"] != renamed {
				t.Fatalf("the upload event of the jar: %+v, want repo %s and redirect_to %s", through, old, renamed)
			}

			rds := redirectsOf(t, s, renamed)
			if len(rds) != 1 || rds[0].Name != old || rds[0].LastUsedAt == nil {
				t.Fatalf("redirects of %s: %+v, want %s with a last use", renamed, rds, old)
			}

			// The name of a redirect is reserved.
			st, b := s.API(t, http.MethodPost, "/api/repos", map[string]any{"name": old, "format": "raw"}, s.Admin())
			wantAPIStatus(t, "create a repository with the name of a redirect", st, b, http.StatusConflict)
			other := RandName("mvn-other")
			s.CreateRepo(t, other, "maven", false, nil)
			st, b = s.API(t, http.MethodPost, "/api/repos/"+other+"/rename", map[string]any{"name": old}, s.Admin())
			wantAPIStatus(t, "rename another repository to the name of a redirect", st, b, http.StatusConflict)
			st, b = s.API(t, http.MethodPost, "/api/repos/"+other+"/rename", map[string]any{"name": renamed}, s.Admin())
			wantAPIStatus(t, "rename to the name of a repository", st, b, http.StatusConflict)

			// Remove the redirect: the old name answers 404 and is free again.
			st, b = s.API(t, http.MethodDelete, "/api/repos/"+renamed+"/redirects/"+old, nil, s.Admin())
			wantAPIStatus(t, "remove the redirect", st, b, http.StatusNoContent)
			if resp := mavenHead(t, s, "/maven/"+old+"/"+jar, Basic{writer, pass}); resp.StatusCode != http.StatusNotFound {
				t.Fatalf("HEAD the jar through the removed redirect: status %d, want 404", resp.StatusCode)
			}
			if st, b := s.API(t, http.MethodGet, "/api/repos/"+old, nil, s.Admin()); st != http.StatusNotFound {
				t.Fatalf("GET the removed redirect: status %d, want 404: %s", st, b)
			}
			if rds := redirectsOf(t, s, renamed); len(rds) != 0 {
				t.Fatalf("redirects after the remove: %+v", rds)
			}
			s.CreateRepo(t, old, "maven", false, nil)
			if resp := mavenHead(t, s, "/maven/"+old+"/"+jar, s.AdminBasic()); resp.StatusCode != http.StatusNotFound {
				t.Fatalf("HEAD the jar in the new repository of the old name: status %d, want 404", resp.StatusCode)
			}

			// Add a redirect without a rename: the new name serves the repository at once.
			extra := RandName("mvn-extra")
			var added redirectJSON
			s.MustAPI(t, http.MethodPost, "/api/repos/"+renamed+"/redirects", map[string]any{"name": extra}, s.Admin(), &added)
			if added.Name != extra || added.CreatedAt == "" || added.LastUsedAt != nil {
				t.Fatalf("add the redirect %s: the answer is %+v", extra, added)
			}
			if resp := mavenHead(t, s, "/maven/"+extra+"/"+jar, Basic{writer, pass}); resp.StatusCode != http.StatusOK {
				t.Fatalf("HEAD the jar through the added redirect: status %d", resp.StatusCode)
			}
			if rds := redirectsOf(t, s, renamed); len(rds) != 1 || rds[0].Name != extra {
				t.Fatalf("redirects after the add: %+v, want %s", rds, extra)
			}
			if evs := auditAll(t, s, "action=repo.redirect.add&repo="+renamed); len(evs) != 1 || evs[0].Detail["redirect"] != extra {
				t.Fatalf("repo.redirect.add events of %s: %+v, want one with redirect %s", renamed, evs, extra)
			}
			st, b = s.API(t, http.MethodPost, "/api/repos/"+renamed+"/redirects", map[string]any{"name": extra}, s.Admin())
			wantAPIStatus(t, "add the same redirect again", st, b, http.StatusConflict)
			st, b = s.API(t, http.MethodPost, "/api/repos/"+other+"/redirects", map[string]any{"name": extra}, s.Admin())
			wantAPIStatus(t, "add a redirect of another repository", st, b, http.StatusConflict)
			st, b = s.API(t, http.MethodPost, "/api/repos/"+renamed+"/redirects", map[string]any{"name": old}, s.Admin())
			wantAPIStatus(t, "add a redirect with the name of a repository", st, b, http.StatusConflict)
			st, b = s.API(t, http.MethodPost, "/api/repos/"+renamed+"/redirects", map[string]any{"name": "Bad Name"}, s.Admin())
			wantAPIStatus(t, "add a redirect with a bad name", st, b, http.StatusBadRequest)
		})

		t.Run("npm", func(t *testing.T) {
			old, renamed := RandName("npm-old"), RandName("npm-new")
			s.CreateRepo(t, old, "npm", false, nil)
			s.SetPerms(t, old, writer, "repo.download", "repo.publish")
			renameRepo(t, s, old, renamed)

			pkg := RandName("lib")
			registry := s.InternalURL + "/npm/" + old + "/"
			files := append(npmPackage("/work/lib", pkg, "1.0.0", "lib 1.0.0"),
				ContainerFile{Path: "/root/.npmrc", Content: npmrc(registry, PublishToken(t, s, writer, old))})
			npmMust(t, nodeImage, "npm publish to the old name", "set -e\ncd /work/lib && npm publish\n", files)
			if _, d := npmGet(t, s, renamed, pkg, true, Basic{writer, pass}); d == nil || d.DistTags["latest"] != "1.0.0" {
				t.Fatalf("packument of %s in %s after the publish through %s: %+v", pkg, renamed, old, d)
			}

			// A rename back takes the old name again; the name it had becomes the redirect.
			renameRepo(t, s, renamed, old)
			if rds := redirectsOf(t, s, old); len(rds) != 1 || rds[0].Name != renamed {
				t.Fatalf("redirects after the rename back: %+v, want %s", rds, renamed)
			}
			if _, d := npmGet(t, s, renamed, pkg, true, Basic{writer, pass}); d == nil || d.DistTags["latest"] != "1.0.0" {
				t.Fatalf("packument of %s through %s after the rename back: %+v", pkg, renamed, d)
			}
		})

		t.Run("docker", func(t *testing.T) {
			old, renamed := RandName("dk-old"), RandName("dk-new")
			s.CreateRepo(t, old, "docker", false, nil)
			s.SetPerms(t, old, writer, "repo.download", "repo.publish")
			// An image from before the rename moves to the new name in the catalog.
			ociImage(t, s, Basic{writer, PublishToken(t, s, writer, old)}, old+"/before", "v1", nil, rawRandom(t, 4096))
			// A pull token from before the rename names the old name; it still pulls through both.
			st, pull, b := registryToken(t, s, Basic{writer, pass}, "repository:"+old+"/before:pull")
			wantAPIStatus(t, "pull token before the rename", st, b, http.StatusOK)
			renameRepo(t, s, old, renamed)
			for _, name := range []string{old, renamed} {
				resp, b := ociDo(t, s, http.MethodGet, "/v2/"+name+"/before/manifests/v1", nil, pull, nil)
				wantOCI(t, "pull "+name+"/before with the token of before the rename", resp, b, http.StatusOK, "")
			}

			host := registryHost(s)
			d := StartDind(t, host)
			img := host + "/" + old + "/app"
			d.MustExec(t, "docker push to the old name", "set -e\n"+DockerScratchImage("/ctx", 64<<10, 2048)+
				fmt.Sprintf("printf '%%s' '%s' | docker login %s -u %s --password-stdin\n", PublishToken(t, s, writer, old), host, writer)+
				fmt.Sprintf("docker build -q -t %[1]s:v1 /ctx\ndocker push %[1]s:v1\ndocker rmi %[1]s:v1\ndocker pull %[1]s:v1\n", img))
			if got := headDigest(t, s, s.AdminBasic(), renamed+"/app", "v1"); !strings.HasPrefix(got, "sha256:") {
				t.Fatalf("the push through %s: %s/app:v1 has the digest %q", old, renamed, got)
			}
			resp, b := ociDo(t, s, http.MethodGet, "/v2/_catalog", nil, s.AdminBasic(), nil)
			wantOCI(t, "GET /v2/_catalog", resp, b, http.StatusOK, "")
			var cat struct {
				Repositories []string `json:"repositories"`
			}
			if err := json.Unmarshal(b, &cat); err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{renamed + "/before", renamed + "/app"} {
				if !slices.Contains(cat.Repositories, want) {
					t.Fatalf("catalog %v: no %s", cat.Repositories, want)
				}
			}
			if slices.Contains(cat.Repositories, old+"/before") {
				t.Fatalf("catalog %v still names the image under the old name", cat.Repositories)
			}
		})
	})
}
