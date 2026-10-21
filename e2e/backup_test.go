//go:build e2e

package e2e

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// backupWait bounds one backup in these tests.
const backupWait = 5 * time.Minute

// backupOverview is GET /api/backup.
type backupOverview struct {
	Job struct {
		Running   bool   `json:"running"`
		LastError string `json:"last_error"`
	} `json:"job"`
	NextAt  *time.Time    `json:"next_at"`
	Backups []backupEntry `json:"backups"`
	Error   string        `json:"error"`
}

// backupEntry is one backup of GET /api/backup.
type backupEntry struct {
	Name              string `json:"name"`
	Complete          bool   `json:"complete"`
	Version           string `json:"version"`
	Storage           string `json:"storage"`
	Size              int64  `json:"size"`
	Files             int64  `json:"files"`
	BlobsIncluded     bool   `json:"blobs_included"`
	MasterKeyIncluded bool   `json:"master_key_included"`
}

// backupManifest is the part of backup.json that the tests read.
type backupManifest struct {
	Format        int                                `json:"format"`
	Version       string                             `json:"version"`
	Storage       string                             `json:"storage"`
	BlobsIncluded bool                               `json:"blobs_included"`
	S3            *struct{ Bucket, Prefix string }   `json:"s3"`
	Migrations    []struct{ Component, Name string } `json:"migrations"`
	Counts        struct {
		Repos int64 `json:"repos"`
		Files int64 `json:"files"`
		Blobs int64 `json:"blobs"`
	} `json:"counts"`
	Database struct {
		Size   int64  `json:"size"`
		SHA256 string `json:"sha256"`
	} `json:"database"`
	MasterKeyIncluded bool   `json:"master_key_included"`
	MasterKeySource   string `json:"master_key_source"`
}

func backupList(t *testing.T, s *Server) backupOverview {
	t.Helper()
	var o backupOverview
	s.MustAPI(t, http.MethodGet, "/api/backup", nil, s.Admin(), &o)
	return o
}

