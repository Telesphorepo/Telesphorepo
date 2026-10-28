//go:build e2e

package e2e

import (
	"archive/zip"
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

// Browse, delete and upload repo files through the JSON API.

// Bearer sends a token (a publish session token or a read token) as "Authorization: Bearer".
type Bearer struct{ Token string }

func (b Bearer) apply(r *http.Request) { r.Header.Set("Authorization", "Bearer "+b.Token) }

type treeNode struct {
	Name       string     `json:"name"`
	Type       string     `json:"type"`
	Path       string     `json:"path"`
	Size       *int64     `json:"size"`
	SHA256     string     `json:"sha256"`
	UpdatedAt  *time.Time `json:"updated_at"`
	UploadedBy string     `json:"uploaded_by"`
	Repo       string     `json:"repo"` // a group: the member that serves the file
}

// treePage fetches one page of GET /api/repos/{repo}/tree.
func treePage(t *testing.T, s *Server, repo, dir, after string, limit int, auth Auth) ([]treeNode, *string) {
	t.Helper()
	q := url.Values{"path": {dir}, "limit": {fmt.Sprint(limit)}}
	if after != "" {
		q.Set("after", after)
	}
	var page struct {
		Items []treeNode `json:"items"`
		Next  *string    `json:"next"`
	}
	s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/tree?"+q.Encode(), nil, auth, &page)
	return page.Items, page.Next
}

// treeAll follows the pages of a folder and returns every child.
func treeAll(t *testing.T, s *Server, repo, dir string, limit int, auth Auth) []treeNode {
	t.Helper()
	var all []treeNode
	after := ""
	for i := 0; i < 100; i++ {
		items, next := treePage(t, s, repo, dir, after, limit, auth)
		if len(items) > limit {
			t.Fatalf("tree %s: page of %d items, limit %d", dir, len(items), limit)
		}
		all = append(all, items...)
		if next == nil {
			return all
		}
		after = *next
	}
	t.Fatalf("tree %s: more than 100 pages", dir)
	return nil
}

func nodeNames(nodes []treeNode) []string {
	out := make([]string, len(nodes))
	for i, n := range nodes {
		out[i] = n.Type + ":" + n.Name
	}
	return out
}

// deleteFiles sends POST /api/repos/{repo}/files/delete with one item: a file (key "path") or a
// folder (key "prefix").
func deleteFiles(t *testing.T, s *Server, repo, key, value string, auth Auth) (int, int) {
	t.Helper()
	body := map[string][]string{"paths": {value}}
	if key == "prefix" {
		body = map[string][]string{"prefixes": {value}}
	}
	st, b := s.API(t, http.MethodPost, "/api/repos/"+repo+"/files/delete", body, auth)
	var out struct {
		Deleted int `json:"deleted"`
	}
	if st == http.StatusOK {
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("delete %s=%s: decode %s: %v", key, value, b, err)
		}
	}
	return st, out.Deleted
}

func mustDelete(t *testing.T, s *Server, repo, key, value string, auth Auth, want int) {
	t.Helper()
	st, n := deleteFiles(t, s, repo, key, value, auth)
	if st != http.StatusOK || n != want {
		t.Fatalf("delete %s=%s in %s: status %d, deleted %d, want 200 and %d", key, value, repo, st, n, want)
	}
}

type uploadPart struct {
	Name string
	Body []byte
}

type uploaded struct {
	Path   string `json:"path"`
	Status string `json:"status"`
	SHA256 string `json:"sha256"`
}

