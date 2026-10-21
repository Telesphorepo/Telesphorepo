//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// moveWait bounds one storage move in these tests.
const moveWait = 5 * time.Minute

// moveS3Patch is the settings PATCH that moves storage to tg.
func moveS3Patch(tg *S3Target) map[string]any {
	return map[string]any{
		"storage":              "s3",
		"s3.endpoint":          tg.Endpoint,
		"s3.region":            tg.Region,
		"s3.bucket":            tg.Bucket,
		"s3.access_key_id":     tg.AccessKey,
		"s3.secret_access_key": tg.SecretKey,
		"s3.prefix":            tg.Prefix,
		"s3.path_style":        true,
		"s3.redirect":          false,
	}
}

// moveFSPatch is the settings PATCH that moves storage to the default fs root.
func moveFSPatch() map[string]any { return map[string]any{"storage": "fs"} }

// blobKey is the storage key of the blob with SHA-256 sum: blobs/sha256/<first 2 hex>/<hex>.
func blobKey(sum string) string { return "blobs/sha256/" + sum[:2] + "/" + sum }

// movePut uploads body to path with the publish-session token and requires 201. It does not
// take t, so worker goroutines can call it.
func movePut(s *Server, p string, body []byte, token string) error {
	req, err := http.NewRequest(http.MethodPut, s.URL+p, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.SetBasicAuth(s.AdminUser, token)
	resp, err := s.HTTPClient(nil).Do(req)
	if err != nil {
		return fmt.Errorf("PUT %s: %w", p, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("PUT %s: status %d, want 201: %s", p, resp.StatusCode, b)
	}
	return nil
}

// moveRawSeed uploads n random files of size bytes under dir of raw repo with 8 parallel
// clients and returns their contents by repo path.
func moveRawSeed(t *testing.T, s *Server, repo, dir string, n, size int) map[string][]byte {
	t.Helper()
	files := make(map[string][]byte, n)
	paths := make([]string, 0, n)
	for i := range n {
		p := fmt.Sprintf("%s/f%05d.bin", dir, i)
		files[p] = rawRandom(t, size)
		paths = append(paths, p)
	}
	token := PublishToken(t, s, s.AdminUser, repo)
	jobs := make(chan string)
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for p := range jobs {
				errs <- movePut(s, "/raw/"+repo+"/"+p, files[p], token)
			}
		})
	}
	for _, p := range paths {
		jobs <- p
	}
	close(jobs)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	return files
}

// moveAssets returns the SHA-256 of the blob of every asset of the repos, keyed by its storage
// key, from the asset API. Assets with the same bytes share one blob.
func moveAssets(t *testing.T, s *Server, repos ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, repo := range repos {
		after := ""
		for {
			var page struct {
				Items []struct {
					Path   string `json:"path"`
					SHA256 string `json:"sha256"`
				} `json:"items"`
				Next *string `json:"next"`
			}
			q := "/api/repos/" + repo + "/assets?limit=200"
			if after != "" {
				q += "&after=" + url.QueryEscape(after)
			}
			s.MustAPI(t, http.MethodGet, q, nil, s.Admin(), &page)
			for _, it := range page.Items {
				out[blobKey(it.SHA256)] = it.SHA256
			}
			if page.Next == nil || *page.Next == "" {
				break
			}
			after = *page.Next
		}
	}
	return out
}

// moveWantBlobs fails unless have holds every key of want with the same SHA-256.
func moveWantBlobs(t *testing.T, store string, have, want map[string]string) {
	t.Helper()
	var bad []string
	for k, sum := range want {
		switch got, ok := have[k]; {
		case !ok:
			bad = append(bad, "missing "+k)
		case got != sum:
			bad = append(bad, fmt.Sprintf("%s: sha256 %s, want %s", k, got, sum))
		}
	}
	if len(bad) > 0 {
		slices.Sort(bad)
		t.Fatalf("%s store: %d of %d blobs wrong (store holds %d), first: %s",
			store, len(bad), len(want), len(have), strings.Join(bad[:min(len(bad), 5)], "; "))
	}
}

// moveWantStorage fails unless the active "storage" setting is want.
func moveWantStorage(t *testing.T, s *Server, want string) {
	t.Helper()
	items, _ := listSettings(t, s)
	if got := string(items["storage"].Value); got != `"`+want+`"` {
		t.Fatalf("storage setting = %s, want %q", got, want)
	}
}

