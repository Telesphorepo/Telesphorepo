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
	"regexp"
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

var nexusImage = floatingImage("docker.io/sonatype/nexus3:latest")

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
	anonRoles   []string // the roles of the anonymous user, one per repo it may read
}

// StartNexus starts Nexus, reads the generated admin password and accepts the Community
// Edition EULA, which Nexus requires before it accepts uploads.
func StartNexus(t *testing.T) *Nexus {
	t.Helper()
	ctx := context.Background()
	alias := RandName("nexus")
	c, err := runContainer(ctx, nexusImage,
		testcontainers.WithLabels(e2eLabels),
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

// nexusMavenSettings is the maven block of a maven2 repo.
var nexusMavenSettings = map[string]any{"versionPolicy": "RELEASE", "layoutPolicy": "STRICT", "contentDisposition": "INLINE"}

// nexusRemoteUser and nexusRemotePass are the remote credentials of the Nexus proxy. Nexus
// gives the username over its API, never the password.
const (
	nexusRemoteUser = "remote-user"
	nexusRemotePass = "remote-secret-e2e"
)

// nexusRemoteURL is the remote of the Nexus proxy. Neither Nexus nor this server asks it in
// the test.
const nexusRemoteURL = "https://repo1.maven.org/maven2/"

// nexusRepoSet names the source repos of one subtest.
type nexusRepoSet struct {
	maven   string // hosted, anonymous read in Nexus
	maven2  string // hosted, private in Nexus
	apt     string // hosted with the key pair aptPriv (passphrase aptPass)
	raw     string // hosted
	npm     string // a format no plugin imports
	offline string // raw hosted, offline in Nexus
	proxy   string // maven2 proxy with remote credentials
	group   string // maven2 group of maven, maven2 and proxy, in that order

	aptPub, aptPass string
}

// createRepos creates the source repos of one subtest with fresh names and gives anonymous
// read to rs.maven only.
func (n *Nexus) createRepos(t *testing.T) nexusRepoSet {
	t.Helper()
	rs := nexusRepoSet{maven: RandName("src-maven"), maven2: RandName("src-maven2"), apt: RandName("src-apt"),
		raw: RandName("src-raw"), npm: RandName("src-npm"), offline: RandName("src-off"),
		proxy: RandName("src-proxy"), group: RandName("src-group")}
	priv, pub, pass := nexusAptSigningKey(t)
	rs.aptPub, rs.aptPass = pub, pass
	for _, name := range []string{rs.maven, rs.maven2} {
		n.mustAPI(t, http.MethodPost, "/service/rest/v1/repositories/maven/hosted", map[string]any{
			"name": name, "online": true, "storage": nexusStorage, "maven": nexusMavenSettings,
		}, nil)
	}
	n.mustAPI(t, http.MethodPost, "/service/rest/v1/repositories/apt/hosted", map[string]any{
		"name": rs.apt, "online": true, "storage": nexusStorage,
		"apt":        map[string]any{"distribution": nexusAptDist},
		"aptSigning": map[string]any{"keypair": priv, "passphrase": pass},
	}, nil)
	for _, name := range []string{rs.raw, rs.offline} {
		n.mustAPI(t, http.MethodPost, "/service/rest/v1/repositories/raw/hosted", map[string]any{
			"name": name, "online": true, "storage": nexusStorage,
			"raw": map[string]any{"contentDisposition": "ATTACHMENT"},
		}, nil)
	}
	n.mustAPI(t, http.MethodPut, "/service/rest/v1/repositories/raw/hosted/"+rs.offline, map[string]any{
		"name": rs.offline, "online": false, "storage": nexusStorage,
		"raw": map[string]any{"contentDisposition": "ATTACHMENT"},
	}, nil)
	n.mustAPI(t, http.MethodPost, "/service/rest/v1/repositories/npm/hosted", map[string]any{
		"name": rs.npm, "online": true, "storage": nexusStorage,
	}, nil)
	n.mustAPI(t, http.MethodPost, "/service/rest/v1/repositories/maven/proxy", map[string]any{
		"name": rs.proxy, "online": true,
		"storage":       map[string]any{"blobStoreName": "default", "strictContentTypeValidation": true},
		"proxy":         map[string]any{"remoteUrl": nexusRemoteURL, "contentMaxAge": -1, "metadataMaxAge": 60},
		"negativeCache": map[string]any{"enabled": true, "timeToLive": 10},
		"httpClient": map[string]any{"blocked": false, "autoBlock": true,
			"authentication": map[string]any{"type": "username", "username": nexusRemoteUser, "password": nexusRemotePass}},
		"maven": nexusMavenSettings,
	}, nil)
	n.mustAPI(t, http.MethodPost, "/service/rest/v1/repositories/maven/group", map[string]any{
		"name": rs.group, "online": true,
		"storage": map[string]any{"blobStoreName": "default", "strictContentTypeValidation": true},
		"group":   map[string]any{"memberNames": []string{rs.maven, rs.maven2, rs.proxy}},
	}, nil)
	n.allowAnonymous(t, "maven2", rs.maven)
	return rs
}

// allowAnonymous gives the anonymous user read access to repo (of Nexus format) and to the repos
// of earlier calls, and turns anonymous access on. Nexus then answers an anonymous download of
// repo with 200 and of every other repo with 401.
func (n *Nexus) allowAnonymous(t *testing.T, format, repo string) {
	t.Helper()
	role := "e2e-anon-" + repo
	n.mustAPI(t, http.MethodPost, "/service/rest/v1/security/roles", map[string]any{
		"id": role, "name": role, "description": "anonymous read of " + repo,
		"privileges": []string{"nx-repository-view-" + format + "-" + repo + "-read", "nx-repository-view-" + format + "-" + repo + "-browse"},
		"roles":      []string{},
	}, nil)
	n.anonRoles = append(n.anonRoles, role)
	n.mustAPI(t, http.MethodPut, "/service/rest/v1/security/users/anonymous", map[string]any{
		"userId": "anonymous", "firstName": "Anonymous", "lastName": "User", "emailAddress": "anonymous@example.org",
		"source": "default", "status": "active", "readOnly": false, "roles": n.anonRoles, "externalRoles": []string{},
	}, nil)
	n.mustAPI(t, http.MethodPut, "/service/rest/v1/security/anonymous", map[string]any{
		"enabled": true, "userId": "anonymous", "realmName": "NexusAuthorizingRealm"}, nil)
}

// nexusAptSigningKey returns an armored RSA private key encrypted with a passphrase (iterated
// and salted SHA-256, AES-256), the form the Nexus apt signing config takes, and its armored
// public key.
func nexusAptSigningKey(t *testing.T) (armored, public, passphrase string) {
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
	var pub bytes.Buffer
	pw, err := armor.Encode(&pub, openpgp.PublicKeyType, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ent.Serialize(pw); err != nil {
		t.Fatalf("nexus apt key: public: %v", err)
	}
	if err := pw.Close(); err != nil {
		t.Fatal(err)
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
	return buf.String(), pub.String(), passphrase
}

// nexusRawFile is one file of the raw source repo.
type nexusRawFile struct {
	path string
	data []byte
}

// nexusAsset is one asset of a Nexus repo with its size and blob dates.
type nexusAsset struct {
	Path        string `json:"path"`
	FileSize    int64  `json:"fileSize"`
	BlobCreated string `json:"blobCreated"`
	BlobUpdated string `json:"blobUpdated"`
}

// assets lists every asset of a Nexus repo, following continuation tokens. Paths have no
// leading slash.
func (n *Nexus) assets(t *testing.T, repo string) []nexusAsset {
	t.Helper()
	var out []nexusAsset
	token := ""
	for {
		q := "/service/rest/v1/assets?repository=" + url.QueryEscape(repo)
		if token != "" {
			q += "&continuationToken=" + url.QueryEscape(token)
		}
		var page struct {
			Items             []nexusAsset `json:"items"`
			ContinuationToken *string      `json:"continuationToken"`
		}
		n.mustAPI(t, http.MethodGet, q, nil, &page)
		for _, it := range page.Items {
			it.Path = strings.TrimPrefix(it.Path, "/")
			out = append(out, it)
		}
		if page.ContinuationToken == nil || *page.ContinuationToken == "" {
			return out
		}
		token = *page.ContinuationToken
	}
}

// asset returns the asset of repo whose path ends in suffix.
func (n *Nexus) asset(t *testing.T, repo, suffix string) nexusAsset {
	t.Helper()
	for _, a := range n.assets(t, repo) {
		if strings.HasSuffix(a.Path, suffix) {
			return a
		}
	}
	t.Fatalf("nexus repo %s has no asset ending in %s", repo, suffix)
	return nexusAsset{}
}

// ---------------------------------------------------------------- Telesphorepo Nexus API

// nexusSource is the body of POST /api/nexus/sources and GET /api/nexus/sources/{id}.
type nexusSource struct {
	ID            int64             `json:"id"`
	Name          string            `json:"name"`
	State         string            `json:"state"` // "mirroring" | "cut_over"
	LastError     string            `json:"last_error"`
	Status        string            `json:"status"`
	NextSyncAt    *time.Time        `json:"next_sync_at"`
	RunningSyncID int64             `json:"running_sync_id"`
	LastSync      *nexusSync        `json:"last_sync"`
	Repos         []nexusSourceRepo `json:"repos"`
}

// nexusSourceRepo is one Nexus repo of a source with its mapping.
type nexusSourceRepo struct {
	SourceRepo     string   `json:"name"`
	SourceFormat   string   `json:"format"`
	Type           string   `json:"type"`
	Supported      bool     `json:"supported"`
	Reason         string   `json:"reason"`
	Offline        bool     `json:"offline"`
	Public         bool     `json:"public"`
	Target         string   `json:"target"` // target repo name; "" when not mapped
	RemoteURL      string   `json:"remote_url"`
	RemoteUsername string   `json:"remote_username"`
	Members        []string `json:"members"`
	MembersKnown   bool     `json:"members_known"`
	// Questions and Facts come from the planning hook of the target format.
	Questions []struct {
		Key     string `json:"key"`
		Type    string `json:"type"`
		Options []struct {
			Value string `json:"value"`
		} `json:"options"`
	} `json:"questions"`
	Facts map[string]any `json:"facts"`
}

// hasQuestion reports whether r asks question key of type typ with every option of options.
func (r nexusSourceRepo) hasQuestion(key, typ string, options ...string) bool {
	for _, q := range r.Questions {
		if q.Key != key || q.Type != typ {
			continue
		}
		for _, o := range options {
			if !slices.ContainsFunc(q.Options, func(x struct {
				Value string `json:"value"`
			}) bool {
				return x.Value == o
			}) {
				return false
			}
		}
		return true
	}
	return false
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
	ID         int64           `json:"id"`
	State      string          `json:"state"`   // "running" | "done" | "failed" | "cancelled"
	Trigger    string          `json:"trigger"` // "save" | "manual" | "schedule" | "cutover"
	StartedBy  string          `json:"started_by"`
	FinishedAt *time.Time      `json:"finished_at"`
	Error      string          `json:"error"`
	Checked    int64           `json:"checked"`
	Imported   int64           `json:"imported"`
	Bytes      int64           `json:"bytes"`
	Repos      []nexusSyncRepo `json:"repos"`
}

// nexusSyncRepo is the per-repo progress of a sync.
type nexusSyncRepo struct {
	SourceRepo      string     `json:"source_repo"`
	State           string     `json:"state"`
	Checked         int64      `json:"checked"`
	Imported        int64      `json:"imported"`
	Skipped         int64      `json:"skipped"`
	Ignored         int64      `json:"ignored"`
	Failed          int64      `json:"failed"`
	Bytes           int64      `json:"bytes"`
	ActiveAt        *time.Time `json:"active_at"`
	PreviousChecked int64      `json:"previous_checked"`
}

func (sy *nexusSync) repo(t *testing.T, name string) nexusSyncRepo {
	t.Helper()
	i := slices.IndexFunc(sy.Repos, func(r nexusSyncRepo) bool { return r.SourceRepo == name })
	if i < 0 {
		t.Fatalf("nexus sync %d has no repo %s: %+v", sy.ID, name, sy.Repos)
	}
	return sy.Repos[i]
}

// nexusLogLine is one line of GET /api/nexus/syncs/{id}/log.
type nexusLogLine struct {
	ID         int64  `json:"id"`
	SourceRepo string `json:"source_repo"`
	Level      string `json:"level"`
	Message    string `json:"message"`
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
	st, body := s.API(t, http.MethodGet, nexusSourcePath(id), nil, s.Admin())
	if st != http.StatusOK {
		t.Fatalf("GET %s: status %d: %s", nexusSourcePath(id), st, body)
	}
	// The UI iterates the lists of each repo, so a list is never null.
	if regexp.MustCompile(`"(members|target_members|repos)":null`).Match(body) {
		t.Fatalf("GET %s has a null list: %s", nexusSourcePath(id), body)
	}
	var src nexusSource
	if err := json.Unmarshal(body, &src); err != nil {
		t.Fatal(err)
	}
	return &src
}

// nexusSave saves the repos to copy (PUT .../repos with {repos}) and returns the source detail
// that the route answers.
func nexusSave(t *testing.T, s *Server, id int64, maps []map[string]any) *nexusSource {
	t.Helper()
	var src nexusSource
	s.MustAPI(t, http.MethodPut, nexusSourcePath(id)+"/repos", map[string]any{"repos": maps}, s.Admin(), &src)
	return &src
}

// nexusNewestSync returns the newest sync of the source.
func nexusNewestSync(t *testing.T, s *Server, id int64) nexusSync {
	t.Helper()
	var page struct {
		Items []nexusSync `json:"items"`
	}
	s.MustAPI(t, http.MethodGet, nexusSourcePath(id)+"/syncs?limit=1", nil, s.Admin(), &page)
	if len(page.Items) == 0 {
		t.Fatalf("nexus source %d has no sync", id)
	}
	return page.Items[0]
}

// nexusSaveAndWait saves the repos, checks that the save started a sync with the trigger save,
// and waits for it.
func nexusSaveAndWait(t *testing.T, s *Server, id int64, maps []map[string]any) *nexusSync {
	t.Helper()
	got := nexusSave(t, s, id, maps)
	newest := nexusNewestSync(t, s, id)
	if newest.Trigger != "save" {
		t.Fatalf("the save started no sync: the newest sync is %+v", newest)
	}
	if got.RunningSyncID != 0 && (got.RunningSyncID != newest.ID || got.Status != "syncing") {
		t.Fatalf("PUT repos: running_sync_id %d, status %q; want sync %d and status syncing", got.RunningSyncID, got.Status, newest.ID)
	}
	return nexusWaitSync(t, s, newest.ID)
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
		if sy.Trigger != "manual" || sy.StartedBy != s.AdminUser {
			t.Fatalf("POST sync: trigger %q started by %q, want manual by %s", sy.Trigger, sy.StartedBy, s.AdminUser)
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
		t.Fatalf("nexus sync %d did not finish cleanly: %+v\nfailures: %+v\nlog: %+v", syncID, sy, fails.Items, nexusLog(t, s, syncID, ""))
	}
	return &sy
}

// nexusLogPage reads one page of a sync's log: lines after id after, at most limit, of repo
// ("" for all). next is the cursor of the next page, "" at the end.
func nexusLogPage(t *testing.T, s *Server, syncID, after int64, repo string, limit int) (lines []nexusLogLine, next string) {
	t.Helper()
	q := url.Values{"limit": {fmt.Sprint(limit)}}
	if after > 0 {
		q.Set("after", fmt.Sprint(after))
	}
	if repo != "" {
		q.Set("repo", repo)
	}
	var page struct {
		Items []nexusLogLine `json:"items"`
		Next  *string        `json:"next"`
	}
	s.MustAPI(t, http.MethodGet, fmt.Sprintf("/api/nexus/syncs/%d/log?%s", syncID, q.Encode()), nil, s.Admin(), &page)
	if page.Next != nil {
		next = *page.Next
	}
	return page.Items, next
}

// nexusLog reads every line of a sync's log, of repo ("" for all).
func nexusLog(t *testing.T, s *Server, syncID int64, repo string) []nexusLogLine {
	t.Helper()
	var all []nexusLogLine
	var after int64
	for range 100 {
		lines, next := nexusLogPage(t, s, syncID, after, repo, 200)
		all = append(all, lines...)
		if next == "" {
			return all
		}
		if _, err := fmt.Sscan(next, &after); err != nil {
			t.Fatalf("log of sync %d: cursor %q: %v", syncID, next, err)
		}
	}
	t.Fatalf("log of sync %d: more than 100 pages", syncID)
	return nil
}

// nexusWantLine requires a line of lines whose message contains want.
func nexusWantLine(t *testing.T, lines []nexusLogLine, want string) {
	t.Helper()
	if !slices.ContainsFunc(lines, func(l nexusLogLine) bool { return strings.Contains(l.Message, want) }) {
		t.Fatalf("the sync log has no line with %q:\n%+v", want, lines)
	}
}

// nexusWantRepoLine fails unless the log has a line of repo that starts with want.
func nexusWantRepoLine(t *testing.T, lines []nexusLogLine, repo, want string) {
	t.Helper()
	if !slices.ContainsFunc(lines, func(l nexusLogLine) bool { return l.SourceRepo == repo && strings.HasPrefix(l.Message, want) }) {
		t.Fatalf("the sync log has no line of %s that starts with %q:\n%+v", repo, want, lines)
	}
}

// nexusCutOver cuts the source over and waits until its state is cut_over.
func nexusCutOver(t *testing.T, s *Server, id int64) {
	t.Helper()
	var sy nexusSync
	s.MustAPI(t, http.MethodPost, nexusSourcePath(id)+"/cutover", nil, s.Admin(), &sy)
	if sy.Trigger != "cutover" {
		t.Fatalf("POST cutover: trigger %q, want cutover", sy.Trigger)
	}
	deadline := time.Now().Add(nexusSyncWait)
	for {
		src := nexusGetSource(t, s, id)
		if src.State == "cut_over" {
			if src.Status != "cut_over" || src.NextSyncAt != nil {
				t.Fatalf("cut-over source: status %q, next_sync_at %v; want cut_over and none", src.Status, src.NextSyncAt)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("nexus source %d not cut over after %s: %+v", id, nexusSyncWait, src)
		}
		time.Sleep(time.Second)
	}
}

// nexusRepoInfo is the part of GET /api/repos/{name} that the Nexus test reads.
type nexusRepoInfo struct {
	Format        string `json:"format"`
	Kind          string `json:"kind"`
	AnonymousRead bool   `json:"anonymous_read"`
	Config        struct {
		Distribution string `json:"distribution"`
	} `json:"config"`
	Fields struct {
		SigningKey string `json:"signing_key"`
	} `json:"fields"`
	Members []struct {
		Name string `json:"name"`
	} `json:"members"`
	Proxy *struct {
		RemoteURL      string `json:"remote_url"`
		HasCredentials bool   `json:"has_credentials"`
		MetadataTTL    int64  `json:"metadata_ttl"`
		NotFoundTTL    int64  `json:"not_found_ttl"`
	} `json:"proxy"`
}

func nexusRepoInfoOf(t *testing.T, s *Server, name string) nexusRepoInfo {
	t.Helper()
	var info nexusRepoInfo
	s.MustAPI(t, http.MethodGet, "/api/repos/"+name, nil, s.Admin(), &info)
	return info
}

// nexusWantDates requires that file path of repo here has the blob dates of the Nexus asset a,
// to the second.
func nexusWantDates(t *testing.T, s *Server, repo, path string, a nexusAsset) {
	t.Helper()
	var f struct {
		CreatedAt time.Time `json:"created_at"`
		UpdatedAt time.Time `json:"updated_at"`
	}
	s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/file?path="+url.QueryEscape(path), nil, s.Admin(), &f)
	created, err1 := time.Parse(time.RFC3339Nano, a.BlobCreated)
	updated, err2 := time.Parse(time.RFC3339Nano, a.BlobUpdated)
	if err1 != nil || err2 != nil {
		t.Fatalf("nexus asset %s: dates %q and %q do not parse", a.Path, a.BlobCreated, a.BlobUpdated)
	}
	if f.CreatedAt.Unix() != created.Unix() || f.UpdatedAt.Unix() != updated.Unix() {
		t.Fatalf("%s in %s: created %s, updated %s; want the Nexus blob dates %s and %s",
			path, repo, f.CreatedAt, f.UpdatedAt, created.UTC(), updated.UTC())
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

// TestNexus mirrors maven2, apt and raw repos, a proxy and a group from a real Nexus. It checks
// the status of the source, who can download each repo, the Nexus signing key, the file dates,
// the progress counters and the log of each sync, unpicking a repo, the refusal of uploads to
// a mirrored repo, and the cut-over.
func TestNexus(t *testing.T) {
	t.Parallel()
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

	ForEachStorageInTurn(t, func(t *testing.T, opts ServerOpts) {
		// Each storage backend gets its own source repos, since the test adds to them.
		rs := n.createRepos(t)
		n.uploadMaven(t, rs.maven, "1.0.0", jar1, pom1)
		n.uploadMaven(t, rs.maven2, "1.0.0", jar1, pom1)
		n.upload(t, rs.apt, nexusPart{field: "apt.asset", value: "e2e-hello_1.0_all.deb", data: debs.hello1})
		n.upload(t, rs.apt, nexusPart{field: "apt.asset", value: "e2e-native_1.0.deb", data: debs.native})
		n.uploadRaw(t, rs.raw, raws...)
		n.waitAssets(t, rs.maven, "/hello/1.0.0/hello-1.0.0.jar", "/hello/1.0.0/hello-1.0.0.pom")
		n.waitAssets(t, rs.maven2, "/hello/1.0.0/hello-1.0.0.jar")
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

		// A new source has no repos and nothing to sync.
		src := nexusCreateSource(t, s, n)
		got := nexusGetSource(t, s, src.ID)
		if got.State != "mirroring" || got.Status != "no_repos" || got.NextSyncAt != nil || got.RunningSyncID != 0 {
			t.Fatalf("new source: state %q, status %q, next_sync_at %v, running %d; want mirroring, no_repos, none, 0",
				got.State, got.Status, got.NextSyncAt, got.RunningSyncID)
		}

		// The source lists each Nexus repo with its format, its type, whether it can be copied
		// and whether anyone can download it in Nexus.
		for name, want := range map[string]struct {
			format, typ       string
			supported, public bool
		}{
			rs.maven: {"maven2", "hosted", true, true}, rs.maven2: {"maven2", "hosted", true, false},
			rs.apt: {"apt", "hosted", true, false}, rs.raw: {"raw", "hosted", true, false},
			rs.npm: {"npm", "hosted", false, false}, rs.proxy: {"maven2", "proxy", true, false},
			rs.group: {"maven2", "group", true, false}, rs.offline: {"raw", "hosted", false, false},
		} {
			r := got.repo(t, name)
			if r.SourceFormat != want.format || r.Type != want.typ || r.Supported != want.supported || r.Public != want.public || r.Target != "" {
				t.Fatalf("source repo %s: %+v, want format %s, type %s, supported %v, public %v, no target",
					name, r, want.format, want.typ, want.supported, want.public)
			}
		}
		if r := got.repo(t, rs.offline); !r.Offline || !strings.Contains(r.Reason, "Offline in Nexus") {
			t.Fatalf("offline source repo: %+v, want offline with the reason", r)
		}
		if r := got.repo(t, rs.apt); r.Facts["key_in_source"] != true || r.Facts["key_encrypted"] != true || r.Facts["distribution"] != nexusAptDist ||
			!r.hasQuestion("signing_key", "choice", "default", "new", "source") || !r.hasQuestion("signing_passphrase", "secret") {
			t.Fatalf("apt source repo: %+v, want the facts of the Nexus key pair with a passphrase and the key questions", r)
		}
		if r := got.repo(t, rs.maven); len(r.Questions) != 0 {
			t.Fatalf("maven source repo: questions %+v, want none", r.Questions)
		}
		if r := got.repo(t, rs.proxy); r.RemoteURL != nexusRemoteURL || r.RemoteUsername != nexusRemoteUser {
			t.Fatalf("proxy source repo: %+v, want remote %s with username %s", r, nexusRemoteURL, nexusRemoteUser)
		}
		if r := got.repo(t, rs.group); !r.MembersKnown || !slices.Equal(r.Members, []string{rs.maven, rs.maven2, rs.proxy}) {
			t.Fatalf("group source repo: %+v, want the members %s, %s, %s", r, rs.maven, rs.maven2, rs.proxy)
		}

		// Save the repos: who can download follows Nexus, except raw, which is made public here.
		// The apt repo takes the Nexus key. The save creates the targets and starts a sync.
		targets := map[string]string{rs.maven: RandName("nxmvn"), rs.maven2: RandName("nxmvn2"), rs.apt: RandName("nxdeb"),
			rs.raw: RandName("nxraw"), rs.proxy: RandName("nxprx"), rs.group: RandName("nxgrp")}
		maps := []map[string]any{
			{"source_repo": rs.maven, "target": targets[rs.maven], "create": true, "anonymous_read": true},
			{"source_repo": rs.maven2, "target": targets[rs.maven2], "create": true, "anonymous_read": false},
			{"source_repo": rs.apt, "target": targets[rs.apt], "create": true, "anonymous_read": false,
				"answers": map[string]string{"signing_key": "source", "signing_passphrase": rs.aptPass}},
			{"source_repo": rs.raw, "target": targets[rs.raw], "create": true, "anonymous_read": true},
			{"source_repo": rs.proxy, "target": targets[rs.proxy], "create": true, "anonymous_read": false,
				"remote_username": nexusRemoteUser, "remote_password": nexusRemotePass},
			{"source_repo": rs.group, "target": targets[rs.group], "create": true, "anonymous_read": false},
		}
		first := nexusSaveAndWait(t, s, src.ID, maps)
		got = nexusGetSource(t, s, src.ID)
		for source, target := range targets {
			if r := got.repo(t, source); r.Target != target {
				t.Fatalf("source repo %s after the save: %+v, want target %s", source, r, target)
			}
		}
		if got.Status != "up_to_date" || got.NextSyncAt == nil || got.LastSync == nil || got.LastSync.FinishedAt == nil {
			t.Fatalf("source after the first sync: %+v, want up_to_date with the next sync", got)
		}
		if want := got.LastSync.FinishedAt.Add(12 * time.Hour); got.NextSyncAt.Sub(want).Abs() > 2*time.Second {
			t.Fatalf("next_sync_at %s, want one interval after the end of the sync, %s", got.NextSyncAt, want)
		}
		for source, want := range map[string]struct {
			format, kind string
			public       bool
		}{
			rs.maven: {"maven", "hosted", true}, rs.maven2: {"maven", "hosted", false}, rs.apt: {"debian", "hosted", false},
			rs.raw: {"raw", "hosted", true}, rs.proxy: {"maven", "proxy", false}, rs.group: {"maven", "group", false},
		} {
			info := nexusRepoInfoOf(t, s, targets[source])
			if info.Format != want.format || info.Kind != want.kind || info.AnonymousRead != want.public {
				t.Fatalf("target %s: %+v, want format %s, kind %s, public %v", targets[source], info, want.format, want.kind, want.public)
			}
		}

		// The proxy here has the Nexus remote, its credentials and its times; the group has the
		// three members in the Nexus order.
		prx := nexusRepoInfoOf(t, s, targets[rs.proxy])
		if prx.Proxy == nil || prx.Proxy.RemoteURL != nexusRemoteURL || !prx.Proxy.HasCredentials ||
			prx.Proxy.MetadataTTL != 3600 || prx.Proxy.NotFoundTTL != 600 {
			t.Fatalf("proxy %s: %+v, want remote %s with credentials, metadata_ttl 3600, not_found_ttl 600", targets[rs.proxy], prx.Proxy, nexusRemoteURL)
		}
		grp := nexusRepoInfoOf(t, s, targets[rs.group])
		var members []string
		for _, m := range grp.Members {
			members = append(members, m.Name)
		}
		if want := []string{targets[rs.maven], targets[rs.maven2], targets[rs.proxy]}; !slices.Equal(members, want) {
			t.Fatalf("group %s: members %v, want %v", targets[rs.group], members, want)
		}

		// First sync: everything imports. The proxy and the group copy no files.
		if len(first.Repos) != 4 {
			t.Fatalf("first sync: repos %+v, want the 4 hosted repos only", first.Repos)
		}
		for _, r := range first.Repos {
			if r.Checked != r.Imported+r.Skipped+r.Ignored+r.Failed || r.ActiveAt == nil || r.PreviousChecked != 0 {
				t.Fatalf("first sync of %s: %+v, want checked = imported + skipped + ignored + failed, an activity time and no previous walk", r.SourceRepo, r)
			}
		}
		if r := first.repo(t, rs.maven); r.Imported < 2 || r.Bytes == 0 {
			t.Fatalf("first sync of maven: %+v, want at least the jar and the pom imported", r)
		}
		if r, want := first.repo(t, rs.apt), int64(len(debs.hello1)+len(debs.native)); r.Imported != 2 || r.Bytes != want {
			t.Fatalf("first sync of apt: %+v, want 2 packages imported, %d bytes", r, want)
		}
		var rawBytes int64
		for _, f := range raws {
			rawBytes += int64(len(f.data))
		}
		if r := first.repo(t, rs.raw); r.Imported != int64(len(raws)) || r.Skipped != 0 || r.Ignored != 0 || r.Bytes != rawBytes {
			t.Fatalf("first sync of raw: %+v, want %d files imported, %d bytes", r, len(raws), rawBytes)
		}

		// The log: the start line, the lines of each repo, the proxy line and the finish line.
		log1 := nexusLog(t, s, first.ID, "")
		if len(log1) == 0 || log1[0].Message != "Started after the repositories were saved." {
			t.Fatalf("first sync log: %+v, want the save line first", log1)
		}
		nexusWantLine(t, log1, "Skipped 1 proxy")
		for _, repo := range []string{rs.maven, rs.maven2, rs.apt, rs.raw} {
			nexusWantRepoLine(t, log1, repo, "Started.")
			nexusWantRepoLine(t, log1, repo, "Done. Copied ")
		}
		if last := log1[len(log1)-1]; !strings.HasPrefix(last.Message, "Finished in ") {
			t.Fatalf("first sync log: last line %+v, want the finish line", last)
		}

		// An imported file has the Nexus blob dates.
		nexusWantDates(t, s, targets[rs.maven], groupPath+"/hello/1.0.0/hello-1.0.0.jar", n.asset(t, rs.maven, "/hello/1.0.0/hello-1.0.0.jar"))
		nexusWantDates(t, s, targets[rs.apt], "pool/main/e/e2e-hello/e2e-hello_1.0_all.deb", n.asset(t, rs.apt, "e2e-hello_1.0_all.deb"))

		// The apt repo signs with the Nexus key: Release.gpg verifies with the Nexus public key.
		s.WaitDebianIndexed(t, targets[rs.apt])
		if info := nexusRepoInfoOf(t, s, targets[rs.apt]); info.Fields.SigningKey != "nexus-"+targets[rs.apt] || info.Config.Distribution != nexusAptDist {
			t.Fatalf("apt target: signing key %q, distribution %q, want nexus-%s and %s", info.Fields.SigningKey, info.Config.Distribution, targets[rs.apt], nexusAptDist)
		}
		dists := "/debian/" + targets[rs.apt] + "/dists/" + nexusAptDist + "/"
		release := getFollowS3(t, s, opts.S3, dists+"Release")
		sig := getFollowS3(t, s, opts.S3, dists+"Release.gpg")
		ring, err := openpgp.ReadArmoredKeyRing(strings.NewReader(rs.aptPub))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := openpgp.CheckArmoredDetachedSignature(ring, bytes.NewReader(release), bytes.NewReader(sig), nil); err != nil {
			t.Fatalf("Release.gpg of %s does not verify with the Nexus public key: %v", targets[rs.apt], err)
		}

		nexusMavenResolve(t, s, targets[rs.maven], group, "1.0.0", message1)
		key := s.DebianKey(t, targets[rs.apt], s.AdminBasic())
		out := debAptInstall(t, s, targets[rs.apt], s.AdminUser, s.AdminPass, key)
		if !strings.Contains(out, "RESULT: hello 1.0\n") || !strings.Contains(out, "NATIVE: native "+debs.arch) {
			t.Fatalf("apt install after the first sync did not produce the expected output:\n%s", out)
		}

		// A new version in Nexus: the second sync imports only the new files, and each repo
		// knows the checked count of the first sync.
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
		if r, a := second.repo(t, rs.apt), first.repo(t, rs.apt); r.Imported != 1 || r.Skipped != a.Imported || r.Bytes != int64(len(debs.hello2)) {
			t.Fatalf("second sync of apt: %+v, want 1 package imported and %d skipped", r, a.Imported)
		}
		if r := second.repo(t, rs.raw); r.Imported != 1 || r.Skipped != int64(len(raws)) || r.Ignored != 0 || r.Bytes != int64(len(rawNew.data)) {
			t.Fatalf("second sync of raw: %+v, want 1 file imported and %d skipped", r, len(raws))
		}
		for _, r := range second.Repos {
			if r.PreviousChecked != first.repo(t, r.SourceRepo).Checked {
				t.Fatalf("second sync of %s: previous_checked %d, want the first sync's checked %d", r.SourceRepo, r.PreviousChecked, first.repo(t, r.SourceRepo).Checked)
			}
		}
		nexusWantLine(t, nexusLog(t, s, second.ID, ""), "Started by "+s.AdminUser+".")

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

		// A third sync with nothing new: each repo says so, and the log pages and filters.
		third := nexusSyncNow(t, s, src.ID)
		log3 := nexusLog(t, s, third.ID, "")
		for _, repo := range []string{rs.maven, rs.maven2, rs.apt, rs.raw} {
			nexusWantRepoLine(t, log3, repo, "No new files.")
			if r := third.repo(t, repo); r.Imported != 0 || r.PreviousChecked != second.repo(t, repo).Checked {
				t.Fatalf("third sync of %s: %+v, want nothing imported and previous_checked %d", repo, r, second.repo(t, repo).Checked)
			}
		}
		nexusWantLine(t, log3, "No new files.")
		head, next := nexusLogPage(t, s, third.ID, 0, "", 2)
		if len(head) != 2 || next == "" || head[0].ID != log3[0].ID || head[1].ID != log3[1].ID {
			t.Fatalf("log page of 2: %+v next %q, want the first 2 of %d lines and a cursor", head, next, len(log3))
		}
		rest, _ := nexusLogPage(t, s, third.ID, head[1].ID, "", 200)
		if len(rest) != len(log3)-2 || rest[0].ID != log3[2].ID {
			t.Fatalf("log after line %d: %d lines, want %d starting at line %d", head[1].ID, len(rest), len(log3)-2, log3[2].ID)
		}
		onlyRaw := nexusLog(t, s, third.ID, rs.raw)
		if len(onlyRaw) == 0 || slices.ContainsFunc(onlyRaw, func(l nexusLogLine) bool { return l.SourceRepo != rs.raw }) {
			t.Fatalf("log of repo %s: %+v, want only its lines", rs.raw, onlyRaw)
		}

		// Mirrored repos refuse publish sessions, so nothing uploads and the two servers cannot
		// diverge.
		for _, repo := range []string{targets[rs.maven], targets[rs.raw], targets[rs.apt]} {
			st, b := s.Mint(t, s.device(t, s.AdminUser), repo)
			nexusWantMirrorBlocked(t, "publish session for "+repo, st, b)
		}

		// Unpicking a repo stops its copy and unlocks it: an upload with a publish session works.
		var kept []map[string]any
		for _, m := range maps {
			if m["source_repo"] != rs.raw {
				kept = append(kept, map[string]any{"source_repo": m["source_repo"], "target": m["target"], "create": false})
			}
		}
		nexusSaveAndWait(t, s, src.ID, kept)
		if r := nexusGetSource(t, s, src.ID).repo(t, rs.raw); r.Target != "" {
			t.Fatalf("raw source repo after the unpick: %+v, want no target", r)
		}
		rawPath := "/raw/" + targets[rs.raw] + "/dist/after-unpick.txt"
		resp, b := rawPut(t, s, rawPath, []byte("written here"), s.PublishAuth(t, s.AdminUser, targets[rs.raw]))
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("raw PUT after the unpick: status %d, want 201: %s", resp.StatusCode, b)
		}

		// No log line holds the Nexus password or the remote password.
		var page struct {
			Items []nexusSync `json:"items"`
		}
		s.MustAPI(t, http.MethodGet, nexusSourcePath(src.ID)+"/syncs?limit=50", nil, s.Admin(), &page)
		for _, sy := range page.Items {
			for _, l := range nexusLog(t, s, sy.ID, "") {
				if strings.Contains(l.Message, n.Pass) || strings.Contains(l.Message, nexusRemotePass) {
					t.Fatalf("sync %d log line %d holds a password: %q", sy.ID, l.ID, l.Message)
				}
			}
		}

		// Cut over: uploads work here, and the source can no longer sync.
		nexusCutOver(t, s, src.ID)
		mavenPut := "/maven/" + targets[rs.maven] + "/" + groupPath + "/hello/9.9.9/hello-9.9.9.jar"
		resp, b = rawPut(t, s, mavenPut, []byte("not from nexus"), s.PublishAuth(t, s.AdminUser, targets[rs.maven]))
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("maven PUT after cut-over: status %d, want 201: %s", resp.StatusCode, b)
		}
		if st, b := debUpload(t, s, targets[rs.apt], debs.hello2, s.PublishAuth(t, s.AdminUser, targets[rs.apt])); st != http.StatusOK {
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

		// A docker repo, through a source of its own (this one is cut over).
		t.Run("docker", func(t *testing.T) { nexusDocker(t, n, s) })
		// A nuget repo, through a source of its own.
		t.Run("nuget", func(t *testing.T) { nexusNuGet(t, n, s) })
	})
}