// backupIdle waits until no backup runs on s and returns the overview.
func backupIdle(t *testing.T, s *Server) backupOverview {
	t.Helper()
	deadline := time.Now().Add(backupWait)
	for {
		o := backupList(t, s)
		if !o.Job.Running {
			return o
		}
		if time.Now().After(deadline) {
			t.Fatalf("a backup still runs after %s", backupWait)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// backupNow starts a backup through the API, waits for it and returns the name of the new
// backup. A scheduled backup that runs at that moment answers 409; the helper waits for it and
// starts its own.
func backupNow(t *testing.T, s *Server) string {
	t.Helper()
	for {
		st, b := s.API(t, http.MethodPost, "/api/backup/run", nil, s.Admin())
		if st == http.StatusConflict {
			backupIdle(t, s)
			continue
		}
		if st != http.StatusAccepted {
			t.Fatalf("POST /api/backup/run: status %d, want 202: %s", st, b)
		}
		break
	}
	o := backupIdle(t, s)
	if o.Job.LastError != "" || o.Error != "" {
		t.Fatalf("backup failed: %q %q", o.Job.LastError, o.Error)
	}
	for _, b := range o.Backups {
		if b.Complete {
			return b.Name
		}
	}
	t.Fatalf("no complete backup after a run: %+v", o.Backups)
	return ""
}

// startBackupServer starts a server with opts on its own data volume and a backup volume at
// /backups, with backup.dir=/backups and backup.keep=2. It returns the server and the backup
// volume.
func startBackupServer(t *testing.T, opts ServerOpts) (*Server, string) {
	t.Helper()
	bvol := NewDataVolume(t)
	VolumeExec(t, bvol, "set -e\nchown 65532:65532 /data\n")
	opts.DataVolume = NewDataVolume(t)
	opts.Mounts = map[string]string{bvol: "/backups"}
	s := StartServer(t, opts)
	s.MustAPI(t, http.MethodPatch, "/api/settings", map[string]any{"backup.dir": "/backups", "backup.keep": 2}, s.Admin(), nil)
	return s, bvol
}

// readManifest reads backup.json of backup name on the backup volume.
func readManifest(t *testing.T, bvol, name string) backupManifest {
	t.Helper()
	out := VolumeExec(t, bvol, "set -e\ncat /data/"+name+"/backup.json\n")
	var m backupManifest
	if err := json.Unmarshal([]byte(out[strings.Index(out, "{"):]), &m); err != nil {
		t.Fatalf("backup.json of %s: %v\n%s", name, err, out)
	}
	return m
}

// backupBlobs returns the SHA-256 of every file of the shared blobs folder of the backup volume,
// keyed by storage key.
func backupBlobs(t *testing.T, bvol string) map[string]string {
	t.Helper()
	out := VolumeExec(t, bvol, "set -e\ncd /data\nif [ -d blobs ]; then find ./blobs -type f ! -name '.tmp-*' -exec sha256sum {} +; fi\n")
	blobs := map[string]string{}
	for _, m := range sha256sumLineRe.FindAllStringSubmatch(out, -1) {
		blobs[m[2]] = m[1]
	}
	return blobs
}

// runRestore runs "telesphorepo-server restore /backups/<name>" of the server image with the
// backup volume and the data volume vol at /data, and returns the exit code and the output.
func runRestore(t *testing.T, bvol, vol, name string) (int, string) {
	t.Helper()
	return RunClient(t, ServerImage, ClientOpts{
		NoShell: true,
		Cmd:     []string{"restore", "/backups/" + name},
		Mounts:  map[string]string{bvol: "/backups", vol: "/data"},
		Timeout: 5 * time.Minute,
	})
}

// restoredServer starts a server on the restored data volume with opts. Its accounts are the
// accounts of s, so the harness signs in with what it knows of s.
func restoredServer(t *testing.T, s *Server, opts ServerOpts) *Server {
	t.Helper()
	r := StartServer(t, opts)
	r.AdminUser, r.AdminPass = s.AdminUser, s.AdminPass
	s.mu.Lock()
	r.users = s.users
	s.mu.Unlock()
	return r
}

// assetPaths returns the SHA-256 of every file of repo on s, keyed by path.
func assetPaths(t *testing.T, s *Server, repo string) map[string]string {
	t.Helper()
	out := map[string]string{}
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
			out[it.Path] = it.SHA256
		}
		if page.Next == nil || *page.Next == "" {
			return out
		}
		after = *page.Next
	}
}

// TestBackupRestore backs up a server through the API, restores the backup with the restore
// command of the image into a new data volume and starts a server on it. On fs the backup holds
// the blobs, retention keeps two backups and removes the blobs that only the removed ones used,
// and an incomplete backup folder is listed, refused by restore and removed by the next run. On
// S3 the backup holds no blobs and the restored server reads the files from the bucket.
func TestBackupRestore(t *testing.T) {
	t.Parallel()
	t.Run("fs", func(t *testing.T) { t.Parallel(); backupRestoreFS(t) })
	t.Run("seaweedfs", func(t *testing.T) { t.Parallel(); backupRestoreS3(t) })
}

func backupRestoreFS(t *testing.T) {
	pkgs := buildDebPackages(t)
	s, bvol := startBackupServer(t, ServerOpts{})
	mvnRepo, debRepo, rawRepo := RandName("mvn"), RandName("deb"), RandName("raw")
	s.CreateRepo(t, mvnRepo, "maven", false, nil)
	s.CreateRepo(t, debRepo, "debian", false, map[string]any{"architectures": []string{pkgs.arch}})
	s.CreateRepo(t, rawRepo, "raw", false, nil)

	group := "com.example.backup" + randHex(4)
	groupPath := strings.ReplaceAll(group, ".", "/")
	repoURL := func(srv *Server) string { return srv.InternalURL + "/maven/" + mvnRepo }
	msg := "hello from " + group
	m2 := map[string]string{M2Volume: "/root/.m2/repository"}
	code, out := RunClient(t, mavenImage, ClientOpts{
		Cmd: []string{"cd /work/lib && mvn -B -ntp -s /work/settings.xml deploy"},
		Files: append(mavenLibProject(group, "1.0.0", repoURL(s), msg),
			ContainerFile{Path: "/work/settings.xml", Content: mavenSettingsXML(s.AdminUser, PublishToken(t, s, s.AdminUser, mvnRepo))}),
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
	files := moveRawSeed(t, s, rawRepo, "seed", 200, 2048)
	token := PublishToken(t, s, s.AdminUser, rawRepo)
	gone := rawRandom(t, 4096) // deleted after the first backup; only the first backup keeps it
	if err := movePut(s, "/raw/"+rawRepo+"/gone.bin", gone, token); err != nil {
		t.Fatal(err)
	}
	files["gone.bin"] = gone
	before := moveAssets(t, s, mvnRepo, debRepo, rawRepo)

	// Uploads continue while the backup runs.
	var mu sync.Mutex
	during := map[string][]byte{}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var upErr error
	wg.Go(func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			p := fmt.Sprintf("during/f%05d.bin", i)
			body := make([]byte, 2048)
			rand.Read(body)
			if err := movePut(s, "/raw/"+rawRepo+"/"+p, body, token); err != nil {
				upErr = err
				return
			}
			mu.Lock()
			during[p] = body
			mu.Unlock()
		}
	})
	name := backupNow(t, s)
	close(stop)
	wg.Wait()
	if upErr != nil {
		t.Fatal(upErr)
	}
	t.Logf("backup %s; %d uploads while it ran", name, len(during))

	// The folder: the database, the master key (owner only) and backup.json, which names the
	// database's SHA-256.
	m := readManifest(t, bvol, name)
	core := slices.ContainsFunc(m.Migrations, func(x struct{ Component, Name string }) bool {
		return x.Component == "core" && x.Name == "0001_init.sql"
	})
	if m.Format != 1 || m.Version == "" || m.Storage != "fs" || !m.BlobsIncluded || !m.MasterKeyIncluded ||
		m.MasterKeySource != "file" || !core || m.Counts.Repos < 3 || m.Counts.Blobs < int64(len(before)) {
		t.Fatalf("backup.json of %s: %+v", name, m)
	}
	ls := VolumeExec(t, bvol, "set -e\ncd /data/"+name+"\nls -1\nsha256sum telesphorepo.db\nstat -c 'mode %a' master.key telesphorepo.db\n")
	for _, want := range []string{"backup.json\n", "master.key\n", "telesphorepo.db\n", m.Database.SHA256 + "  telesphorepo.db", "mode 600\nmode 600"} {
		if !strings.Contains(ls, want) {
			t.Fatalf("backup folder %s: no %q in:\n%s", name, want, ls)
		}
	}
	o := backupList(t, s)
	if len(o.Backups) == 0 || o.Backups[0].Name != name || !o.Backups[0].Complete || !o.Backups[0].BlobsIncluded ||
		!o.Backups[0].MasterKeyIncluded || o.Backups[0].Size <= m.Database.Size || o.NextAt == nil {
		t.Fatalf("GET /api/backup after the backup: %+v", o)
	}
	moveWantBlobs(t, "backup blobs folder", backupBlobs(t, bvol), before)

	// Restore into a new volume with the server binary, then start a server on it.
	rvol := NewDataVolume(t)
	code, out = runRestore(t, bvol, rvol, name)
	if code != 0 || !strings.Contains(out, "Restored the backup "+name) || !strings.Contains(out, "master.key") {
		t.Fatalf("restore: exit %d\n%s", code, out)
	}
	if code, out := runRestore(t, bvol, rvol, name); code == 0 || !strings.Contains(out, "not empty") {
		t.Fatalf("restore into a data folder that is not empty: exit %d, want a refusal:\n%s", code, out)
	}
	moveWantBlobs(t, "restored store", FSBlobs(t, rvol), before)
	r := restoredServer(t, s, ServerOpts{DataVolume: rvol})
	moveWantBlobs(t, "restored assets", moveAssets(t, r, mvnRepo, debRepo, rawRepo), before)
	moveWantRaw(t, r, rawRepo, files)
	restored := assetPaths(t, r, rawRepo)
	mu.Lock()
	for p, sum := range restored {
		if !strings.HasPrefix(p, "during/") {
			continue
		}
		body, ok := during[p]
		if !ok || sha256Hex(body) != sum {
			t.Fatalf("restored %s has a file %s that no upload wrote", rawRepo, p)
		}
		rawWantGet(t, r, nil, "/raw/"+rawRepo+"/"+p, r.AdminBasic(), body)
	}
	mu.Unlock()

	// Real clients install from the restored server.
	script := strings.Join([]string{
		"set -e",
		"rm -rf /root/.m2/repository/" + groupPath,
		"cd /work/consumer",
		"mvn -B -ntp -q -U -s /work/settings.xml -Ddep.version=1.0.0 -Dmdep.outputFile=/tmp/cp.txt org.apache.maven.plugins:maven-dependency-plugin:3.8.1:build-classpath",
		`java -cp "$(cat /tmp/cp.txt)" ` + group + ".Hello",
	}, "\n")
	code, out = RunClient(t, mavenImage, ClientOpts{
		Cmd:     []string{script},
		Files:   []ContainerFile{{Path: "/work/settings.xml", Content: mavenSettingsXML(r.AdminUser, r.AdminPass)}, mavenConsumerProject(group, repoURL(r))},
		Mounts:  m2,
		Timeout: 15 * time.Minute,
	})
	if code != 0 || !strings.Contains(out, msg) {
		t.Fatalf("maven consumer on the restored server: exit %d, want 0 with %q:\n%s", code, msg, out)
	}
	key := r.DebianKey(t, debRepo, r.AdminBasic())
	if key != s.DebianKey(t, debRepo, s.AdminBasic()) {
		t.Fatal("the restored server signs with another key than the backed-up one")
	}
	out = debAptInstall(t, r, debRepo, r.AdminUser, r.AdminPass, key)
	if !strings.Contains(out, "RESULT: hello 1.0\n") || !strings.Contains(out, "NATIVE: native "+pkgs.arch) {
		t.Fatalf("apt install from the restored server:\n%s", out)
	}

	// Retention with keep=2: after two more backups the first one goes, and with it the blob
	// that only it used. An incomplete folder shows in the list, restore refuses it, and the next
	// backup removes it.
	s.MustAPI(t, http.MethodPost, "/api/repos/"+rawRepo+"/files/delete", map[string][]string{"paths": {"gone.bin"}}, s.Admin(), nil)
	kept := rawRandom(t, 4096)
	if err := movePut(s, "/raw/"+rawRepo+"/kept.bin", kept, token); err != nil {
		t.Fatal(err)
	}
	second := backupNow(t, s)
	const partial = "20200101T000000.000Z"
	VolumeExec(t, bvol, "set -e\nmkdir /data/"+partial+"\necho partial > /data/"+partial+"/telesphorepo.db\ntouch -d '2020-01-01 00:00:00' /data/"+partial+"\nchown -R 65532:65532 /data/"+partial+"\n")
	o = backupList(t, s)
	if i := slices.IndexFunc(o.Backups, func(b backupEntry) bool { return b.Name == partial }); i < 0 || o.Backups[i].Complete {
		t.Fatalf("GET /api/backup with an incomplete folder: %+v, want %s listed as incomplete", o.Backups, partial)
	}
	if code, out := runRestore(t, bvol, NewDataVolume(t), partial); code == 0 || !strings.Contains(out, "backup.json") {
		t.Fatalf("restore of an incomplete backup: exit %d, want a refusal that names backup.json:\n%s", code, out)
	}
	// Retention leaves a blob file alone for an hour after its last write (a backup from the
	// command line may be writing into the folder): the blob of gone.bin is made old, and a fresh
	// file that no backup uses stays.
	goneKey := blobKey(sha256Hex(gone))
	fresh := blobKey(strings.Repeat("f", 64))
	VolumeExec(t, bvol, "set -e\ntouch -d '2020-01-01 00:00:00' /data/"+goneKey+"\nmkdir -p /data/"+fresh[:strings.LastIndex(fresh, "/")]+
		"\necho fresh > /data/"+fresh+"\nchown -R 65532:65532 /data/blobs\n")
	third := backupNow(t, s)

	var names []string
	for _, b := range backupList(t, s).Backups {
		names = append(names, b.Name)
	}
	if !slices.Equal(names, []string{third, second}) {
		t.Fatalf("backups after retention: %v, want [%s %s]", names, third, second)
	}
	top := VolumeExec(t, bvol, "set -e\nls -1 /data\n")
	if strings.Contains(top, name) || strings.Contains(top, partial) {
		t.Fatalf("the backup volume still holds %s or %s:\n%s", name, partial, top)
	}
	have := backupBlobs(t, bvol)
	if _, ok := have[goneKey]; ok {
		t.Fatal("retention kept the blob that only the removed backup used")
	}
	if _, ok := have[fresh]; !ok {
		t.Fatal("retention removed a blob file younger than an hour")
	}
	moveWantBlobs(t, "backup blobs folder after retention", have, moveAssets(t, s, mvnRepo, debRepo, rawRepo))
}

func backupRestoreS3(t *testing.T) {
	tg := StartSeaweedFS(t)
	s, bvol := startBackupServer(t, ServerOpts{Storage: "s3", S3: tg})
	repo := RandName("raw")
	s.CreateRepo(t, repo, "raw", false, nil)
	files := moveRawSeed(t, s, repo, "seed", 50, 2048)
	name := backupNow(t, s)

	m := readManifest(t, bvol, name)
	core := slices.ContainsFunc(m.Migrations, func(x struct{ Component, Name string }) bool {
		return x.Component == "core" && x.Name == "0001_init.sql"
	})
	if m.Storage != "s3" || m.BlobsIncluded || m.S3 == nil || m.S3.Bucket != tg.Bucket || m.S3.Prefix != tg.Prefix || !m.MasterKeyIncluded {
		t.Fatalf("backup.json of an S3 server: %+v", m)
	}
	if out := VolumeExec(t, bvol, "if [ -e /data/blobs ]; then echo HAS-BLOBS; fi\n"); strings.Contains(out, "HAS-BLOBS") {
		t.Fatal("the backup of an S3 server wrote a blobs folder")
	}
	if o := backupList(t, s); len(o.Backups) == 0 || o.Backups[0].BlobsIncluded || o.Backups[0].Storage != "s3" {
		t.Fatalf("GET /api/backup on an S3 server: %+v", o.Backups)
	}

	rvol := NewDataVolume(t)
	code, out := runRestore(t, bvol, rvol, name)
	if code != 0 || !strings.Contains(out, "S3 bucket "+tg.Bucket) {
		t.Fatalf("restore of an S3 backup: exit %d, want it to name the bucket %s:\n%s", code, tg.Bucket, out)
	}
	r := restoredServer(t, s, ServerOpts{Storage: "s3", S3: tg, DataVolume: rvol})
	for p, body := range files {
		rawWantGet(t, r, tg, "/raw/"+repo+"/"+p, r.AdminBasic(), body)
	}
}