// moveWantDone fails unless m ended as done with every asset copied or skipped.
func moveWantDone(t *testing.T, s *Server, m Move) {
	t.Helper()
	if m.State != "done" || m.Failed != 0 || m.Error != "" {
		t.Fatalf("move %d ended %+v, want done; failures: %+v", m.ID, m, s.MoveFailures(t, m.ID))
	}
	if m.Copied+m.Skipped < m.Total {
		t.Fatalf("move %d done with copied %d + skipped %d < total %d", m.ID, m.Copied, m.Skipped, m.Total)
	}
}

// moveWantRaw downloads every file of the raw repo and compares the bytes.
func moveWantRaw(t *testing.T, s *Server, repo string, files map[string][]byte) {
	t.Helper()
	for p, body := range files {
		rawWantGet(t, s, nil, "/raw/"+repo+"/"+p, s.AdminBasic(), body)
	}
}

// TestStorageMove moves Maven, Debian and raw content between fs and SeaweedFS in both
// directions while uploads continue, then checks both stores and real clients.
func TestStorageMove(t *testing.T) {
	t.Parallel()
	pkgs := buildDebPackages(t)

	t.Run("fs-to-seaweedfs", func(t *testing.T) {
		tg := StartSeaweedFS(t)
		vol := NewDataVolume(t)
		s := StartServer(t, ServerOpts{DataVolume: vol})
		moveContent(t, s, pkgs, moveS3Patch(tg), "s3",
			func(t *testing.T) map[string]string { return FSBlobs(t, vol) },
			func(t *testing.T) map[string]string { return tg.Blobs(t) })
	})

	t.Run("seaweedfs-to-fs", func(t *testing.T) {
		tg := StartSeaweedFS(t)
		vol := NewDataVolume(t)
		s := StartServer(t, ServerOpts{DataVolume: vol})
		// A server without assets switches at once; no move starts.
		if st, b := s.API(t, http.MethodPatch, "/api/settings", moveS3Patch(tg), s.Admin()); st != http.StatusOK {
			t.Fatalf("PATCH to s3 on an empty server: status %d, want 200: %s", st, b)
		}
		moveWantStorage(t, s, "s3")
		moveContent(t, s, pkgs, moveFSPatch(), "fs",
			func(t *testing.T) map[string]string { return tg.Blobs(t) },
			func(t *testing.T) map[string]string { return FSBlobs(t, vol) })
	})
}

