//go:build e2e

package e2e

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// countKeys returns how many stored keys end in key (an S3 key carries the target's prefix).
func countKeys(t *testing.T, opts ServerOpts, key string) int {
	t.Helper()
	n := 0
	for _, k := range storedKeys(t, opts) {
		if strings.HasSuffix(k, key) {
			n++
		}
	}
	return n
}

// wantContentType checks the content type of the request path p: HEAD answers it itself, and a
// GET that redirects to S3 signs it into the presigned URL, with a disposition that names the
// file of p, so a browser saves the download under that name. It reports whether the GET
// redirected.
func wantContentType(t *testing.T, s *Server, p, want string, auth Auth) bool {
	t.Helper()
	resp, b := s.Do(t, s.NewRequest(t, http.MethodHead, p, nil), auth)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != want {
		t.Fatalf("HEAD %s: status %d, Content-Type %q, want 200 and %q: %s", p, resp.StatusCode, resp.Header.Get("Content-Type"), want, b)
	}
	resp, _ = s.Do(t, s.NewRequest(t, http.MethodGet, p, nil), auth)
	if resp.StatusCode != http.StatusFound {
		if ct := resp.Header.Get("Content-Type"); resp.StatusCode != http.StatusOK || ct != want {
			t.Fatalf("GET %s: status %d, Content-Type %q, want 200 and %q", p, resp.StatusCode, ct, want)
		}
		return false
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || loc.Query().Get("response-content-type") != want {
		t.Fatalf("GET %s: redirect %q, want a presigned URL with response-content-type %q", p, resp.Header.Get("Location"), want)
	}
	name, err := url.PathUnescape(path.Base(p))
	if err != nil {
		t.Fatalf("request path %s: %v", p, err)
	}
	cd := loc.Query().Get("response-content-disposition")
	kind, params, err := mime.ParseMediaType(cd)
	if err != nil || (kind != "attachment" && kind != "inline") || params["filename"] != name {
		t.Fatalf("GET %s: presigned response-content-disposition %q, want attachment or inline with the filename %q", p, cd, name)
	}
	return true
}

// TestBlobDedupeAndGC stores the same bytes in two raw repos and a Maven repo: the store holds
// them once, and each path serves them with its own content type. Deleting files leaves the blob
// while another file uses it; after the last delete the blob GC removes it once the grace
// (TR_BLOBS_GC_GRACE=1s) is over, and an upload of the same bytes stores them again.
func TestBlobDedupeAndGC(t *testing.T) {
	t.Parallel()
	ForEachStorage(t, func(t *testing.T, opts ServerOpts) {
		if opts.S3 == nil {
			opts.DataVolume = NewDataVolume(t)
		}
		if opts.Env == nil {
			opts.Env = map[string]string{}
		}
		opts.Env["TR_BLOBS_GC_GRACE"] = "1s"
		s := StartServer(t, opts)
		a, b, m := RandName("dedupe-a"), RandName("dedupe-b"), RandName("dedupe-m")
		s.CreateRepo(t, a, "raw", false, nil)
		s.CreateRepo(t, b, "raw", false, nil)
		s.CreateRepo(t, m, "maven", false, nil)

		body := rawRandom(t, 64<<10)
		key := blobKey(sha256Hex(body))
		files := []struct{ path, ct string }{
			{"/raw/" + a + "/x/one.bin", "application/octet-stream"},
			{"/raw/" + b + "/y/notes.txt", "text/plain"},
			{"/maven/" + m + "/com/e2e/dedupe/1.0/dedupe-1.0.jar", "application/java-archive"},
		}
		for _, f := range files {
			repo := strings.Split(f.path, "/")[2]
			resp, rb := rawPut(t, s, f.path, body, s.PublishAuth(t, s.AdminUser, repo))
			wantStatus(t, resp, rb, http.StatusCreated)
		}
		if n := countKeys(t, opts, key); n != 1 {
			t.Fatalf("the same bytes in 3 files of 3 repos: %d objects with key %s, want 1", n, key)
		}
		for _, f := range files {
			rawWantGet(t, s, opts.S3, f.path, s.AdminBasic(), body)
			wantContentType(t, s, f.path, f.ct, s.AdminBasic())
		}
		// The disposition of a redirect quotes a file name with a space. Other bytes, so the GC
		// below sees only the files above.
		spaced := "/raw/" + a + "/x/two%20words.bin"
		sresp, sb := rawPut(t, s, spaced, rawRandom(t, 1024), s.PublishAuth(t, s.AdminUser, a))
		wantStatus(t, sresp, sb, http.StatusCreated)
		if !wantContentType(t, s, spaced, files[0].ct, s.AdminBasic()) && opts.S3 != nil {
			t.Fatalf("GET %s on S3 storage: no redirect, want 302 to a presigned URL", spaced)
		}

		del := func(repo, p string) {
			t.Helper()
			var out struct {
				Deleted int `json:"deleted"`
			}
			s.MustAPI(t, http.MethodPost, "/api/repos/"+repo+"/files/delete", map[string][]string{"paths": {p}}, s.Admin(), &out)
			if out.Deleted != 1 {
				t.Fatalf("delete %s of %s: deleted %d, want 1", p, repo, out.Deleted)
			}
		}
		// Two of three files go: the blob stays for the third, also after the grace.
		del(a, "x/one.bin")
		del(b, "y/notes.txt")
		time.Sleep(3 * time.Second)
		if n := countKeys(t, opts, key); n != 1 {
			t.Fatalf("blob after 2 of 3 deletes: %d objects, want 1", n)
		}
		rawWantGet(t, s, opts.S3, files[2].path, s.AdminBasic(), body)

		// The last file goes: the GC removes the blob after the grace.
		del(m, "com/e2e/dedupe/1.0/dedupe-1.0.jar")
		deadline := time.Now().Add(60 * time.Second)
		for countKeys(t, opts, key) > 0 {
			if time.Now().After(deadline) {
				t.Fatalf("blob %s is still stored 60s after its last file was deleted", key)
			}
			time.Sleep(time.Second)
		}

		// The same bytes again: stored again and served.
		resp, rb := rawPut(t, s, files[0].path, body, s.PublishAuth(t, s.AdminUser, a))
		wantStatus(t, resp, rb, http.StatusCreated)
		if n := countKeys(t, opts, key); n != 1 {
			t.Fatalf("blob after a new upload of the collected bytes: %d objects, want 1", n)
		}
		rawWantGet(t, s, opts.S3, files[0].path, s.AdminBasic(), body)
	})
}

// uploadSession calls a route of the resumable uploads of repo and returns the status and body.
func uploadSession(t *testing.T, s *Server, method, path string, body []byte, auth Auth, header map[string]string) (int, []byte) {
	t.Helper()
	req := s.NewRequest(t, method, path, bytes.NewReader(body))
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, b := s.Do(t, req, auth)
	return resp.StatusCode, b
}

// TestUploadSessions uploads a file in chunks through a resumable upload: a wrong offset answers
// 416 with the bytes received, the upload survives a restart, a wrong digest deletes the upload,
// and a finished upload is a file of a raw and of a Maven repo. Only the publish session that
// started an upload continues it. A Debian repo refuses a finished upload at a path (400); its
// upload route of a distribution finishes the upload of a .deb.
func TestUploadSessions(t *testing.T) {
	t.Parallel()
	s := StartServer(t, ServerOpts{DataVolume: NewDataVolume(t)})
	raw, mvn := RandName("chunks"), RandName("chunks-mvn")
	s.CreateRepo(t, raw, "raw", false, nil)
	s.CreateRepo(t, mvn, "maven", false, nil)
	auth := s.PublishAuth(t, s.AdminUser, raw)
	base := "/api/repos/" + raw + "/uploads"
	body := rawRandom(t, 300<<10)
	first, second := body[:100<<10], body[100<<10:]
	chunk := func(from int, b []byte) map[string]string {
		return map[string]string{"Content-Range": fmt.Sprintf("bytes %d-%d/*", from, from+len(b)-1)}
	}
	start := func(base string, auth Auth) string {
		t.Helper()
		st, b := uploadSession(t, s, http.MethodPost, base, nil, auth, nil)
		if st != http.StatusCreated || !strings.Contains(string(b), `"received":0`) {
			t.Fatalf("POST %s: status %d, want 201 with received 0: %s", base, st, b)
		}
		var out struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(b, &out); err != nil || out.ID == "" {
			t.Fatalf("POST %s: no upload id in %s", base, b)
		}
		return out.ID
	}

	// Only a publish session of the repo starts an upload.
	if st, b := uploadSession(t, s, http.MethodPost, base, nil, s.AdminBasic(), nil); st != http.StatusUnauthorized && st != http.StatusForbidden {
		t.Fatalf("start an upload with a password: status %d, want 401 or 403: %s", st, b)
	}

	id := start(base, auth)
	if st, b := uploadSession(t, s, http.MethodPatch, base+"/"+id, first, auth, chunk(0, first)); st != http.StatusAccepted ||
		!strings.Contains(string(b), `"received":`+strconv.Itoa(len(first))) {
		t.Fatalf("append the first chunk: status %d: %s", st, b)
	}
	if st, b := uploadSession(t, s, http.MethodPatch, base+"/"+id, first, auth, chunk(0, first)); st != http.StatusRequestedRangeNotSatisfiable ||
		!strings.Contains(string(b), `"received":`+strconv.Itoa(len(first))) {
		t.Fatalf("append at a wrong offset: status %d, want 416 with the bytes received: %s", st, b)
	}
	// Another publish session of the repo does not reach this upload.
	if st, b := uploadSession(t, s, http.MethodPatch, base+"/"+id, second, s.PublishAuth(t, s.AdminUser, raw), chunk(len(first), second)); st != http.StatusNotFound {
		t.Fatalf("append with another publish session: status %d, want 404: %s", st, b)
	}

	s.Restart(t)
	if st, b := uploadSession(t, s, http.MethodPatch, base+"/"+id, second, auth, chunk(len(first), second)); st != http.StatusAccepted {
		t.Fatalf("append the second chunk after a restart: status %d: %s", st, b)
	}
	// A wrong digest refuses the upload and deletes it.
	wrong := strings.Repeat("0", 64)
	if st, b := uploadSession(t, s, http.MethodPut, base+"/"+id+"?path=big/file.bin&sha256="+wrong, nil, auth, nil); st != http.StatusBadRequest {
		t.Fatalf("finish with a wrong digest: status %d, want 400: %s", st, b)
	}
	if st, b := uploadSession(t, s, http.MethodPatch, base+"/"+id, second, auth, chunk(len(body), second)); st != http.StatusNotFound {
		t.Fatalf("append to an upload that a wrong digest ended: status %d, want 404: %s", st, b)
	}

	// The whole file in two chunks, finished into the raw repo.
	id = start(base, auth)
	for _, c := range []struct {
		from int
		b    []byte
	}{{0, first}, {len(first), second}} {
		if st, b := uploadSession(t, s, http.MethodPatch, base+"/"+id, c.b, auth, chunk(c.from, c.b)); st != http.StatusAccepted {
			t.Fatalf("append at %d: status %d: %s", c.from, st, b)
		}
	}
	q := "?path=big/file.bin&sha256=" + sha256Hex(body) + "&size=" + strconv.Itoa(len(body))
	if st, b := uploadSession(t, s, http.MethodPut, base+"/"+id+q, nil, auth, nil); st != http.StatusCreated {
		t.Fatalf("finish into the raw repo: status %d, want 201: %s", st, b)
	}
	rawWantGet(t, s, nil, "/raw/"+raw+"/big/file.bin", s.AdminBasic(), body)

	// The same into a Maven repo, at a release path.
	mauth := s.PublishAuth(t, s.AdminUser, mvn)
	mbase := "/api/repos/" + mvn + "/uploads"
	id = start(mbase, mauth)
	if st, b := uploadSession(t, s, http.MethodPatch, mbase+"/"+id, body, mauth, nil); st != http.StatusAccepted {
		t.Fatalf("append the whole file without Content-Range: status %d: %s", st, b)
	}
	jar := "com/e2e/chunks/1.0/chunks-1.0.jar"
	if st, b := uploadSession(t, s, http.MethodPut, mbase+"/"+id+"?path="+jar+"&sha256="+sha256Hex(body), nil, mauth, nil); st != http.StatusCreated {
		t.Fatalf("finish into the Maven repo: status %d, want 201: %s", st, b)
	}
	rawWantGet(t, s, nil, "/maven/"+mvn+"/"+jar, s.AdminBasic(), body)
	wantContentType(t, s, "/maven/"+mvn+"/"+jar, "application/java-archive", s.AdminBasic())
	// A release path keeps its bytes: other bytes through an upload answer 409.
	id = start(mbase, mauth)
	other := rawRandom(t, 1024)
	if st, b := uploadSession(t, s, http.MethodPatch, mbase+"/"+id, other, mauth, nil); st != http.StatusAccepted {
		t.Fatalf("append: status %d: %s", st, b)
	}
	if st, b := uploadSession(t, s, http.MethodPut, mbase+"/"+id+"?path="+jar+"&sha256="+sha256Hex(other), nil, mauth, nil); st != http.StatusConflict {
		t.Fatalf("finish other bytes at a release path: status %d, want 409: %s", st, b)
	}
	// A cancelled upload is gone.
	id = start(mbase, mauth)
	if st, b := uploadSession(t, s, http.MethodDelete, mbase+"/"+id, nil, mauth, nil); st != http.StatusNoContent {
		t.Fatalf("cancel: status %d, want 204: %s", st, b)
	}
	if st, b := uploadSession(t, s, http.MethodPatch, mbase+"/"+id, other, mauth, nil); st != http.StatusNotFound {
		t.Fatalf("append to a cancelled upload: status %d, want 404: %s", st, b)
	}

	// A Debian repo takes packages only through its upload route: a finished upload at an index
	// or a pool path answers 400.
	deb := RandName("chunks-deb")
	s.CreateRepo(t, deb, "debian", false, nil)
	dauth := s.PublishAuth(t, s.AdminUser, deb)
	dbase := "/api/repos/" + deb + "/uploads"
	for _, p := range []string{"dists/stable/InRelease", "pool/main/x/xdeb/xdeb_1.0_amd64.deb"} {
		id = start(dbase, dauth)
		if st, b := uploadSession(t, s, http.MethodPatch, dbase+"/"+id, other, dauth, nil); st != http.StatusAccepted {
			t.Fatalf("append: status %d: %s", st, b)
		}
		if st, b := uploadSession(t, s, http.MethodPut, dbase+"/"+id+"?path="+p+"&sha256="+sha256Hex(other), nil, dauth, nil); st != http.StatusBadRequest {
			t.Fatalf("finish into the Debian repo at %s: status %d, want 400: %s", p, st, b)
		}
		if st, b := rawGet(t, s, nil, "/debian/"+deb+"/"+p, s.AdminBasic()); st == http.StatusOK && bytes.Equal(b, other) {
			t.Fatalf("GET %s serves the bytes of the refused upload", p)
		}
	}
	// The upload route of a distribution finishes a resumable upload of a .deb (telesphorepo
	// publish of a large package): ?upload=<id>&sha256=<hex>&size=<n> and no body.
	pkg := goDeb(t, debMember{"./control", []byte("Package: e2e-chunked\nVersion: 1.0\nArchitecture: all\nMaintainer: E2E <e2e@telesphorepo.test>\nDescription: chunked\n")})
	up := "/debian/" + deb + "/upload/stable/main?upload="
	id = start(dbase, dauth)
	half := len(pkg) / 2
	for _, c := range []struct {
		from int
		b    []byte
	}{{0, pkg[:half]}, {half, pkg[half:]}} {
		if st, b := uploadSession(t, s, http.MethodPatch, dbase+"/"+id, c.b, dauth, chunk(c.from, c.b)); st != http.StatusAccepted {
			t.Fatalf("append a .deb at %d: status %d: %s", c.from, st, b)
		}
	}
	if st, b := uploadSession(t, s, http.MethodPut, up+id+"&sha256="+sha256Hex(pkg)+"&size="+strconv.Itoa(len(pkg)), nil, s.PublishAuth(t, s.AdminUser, deb), nil); st != http.StatusNotFound {
		t.Fatalf("finish a .deb upload with another publish session: status %d, want 404: %s", st, b)
	}
	if st, b := uploadSession(t, s, http.MethodPut, up+id+"&sha256="+sha256Hex(pkg)+"&size="+strconv.Itoa(len(pkg)), nil, dauth, nil); st != http.StatusCreated ||
		!strings.Contains(string(b), `"package":"e2e-chunked"`) {
		t.Fatalf("finish a .deb upload through the upload route: status %d, want 201 with the package: %s", st, b)
	}
	rawWantGet(t, s, nil, "/debian/"+deb+"/pool/main/e/e2e-chunked/e2e-chunked_1.0_all.deb", s.AdminBasic(), pkg)
	if st, b := uploadSession(t, s, http.MethodPut, up+id+"&sha256="+sha256Hex(pkg), nil, dauth, nil); st != http.StatusNotFound {
		t.Fatalf("finish a finished .deb upload again: status %d, want 404: %s", st, b)
	}

	// The NuGet push finishes a resumable upload of a .nupkg (telesphorepo publish of a large
	// package), with the session as the API key: ?upload=<id>&sha256=<hex>&size=<n> and no body.
	ng := RandName("chunks-nuget")
	s.CreateRepo(t, ng, "nuget", false, nil)
	tok := s.MintSession(t, s.AdminUser, ng).Token
	nauth, key := Bearer{tok}, map[string]string{"X-NuGet-ApiKey": tok}
	nbase := "/api/repos/" + ng + "/uploads"
	nupkg := testNupkg(t, "E2E.Chunked", "1.2.0")
	push := "/nuget/" + ng + "/package?upload="
	id = start(nbase, nauth)
	half = len(nupkg) / 2
	for _, c := range []struct {
		from int
		b    []byte
	}{{0, nupkg[:half]}, {half, nupkg[half:]}} {
		if st, b := uploadSession(t, s, http.MethodPatch, nbase+"/"+id, c.b, nauth, chunk(c.from, c.b)); st != http.StatusAccepted {
			t.Fatalf("append a .nupkg at %d: status %d: %s", c.from, st, b)
		}
	}
	if st, b := uploadSession(t, s, http.MethodPut, push+id+"&sha256="+wrong, nil, nil, key); st != http.StatusBadRequest {
		t.Fatalf("finish a .nupkg upload with a wrong digest: status %d, want 400: %s", st, b)
	}
	id = start(nbase, nauth)
	if st, b := uploadSession(t, s, http.MethodPatch, nbase+"/"+id, nupkg, nauth, nil); st != http.StatusAccepted {
		t.Fatalf("append a .nupkg: status %d: %s", st, b)
	}
	if st, b := uploadSession(t, s, http.MethodPut, push+id+"&sha256="+sha256Hex(nupkg)+"&size="+strconv.Itoa(len(nupkg)), nil, nil, key); st != http.StatusCreated {
		t.Fatalf("finish a .nupkg upload through the push: status %d, want 201: %s", st, b)
	}
	rawWantGet(t, s, nil, "/nuget/"+ng+"/v3-flatcontainer/e2e.chunked/1.2.0/e2e.chunked.1.2.0.nupkg", s.AdminBasic(), nupkg)
	if st, b := uploadSession(t, s, http.MethodPut, push+id+"&sha256="+sha256Hex(nupkg), nil, nil, key); st != http.StatusNotFound {
		t.Fatalf("finish a finished .nupkg upload again: status %d, want 404: %s", st, b)
	}
}

// testNupkg is a .nupkg of package id at version: a zip with the .nuspec at its root and a file.
func testNupkg(t *testing.T, id, version string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{
		id + ".nuspec": `<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://schemas.microsoft.com/packaging/2013/05/nuspec.xsd">
  <metadata>
    <id>` + id + `</id>
    <version>` + version + `</version>
    <authors>e2e</authors>
    <description>A package of the e2e tests.</description>
  </metadata>
</package>
`,
		"content/data.txt": strings.Repeat("e2e nuget chunk\n", 4096),
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestLargeUploadAndRepoGC uploads an 80 MiB file into a raw repo through a resumable upload in
// three chunks, one of them sent again at a wrong offset (416). The checksum headers of the file
// are the MD5, SHA-1 and SHA-256 of the whole file (the hashes carry over from append to append).
// The S3 store holds it as a multipart upload of more than one part, and both stores
// serve it back byte for byte. After a delete of the repo, the blob GC (TR_BLOBS_GC_GRACE=1s)
// removes the object of every file of the repo.
func TestLargeUploadAndRepoGC(t *testing.T) {
	t.Parallel()
	ForEachStorage(t, func(t *testing.T, opts ServerOpts) {
		if opts.S3 == nil {
			opts.DataVolume = NewDataVolume(t)
		}
		opts.Env = map[string]string{"TR_BLOBS_GC_GRACE": "1s"}
		s := StartServer(t, opts)
		repo := RandName("large")
		s.CreateRepo(t, repo, "raw", false, nil)
		auth := s.PublishAuth(t, s.AdminUser, repo)
		base := "/api/repos/" + repo + "/uploads"

		body := rawRandom(t, 80<<20)
		st, b := uploadSession(t, s, http.MethodPost, base, nil, auth, nil)
		var up struct {
			ID string `json:"id"`
		}
		if st != http.StatusCreated || json.Unmarshal(b, &up) != nil || up.ID == "" {
			t.Fatalf("start an upload: status %d: %s", st, b)
		}
		const chunk = 30 << 20
		send := func(from, to int, want int) {
			t.Helper()
			part := body[from:to]
			st, b := uploadSession(t, s, http.MethodPatch, base+"/"+up.ID, part, auth,
				map[string]string{"Content-Range": fmt.Sprintf("bytes %d-%d/*", from, to-1)})
			if st != want {
				t.Fatalf("append bytes %d-%d: status %d, want %d: %s", from, to-1, st, want, b)
			}
		}
		send(0, chunk, http.StatusAccepted)
		send(0, chunk, http.StatusRequestedRangeNotSatisfiable)
		send(chunk, 2*chunk, http.StatusAccepted)
		send(2*chunk, len(body), http.StatusAccepted)
		q := "?path=big/large.bin&sha256=" + sha256Hex(body) + "&size=" + strconv.Itoa(len(body))
		if st, b := uploadSession(t, s, http.MethodPut, base+"/"+up.ID+q, nil, auth, nil); st != http.StatusCreated {
			t.Fatalf("finish the upload: status %d, want 201: %s", st, b)
		}

		path := "/raw/" + repo + "/big/large.bin"
		resp, _ := s.Do(t, s.NewRequest(t, http.MethodHead, path, nil), s.AdminBasic())
		m5, s1 := md5.Sum(body), sha1.Sum(body)
		for h, want := range map[string]string{
			"X-Checksum-MD5": hex.EncodeToString(m5[:]), "X-Checksum-SHA1": hex.EncodeToString(s1[:]), "X-Checksum-SHA256": sha256Hex(body),
		} {
			if got := resp.Header.Get(h); got != want {
				t.Fatalf("HEAD %s: %s %q, want %q", path, h, got, want)
			}
		}
		rawWantGet(t, s, opts.S3, path, s.AdminBasic(), body)
		key := blobKey(sha256Hex(body))
		if tg := opts.S3; tg != nil {
			o, err := tg.Client().HeadObject(context.Background(), &s3.HeadObjectInput{Bucket: &tg.Bucket, Key: aws.String(tg.Prefix + "/" + key)})
			if err != nil {
				t.Fatalf("head the object of the large file: %v", err)
			}
			// The ETag of a multipart upload ends in "-<number of parts>".
			etag := strings.Trim(aws.ToString(o.ETag), `"`)
			i := strings.LastIndex(etag, "-")
			if parts, err := strconv.Atoi(etag[i+1:]); i < 0 || err != nil || parts <= 1 {
				t.Fatalf("the object of an 80 MiB file has ETag %q, want the ETag of a multipart upload of more than one part", etag)
			}
		}

		keys := []string{key}
		for i := range 3 {
			small := rawRandom(t, 4<<10)
			resp, rb := rawPut(t, s, fmt.Sprintf("/raw/%s/small/%d.bin", repo, i), small, auth)
			wantStatus(t, resp, rb, http.StatusCreated)
			keys = append(keys, blobKey(sha256Hex(small)))
		}
		for _, k := range keys {
			if n := countKeys(t, opts, k); n != 1 {
				t.Fatalf("blob %s: %d objects, want 1", k, n)
			}
		}
		deleteRepos(t, s, repo)
		deadline := time.Now().Add(60 * time.Second)
		for {
			stored := storedKeys(t, opts)
			left := slices.DeleteFunc(slices.Clone(keys), func(k string) bool { return !slices.Contains(stored, k) })
			if len(left) == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("60s after the repo delete, the store still holds %v", left)
			}
			time.Sleep(time.Second)
		}
	})
}

// TestMavenMetadataAfterDelete deletes a version folder of a Maven artifact: the server writes
// the artifact's maven-metadata.xml and its existing checksum again without that version.
func TestMavenMetadataAfterDelete(t *testing.T) {
	t.Parallel()
	s := StartServer(t, ServerOpts{})
	repo := RandName("mvn-md")
	s.CreateRepo(t, repo, "maven", false, nil)
	md := `<?xml version="1.0" encoding="UTF-8"?>
<metadata>
  <groupId>com.e2e</groupId>
  <artifactId>lib</artifactId>
  <versioning>
    <latest>2.0</latest>
    <release>2.0</release>
    <versions>
      <version>1.0</version>
      <version>2.0</version>
    </versions>
    <lastUpdated>20260101000000</lastUpdated>
  </versioning>
</metadata>
`
	sum := sha1Hex([]byte(md))
	for p, body := range map[string]string{
		"com/e2e/lib/1.0/lib-1.0.jar":         "jar 1.0",
		"com/e2e/lib/2.0/lib-2.0.jar":         "jar 2.0",
		"com/e2e/lib/maven-metadata.xml":      md,
		"com/e2e/lib/maven-metadata.xml.sha1": sum,
	} {
		mavenPut(t, s, s.AdminUser, repo, p, []byte(body))
	}
	var out struct {
		Deleted int `json:"deleted"`
	}
	s.MustAPI(t, http.MethodPost, "/api/repos/"+repo+"/files/delete", map[string][]string{"prefixes": {"com/e2e/lib/1.0"}}, s.Admin(), &out)
	if out.Deleted != 1 {
		t.Fatalf("delete version 1.0: deleted %d, want 1", out.Deleted)
	}
	st, got := rawGet(t, s, nil, "/maven/"+repo+"/com/e2e/lib/maven-metadata.xml", s.AdminBasic())
	if st != http.StatusOK || strings.Contains(string(got), "<version>1.0</version>") || !strings.Contains(string(got), "<version>2.0</version>") {
		t.Fatalf("maven-metadata.xml after the delete of 1.0: status %d:\n%s", st, got)
	}
	st, gotSum := rawGet(t, s, nil, "/maven/"+repo+"/com/e2e/lib/maven-metadata.xml.sha1", s.AdminBasic())
	if st != http.StatusOK || string(gotSum) != sha1Hex(got) {
		t.Fatalf("maven-metadata.xml.sha1 after the delete: status %d, %q, want %s", st, gotSum, sha1Hex(got))
	}
}

func sha1Hex(b []byte) string {
	s := sha1.Sum(b)
	return hex.EncodeToString(s[:])
}
