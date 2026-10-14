//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type settingItem struct {
	Key    string          `json:"key"`
	Group  string          `json:"group"`
	Source string          `json:"source"`
	Locked bool            `json:"locked"`
	Value  json.RawMessage `json:"value"`
}

// listSettings fetches GET /api/settings as the admin ({groups:[{name, settings:[item]}]}) and
// returns the items by key plus the raw body.
func listSettings(t *testing.T, s *Server) (map[string]settingItem, string) {
	t.Helper()
	st, body := s.API(t, http.MethodGet, "/api/settings", nil, s.Admin())
	if st != http.StatusOK {
		t.Fatalf("GET /api/settings: status %d: %s", st, body)
	}
	var resp struct {
		Groups []struct {
			Name     string        `json:"name"`
			Settings []settingItem `json:"settings"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode settings: %v: %s", err, body)
	}
	var items []settingItem
	for _, g := range resp.Groups {
		for _, it := range g.Settings {
			if it.Group != g.Name {
				t.Errorf("setting %s has group %q but is listed under %q", it.Key, it.Group, g.Name)
			}
			items = append(items, it)
		}
	}
	if len(items) == 0 {
		t.Fatalf("no settings items in %s", body)
	}
	m := map[string]settingItem{}
	for _, it := range items {
		m[it.Key] = it
	}
	return m, string(body)
}

func TestSettingsAccess(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	items, _ := listSettings(t, s)
	groups := map[string]bool{}
	for _, it := range items {
		groups[it.Group] = true
	}
	for _, g := range []string{"general", "storage", "security", "signing", "debian"} {
		if !groups[g] {
			t.Errorf("group %q missing from settings (have %v)", g, groups)
		}
	}
	s.CreateUser(t, "plain", "plainpass123", false)
	plain := s.SignIn(t, "plain", "plainpass123")
	if st, b := s.API(t, http.MethodGet, "/api/settings", nil, plain); st != http.StatusForbidden {
		t.Errorf("non-admin GET /api/settings: status %d, want 403: %s", st, b)
	}
	if st, b := s.API(t, http.MethodGet, "/api/settings", nil, nil); st != http.StatusUnauthorized {
		t.Errorf("anonymous GET /api/settings: status %d, want 401: %s", st, b)
	}
	if st, _ := s.API(t, http.MethodPatch, "/api/settings", map[string]any{"max_json_bytes": 4096}, plain); st != http.StatusForbidden {
		t.Errorf("non-admin PATCH: status %d, want 403", st)
	}
}

func TestSettingsLiveUpdateAndEnvLock(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	items, _ := listSettings(t, s)

	// cookie.secure is forced by the harness env.
	cs := items["cookie.secure"]
	if cs.Source != "env" || !cs.Locked {
		t.Fatalf("cookie.secure: source=%q locked=%v, want env/true", cs.Source, cs.Locked)
	}
	if st, b := s.API(t, http.MethodPatch, "/api/settings", map[string]any{"cookie.secure": true}, s.Admin()); st != http.StatusConflict {
		t.Fatalf("PATCH env-locked cookie.secure=true: status %d, want 409: %s", st, b)
	}
	if mj := items["max_json_bytes"]; mj.Locked || mj.Source == "env" {
		t.Fatalf("max_json_bytes unexpectedly env-controlled: %+v", mj)
	}

	// Lower the JSON body limit; it applies without a restart.
	big := map[string]any{"name": strings.Repeat("x", 8192)}
	if st, _ := s.API(t, http.MethodPost, "/api/tokens", big, s.Admin()); st == http.StatusRequestEntityTooLarge {
		t.Fatalf("8KiB token request already rejected with default limit")
	}
	s.MustAPI(t, http.MethodPatch, "/api/settings", map[string]any{"max_json_bytes": 2048}, s.Admin(), nil)
	if st, b := s.API(t, http.MethodPost, "/api/tokens", big, s.Admin()); st != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize JSON after lowering limit: status %d, want 413: %s", st, b)
	}
	items, _ = listSettings(t, s)
	if got := items["max_json_bytes"]; got.Source != "db" || string(got.Value) != "2048" {
		t.Fatalf("max_json_bytes after patch: %+v", got)
	}
	// null resets to the default and the large body passes again.
	s.MustAPI(t, http.MethodPatch, "/api/settings", map[string]any{"max_json_bytes": nil}, s.Admin(), nil)
	if st, b := s.API(t, http.MethodPost, "/api/tokens", big, s.Admin()); st == http.StatusRequestEntityTooLarge {
		t.Fatalf("still 413 after reset: %s", b)
	}
	// Invalid input is a 400.
	if st, b := s.API(t, http.MethodPatch, "/api/settings", map[string]any{"max_json_bytes": "lots"}, s.Admin()); st != http.StatusBadRequest {
		t.Fatalf("invalid value: status %d, want 400: %s", st, b)
	}
}

func TestSettingsSigningKey(t *testing.T) {
	s := StartServer(t, ServerOpts{NoSigningKey: true})
	pubKey := func() string {
		req := s.NewRequest(t, http.MethodGet, "/keys/debian.asc", nil)
		resp, b := s.Do(t, req, nil)
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(b), "BEGIN PGP PUBLIC KEY BLOCK") {
			t.Fatalf("GET /keys/debian.asc: status %d: %s", resp.StatusCode, b)
		}
		return string(b)
	}
	before := pubKey()
	_, raw := listSettings(t, s)
	if strings.Contains(raw, "PRIVATE KEY") {
		t.Fatal("GET /api/settings leaks private key material")
	}

	var out struct {
		PublicKey   string `json:"public_key"`
		Fingerprint string `json:"fingerprint"`
	}
	s.MustAPI(t, http.MethodPost, "/api/settings/signing-key", map[string]any{}, s.Admin(), &out)
	if out.Fingerprint == "" {
		t.Fatal("signing-key response has no fingerprint")
	}
	after := pubKey()
	if after == before {
		t.Fatal("/keys/debian.asc unchanged after generating a new signing key")
	}
	if out.PublicKey != "" && strings.TrimSpace(out.PublicKey) != strings.TrimSpace(after) {
		t.Fatal("response public_key differs from /keys/debian.asc")
	}
	var out2 struct {
		Fingerprint string `json:"fingerprint"`
	}
	s.MustAPI(t, http.MethodPost, "/api/settings/signing-key", map[string]any{}, s.Admin(), &out2)
	if out2.Fingerprint == out.Fingerprint {
		t.Fatal("fingerprint did not change on regeneration")
	}
	_, raw = listSettings(t, s)
	if strings.Contains(raw, "PRIVATE KEY") {
		t.Fatal("GET /api/settings leaks private key material after generation")
	}
	// The signing key cannot be written through PATCH.
	if st, _ := s.API(t, http.MethodPatch, "/api/settings", map[string]any{"signing.key": SigningPrivateKey}, s.Admin()); st != http.StatusBadRequest {
		t.Fatalf("PATCH signing.key: status %d, want 400", st)
	}
	// Non-admins cannot generate keys.
	s.CreateUser(t, "plain", "plainpass123", false)
	if st, _ := s.API(t, http.MethodPost, "/api/settings/signing-key", map[string]any{}, s.SignIn(t, "plain", "plainpass123")); st != http.StatusForbidden {
		t.Fatalf("non-admin signing-key: status %d, want 403", st)
	}
}

func putGetMaven(t *testing.T, s *Server, repo, name, content string) {
	t.Helper()
	p := "/maven/" + repo + "/com/example/lib/1.0.0/" + name
	req := s.NewRequest(t, http.MethodPut, p, strings.NewReader(content))
	if resp, b := s.Do(t, req, s.PublishAuth(t, s.AdminUser, repo)); resp.StatusCode/100 != 2 {
		t.Fatalf("PUT %s: status %d: %s", p, resp.StatusCode, b)
	}
	resp, b := s.Do(t, s.NewRequest(t, http.MethodGet, p, nil), s.AdminBasic())
	if resp.StatusCode != http.StatusOK || string(b) != content {
		t.Fatalf("GET %s: status %d body %q, want %q", p, resp.StatusCode, b, content)
	}
}

func TestSettingsStorageBogusS3Refused(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	repo := RandName("mvn")
	s.CreateRepo(t, repo, "maven", false, nil)
	bogus := map[string]any{
		"storage":              "s3",
		"s3.endpoint":          "http://127.0.0.1:1",
		"s3.bucket":            "nope",
		"s3.access_key_id":     "a",
		"s3.secret_access_key": "b",
		"s3.path_style":        true,
	}
	var res struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	st, b := s.API(t, http.MethodPost, "/api/settings/storage/test", bogus, s.Admin())
	if st != http.StatusBadRequest {
		t.Fatalf("storage/test with bogus endpoint: status %d, want 400: %s", st, b)
	}
	_ = json.Unmarshal(b, &res)
	if res.OK {
		t.Fatalf("storage/test reported ok=true: %s", b)
	}
	if st, b := s.API(t, http.MethodPatch, "/api/settings", bogus, s.Admin()); st < 400 || st >= 500 {
		t.Fatalf("PATCH to bogus s3: status %d, want 4xx: %s", st, b)
	}
	items, _ := listSettings(t, s)
	if v := string(items["storage"].Value); v != `"fs"` {
		t.Fatalf("storage after refused switch = %s, want \"fs\"", v)
	}
	if strings.Contains(string(items["s3.endpoint"].Value), "127.0.0.1") {
		t.Fatal("refused patch was partially saved")
	}
	putGetMaven(t, s, repo, "lib-1.0.0.jar", "still-on-fs")
}

func TestSettingsLiveSwitchToS3(t *testing.T) {
	tg := StartSeaweedFS(t)
	s := StartServer(t, ServerOpts{})
	repo := RandName("mvn")
	s.CreateRepo(t, repo, "maven", false, nil)
	// No blob is stored yet, so the PATCH switches at once (with blobs it starts a move, see
	// TestStorageMove).

	patch := map[string]any{
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
	var res struct {
		OK bool `json:"ok"`
	}
	s.MustAPI(t, http.MethodPost, "/api/settings/storage/test", patch, s.Admin(), &res)
	if !res.OK {
		t.Fatal("storage/test against SeaweedFS returned ok=false")
	}
	s.MustAPI(t, http.MethodPatch, "/api/settings", patch, s.Admin(), nil)

	items, raw := listSettings(t, s)
	if v := string(items["storage"].Value); v != `"s3"` {
		t.Fatalf("storage = %s after switch", v)
	}
	if strings.Contains(raw, tg.SecretKey) {
		t.Fatal("GET /api/settings leaks the S3 secret access key")
	}

	content := "live-switched-" + randHex(4)
	putGetMaven(t, s, repo, "lib-1.0.0.jar", content)

	keys := tg.ListKeys(t)
	var found bool
	for _, k := range keys {
		if !strings.HasSuffix(k, "lib-1.0.0.jar") {
			continue
		}
		found = true
		out, err := tg.Client().GetObject(context.Background(), &s3.GetObjectInput{Bucket: aws.String(tg.Bucket), Key: aws.String(k)})
		if err != nil {
			t.Fatalf("get %s from bucket: %v", k, err)
		}
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(out.Body)
		out.Body.Close()
		if buf.String() != content {
			t.Fatalf("bucket object %s = %q, want %q", k, buf.String(), content)
		}
	}
	if !found {
		t.Fatalf("no lib-1.0.0.jar object in bucket under %s: %v", tg.Prefix, keys)
	}
}