// moveContent fills s with Maven, Debian and raw content, moves it with patch and checks the
// result. src and dst list the blobs of the source and target stores.
func moveContent(t *testing.T, s *Server, pkgs *debPackages, patch map[string]any, wantStorage string,
	src, dst func(t *testing.T) map[string]string) {
	admin := s.Admin()
	mvnRepo, debRepo, rawRepo := RandName("mvn"), RandName("deb"), RandName("raw")
	s.CreateRepo(t, mvnRepo, "maven", false, nil)
	s.CreateRepo(t, debRepo, "debian", false, map[string]any{"architectures": []string{pkgs.arch}})
	s.CreateRepo(t, rawRepo, "raw", false, nil)

	group := "com.example.move" + randHex(4)
	groupPath := strings.ReplaceAll(group, ".", "/")
	repoURL := s.InternalURL + "/maven/" + mvnRepo
	msg := "hello from " + group
	settingsXML := ContainerFile{Path: "/work/settings.xml", Content: mavenSettingsXML(s.AdminUser, s.AdminPass)}
	deploySettings := ContainerFile{Path: "/work/settings.xml", Content: mavenSettingsXML(s.AdminUser, PublishToken(t, s, s.AdminUser, mvnRepo))}
	m2 := map[string]string{M2Volume: "/root/.m2/repository"}
	code, out := RunClient(t, mavenImage, ClientOpts{
		Cmd:     []string{"cd /work/lib && mvn -B -ntp -s /work/settings.xml deploy"},
		Files:   append(mavenLibProject(group, "1.0.0", repoURL, msg), deploySettings),
		Mounts:  m2,
		Timeout: 15 * time.Minute,
	})
	if code != 0 {
		t.Fatalf("mvn deploy: exit %d\n%s", code, out)
	}

	debAuth := s.PublishAuth(t, s.AdminUser, debRepo)
	debMustUpload(t, s, debRepo, pkgs.hello1, debAuth, "e2e-hello", "1.0", "all")
	debMustUpload(t, s, debRepo, pkgs.native, debAuth, "e2e-native", "1.0", pkgs.arch)
	s.WaitDebianIndexed(t, debRepo)

	raw := moveRawSeed(t, s, rawRepo, "seed", 500, 2048)

	before := moveAssets(t, s, mvnRepo, debRepo, rawRepo)
	moveWantBlobs(t, "source (before the move)", src(t), before)

	m := s.StartMove(t, patch)
	// The total counts every blob row, also blobs that no asset uses any more and that wait for the GC.
	if m.State != "copying" || m.Total < int64(len(before)) {
		t.Fatalf("new move %+v, want copying with total >= %d", m, len(before))
	}

	// Uploads while the move copies go to both stores. An upload counts only when the move
	// still reported unfinished work after the upload returned, so it ran before the swap.
	during := map[string][]byte{}
	duringToken := PublishToken(t, s, s.AdminUser, rawRepo)
	for i := range 300 {
		p := fmt.Sprintf("during/f%05d.bin", i)
		body := rawRandom(t, 2048)
		if err := movePut(s, "/raw/"+rawRepo+"/"+p, body, duringToken); err != nil {
			t.Fatal(err)
		}
		cur := s.GetMove(t, m.ID)
		if cur.State != "copying" || cur.Copied+cur.Skipped+cur.Failed >= cur.Total {
			raw[p] = body
			break
		}
		during[p] = body
		raw[p] = body
	}
	if len(during) == 0 {
		t.Fatal("the move finished before the first upload during the move returned; seed more assets")
	}
	t.Logf("%d uploads during the move", len(during))

	m = s.WaitMove(t, m.ID, moveWait)
	moveWantDone(t, s, m)
	moveWantStorage(t, s, wantStorage)
	if secret, _ := patch["s3.secret_access_key"].(string); secret != "" {
		_, b := s.API(t, http.MethodGet, fmt.Sprintf("/api/storage/moves/%d", m.ID), nil, admin)
		if bytes.Contains(b, []byte(secret)) {
			t.Fatal("GET /api/storage/moves/{id} leaks the S3 secret access key")
		}
	}

	// The target holds every asset; the source still holds every blob it had, plus the
	// uploads made during the move.
	after := moveAssets(t, s, mvnRepo, debRepo, rawRepo)
	moveWantBlobs(t, "target", dst(t), after)
	srcWant := maps.Clone(before)
	for _, body := range during {
		srcWant[blobKey(sha256Hex(body))] = sha256Hex(body)
	}
	moveWantBlobs(t, "source", src(t), srcWant)

	moveWantRaw(t, s, rawRepo, raw)

	// Real clients resolve from the new store.
	script := strings.Join([]string{
		"set -e",
		"rm -rf /root/.m2/repository/" + groupPath,
		"cd /work/consumer",
		"mvn -B -ntp -q -U -s /work/settings.xml -Ddep.version=1.0.0 -Dmdep.outputFile=/tmp/cp.txt org.apache.maven.plugins:maven-dependency-plugin:3.8.1:build-classpath",
		`java -cp "$(cat /tmp/cp.txt)" ` + group + ".Hello",
	}, "\n")
	code, out = RunClient(t, mavenImage, ClientOpts{
		Cmd:     []string{script},
		Files:   []ContainerFile{settingsXML, mavenConsumerProject(group, repoURL)},
		Mounts:  m2,
		Timeout: 15 * time.Minute,
	})
	if code != 0 || !strings.Contains(out, msg) {
		t.Fatalf("maven consumer after the move: exit %d, want 0 with %q:\n%s", code, msg, out)
	}
	key := s.DebianKey(t, debRepo, s.AdminBasic())
	out = debAptInstall(t, s, debRepo, s.AdminUser, s.AdminPass, key)
	if !strings.Contains(out, "RESULT: hello 1.0\n") || !strings.Contains(out, "NATIVE: native "+pkgs.arch) {
		t.Fatalf("apt install after the move did not produce expected output:\n%s", out)
	}

	// A new upload lands in the new store.
	body := rawRandom(t, 4096)
	if err := movePut(s, "/raw/"+rawRepo+"/after/new.bin", body, PublishToken(t, s, s.AdminUser, rawRepo)); err != nil {
		t.Fatal(err)
	}
	moveWantBlobs(t, "target (upload after the move)", dst(t),
		map[string]string{blobKey(sha256Hex(body)): sha256Hex(body)})
}

