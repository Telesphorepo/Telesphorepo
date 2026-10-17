//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type settingItem struct {
	Key    string          `json:"key"`
	Group  string          `json:"group"`
	Source string          `json:"source"`
	Locked bool            `json:"locked"`
	Value  json.RawMessage `json:"value"`
	// Display is the size or duration in words.
	Display string `json:"display"`
}

// listSettings fetches GET /api/settings as the admin ({groups:[{id, settings:[item]}]}) and
// returns the items by key plus the raw body.
func listSettings(t *testing.T, s *Server) (map[string]settingItem, string) {
	t.Helper()
	st, body := s.API(t, http.MethodGet, "/api/settings", nil, s.Admin())
	if st != http.StatusOK {
		t.Fatalf("GET /api/settings: status %d: %s", st, body)
	}
	var resp struct {
		Groups []struct {
			ID       string        `json:"id"`
			Settings []settingItem `json:"settings"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode settings: %v: %s", err, body)
	}
	var items []settingItem
	for _, g := range resp.Groups {
		for _, it := range g.Settings {
			if it.Group != g.ID {
				t.Errorf("setting %s has group %q but is listed under %q", it.Key, it.Group, g.ID)
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
	t.Parallel()
	s := StartServer(t, ServerOpts{})
	items, _ := listSettings(t, s)
	groups := map[string]bool{}
	for _, it := range items {
		groups[it.Group] = true
	}
	for _, g := range []string{"general", "activity", "storage", "network", "publishing", "sso", "nexus"} {
		if !groups[g] {
			t.Errorf("group %q missing from settings (have %v)", g, groups)
		}
	}
	s.CreateUser(t, "plain", "plainpass123")
	plain := s.SignIn(t, "plain", "plainpass123")
	if st, b := s.API(t, http.MethodGet, "/api/settings", nil, plain); st != http.StatusForbidden {
		t.Errorf("non-admin GET /api/settings: status %d, want 403: %s", st, b)
	}
	if st, b := s.API(t, http.MethodGet, "/api/settings", nil, nil); st != http.StatusUnauthorized {
		t.Errorf("anonymous GET /api/settings: status %d, want 401: %s", st, b)
	}
	if st, _ := s.API(t, http.MethodPatch, "/api/settings", map[string]any{"max_json_size": 4096}, plain); st != http.StatusForbidden {
		t.Errorf("non-admin PATCH: status %d, want 403", st)
	}
}

func TestSettingsLiveUpdateAndEnvLock(t *testing.T) {
	t.Parallel()
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
	if mj := items["max_json_size"]; mj.Locked || mj.Source == "env" {
		t.Fatalf("max_json_size unexpectedly env-controlled: %+v", mj)
	}

	// Lower the JSON body limit; it applies without a restart.
	big := map[string]any{"name": strings.Repeat("x", 8192)}
	if st, _ := s.API(t, http.MethodPost, "/api/tokens", big, s.Admin()); st == http.StatusRequestEntityTooLarge {
		t.Fatalf("8KiB token request already rejected with default limit")
	}
	s.MustAPI(t, http.MethodPatch, "/api/settings", map[string]any{"max_json_size": 2048}, s.Admin(), nil)
	if st, b := s.API(t, http.MethodPost, "/api/tokens", big, s.Admin()); st != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize JSON after lowering limit: status %d, want 413: %s", st, b)
	}
	items, _ = listSettings(t, s)
	if got := items["max_json_size"]; got.Source != "stored" || string(got.Value) != "2048" || got.Display != "2 KiB" {
		t.Fatalf("max_json_size after patch: %+v", got)
	}
	// null resets to the default and the large body passes again.
	s.MustAPI(t, http.MethodPatch, "/api/settings", map[string]any{"max_json_size": nil}, s.Admin(), nil)
	if st, b := s.API(t, http.MethodPost, "/api/tokens", big, s.Admin()); st == http.StatusRequestEntityTooLarge {
		t.Fatalf("still 413 after reset: %s", b)
	}
	// Invalid input is a 400.
	if st, b := s.API(t, http.MethodPatch, "/api/settings", map[string]any{"max_json_size": "lots"}, s.Admin()); st != http.StatusBadRequest {
		t.Fatalf("invalid value: status %d, want 400: %s", st, b)
	}
}

// TestSettingsSurviveRestart checks that stored settings, a secret among them, come back from the
// database after a restart, and that the secret stays hidden.
func TestSettingsSurviveRestart(t *testing.T) {
	t.Parallel()
	s := StartServer(t, ServerOpts{})
	const secret = "restart-client-secret"
	s.MustAPI(t, http.MethodPatch, "/api/settings", map[string]any{
		"max_json_size":       2048,
		"publish.session_ttl": "30m",
		"oidc.client_secret":  secret,
	}, s.Admin(), nil)
	s.Restart(t)

	items, raw := listSettings(t, s)
	if got := items["max_json_size"]; got.Source != "stored" || string(got.Value) != "2048" {
		t.Errorf("max_json_size after a restart: %+v", got)
	}
	if got := items["publish.session_ttl"]; got.Source != "stored" || got.Display != "30 minutes" {
		t.Errorf("publish.session_ttl after a restart: %+v", got)
	}
	var sv struct {
		Set bool `json:"set"`
	}
	got := items["oidc.client_secret"]
	if err := json.Unmarshal(got.Value, &sv); err != nil || got.Source != "stored" || !sv.Set {
		t.Errorf("oidc.client_secret after a restart: %+v (decode: %v)", got, err)
	}
	if strings.Contains(raw, secret) {
		t.Fatal("GET /api/settings shows the client secret after a restart")
	}
	// The stored limit applies after the restart.
	big := map[string]any{"name": strings.Repeat("x", 8192)}
	if st, b := s.API(t, http.MethodPost, "/api/tokens", big, s.Admin()); st != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize JSON after a restart: status %d, want 413: %s", st, b)
	}
}

// TestSettingsSizeAndDuration checks the friendly input and the display text of size and
// duration settings.
func TestSettingsSizeAndDuration(t *testing.T) {
	t.Parallel()
	s := StartServer(t, ServerOpts{})
	patch := func(key string, v any) {
		t.Helper()
		s.MustAPI(t, http.MethodPatch, "/api/settings", map[string]any{key: v}, s.Admin(), nil)
	}
	for _, c := range []struct{ in, value, display string }{
		{"2GiB", "2147483648", "2 GiB"},
		{"1.5 GiB", "1610612736", "1536 MiB"},
		{"500MB", "500000000", "476.8 MiB"},
		{"512 mib", "536870912", "512 MiB"},
		{"4096", "4096", "4 KiB"},
	} {
		patch("max_upload_size", c.in)
		got := func() settingItem { items, _ := listSettings(t, s); return items["max_upload_size"] }()
		if string(got.Value) != c.value || got.Display != c.display || got.Source != "stored" {
			t.Errorf("max_upload_size %q: value %s display %q, want %s / %q", c.in, got.Value, got.Display, c.value, c.display)
		}
	}
	for _, bad := range []any{"5 parsecs", "MiB", "-1GiB", "0", "0.5 B"} {
		st, b := s.API(t, http.MethodPatch, "/api/settings", map[string]any{"max_upload_size": bad}, s.Admin())
		if st != http.StatusBadRequest || !strings.Contains(string(b), `"max_upload_size"`) {
			t.Errorf("max_upload_size %v: status %d, want 400 with a field error: %s", bad, st, b)
		}
	}

	for _, c := range []struct{ in, value, display string }{
		{"90 days", "2160h", "90 days"},
		{"365d", "8760h", "365 days"},
		{"15 minutes", "15m", "15 minutes"},
		{"1h30m", "1h30m", "1 hour 30 minutes"},
		{"12h", "12h", "12 hours"},
	} {
		patch("publish.device_ttl", c.in)
		items, _ := listSettings(t, s)
		got := items["publish.device_ttl"]
		if string(got.Value) != `"`+c.value+`"` || got.Display != c.display {
			t.Errorf("publish.device_ttl %q: value %s display %q, want %q / %q", c.in, got.Value, got.Display, c.value, c.display)
		}
	}
	if st, b := s.API(t, http.MethodPatch, "/api/settings", map[string]any{"publish.device_ttl": "soon"}, s.Admin()); st != http.StatusBadRequest {
		t.Errorf("bad duration: status %d, want 400: %s", st, b)
	}
}

// TestSettingsSizeEnv checks that TR_MAX_UPLOAD_SIZE with a unit limits uploads.
func TestSettingsSizeEnv(t *testing.T) {
	t.Parallel()
	s := StartServer(t, ServerOpts{Env: map[string]string{"TR_MAX_UPLOAD_SIZE": "1MiB"}})
	items, _ := listSettings(t, s)
	if got := items["max_upload_size"]; got.Source != "env" || string(got.Value) != "1048576" || got.Display != "1 MiB" {
		t.Fatalf("max_upload_size from env: %+v", got)
	}
	repo := RandName("mvn")
	s.CreateRepo(t, repo, "maven", false, nil)
	p := "/maven/" + repo + "/com/example/lib/1.0.0/lib-1.0.0.jar"
	// Send "Expect: 100-continue", as Maven and curl do for a large PUT: the server refuses
	// before the body is sent. Without it, the server answers 413 before it reads the body and
	// closes the connection, and the port forwarder of rootless Podman can drop the answer.
	req := s.NewRequest(t, http.MethodPut, p, strings.NewReader(strings.Repeat("x", 2<<20)))
	req.Header.Set("Expect", "100-continue")
	s.PublishAuth(t, s.AdminUser, repo).apply(req)
	c := &http.Client{Timeout: time.Minute, Transport: &http.Transport{ExpectContinueTimeout: 30 * time.Second}}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("2 MiB PUT with a 1 MiB limit: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("2 MiB PUT with a 1 MiB limit: status %d, want 413", resp.StatusCode)
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
	t.Parallel()
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
		OK bool `json:"ok"`
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
	t.Parallel()
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
		if !strings.HasSuffix(k, blobKey(sha256Hex([]byte(content)))) {
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
		t.Fatalf("no blob of lib-1.0.0.jar in bucket under %s: %v", tg.Prefix, keys)
	}
}