// manualUpload sends POST /api/repos/{repo}/upload as multipart/form-data.
func manualUpload(t *testing.T, s *Server, repo string, auth Auth, fields map[string]string, parts ...uploadPart) (int, []byte) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range parts {
		w, err := mw.CreateFormFile("file", p.Name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(p.Body)
	}
	mw.Close()
	req := s.NewRequest(t, http.MethodPost, "/api/repos/"+repo+"/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, b := s.Do(t, req, auth)
	return resp.StatusCode, b
}

func mustUpload(t *testing.T, s *Server, repo string, auth Auth, fields map[string]string, parts ...uploadPart) []uploaded {
	t.Helper()
	st, b := manualUpload(t, s, repo, auth, fields, parts...)
	if st != http.StatusCreated {
		t.Fatalf("upload to %s: status %d, want 201: %s", repo, st, b)
	}
	var out struct {
		Files []uploaded `json:"files"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("upload answer %s: %v", b, err)
	}
	return out.Files
}

// storedKeys lists the blob keys of the server's store: the S3 target's objects, or the fs
// store on the data volume.
func storedKeys(t *testing.T, opts ServerOpts) []string {
	t.Helper()
	var keys []string
	if opts.S3 != nil {
		for _, k := range opts.S3.ListKeys(t) {
			keys = append(keys, strings.TrimPrefix(k, opts.S3.Prefix+"/"))
		}
		return keys
	}
	for k := range FSBlobs(t, opts.DataVolume) {
		keys = append(keys, k)
	}
	return keys
}

func TestTree(t *testing.T) {
	t.Parallel()
	s := StartServer(t, ServerOpts{})
	repo := RandName("tree")
	s.CreateRepo(t, repo, "maven", false, nil)
	w, reader := RandName("writer"), RandName("reader")
	s.CreateUser(t, w, "writer-pass-1234")
	s.CreateUser(t, reader, "reader-pass-1234")
	s.SetPerms(t, repo, w, "repo.download", "repo.publish")
	s.SetPerms(t, repo, reader, "repo.download")
	rauth := Basic{reader, "reader-pass-1234"}

	files := []string{
		"com/acme/lib/1.0/lib-1.0.jar", "com/acme/lib/1.0/lib-1.0.pom", "com/acme/lib/2.0/lib-2.0.jar",
		"com/acme/other/1.0/x.txt", "com/acme-b/file.txt", "top.txt", "z/deep/er/file.txt",
	}
	for i := 1; i <= 12; i++ {
		files = append(files, fmt.Sprintf("com/acme/many/v%02d/a.txt", i), fmt.Sprintf("com/acme/many/v%02d/b.txt", i))
	}
	ps := s.MintSession(t, w, repo)
	for _, f := range files {
		resp, body := authPut(t, s, "/maven/"+repo+"/"+f, "content of "+f, ps.Auth())
		wantStatus(t, resp, body, http.StatusCreated)
	}

	if got, want := nodeNames(treeAll(t, s, repo, "", 50, rauth)), []string{"dir:com", "file:top.txt", "dir:z"}; !slices.Equal(got, want) {
		t.Fatalf("root: %v, want %v", got, want)
	}
	// "acme-b/" sorts before "acme/" ('-' < '/'): the seek past a folder must not skip it.
	if got, want := nodeNames(treeAll(t, s, repo, "com", 50, rauth)), []string{"dir:acme-b", "dir:acme"}; !slices.Equal(got, want) {
		t.Fatalf("com: %v, want %v", got, want)
	}

	// Pages of folders: the cursor of a folder ends in "/".
	items, next := treePage(t, s, repo, "com/acme/", "", 2, rauth)
	if got := nodeNames(items); !slices.Equal(got, []string{"dir:lib", "dir:many"}) || next == nil || *next != "many/" {
		t.Fatalf("com/acme page 1: %v next %v, want [dir:lib dir:many] next many/", got, next)
	}
	items, next = treePage(t, s, repo, "com/acme/", *next, 2, rauth)
	if got := nodeNames(items); !slices.Equal(got, []string{"dir:other"}) || next != nil {
		t.Fatalf("com/acme page 2: %v next %v, want [dir:other] and no next", got, next)
	}
	many := treeAll(t, s, repo, "com/acme/many", 5, rauth)
	if len(many) != 12 || many[0].Name != "v01" || many[11].Name != "v12" || many[3].Path != "com/acme/many/v04/" {
		t.Fatalf("com/acme/many: %v", nodeNames(many))
	}

	// Files carry size, checksum, time and the uploader's name.
	lib := treeAll(t, s, repo, "com/acme/lib/1.0", 1, rauth)
	if got := nodeNames(lib); !slices.Equal(got, []string{"file:lib-1.0.jar", "file:lib-1.0.pom"}) {
		t.Fatalf("com/acme/lib/1.0: %v", got)
	}
	jar := lib[0]
	if jar.Size == nil || *jar.Size != int64(len("content of com/acme/lib/1.0/lib-1.0.jar")) || len(jar.SHA256) != 64 ||
		jar.UpdatedAt == nil || jar.UploadedBy != w || jar.Path != "com/acme/lib/1.0/lib-1.0.jar" {
		t.Fatalf("file node %+v: want size, sha256, updated_at, path and uploaded_by %s", jar, w)
	}

	// Access: anonymous 401 on a private repo; invalid paths 400.
	if st, b := s.API(t, http.MethodGet, "/api/repos/"+repo+"/tree", nil, nil); st != http.StatusUnauthorized {
		t.Fatalf("anonymous tree of a private repo: status %d: %s", st, b)
	}
	for _, q := range []string{"path=a/../b", "path=a//b", "after=a/b"} {
		if st, b := s.API(t, http.MethodGet, "/api/repos/"+repo+"/tree?"+q, nil, rauth); st != http.StatusBadRequest {
			t.Fatalf("tree ?%s: status %d, want 400: %s", q, st, b)
		}
	}
	if items, _ := treePage(t, s, repo, "no/such/dir", "", 10, rauth); len(items) != 0 {
		t.Fatalf("tree of a missing folder: %v", nodeNames(items))
	}
}

func TestDelete(t *testing.T) {
	t.Parallel()
	ForEachStorage(t, func(t *testing.T, opts ServerOpts) {
		if opts.S3 == nil {
			opts.DataVolume = NewDataVolume(t)
		}
		// The blob GC keeps an unused blob for a day, so every blob outlives this test.
		opts.Env = map[string]string{"TR_BLOBS_GC_GRACE": "24h"}
		s := StartServer(t, opts)
		mvn, raw, other := RandName("mvn"), RandName("raw"), RandName("other")
		s.CreateRepo(t, mvn, "maven", false, nil)
		s.CreateRepo(t, raw, "raw", false, nil)
		s.CreateRepo(t, other, "raw", false, nil)
		// w deletes files of mvn and raw; pub publishes to mvn but does not delete.
		w, pub := RandName("writer"), RandName("pub")
		s.CreateUser(t, w, "writer-pass-1234")
		s.CreateUser(t, pub, "pub-pass-1234")
		s.SetPerms(t, mvn, w, "repo.download", "repo.publish", "repo.delete")
		s.SetPerms(t, raw, w, "repo.download", "repo.publish", "repo.delete")
		s.SetPerms(t, other, w, "repo.download", "repo.publish")
		s.SetPerms(t, mvn, pub, "repo.download", "repo.publish")

		mfiles := []string{"g/a/1.0/a-1.0.jar", "g/a/1.0/a-1.0.pom", "g/a/2.0/a-2.0.jar", "g/a/maven-metadata.xml", "g/b/1.0/b-1.0.jar", "g/c/1.0/c-1.0.jar"}
		ps := s.MintSession(t, w, mvn)
		for _, f := range mfiles {
			resp, body := authPut(t, s, "/maven/"+mvn+"/"+f, "x "+f, ps.Auth())
			wantStatus(t, resp, body, http.StatusCreated)
		}
		rps := s.MintSession(t, w, raw)
		rfiles := []string{"dir/one.bin", "dir/sub/two.bin", "dir/sub/three.bin", "keep.bin"}
		for _, f := range rfiles {
			resp, body := rawPut(t, s, "/raw/"+raw+"/"+f, []byte("raw "+f), rps.Auth())
			wantStatus(t, resp, body, http.StatusCreated)
		}

		// Who may delete: a person who holds delete on the repo, with a publish session for the repo
		// or a UI session.
		jar := "g/a/1.0/a-1.0.jar"
		refused := []struct {
			what string
			auth Auth
		}{
			{"anonymous", nil},
			{"the writer's password", Basic{w, "writer-pass-1234"}},
			{"the admin's password", s.AdminBasic()},
			{"a session for another repo", s.MintSession(t, w, other).Auth()},
			{"a publish session of a person without delete", s.MintSession(t, pub, mvn).Auth()},
			{"the UI session of a person without delete", s.LoginTOTP(t, pub)},
		}
		for _, r := range refused {
			if st, _ := deleteFiles(t, s, mvn, "path", jar, r.auth); st != http.StatusUnauthorized && st != http.StatusForbidden {
				t.Fatalf("delete with %s: status %d, want 401 or 403", r.what, st)
			}
		}
		if st, _ := deleteFiles(t, s, mvn, "path", "g/a/../x", ps.Auth()); st != http.StatusBadRequest {
			t.Fatalf("delete of an invalid path: status %d, want 400", st)
		}

		mustDelete(t, s, mvn, "path", jar, ps.Auth(), 1)
		if resp, _ := authGet(t, s, "/maven/"+mvn+"/"+jar, Basic{w, "writer-pass-1234"}); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("GET a deleted file: status %d, want 404", resp.StatusCode)
		}
		// A file that is gone already counts as done.
		mustDelete(t, s, mvn, "path", jar, ps.Auth(), 0)
		// A folder by prefix, with the token as a Bearer token.
		mustDelete(t, s, mvn, "prefix", "g/a/2.0", Bearer{ps.Token}, 1)
		// The writer signed in to the UI (delete needs the second factor) deletes too.
		mustDelete(t, s, mvn, "prefix", "g/c", s.SignIn(t, w, "writer-pass-1234"), 1)
		// An admin signed in to the UI (with the second factor) deletes too.
		mustDelete(t, s, mvn, "prefix", "g/a/1.0/", s.LoginTOTP(t, s.AdminUser), 1)
		if got := nodeNames(treeAll(t, s, mvn, "g/a", 50, s.Admin())); !slices.Equal(got, []string{"file:maven-metadata.xml"}) {
			t.Fatalf("g/a after the deletes: %v", got)
		}

		mustDelete(t, s, raw, "prefix", "dir", rps.Auth(), 3)
		mustDelete(t, s, raw, "prefix", "dir", rps.Auth(), 0)
		if got := nodeNames(treeAll(t, s, raw, "", 50, s.Admin())); !slices.Equal(got, []string{"file:keep.bin"}) {
			t.Fatalf("raw root after deleting dir/: %v", got)
		}

		// A delete that takes longer than delete.wait (1ns: every one) answers 202 and goes on in the
		// background; its state is readable by id on its own repo only.
		s.MustAPI(t, http.MethodPatch, "/api/settings", map[string]any{"delete.wait": "1ns"}, s.Admin(), nil)
		var started struct {
			ID string `json:"id"`
		}
		st, b := s.API(t, http.MethodPost, "/api/repos/"+raw+"/files/delete", map[string][]string{"paths": {"keep.bin"}}, rps.Auth())
		if st != http.StatusAccepted || json.Unmarshal(b, &started) != nil || started.ID == "" {
			t.Fatalf("delete with delete.wait 1ns: status %d, want 202 with an id: %s", st, b)
		}
		var state struct {
			State   string `json:"state"`
			Deleted int    `json:"deleted"`
		}
		for end := time.Now().Add(30 * time.Second); ; {
			s.MustAPI(t, http.MethodGet, "/api/repos/"+raw+"/files/delete/"+started.ID, nil, rps.Auth(), &state)
			if state.State != "running" {
				break
			}
			if time.Now().After(end) {
				t.Fatalf("the delete in the background still runs after 30 s: %+v", state)
			}
			time.Sleep(100 * time.Millisecond)
		}
		if state.State != "done" || state.Deleted != 1 {
			t.Fatalf("delete in the background: %+v, want done with 1 file", state)
		}
		if got := nodeNames(treeAll(t, s, raw, "", 50, s.Admin())); len(got) != 0 {
			t.Fatalf("raw root after the delete in the background: %v", got)
		}
		for _, p := range []string{"/api/repos/" + raw + "/files/delete/no-such-id", "/api/repos/" + mvn + "/files/delete/" + started.ID} {
			auth := rps.Auth()
			if strings.Contains(p, mvn) {
				auth = ps.Auth()
			}
			if st, b := s.API(t, http.MethodGet, p, nil, auth); st != http.StatusNotFound {
				t.Fatalf("GET %s: status %d, want 404: %s", p, st, b)
			}
		}

		// A delete leaves the blobs to the blob GC, whose grace (a day here) is not over yet.
		keys := storedKeys(t, opts)
		for _, content := range []string{"x g/a/1.0/a-1.0.jar", "x g/b/1.0/b-1.0.jar", "raw dir/one.bin", "raw keep.bin"} {
			if k := blobKey(sha256Hex([]byte(content))); !slices.Contains(keys, k) {
				t.Fatalf("blob %s (%q) is gone before the blob GC (keys %v)", k, content, keys)
			}
		}

		// Repo delete needs the All repositories permission; the rows go, and the name is free again.
		del := map[string][]string{"names": {raw}}
		if st, b := s.API(t, http.MethodPost, "/api/repos/delete", del, s.SignIn(t, w, "writer-pass-1234")); st != http.StatusForbidden {
			t.Fatalf("repo delete by a writer: status %d, want 403: %s", st, b)
		}
		if st, b := s.API(t, http.MethodPost, "/api/repos/delete", del, s.AdminBasic()); st != http.StatusUnauthorized {
			t.Fatalf("repo delete with the admin's password: status %d, want 401: %s", st, b)
		}
		deleteRepos(t, s, raw)
		if resp, body := rawPut(t, s, "/raw/"+raw+"/after.bin", []byte("x"), rps.Auth()); resp.StatusCode/100 == 2 {
			t.Fatalf("upload with a session of a deleted repo: status %d: %s", resp.StatusCode, body)
		}
		s.CreateRepo(t, raw, "raw", false, nil)
		if items, _ := treePage(t, s, raw, "", "", 10, s.Admin()); len(items) != 0 {
			t.Fatalf("a new repo with the name of a deleted one holds files: %v", nodeNames(items))
		}
	})
}

type repoCounters struct {
	FileCount       int64      `json:"file_count"`
	TotalSize       int64      `json:"total_size"`
	LastPublishedAt *time.Time `json:"last_published_at"`
}

// countersOf returns the counters of repo from the repo list (GET /api/repos).
func countersOf(t *testing.T, s *Server, repo string) repoCounters {
	t.Helper()
	var list struct {
		Items []struct {
			Name string `json:"name"`
			repoCounters
		} `json:"items"`
	}
	s.MustAPI(t, http.MethodGet, "/api/repos?name="+repo, nil, s.Admin(), &list)
	for _, r := range list.Items {
		if r.Name == repo {
			return r.repoCounters
		}
	}
	t.Fatalf("repo %s is not in the repo list", repo)
	return repoCounters{}
}

// TestRepoCounters checks file_count, total_size and last_published_at of the repo list through
// uploads, a replace, a file delete and a folder delete.
func TestRepoCounters(t *testing.T) {
	t.Parallel()
	s := StartServer(t, ServerOpts{})
	repo := RandName("raw")
	s.CreateRepo(t, repo, "raw", false, map[string]any{"overwrite": "allow"})
	w := RandName("writer")
	s.CreateUser(t, w, "writer-pass-1234")
	s.SetPerms(t, repo, w, "repo.download", "repo.publish", "repo.delete")
	want := func(what string, n, size int, published bool) {
		t.Helper()
		c := countersOf(t, s, repo)
		if c.FileCount != int64(n) || c.TotalSize != int64(size) || (c.LastPublishedAt != nil) != published {
			t.Fatalf("%s: counters %+v, want %d files, %d bytes, last publish set %v", what, c, n, size, published)
		}
	}
	want("a new repo", 0, 0, false)

	ps := s.MintSession(t, w, repo)
	one, two := []byte("the first file"), []byte("the second, longer file")
	for p, b := range map[string][]byte{"a/one.bin": one, "a/two.bin": two} {
		resp, body := rawPut(t, s, "/raw/"+repo+"/"+p, b, ps.Auth())
		wantStatus(t, resp, body, http.StatusCreated)
	}
	want("two uploads", 2, len(one)+len(two), true)

	three := []byte("a much longer replacement of the first file")
	resp, body := rawPut(t, s, "/raw/"+repo+"/a/one.bin", three, ps.Auth())
	wantStatus(t, resp, body, http.StatusOK)
	want("a replace", 2, len(three)+len(two), true)

	mustDelete(t, s, repo, "path", "a/two.bin", ps.Auth(), 1)
	want("one file deleted", 1, len(three), true)
	mustDelete(t, s, repo, "prefix", "a", ps.Auth(), 1)
	want("the folder deleted", 0, 0, true)
}

// tinyJar returns a jar (zip) with a manifest and one marker entry.
func tinyJar(t *testing.T, marker string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{"META-INF/MANIFEST.MF": "Manifest-Version: 1.0\n", "e2e/" + marker + ".txt": marker} {
		f, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		f.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestManualUploadMaven(t *testing.T) {
	t.Parallel()
	s := StartServer(t, ServerOpts{})
	repo := RandName("mvn")
	s.CreateRepo(t, repo, "maven", false, nil)
	w, reader := RandName("writer"), RandName("reader")
	s.CreateUser(t, w, "writer-pass-1234")
	s.CreateUser(t, reader, "reader-pass-1234")
	s.SetPerms(t, repo, w, "repo.download", "repo.publish")
	s.SetPerms(t, repo, reader, "repo.download")
	rauth := Basic{reader, "reader-pass-1234"}
	ps := s.MintSession(t, w, repo)

	group := "com.example.up" + randHex(4)
	gpath := strings.ReplaceAll(group, ".", "/")
	pom := []byte(fmt.Sprintf(`<?xml version="1.0"?>
<project xmlns="http://maven.apache.org/POM/4.0.0">
  <modelVersion>4.0.0</modelVersion>
  <groupId>%s</groupId>
  <artifactId>hello</artifactId>
  <version>1.0.0</version>
  <packaging>jar</packaging>
</project>
`, group))
	jar, sources := tinyJar(t, "release-100"), tinyJar(t, "sources-100")

	// The write gate: a password or a read token does not upload.
	readToken := s.CreateToken(t, w, "writer-pass-1234", "read")
	for _, a := range []Auth{Basic{w, "writer-pass-1234"}, Basic{"x", readToken}, nil} {
		if st, b := manualUpload(t, s, repo, a, nil, uploadPart{"hello-1.0.0.jar", jar}); st != http.StatusUnauthorized {
			t.Fatalf("upload without a publish session: status %d, want 401: %s", st, b)
		}
	}

	got := mustUpload(t, s, repo, ps.Auth(), nil,
		uploadPart{"pom.xml", pom}, uploadPart{"hello-1.0.0.jar", jar}, uploadPart{"hello-1.0.0-sources.jar", sources})
	dir := gpath + "/hello/1.0.0/"
	want := map[string]string{}
	for _, f := range []string{"hello-1.0.0.jar", "hello-1.0.0-sources.jar", "hello-1.0.0.pom"} {
		for _, ext := range []string{"", ".md5", ".sha1", ".sha256"} {
			want[dir+f+ext] = "created"
		}
	}
	for _, ext := range []string{"", ".md5", ".sha1", ".sha256"} {
		want[gpath+"/hello/maven-metadata.xml"+ext] = "created"
	}
	have := map[string]string{}
	for _, u := range got {
		have[u.Path] = u.Status
	}
	for p, st := range want {
		if have[p] != st {
			t.Fatalf("upload answer: %s is %q, want %q (answer %+v)", p, have[p], st, got)
		}
	}
	sum := sha1.Sum(jar)
	if resp, body := authGet(t, s, "/maven/"+repo+"/"+dir+"hello-1.0.0.jar.sha1", rauth); resp.StatusCode != http.StatusOK || string(body) != hex.EncodeToString(sum[:]) {
		t.Fatalf("GET the .sha1 of the jar: status %d, body %q, want %s", resp.StatusCode, body, hex.EncodeToString(sum[:]))
	}

	// The same files again change nothing; a different jar for the release is refused.
	for _, u := range mustUpload(t, s, repo, ps.Auth(), nil, uploadPart{"pom.xml", pom}, uploadPart{"hello-1.0.0.jar", jar}) {
		if u.Status != "unchanged" {
			t.Fatalf("identical re-upload: %s is %s, want unchanged", u.Path, u.Status)
		}
	}
	if st, b := manualUpload(t, s, repo, Bearer{ps.Token}, nil, uploadPart{"pom.xml", pom}, uploadPart{"hello-1.0.0.jar", tinyJar(t, "other")}); st != http.StatusConflict {
		t.Fatalf("upload of a changed release jar: status %d, want 409: %s", st, b)
	}

	// Lone jars with coordinates in the fields get a generated pom; the metadata lists the
	// versions in Maven order.
	for _, v := range []string{"1.10.0", "1.1.0", "1.9.0"} {
		mustUpload(t, s, repo, ps.Auth(), map[string]string{"groupId": group, "artifactId": "hello", "version": v},
			uploadPart{"build-output.jar", tinyJar(t, "lone-"+v)})
	}
	if st, b := manualUpload(t, s, repo, ps.Auth(), map[string]string{"groupId": group, "version": "2.0"}, uploadPart{"x.jar", jar}); st != http.StatusBadRequest {
		t.Fatalf("lone jar without an artifactId: status %d, want 400: %s", st, b)
	}
	resp, body := authGet(t, s, "/maven/"+repo+"/"+gpath+"/hello/maven-metadata.xml", rauth)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET maven-metadata.xml: status %d", resp.StatusCode)
	}
	md := string(body)
	order := []string{"<version>1.0.0</version>", "<version>1.1.0</version>", "<version>1.9.0</version>", "<version>1.10.0</version>"}
	last := -1
	for _, v := range order {
		i := strings.Index(md, v)
		if i <= last {
			t.Fatalf("maven-metadata.xml does not list %v in this order:\n%s", order, md)
		}
		last = i
	}
	if !strings.Contains(md, "<latest>1.10.0</latest>") || !strings.Contains(md, "<release>1.10.0</release>") {
		t.Fatalf("maven-metadata.xml: want latest and release 1.10.0:\n%s", md)
	}

	// A real Maven client resolves both the uploaded and the generated pom, with strict checksums.
	repoURL := s.InternalURL + "/maven/" + repo
	script := strings.Join([]string{
		"set -e",
		"cd /work/consumer",
		"for v in 1.0.0 1.9.0; do",
		"mvn -B -ntp -q -C -U -s /work/settings.xml -Ddep.version=$v -Dmdep.outputFile=/tmp/cp-$v.txt org.apache.maven.plugins:maven-dependency-plugin:3.8.1:build-classpath;",
		`echo "ENTRIES $v: $(jar tf "$(cat /tmp/cp-$v.txt)" | tr '\n' ' ')";`,
		"done",
	}, "\n")
	code, out := RunClient(t, mavenImage, ClientOpts{
		Cmd:     []string{script},
		Files:   []ContainerFile{{Path: "/work/settings.xml", Content: mavenSettingsXML(reader, "reader-pass-1234")}, mavenConsumerProject(group, repoURL)},
		Mounts:  map[string]string{M2Volume: "/root/.m2/repository"},
		Timeout: 15 * time.Minute,
	})
	if code != 0 {
		t.Fatalf("mvn resolve of the uploaded artifacts: exit %d\n%s", code, out)
	}
	for _, wantEntry := range []string{"e2e/release-100.txt", "e2e/lone-1.9.0.txt"} {
		if !strings.Contains(out, wantEntry) {
			t.Fatalf("mvn output does not show the jar entry %q:\n%s", wantEntry, out)
		}
	}
}

func TestManualUploadRaw(t *testing.T) {
	t.Parallel()
	s := StartServer(t, ServerOpts{})
	repo, other := RandName("raw"), RandName("other")
	s.CreateRepo(t, repo, "raw", false, nil)
	s.CreateRepo(t, other, "raw", false, nil)
	w := RandName("writer")
	s.CreateUser(t, w, "writer-pass-1234")
	s.SetPerms(t, repo, w, "repo.download", "repo.publish")
	s.SetPerms(t, other, w, "repo.download", "repo.publish")
	ps := s.MintSession(t, w, repo)

	a, b := rawRandom(t, 3000), rawRandom(t, 10)
	got := mustUpload(t, s, repo, Bearer{ps.Token}, map[string]string{"folder": "/dl/v1/"}, uploadPart{"a.bin", a}, uploadPart{"b.txt", b})
	if len(got) != 2 || got[0].Path != "dl/v1/a.bin" || got[0].Status != "created" || got[1].Path != "dl/v1/b.txt" {
		t.Fatalf("raw upload answer: %+v", got)
	}
	rawWantGet(t, s, nil, "/raw/"+repo+"/dl/v1/a.bin", s.AdminBasic(), a)
	rawWantGet(t, s, nil, "/raw/"+repo+"/dl/v1/b.txt", s.AdminBasic(), b)

	if got := mustUpload(t, s, repo, ps.Auth(), map[string]string{"folder": "dl/v1"}, uploadPart{"a.bin", a}); got[0].Status != "unchanged" {
		t.Fatalf("identical re-upload: %+v", got)
	}
	if st, body := manualUpload(t, s, repo, ps.Auth(), map[string]string{"folder": "dl/v1"}, uploadPart{"a.bin", b}); st != http.StatusConflict {
		t.Fatalf("different bytes under overwrite deny: status %d, want 409: %s", st, body)
	}
	for _, f := range []map[string]string{{"folder": "../x"}, {"nope": "1"}} {
		if st, body := manualUpload(t, s, repo, ps.Auth(), f, uploadPart{"c.bin", a}); st != http.StatusBadRequest {
			t.Fatalf("upload with fields %v: status %d, want 400: %s", f, st, body)
		}
	}
	if st, body := manualUpload(t, s, other, ps.Auth(), nil, uploadPart{"c.bin", a}); st != http.StatusUnauthorized {
		t.Fatalf("upload with a session of another repo: status %d, want 401: %s", st, body)
	}
}

// debAptScriptAfterDelete updates apt and reports which test packages apt still sees.
const debAptScriptAfterDelete = `#!/bin/sh
set -e
rm -f /etc/apt/sources.list.d/debian.sources /etc/apt/sources.list
if ! apt-get update >/tmp/update.log 2>&1; then cat /tmp/update.log; exit 1; fi
for p in e2e-hello e2e-native; do
  if apt-cache show "$p" >/dev/null 2>&1; then echo "SEES $p"; else echo "GONE $p"; fi
done
`

// TestDebianDeleteAndUpload plans a folder of .debs and uploads a package through the web UI
// route, deletes one through the API (apt no longer sees it) and checks the by-hash cleanup.
func TestDebianDeleteAndUpload(t *testing.T) {
	t.Parallel()
	pkgs := buildDebPackages(t)
	archs := []string{"amd64", "arm64"}
	if !slices.Contains(archs, pkgs.arch) {
		archs = append(archs, pkgs.arch)
	}
	s := StartServer(t, ServerOpts{})
	repo := RandName("deb")
	s.CreateRepo(t, repo, "debian", false, map[string]any{"architectures": archs})
	w, reader := RandName("writer"), RandName("reader")
	s.CreateUser(t, w, "writer-pass-1234")
	s.CreateUser(t, reader, "reader-pass-1234")
	s.SetPerms(t, repo, w, "repo.download", "repo.publish", "repo.delete")
	s.SetPerms(t, repo, reader, "repo.download")
	rauth := Basic{reader, "reader-pass-1234"}

	ps := s.MintSession(t, w, repo)
	// The plan of a folder of .debs: a broken one has problems, a file that is not a .deb is
	// ignored, and the open folder dists/trixie/ gives the distribution.
	debs := []uploadPart{
		{"debs/native.deb", pkgs.native},
		{"debs/hello.deb", pkgs.hello1},
		{"debs/broken.deb", []byte("not an ar archive")},
		{"debs/notes.txt", []byte("x")},
	}
	sc := mustPlan(t, s, repo, ps.Auth(), planOpts{folder: "dists/trixie/", reads: readsDebs}, debs...)
	native := findTitle(t, sc, "e2e-native 1.0 ("+pkgs.arch+")")
	hello := findTitle(t, sc, "e2e-hello 1.0 (all)")
	broken := findTitle(t, sc, "debs/broken.deb")
	if len(sc.Items) != 3 || len(native.Problems)+len(hello.Problems) != 0 || len(broken.Problems) == 0 ||
		!sameSet(sc.Ignored, []string{"debs/notes.txt"}) || sc.field("distribution").Value != "trixie" ||
		!strings.HasPrefix(hello.target("debs/hello.deb"), "pool/"+sc.field("component").Value+"/") {
		t.Fatalf("plan of the .deb folder: %+v", sc)
	}
	// A bad distribution is a problem of its field.
	sc = mustPlan(t, s, repo, ps.Auth(), planOpts{fields: map[string]string{"distribution": "Bad Name"}, reads: readsDebs}, debs...)
	if sc.field("distribution").Problem == "" {
		t.Fatalf("plan with a bad distribution: %+v", sc)
	}
	if st, b := manualUpload(t, s, repo, ps.Auth(), map[string]string{"distribution": "trixie"}, uploadPart{"notes.txt", []byte("x")}); st != http.StatusBadRequest {
		t.Fatalf("upload of a non-.deb file: status %d, want 400: %s", st, b)
	}
	got := mustUpload(t, s, repo, ps.Auth(), map[string]string{"distribution": "trixie", "component": "main"}, uploadPart{"native.deb", pkgs.native})
	nativePool := "pool/main/e/e2e-native/e2e-native_1.0_" + pkgs.arch + ".deb"
	if len(got) != 1 || got[0].Path != nativePool || got[0].Status != "created" {
		t.Fatalf("debian upload answer: %+v, want %s created", got, nativePool)
	}
	debMustUpload(t, s, repo, pkgs.hello1, ps.Auth(), "e2e-hello", "1.0", "all")
	s.WaitDebianIndexed(t, repo)
	key := s.DebianKey(t, repo, rauth)
	out := debAptInstall(t, s, repo, reader, "reader-pass-1234", key)
	if !strings.Contains(out, "NATIVE: native "+pkgs.arch) {
		t.Fatalf("apt install of the uploaded package:\n%s", out)
	}

	debMustUpload(t, s, repo, pkgs.hello2, s.MintSession(t, w, repo).Auth(), "e2e-hello", "2.0", "all")
	s.WaitDebianIndexed(t, repo)
	unit := "dists/trixie/main/binary-" + pkgs.arch
	// indexHashes is the SHA-256 of each index file of the unit's current generation.
	indexHashes := func() []string {
		var out []string
		for _, n := range treeAll(t, s, repo, unit, 50, rauth) {
			if n.Type == "file" {
				out = append(out, n.SHA256)
			}
		}
		if len(out) == 0 {
			t.Fatalf("%s holds no index files", unit)
		}
		return out
	}
	previous := indexHashes()

	mustDelete(t, s, repo, "path", nativePool, s.MintSession(t, w, repo).Auth(), 1)
	s.WaitDebianIndexed(t, repo)
	current := indexHashes()
	st, packages := debGet(t, s, nil, "/debian/"+repo+"/"+unit+"/Packages", rauth)
	if st != http.StatusOK || strings.Contains(string(packages), "Package: e2e-native") || !strings.Contains(string(packages), "Package: e2e-hello") {
		t.Fatalf("Packages after the delete: status %d, want e2e-hello without e2e-native:\n%s", st, packages)
	}
	code, aptOut := RunClient(t, debImage, ClientOpts{
		Cmd: []string{"sh", "/apt.sh"},
		Files: []ContainerFile{
			{Content: debAptScriptAfterDelete, Path: "/apt.sh", Mode: 0o755},
			{Content: key, Path: "/etc/apt/keyrings/tr.asc"},
			{Content: fmt.Sprintf("Types: deb\nURIs: %s/debian/%s\nSuites: trixie\nComponents: main\nSigned-By: /etc/apt/keyrings/tr.asc\n", s.InternalURL, repo), Path: "/etc/apt/sources.list.d/tr.sources"},
			{Content: fmt.Sprintf("machine http://%s:8080/debian/%s login %s password %s\n", s.Alias, repo, reader, "reader-pass-1234"), Path: "/etc/apt/auth.conf.d/tr.conf"},
		},
	})
	if code != 0 || !strings.Contains(aptOut, "SEES e2e-hello") || !strings.Contains(aptOut, "GONE e2e-native") {
		t.Fatalf("apt after the delete: exit %d, want SEES e2e-hello and GONE e2e-native:\n%s", code, aptOut)
	}

	// Three index generations of this unit were built (before 2.0, with 2.0, after the delete);
	// by-hash holds exactly the files of the current and the previous one.
	want := slices.Concat(previous, current)
	slices.Sort(want)
	want = slices.Compact(want)
	var byHash []string
	for _, n := range treeAll(t, s, repo, unit+"/by-hash/SHA256", 50, rauth) {
		byHash = append(byHash, n.Name)
	}
	slices.Sort(byHash)
	if !slices.Equal(byHash, want) {
		t.Fatalf("%s/by-hash/SHA256 holds %v, want the index files of the current and the previous generation %v", unit, byHash, want)
	}
}