// TestStorageMoveFailure removes one blob from the source: the move fails with that blob in
// its failures and storage stays on the source. After the blob is restored, a second move
// skips what the first one copied and finishes.
func TestStorageMoveFailure(t *testing.T) {
	t.Parallel()
	tg := StartSeaweedFS(t)
	vol := NewDataVolume(t)
	s := StartServer(t, ServerOpts{DataVolume: vol})
	repo := RandName("raw")
	s.CreateRepo(t, repo, "raw", false, nil)
	files := moveRawSeed(t, s, repo, "seed", 20, 4096)

	lost := "seed/f00007.bin"
	lostKey := blobKey(sha256Hex(files[lost]))
	VolumeExec(t, vol, "set -e\nrm /data/blobs/"+lostKey+"\n")

	m := s.StartMove(t, moveS3Patch(tg))
	m = s.WaitMove(t, m.ID, moveWait)
	if m.State != "failed" || m.Failed != 1 {
		t.Fatalf("move with a missing source blob ended %+v, want failed with 1 failure", m)
	}
	fails := s.MoveFailures(t, m.ID)
	if len(fails) != 1 || fails[0].Key != lostKey || fails[0].Reason == "" {
		t.Fatalf("move failures = %+v, want one row for %s with a reason", fails, lostKey)
	}
	moveWantStorage(t, s, "fs")

	// The server stays on the source store for reads and writes.
	rest := map[string][]byte{}
	for p, b := range files {
		if p != lost {
			rest[p] = b
		}
	}
	moveWantRaw(t, s, repo, rest)
	body := rawRandom(t, 4096)
	if err := movePut(s, "/raw/"+repo+"/after/new.bin", body, PublishToken(t, s, s.AdminUser, repo)); err != nil {
		t.Fatal(err)
	}
	newKey := blobKey(sha256Hex(body))
	moveWantBlobs(t, "source (upload after the failed move)", FSBlobs(t, vol), map[string]string{newKey: sha256Hex(body)})

	// Restore the blob and start again.
	VolumeExec(t, vol, "set -e\ncp /restore.bin /data/blobs/"+lostKey+"\nchmod 0644 /data/blobs/"+lostKey+"\n",
		ContainerFile{Path: "/restore.bin", Content: string(files[lost])})
	m2 := s.StartMove(t, moveS3Patch(tg))
	m2 = s.WaitMove(t, m2.ID, moveWait)
	moveWantDone(t, s, m2)
	if m2.Skipped < int64(len(files)-1) {
		t.Fatalf("second move skipped %d, want >= %d (blobs the first move copied)", m2.Skipped, len(files)-1)
	}
	moveWantStorage(t, s, "s3")
	moveWantBlobs(t, "target", tg.Blobs(t), moveAssets(t, s, repo))
	files["after/new.bin"] = body
	moveWantRaw(t, s, repo, files)
}

