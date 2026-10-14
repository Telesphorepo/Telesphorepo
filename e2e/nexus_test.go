//go:build import

package e2e

import (
	"bytes"
	"context"
	"crypto"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
	"github.com/ProtonMail/go-crypto/openpgp/s2k"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

const nexusImage = "sonatype/nexus3:3.96.4"

// nexusAptDist is the distribution of the apt source repo; the Debian target takes it over.
const nexusAptDist = "trixie"

// nexusSyncWait bounds one sync and the cut-over.
const nexusSyncWait = 10 * time.Minute

// ---------------------------------------------------------------- Nexus container

// Nexus is a Sonatype Nexus 3 container on TestNetwork.
type Nexus struct {
	URL         string // host-mapped base URL
	InternalURL string // http://<alias>:8081, reachable from the server container
	User, Pass  string
}

// StartNexus starts Nexus, reads the generated admin password and accepts the Community
// Edition EULA, which Nexus requires before it accepts uploads.
func StartNexus(t *testing.T) *Nexus {
	t.Helper()
	ctx := context.Background()
	alias := RandName("nexus")
	c, err := testcontainers.Run(ctx, nexusImage,
		testcontainers.WithEnv(map[string]string{
			"INSTALL4J_ADD_VM_PARAMS": "-Xms1g -Xmx1g -XX:MaxDirectMemorySize=1g -Djava.util.prefs.userRoot=/nexus-data/javaprefs",
		}),
		testcontainers.WithExposedPorts("8081/tcp"),
		network.WithNetwork([]string{alias}, TestNetwork),
		testcontainers.WithWaitStrategyAndDeadline(6*time.Minute,
			wait.ForHTTP("/service/rest/v1/status/writable").WithPort("8081/tcp").WithStartupTimeout(6*time.Minute)),
	)
	registerCleanup(t, c, "nexus "+alias)
	if err != nil {
		t.Fatalf("start nexus: %v", err)
	}
	ep, err := c.PortEndpoint(ctx, "8081/tcp", "http")
	if err != nil {
		t.Fatalf("nexus endpoint: %v", err)
	}
	n := &Nexus{URL: ep, InternalURL: "http://" + alias + ":8081", User: "admin"}

	deadline := time.Now().Add(time.Minute)
	for {
		rc, err := c.CopyFileFromContainer(ctx, "/nexus-data/admin.password")
		if err == nil {
			b, rerr := io.ReadAll(rc)
			rc.Close()
			if p := strings.TrimSpace(string(b)); rerr == nil && p != "" {
				n.Pass = p
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("nexus: no /nexus-data/admin.password: %v", err)
		}
		time.Sleep(time.Second)
	}

	var eula struct {
		Accepted   bool   `json:"accepted"`
		Disclaimer string `json:"disclaimer"`
	}
	n.mustAPI(t, http.MethodGet, "/service/rest/v1/system/eula", nil, &eula)
	if !eula.Accepted {
		eula.Accepted = true
		n.mustAPI(t, http.MethodPost, "/service/rest/v1/system/eula", eula, nil)
	}
	return n
}

// api sends a JSON request to the Nexus REST API as admin.
func (n *Nexus) api(t *testing.T, method, p string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		j, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r = bytes.NewReader(j)
	}
	req, err := http.NewRequest(method, n.URL+p, r)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return n.do(t, req)
}

func (n *Nexus) do(t *testing.T, req *http.Request) (int, []byte) {
	t.Helper()
	req.SetBasicAuth(n.User, n.Pass)
	resp, err := (&http.Client{Timeout: 2 * time.Minute}).Do(req)
	if err != nil {
		t.Fatalf("nexus %s %s: %v", req.Method, req.URL.Path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("nexus %s %s: read body: %v", req.Method, req.URL.Path, err)
	}
	return resp.StatusCode, b
}

func (n *Nexus) mustAPI(t *testing.T, method, p string, body, out any) {
	t.Helper()
	st, b := n.api(t, method, p, body)
	if st < 200 || st > 299 {
		t.Fatalf("nexus %s %s: status %d: %s", method, p, st, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("nexus %s %s: decode %s: %v", method, p, b, err)
		}
	}
}

// nexusPart is one multipart field of a components upload; data == nil makes a plain field.
type nexusPart struct {
	field, value string
	data         []byte
}

// upload posts one component through POST /service/rest/v1/components.
func (n *Nexus) upload(t *testing.T, repo string, parts ...nexusPart) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, p := range parts {
		if p.data == nil {
			if err := mw.WriteField(p.field, p.value); err != nil {
				t.Fatal(err)
			}
			continue
		}
		w, err := mw.CreateFormFile(p.field, p.value)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(p.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, n.URL+"/service/rest/v1/components?repository="+url.QueryEscape(repo), &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if st, b := n.do(t, req); st < 200 || st > 299 {
		t.Fatalf("nexus upload to %s: status %d: %s", repo, st, b)
	}
}

// uploadMaven uploads a jar and its pom as one maven2 component.
func (n *Nexus) uploadMaven(t *testing.T, repo, version string, jar, pom []byte) {
	t.Helper()
	n.upload(t, repo,
		nexusPart{field: "maven2.asset1", value: "hello-" + version + ".jar", data: jar},
		nexusPart{field: "maven2.asset1.extension", value: "jar"},
		nexusPart{field: "maven2.asset2", value: "pom.xml", data: pom},
		nexusPart{field: "maven2.asset2.extension", value: "pom"},
	)
}

// uploadRaw uploads files (paths "dist/<name>") to a raw repo in one component.
func (n *Nexus) uploadRaw(t *testing.T, repo string, files ...nexusRawFile) {
	t.Helper()
	parts := []nexusPart{{field: "raw.directory", value: "dist"}}
	for i, f := range files {
		k := fmt.Sprintf("raw.asset%d", i+1)
		name := strings.TrimPrefix(f.path, "dist/")
		parts = append(parts, nexusPart{field: k, value: name, data: f.data}, nexusPart{field: k + ".filename", value: name})
	}
	n.upload(t, repo, parts...)
}

// assetPaths lists every asset path of a Nexus repo, following continuation tokens.
func (n *Nexus) assetPaths(t *testing.T, repo string) []string {
	t.Helper()
	var paths []string
	token := ""
	for {
		q := "/service/rest/v1/assets?repository=" + url.QueryEscape(repo)
		if token != "" {
			q += "&continuationToken=" + url.QueryEscape(token)
		}
		var page struct {
			Items []struct {
				Path string `json:"path"`
			} `json:"items"`
			ContinuationToken *string `json:"continuationToken"`
		}
		n.mustAPI(t, http.MethodGet, q, nil, &page)
		for _, it := range page.Items {
			paths = append(paths, strings.TrimPrefix(it.Path, "/"))
		}
		if page.ContinuationToken == nil || *page.ContinuationToken == "" {
			return paths
		}
		token = *page.ContinuationToken
	}
}

// waitAssets polls until repo lists an asset ending in each of suffixes and returns the
// repo's asset paths.
func (n *Nexus) waitAssets(t *testing.T, repo string, suffixes ...string) []string {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for {
		paths := n.assetPaths(t, repo)
		missing := slices.DeleteFunc(slices.Clone(suffixes), func(s string) bool {
			return slices.ContainsFunc(paths, func(p string) bool { return strings.HasSuffix(p, s) })
		})
		if len(missing) == 0 {
			return paths
		}
		if time.Now().After(deadline) {
			t.Fatalf("nexus repo %s: assets %v missing; has %v", repo, missing, paths)
		}
		time.Sleep(time.Second)
	}
}

// nexusStorage is the storage block of a hosted repo.
var nexusStorage = map[string]any{"blobStoreName": "default", "strictContentTypeValidation": false, "writePolicy": "allow_once"}

// nexusRepoSet names the hosted source repos of one subtest.
type nexusRepoSet struct {
	maven, apt, raw string
	npm             string // a format no plugin imports
}

// createRepos creates a maven2, apt, raw and npm hosted repo with fresh names.
func (n *Nexus) createRepos(t *testing.T) nexusRepoSet {
	t.Helper()
	rs := nexusRepoSet{maven: RandName("src-maven"), apt: RandName("src-apt"), raw: RandName("src-raw"), npm: RandName("src-npm")}
	priv, pass := nexusAptSigningKey(t)
	n.mustAPI(t, http.MethodPost, "/service/rest/v1/repositories/maven/hosted", map[string]any{
		"name": rs.maven, "online": true, "storage": nexusStorage,
		"maven": map[string]any{"versionPolicy": "RELEASE", "layoutPolicy": "STRICT", "contentDisposition": "INLINE"},
	}, nil)
	n.mustAPI(t, http.MethodPost, "/service/rest/v1/repositories/apt/hosted", map[string]any{
		"name": rs.apt, "online": true, "storage": nexusStorage,
		"apt":        map[string]any{"distribution": nexusAptDist},
		"aptSigning": map[string]any{"keypair": priv, "passphrase": pass},
	}, nil)
	n.mustAPI(t, http.MethodPost, "/service/rest/v1/repositories/raw/hosted", map[string]any{
		"name": rs.raw, "online": true, "storage": nexusStorage,
		"raw": map[string]any{"contentDisposition": "ATTACHMENT"},
	}, nil)
	n.mustAPI(t, http.MethodPost, "/service/rest/v1/repositories/npm/hosted", map[string]any{
		"name": rs.npm, "online": true, "storage": nexusStorage,
	}, nil)
	return rs
}

// nexusAptSigningKey returns an armored RSA private key encrypted with a passphrase (iterated
// and salted SHA-256, AES-256), the form the Nexus apt signing config takes.
func nexusAptSigningKey(t *testing.T) (armored, passphrase string) {
	t.Helper()
	cfg := &packet.Config{
		Algorithm:     packet.PubKeyAlgoRSA,
		RSABits:       2048,
		DefaultHash:   crypto.SHA256,
		DefaultCipher: packet.CipherAES256,
		S2KConfig:     &s2k.Config{S2KMode: s2k.IteratedSaltedS2K, Hash: crypto.SHA256, S2KCount: 65536},
	}
	ent, err := openpgp.NewEntity("Nexus E2E", "", "nexus@telesphorepo.test", cfg)
	if err != nil {
		t.Fatalf("nexus apt key: %v", err)
	}
	passphrase = randHex(12)
	if err := ent.EncryptPrivateKeys([]byte(passphrase), cfg); err != nil {
		t.Fatalf("nexus apt key: encrypt: %v", err)
	}
	var buf bytes.Buffer
	w, err := armor.Encode(&buf, openpgp.PrivateKeyType, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ent.SerializePrivateWithoutSigning(w, cfg); err != nil {
		t.Fatalf("nexus apt key: serialize: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.String(), passphrase
}

// nexusRawFile is one file of the raw source repo.
type nexusRawFile struct {
	path string
	data []byte
}

// ---------------------------------------------------------------- Telesphorepo Nexus API

// nexusSource is the body of POST /api/nexus/sources and GET /api/nexus/sources/{id}.
type nexusSource struct {
	ID        int64             `json:"id"`
	Name      string            `json:"name"`
	State     string            `json:"state"` // "mirroring" | "cut_over"
	LastError string            `json:"last_error"`
	Repos     []nexusSourceRepo `json:"repos"`
}

// nexusSourceRepo is one Nexus repo of a source with its mapping.
type nexusSourceRepo struct {
	SourceRepo   string `json:"name"`
	SourceFormat string `json:"format"`
	Supported    bool   `json:"supported"`
	Target       string `json:"target"` // target repo name; "" when not mapped
}

func (src *nexusSource) repo(t *testing.T, name string) nexusSourceRepo {
	t.Helper()
	i := slices.IndexFunc(src.Repos, func(r nexusSourceRepo) bool { return r.SourceRepo == name })
	if i < 0 {
		t.Fatalf("nexus source %d lists no repo %s: %+v", src.ID, name, src.Repos)
	}
	return src.Repos[i]
}

// nexusSync is the body of POST /api/nexus/sources/{id}/sync and GET /api/nexus/syncs/{id}.
type nexusSync struct {
	ID    int64           `json:"id"`
	State string          `json:"state"` // "running" | "done" | "failed" | "cancelled"
	Error string          `json:"error"`
	Repos []nexusSyncRepo `json:"repos"`
}

// nexusSyncRepo is the per-repo progress of a sync.
type nexusSyncRepo struct {
	SourceRepo string `json:"source_repo"`
	State      string `json:"state"`
	Imported   int64  `json:"imported"`
	Skipped    int64  `json:"skipped"`
	Ignored    int64  `json:"ignored"`
	Failed     int64  `json:"failed"`
}

func (sy *nexusSync) repo(t *testing.T, name string) nexusSyncRepo {
	t.Helper()
	i := slices.IndexFunc(sy.Repos, func(r nexusSyncRepo) bool { return r.SourceRepo == name })
	if i < 0 {
		t.Fatalf("nexus sync %d has no repo %s: %+v", sy.ID, name, sy.Repos)
	}
	return sy.Repos[i]
}

func nexusSourcePath(id int64) string { return fmt.Sprintf("/api/nexus/sources/%d", id) }

// nexusCreateSource adds n as a source of s.
func nexusCreateSource(t *testing.T, s *Server, n *Nexus) *nexusSource {
	t.Helper()
	var src nexusSource
	s.MustAPI(t, http.MethodPost, "/api/nexus/sources", map[string]any{
		"name": RandName("nexus"), "base_url": n.InternalURL, "username": n.User, "password": n.Pass,
	}, s.Admin(), &src)
	if src.ID == 0 {
		t.Fatalf("POST /api/nexus/sources: no id in %+v", src)
	}
	return &src
}

func nexusGetSource(t *testing.T, s *Server, id int64) *nexusSource {
	t.Helper()
	var src nexusSource
	s.MustAPI(t, http.MethodGet, nexusSourcePath(id), nil, s.Admin(), &src)
	return &src
}

// nexusMapRepos maps each source repo to its target and creates the missing targets.
func nexusMapRepos(t *testing.T, s *Server, id int64, targets map[string]string) {
	t.Helper()
	var body []map[string]any
	for src, target := range targets {
		body = append(body, map[string]any{"source_repo": src, "target": target, "create": true})
	}
	s.MustAPI(t, http.MethodPut, nexusSourcePath(id)+"/repos", body, s.Admin(), nil)
}

// nexusSyncNow starts a sync of the source and waits for it to end. A scheduled sync that
// already runs makes the start answer 409; the helper then waits for that sync and starts its
// own, so the returned counters belong to a sync that started after this call.
func nexusSyncNow(t *testing.T, s *Server, id int64) *nexusSync {
	t.Helper()
	for range 5 {
		st, b := s.API(t, http.MethodPost, nexusSourcePath(id)+"/sync", nil, s.Admin())
		if st == http.StatusConflict {
			if running := nexusRunningSync(t, s, id); running != 0 {
				nexusWaitSync(t, s, running)
			} else {
				time.Sleep(time.Second)
			}
			continue
		}
		if st < 200 || st > 299 {
			t.Fatalf("POST sync of source %d: status %d: %s", id, st, b)
		}
		var sy nexusSync
		if err := json.Unmarshal(b, &sy); err != nil || sy.ID == 0 {
			t.Fatalf("POST sync of source %d: decode sync id from %s: %v", id, b, err)
		}
		return nexusWaitSync(t, s, sy.ID)
	}
	t.Fatalf("POST sync of source %d: still 409 after waiting for the running syncs", id)
	return nil
}

// nexusRunningSync returns the id of a running sync of the source, or 0.
func nexusRunningSync(t *testing.T, s *Server, id int64) int64 {
	t.Helper()
	var page struct {
		Items []nexusSync `json:"items"`
	}
	s.MustAPI(t, http.MethodGet, nexusSourcePath(id)+"/syncs?limit=20", nil, s.Admin(), &page)
	for _, sy := range page.Items {
		if sy.State == "running" {
			return sy.ID
		}
	}
	return 0
}

// nexusWaitSync polls a sync until it ends and fails the test unless it is done with no
// failed asset.
func nexusWaitSync(t *testing.T, s *Server, syncID int64) *nexusSync {
	t.Helper()
	var sy nexusSync
	deadline := time.Now().Add(nexusSyncWait)
	for {
		s.MustAPI(t, http.MethodGet, fmt.Sprintf("/api/nexus/syncs/%d", syncID), nil, s.Admin(), &sy)
		if sy.State != "running" && sy.State != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("nexus sync %d still running after %s: %+v", syncID, nexusSyncWait, sy)
		}
		time.Sleep(time.Second)
	}
	failed := sy.State != "done"
	for _, r := range sy.Repos {
		failed = failed || r.Failed != 0
	}
	if failed {
		var fails struct {
			Items []struct {
				SourceRepo string `json:"source_repo"`
				Path       string `json:"path"`
				Reason     string `json:"reason"`
			} `json:"items"`
		}
		s.MustAPI(t, http.MethodGet, fmt.Sprintf("/api/nexus/syncs/%d/failures?limit=100", syncID), nil, s.Admin(), &fails)
		t.Fatalf("nexus sync %d did not finish cleanly: %+v\nfailures: %+v", syncID, sy, fails.Items)
	}
	return &sy
}

// nexusCutOver cuts the source over and waits until its state is cut_over.
func nexusCutOver(t *testing.T, s *Server, id int64) {
	t.Helper()
	s.MustAPI(t, http.MethodPost, nexusSourcePath(id)+"/cutover", nil, s.Admin(), nil)
	deadline := time.Now().Add(nexusSyncWait)
	for {
		src := nexusGetSource(t, s, id)
		if src.State == "cut_over" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("nexus source %d not cut over after %s: %+v", id, nexusSyncWait, src)
		}
		time.Sleep(time.Second)
	}
}

// ---------------------------------------------------------------- clients

// nexusRawCurlScript downloads each RAW_PATHS entry from RAW_BASE and prints "<path> <sha256>".
const nexusRawCurlScript = `set -e
for p in $RAW_PATHS; do
  echo "SUM $p $(curl -fsSL -u "$RAW_USER:$RAW_PASS" "$RAW_BASE/$p" | sha256sum | cut -d' ' -f1)"
done
echo "RAW DONE"
`

// nexusMavenResolve resolves group:hello:version from the target repo in a fresh Maven
// container, runs it and requires message in the output.
func nexusMavenResolve(t *testing.T, s *Server, repo, group, version, message string) {
	t.Helper()
	script := strings.Join([]string{
		"set -e",
		"rm -rf /root/.m2/repository/" + strings.ReplaceAll(group, ".", "/"),
		"cd /work/consumer",
		"mvn -B -ntp -q -U -s /work/settings.xml -Ddep.version=" + version + " -Dmdep.outputFile=/tmp/cp.txt org.apache.maven.plugins:maven-dependency-plugin:3.8.1:build-classpath",
		`java -cp "$(cat /tmp/cp.txt)" ` + group + ".Hello",
	}, "\n")
	code, out := RunClient(t, mavenImage, ClientOpts{
		Cmd: []string{script},
		Files: []ContainerFile{
			{Path: "/work/settings.xml", Content: mavenSettingsXML(s.AdminUser, s.AdminPass)},
			mavenConsumerProject(group, s.InternalURL+"/maven/"+repo),
		},
		Mounts:  map[string]string{M2Volume: "/root/.m2/repository"},
		Timeout: 15 * time.Minute,
	})
	if code != 0 || !strings.Contains(out, message) {
		t.Fatalf("maven consumer of %s %s: exit %d, want 0 with %q:\n%s", repo, version, code, message, out)
	}
}

// nexusMavenBuild builds group:hello:version with real Maven and returns the jar and the pom.
func nexusMavenBuild(t *testing.T, group, version, message string) (jar, pom []byte) {
	t.Helper()
	build := RunClientResult(t, mavenImage, ClientOpts{
		Cmd:     []string{"cd /work/lib && mvn -B -ntp -q package"},
		Files:   mavenLibProject(group, version, "http://unused.invalid/", message),
		Mounts:  map[string]string{M2Volume: "/root/.m2/repository"},
		Timeout: 15 * time.Minute,
	})
	if build.ExitCode != 0 {
		t.Fatalf("build jar %s: exit %d\n%s", version, build.ExitCode, build.Output)
	}
	return build.ReadFile(t, "/work/lib/target/hello-"+version+".jar"), build.ReadFile(t, "/work/lib/pom.xml")
}

// nexusWantMirrorBlocked requires the 409 that an upload to a mirrored repo answers.
func nexusWantMirrorBlocked(t *testing.T, what string, st int, body []byte) {
	t.Helper()
	if st != http.StatusConflict || !strings.Contains(strings.ToLower(string(body)), "mirror") {
		t.Fatalf("%s to a mirrored repo: status %d, want 409 naming the mirror: %s", what, st, body)
	}
}

// ---------------------------------------------------------------- test

// TestNexus mirrors maven2, apt and raw repos from a real Nexus, syncs a new version, checks
// that mirrored repos refuse uploads, and cuts over.
func TestNexus(t *testing.T) {
	group := "com.example.nexus" + randHex(4)
	groupPath := strings.ReplaceAll(group, ".", "/")
	message1 := "hello from " + group + " 1.0.0 mirrored from nexus"
	message2 := "hello from " + group + " 1.1.0 mirrored from nexus"
	jar1, pom1 := nexusMavenBuild(t, group, "1.0.0", message1)
	jar2, pom2 := nexusMavenBuild(t, group, "1.1.0", message2)
	debs := buildDebPackages(t)
	raws := []nexusRawFile{
		{"dist/readme.txt", []byte("mirrored from nexus " + randHex(8) + "\n")},
		{"dist/app.bin", rawRandom(t, 128<<10)},
		{"dist/config.json", []byte(`{"source":"nexus"}`)},
	}
	rawNew := nexusRawFile{"dist/notes.txt", []byte("added to nexus later " + randHex(8) + "\n")}

	n := StartNexus(t)

	ForEachStorage(t, func(t *testing.T, opts ServerOpts) {
		// Each storage backend gets its own source repos, since the test adds to them.
		rs := n.createRepos(t)
		n.uploadMaven(t, rs.maven, "1.0.0", jar1, pom1)
		n.upload(t, rs.apt, nexusPart{field: "apt.asset", value: "e2e-hello_1.0_all.deb", data: debs.hello1})
		n.upload(t, rs.apt, nexusPart{field: "apt.asset", value: "e2e-native_1.0.deb", data: debs.native})
		n.uploadRaw(t, rs.raw, raws...)
		n.waitAssets(t, rs.maven, "/hello/1.0.0/hello-1.0.0.jar", "/hello/1.0.0/hello-1.0.0.pom")
		n.waitAssets(t, rs.apt, "e2e-hello_1.0_all.deb", "e2e-native_1.0_"+debs.arch+".deb")
		n.waitAssets(t, rs.raw, "dist/readme.txt", "dist/app.bin", "dist/config.json")

		// A long sync interval keeps scheduled syncs out of the counters this test checks.
		opts.Env = map[string]string{"TR_NEXUS_SYNC_INTERVAL": "12h"}
		s := StartServer(t, opts)

		// The connection check refuses a wrong password.
		if st, b := s.API(t, http.MethodPost, "/api/nexus/sources", map[string]any{
			"name": RandName("nexus"), "base_url": n.InternalURL, "username": n.User, "password": "wrong-password",
		}, s.Admin()); st >= 200 && st < 300 {
			t.Fatalf("POST /api/nexus/sources with a wrong password: status %d, want an error: %s", st, b)
		}

		// The source lists each Nexus repo with its format and whether a plugin imports it.
		src := nexusCreateSource(t, s, n)
		got := nexusGetSource(t, s, src.ID)
		if got.State != "mirroring" {
			t.Fatalf("new source: state %q, want mirroring", got.State)
		}
		for name, want := range map[string]struct {
			format    string
			supported bool
		}{rs.maven: {"maven2", true}, rs.apt: {"apt", true}, rs.raw: {"raw", true}, rs.npm: {"npm", false}} {
			if r := got.repo(t, name); r.SourceFormat != want.format || r.Supported != want.supported || r.Target != "" {
				t.Fatalf("source repo %s: %+v, want format %s, supported %v, no target", name, r, want.format, want.supported)
			}
		}

		// Map the three supported repos to new targets.
		targets := map[string]string{rs.maven: RandName("nxmvn"), rs.apt: RandName("nxdeb"), rs.raw: RandName("nxraw")}
		nexusMapRepos(t, s, src.ID, targets)
		got = nexusGetSource(t, s, src.ID)
		for source, target := range targets {
			if r := got.repo(t, source); r.Target != target {
				t.Fatalf("source repo %s after mapping: %+v, want target %s", source, r, target)
			}
		}
		for source, format := range map[string]string{rs.maven: "maven", rs.apt: "debian", rs.raw: "raw"} {
			var info struct {
				Format string `json:"format"`
			}
			s.MustAPI(t, http.MethodGet, "/api/repos/"+targets[source], nil, s.Admin(), &info)
			if info.Format != format {
				t.Fatalf("target %s: format %q, want %q", targets[source], info.Format, format)
			}
		}

		// First sync: everything imports.
		first := nexusSyncNow(t, s, src.ID)
		if r := first.repo(t, rs.maven); r.Imported < 2 {
			t.Fatalf("first sync of maven: %+v, want at least the jar and the pom imported", r)
		}
		if r := first.repo(t, rs.apt); r.Imported != 2 {
			t.Fatalf("first sync of apt: %+v, want 2 packages imported", r)
		}
		if r := first.repo(t, rs.raw); r.Imported != int64(len(raws)) || r.Skipped != 0 || r.Ignored != 0 {
			t.Fatalf("first sync of raw: %+v, want %d files imported", r, len(raws))
		}

		nexusMavenResolve(t, s, targets[rs.maven], group, "1.0.0", message1)
		s.WaitDebianIndexed(t, targets[rs.apt])
		st, keyBody := s.API(t, http.MethodGet, "/keys/debian.asc", nil, nil)
		if st != http.StatusOK {
			t.Fatalf("GET /keys/debian.asc: status %d", st)
		}
		key := string(keyBody)
		out := debAptInstall(t, s, targets[rs.apt], s.AdminUser, s.AdminPass, key)
		if !strings.Contains(out, "RESULT: hello 1.0\n") || !strings.Contains(out, "NATIVE: native "+debs.arch) {
			t.Fatalf("apt install after the first sync did not produce the expected output:\n%s", out)
		}

		// A new version in Nexus: the second sync imports only the new files.
		n.uploadMaven(t, rs.maven, "1.1.0", jar2, pom2)
		n.upload(t, rs.apt, nexusPart{field: "apt.asset", value: "e2e-hello_2.0_all.deb", data: debs.hello2})
		n.uploadRaw(t, rs.raw, rawNew)
		mavenAfter := n.waitAssets(t, rs.maven, "/hello/1.1.0/hello-1.1.0.jar", "/hello/1.1.0/hello-1.1.0.pom")
		n.waitAssets(t, rs.apt, "e2e-hello_2.0_all.deb")
		n.waitAssets(t, rs.raw, rawNew.path)

		second := nexusSyncNow(t, s, src.ID)
		// Maven metadata changes with each version, so only the version directories count.
		var oldFiles, newFiles int64
		for _, p := range mavenAfter {
			switch {
			case strings.Contains(p, "/hello/1.0.0/"):
				oldFiles++
			case strings.Contains(p, "/hello/1.1.0/"):
				newFiles++
			}
		}
		if r := second.repo(t, rs.maven); r.Imported < newFiles || r.Skipped < oldFiles {
			t.Fatalf("second sync of maven: %+v, want at least %d new files imported and %d old files skipped", r, newFiles, oldFiles)
		}
		if r, a := second.repo(t, rs.apt), first.repo(t, rs.apt); r.Imported != 1 || r.Skipped != a.Imported {
			t.Fatalf("second sync of apt: %+v, want 1 package imported and %d skipped", r, a.Imported)
		}
		if r := second.repo(t, rs.raw); r.Imported != 1 || r.Skipped != int64(len(raws)) || r.Ignored != 0 {
			t.Fatalf("second sync of raw: %+v, want 1 file imported and %d skipped", r, len(raws))
		}

		nexusMavenResolve(t, s, targets[rs.maven], group, "1.1.0", message2)
		s.WaitDebianIndexed(t, targets[rs.apt])
		out = debAptInstall(t, s, targets[rs.apt], s.AdminUser, s.AdminPass, key)
		if !strings.Contains(out, "RESULT: hello 2.0\n") {
			t.Fatalf("apt install after the second sync did not produce hello 2.0:\n%s", out)
		}
		allRaws := append(slices.Clone(raws), rawNew)
		var paths []string
		for _, f := range allRaws {
			paths = append(paths, f.path)
		}
		code, out := RunClient(t, curlImage, ClientOpts{
			Cmd:   []string{"sh", "/work/run.sh"},
			Files: []ContainerFile{{Path: "/work/run.sh", Content: nexusRawCurlScript, Mode: 0o755}},
			Env: map[string]string{
				"RAW_BASE":  s.InternalURL + "/raw/" + targets[rs.raw],
				"RAW_PATHS": strings.Join(paths, " "),
				"RAW_USER":  s.AdminUser,
				"RAW_PASS":  s.AdminPass,
			},
			Timeout: 3 * time.Minute,
		})
		if code != 0 || !strings.Contains(out, "RAW DONE") {
			t.Fatalf("curl client: exit %d:\n%s", code, out)
		}
		for _, f := range allRaws {
			sum := sha256.Sum256(f.data)
			if want := "SUM " + f.path + " " + hex.EncodeToString(sum[:]); !strings.Contains(out, want) {
				t.Fatalf("curl output lacks %q:\n%s", want, out)
			}
		}

		// Mirrored repos refuse uploads, so the two servers cannot diverge.
		mavenPut := "/maven/" + targets[rs.maven] + "/" + groupPath + "/hello/9.9.9/hello-9.9.9.jar"
		resp, b := rawPut(t, s, mavenPut, []byte("not from nexus"), s.Admin())
		nexusWantMirrorBlocked(t, "maven PUT", resp.StatusCode, b)
		rawPath := "/raw/" + targets[rs.raw] + "/dist/after-cutover.txt"
		resp, b = rawPut(t, s, rawPath, []byte("written here"), s.Admin())
		nexusWantMirrorBlocked(t, "raw PUT", resp.StatusCode, b)
		st, b = debUpload(t, s, targets[rs.apt], debs.hello2, s.Admin())
		nexusWantMirrorBlocked(t, "debian upload", st, b)

		// Cut over: uploads work here, and the source can no longer sync.
		nexusCutOver(t, s, src.ID)
		resp, b = rawPut(t, s, mavenPut, []byte("not from nexus"), s.Admin())
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("maven PUT after cut-over: status %d, want 201: %s", resp.StatusCode, b)
		}
		resp, b = rawPut(t, s, rawPath, []byte("written here"), s.Admin())
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("raw PUT after cut-over: status %d, want 201: %s", resp.StatusCode, b)
		}
		if st, b := debUpload(t, s, targets[rs.apt], debs.hello2, s.Admin()); st != http.StatusOK {
			t.Fatalf("identical debian upload after cut-over: status %d, want 200: %s", st, b)
		}

		if st, b := s.API(t, http.MethodPost, nexusSourcePath(src.ID)+"/sync", nil, s.Admin()); st != http.StatusConflict {
			t.Fatalf("POST sync after cut-over: status %d, want 409: %s", st, b)
		}
		for _, p := range []string{nexusSourcePath(src.ID), "/api/nexus/sources"} {
			if _, b := s.API(t, http.MethodGet, p, nil, s.Admin()); bytes.Contains(b, []byte(n.Pass)) {
				t.Fatalf("GET %s returns the Nexus password", p)
			}
		}
	})
}