// TestStorageMoveRetry fails a move with a blob missing in the source, then tries it again
// through the retry route of the Storage page: the target settings come without the secret, a
// retry with a changed setting keeps the sealed secret and finishes, the failed move is put
// away, and a dismiss returns to no current move.
func TestStorageMoveRetry(t *testing.T) {
	t.Parallel()
	tg := StartSeaweedFS(t)
	vol := NewDataVolume(t)
	s := StartServer(t, ServerOpts{DataVolume: vol})
	admin := s.Admin()
	repo := RandName("raw")
	s.CreateRepo(t, repo, "raw", false, nil)
	files := moveRawSeed(t, s, repo, "seed", 20, 4096)

	lost := "seed/f00011.bin"
	lostKey := blobKey(sha256Hex(files[lost]))
	VolumeExec(t, vol, "set -e\nrm /data/blobs/"+lostKey+"\n")

	m := s.StartMove(t, moveS3Patch(tg))
	m = s.WaitMove(t, m.ID, moveWait)
	if m.State != "failed" || m.Failed != 1 {
		t.Fatalf("move with a missing source blob ended %+v, want failed with 1 failure", m)
	}
	moveWantStorage(t, s, "fs")

	// The current move carries its target settings for the form, without the secret.
	st, b := s.API(t, http.MethodGet, "/api/storage/moves/current", nil, admin)
	if st != http.StatusOK {
		t.Fatalf("GET current move: status %d: %s", st, b)
	}
	if bytes.Contains(b, []byte(tg.SecretKey)) {
		t.Fatal("GET /api/storage/moves/current leaks the S3 secret access key")
	}
	var cur Move
	if err := json.Unmarshal(b, &cur); err != nil {
		t.Fatal(err)
	}
	ts := cur.TargetSettings
	if cur.ID != m.ID || ts["storage"] != "s3" || ts["s3.bucket"] != tg.Bucket || ts["s3.access_key_id"] != tg.AccessKey {
		t.Fatalf("current move %d target settings %v, want move %d to bucket %s", cur.ID, ts, m.ID, tg.Bucket)
	}
	if sec, _ := ts["s3.secret_access_key"].(map[string]any); sec == nil || sec["set"] != true || len(sec) != 1 {
		t.Fatalf("target secret = %v, want {set: true}", ts["s3.secret_access_key"])
	}

	// The test of a retry probes the sealed target; a key of another group is refused.
	var ok struct {
		OK bool `json:"ok"`
	}
	s.MustAPI(t, http.MethodPost, fmt.Sprintf("/api/storage/moves/%d/test", m.ID), map[string]any{}, admin, &ok)
	if !ok.OK {
		t.Fatal("test of the retry target: ok false")
	}
	if st, b := s.API(t, http.MethodPost, fmt.Sprintf("/api/storage/moves/%d/retry", m.ID), map[string]any{"session_ttl": "5h"}, admin); st != http.StatusBadRequest {
		t.Fatalf("retry with a key outside the storage group: status %d, want 400: %s", st, b)
	}
	// A password reaches neither route.
	if st, b := s.API(t, http.MethodPost, fmt.Sprintf("/api/storage/moves/%d/retry", m.ID), map[string]any{}, s.AdminBasic()); st != http.StatusUnauthorized {
		t.Fatalf("retry with the admin's password: status %d, want 401: %s", st, b)
	}

	// Restore the blob and try again with one changed setting; the secret stays the target's
	// ("" keeps it), so the move can only finish with the sealed secret.
	VolumeExec(t, vol, "set -e\ncp /restore.bin /data/blobs/"+lostKey+"\nchmod 0644 /data/blobs/"+lostKey+"\n",
		ContainerFile{Path: "/restore.bin", Content: string(files[lost])})
	st, b = s.API(t, http.MethodPost, fmt.Sprintf("/api/storage/moves/%d/retry", m.ID),
		map[string]any{"s3.redirect": true, "s3.secret_access_key": ""}, admin)
	if st != http.StatusAccepted {
		t.Fatalf("retry: status %d, want 202: %s", st, b)
	}
	var retried struct {
		Move *Move `json:"move"`
	}
	if err := json.Unmarshal(b, &retried); err != nil || retried.Move == nil || retried.Move.ID == m.ID {
		t.Fatalf("retry answered %s, want a new move", b)
	}
	m2 := s.WaitMove(t, retried.Move.ID, moveWait)
	moveWantDone(t, s, m2)
	if m2.Skipped < int64(len(files)-1) {
		t.Fatalf("retried move skipped %d, want >= %d (blobs the failed move copied)", m2.Skipped, len(files)-1)
	}
	moveWantStorage(t, s, "s3")
	items, _ := listSettings(t, s)
	if got := string(items["s3.redirect"].Value); got != "true" {
		t.Fatalf("s3.redirect after the retry = %s, want true", got)
	}
	moveWantBlobs(t, "target", tg.Blobs(t), moveAssets(t, s, repo))
	// With s3.redirect on, a download answers 302 with a presigned URL on the target: the GET
	// follows it at the host address of SeaweedFS.
	for p, body := range files {
		rawWantGet(t, s, tg, "/raw/"+repo+"/"+p, s.AdminBasic(), body)
	}

	// The failed move is put away; the done one cannot be tried again.
	if old := s.GetMove(t, m.ID); old.DismissedAt == nil {
		t.Fatalf("failed move after the retry: %+v, want dismissed", old)
	}
	if ev := auditEvents(t, s, "storage.move.retry", ""); len(ev) != 1 || ev[0].Target != strconv.FormatInt(m.ID, 10) {
		t.Fatalf("storage.move.retry events = %+v, want one for move %d", ev, m.ID)
	}
	if st, b := s.API(t, http.MethodPost, fmt.Sprintf("/api/storage/moves/%d/retry", m2.ID), map[string]any{}, admin); st != http.StatusConflict {
		t.Fatalf("retry of a done move: status %d, want 409: %s", st, b)
	}

	// Dismiss returns the page to no move.
	s.MustAPI(t, http.MethodGet, "/api/storage/moves/current", nil, admin, &cur)
	if cur.ID != m2.ID {
		t.Fatalf("current move = %d, want %d", cur.ID, m2.ID)
	}
	s.MustAPI(t, http.MethodPost, fmt.Sprintf("/api/storage/moves/%d/dismiss", m2.ID), nil, admin, &cur)
	if cur.DismissedAt == nil {
		t.Fatalf("dismissed move %+v, want dismissed_at", cur)
	}
	if st, b := s.API(t, http.MethodGet, "/api/storage/moves/current", nil, admin); st != http.StatusNotFound {
		t.Fatalf("current move after the dismiss: status %d, want 404: %s", st, b)
	}
	if ev := auditEvents(t, s, "storage.move.dismiss", ""); len(ev) != 1 || ev[0].Target != strconv.FormatInt(m2.ID, 10) {
		t.Fatalf("storage.move.dismiss events = %+v, want one for move %d", ev, m2.ID)
	}
}

// TestStorageMoveRestart restarts the server while a move copies; the move resumes and
// finishes.
func TestStorageMoveRestart(t *testing.T) {
	t.Parallel()
	tg := StartSeaweedFS(t)
	vol := NewDataVolume(t)
	s := StartServer(t, ServerOpts{DataVolume: vol})
	repo := RandName("raw")
	s.CreateRepo(t, repo, "raw", false, nil)
	files := moveRawSeed(t, s, repo, "seed", 1500, 1024)
	before := moveAssets(t, s, repo)

	m := s.StartMove(t, moveS3Patch(tg))
	if cur := s.GetMove(t, m.ID); cur.State != "copying" {
		t.Fatalf("move ended before the restart: %+v; seed more assets", cur)
	}
	s.Restart(t)

	m = s.WaitMove(t, m.ID, moveWait)
	moveWantDone(t, s, m)
	moveWantStorage(t, s, "s3")
	moveWantBlobs(t, "target", tg.Blobs(t), moveAssets(t, s, repo))
	moveWantBlobs(t, "source", FSBlobs(t, vol), before)
	moveWantRaw(t, s, repo, files)
}

// TestStorageMoveCancel cancels a running move: storage drops back to the source, and a new
// move can start afterwards.
func TestStorageMoveCancel(t *testing.T) {
	t.Parallel()
	tg := StartSeaweedFS(t)
	vol := NewDataVolume(t)
	s := StartServer(t, ServerOpts{DataVolume: vol})
	repo := RandName("raw")
	s.CreateRepo(t, repo, "raw", false, nil)
	files := moveRawSeed(t, s, repo, "seed", 1500, 1024)
	before := moveAssets(t, s, repo)

	m := s.StartMove(t, moveS3Patch(tg))
	if st, b := s.API(t, http.MethodPatch, "/api/settings", moveS3Patch(tg), s.Admin()); st != http.StatusConflict {
		t.Fatalf("second storage PATCH during a move: status %d, want 409: %s", st, b)
	}
	var cur Move
	s.MustAPI(t, http.MethodGet, "/api/storage/moves/current", nil, s.Admin(), &cur)
	if cur.ID != m.ID || cur.State != "copying" {
		t.Fatalf("current move = %+v, want move %d copying", cur, m.ID)
	}
	if st, b := s.API(t, http.MethodDelete, fmt.Sprintf("/api/storage/moves/%d", m.ID), nil, s.Admin()); st/100 != 2 {
		t.Fatalf("DELETE move: status %d: %s", st, b)
	}
	m = s.WaitMove(t, m.ID, moveWait)
	if m.State != "cancelled" {
		t.Fatalf("cancelled move ended %+v, want cancelled", m)
	}
	moveWantStorage(t, s, "fs")

	// Reads and writes use the source store again.
	moveWantRaw(t, s, repo, files)
	body := rawRandom(t, 4096)
	if err := movePut(s, "/raw/"+repo+"/after/new.bin", body, PublishToken(t, s, s.AdminUser, repo)); err != nil {
		t.Fatal(err)
	}
	files["after/new.bin"] = body
	srcWant := maps.Clone(before)
	srcWant[blobKey(sha256Hex(body))] = sha256Hex(body)
	moveWantBlobs(t, "source", FSBlobs(t, vol), srcWant)

	m2 := s.StartMove(t, moveS3Patch(tg))
	if m2.ID == m.ID {
		t.Fatalf("new move reuses id %d", m.ID)
	}
	moveWantDone(t, s, s.WaitMove(t, m2.ID, moveWait))
	moveWantStorage(t, s, "s3")
	moveWantBlobs(t, "target", tg.Blobs(t), moveAssets(t, s, repo))
	moveWantRaw(t, s, repo, files)
}
