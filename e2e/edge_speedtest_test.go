//go:build e2e

package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// TestEdgeSpeedTest covers the server side of the speed test of the Edge delivery page on a
// server on S3 with no Worker in front of it: GET /api/edge/speedtest suggests the immutable files
// of public repos with their download paths, and POST /api/edge/speedtest writes a pass object
// with the SHA-256 of the token it answers to the home bucket. Both need a UI session with
// edge.manage, and a server on the local disk has no home bucket for a pass. No Cloudflare
// account is needed.
func TestEdgeSpeedTest(t *testing.T) {
	t.Parallel()
	tg := StartSeaweedFS(t)
	s := StartServer(t, ServerOpts{Storage: "s3", S3: tg})
	s.MustAPI(t, http.MethodPatch, "/api/settings", map[string]any{"edge.home_hint": "weur"}, s.Admin(), nil)

	// A public repo with a release (immutable) and a private one with a larger file.
	s.CreateRepo(t, "speed", "maven", true, nil)
	big := strings.Repeat("x", 4096)
	apiMavenPut(t, s, "speed", "com/acme/app/1.0/app-1.0.jar", big)
	s.CreateRepo(t, "closed", "maven", false, nil)
	apiMavenPut(t, s, "closed", "com/acme/app/1.0/app-1.0.jar", strings.Repeat("y", 8192))

	var files struct {
		BaseURL string `json:"base_url"`
		Unfit   string `json:"unfit"`
		Files   []struct {
			Repo string `json:"repo"`
			Path string `json:"path"`
			Size int64  `json:"size"`
			URL  string `json:"url"`
		} `json:"files"`
	}
	s.MustAPI(t, http.MethodGet, "/api/edge/speedtest", nil, s.Admin(), &files)
	if files.Unfit != "" || !strings.HasSuffix(files.BaseURL, "://"+s.Alias+":8080") {
		t.Errorf("speedtest unfit %q, base_url %q; want none and the base URL of %s", files.Unfit, files.BaseURL, s.Alias)
	}
	// The largest files of each public repo, at most two; the metadata can change, so it is not one.
	wantURL := "/maven/speed/com/acme/app/1.0/app-1.0.jar"
	if len(files.Files) == 0 || len(files.Files) > 2 || files.Files[0].URL != wantURL || files.Files[0].Size != int64(len(big)) {
		t.Fatalf("speedtest files = %+v, want the jar of speed first at %s", files.Files, wantURL)
	}
	for _, f := range files.Files {
		if f.Repo != "speed" || strings.Contains(f.Path, "maven-metadata.xml") {
			t.Errorf("speedtest suggests %s of %s, want only immutable files of the public repo", f.Path, f.Repo)
		}
	}
	// The suggested path downloads the file without a credential.
	rawWantGet(t, s, tg, files.Files[0].URL, nil, []byte(big))

	// A pass: a 256-bit token, and its object in the home bucket under the edge prefix.
	var pass struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	s.MustAPI(t, http.MethodPost, "/api/edge/speedtest", nil, s.Admin(), &pass)
	if raw, err := base64.RawURLEncoding.DecodeString(pass.Token); err != nil || len(raw) != 32 {
		t.Errorf("token %q: %v, want 32 bytes of base64url", pass.Token, err)
	}
	if !pass.ExpiresAt.After(time.Now()) {
		t.Errorf("expires_at %v is not in the future", pass.ExpiresAt)
	}
	sum := sha256.Sum256([]byte(pass.Token))
	hash := hex.EncodeToString(sum[:])
	key := tg.Prefix + "/edge/v1/speedtest/" + hash + ".json"
	obj, err := tg.Client().GetObject(context.Background(), &s3.GetObjectInput{Bucket: aws.String(tg.Bucket), Key: aws.String(key)})
	if err != nil {
		t.Fatalf("get the pass object %s: %v (keys: %v)", key, err, tg.ListKeys(t))
	}
	b, err := io.ReadAll(obj.Body)
	obj.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	var stored struct {
		Hash string `json:"hash"`
		Exp  int64  `json:"exp"`
	}
	if err := json.Unmarshal(b, &stored); err != nil || stored.Hash != hash || stored.Exp != pass.ExpiresAt.Unix() {
		t.Errorf("pass object = %s (%v), want hash %s and exp %d", b, err, hash, pass.ExpiresAt.Unix())
	}
	if strings.Contains(string(b), pass.Token) {
		t.Errorf("the pass object holds the token itself: %s", b)
	}

	// Only a UI session with edge.manage gets the files or a pass.
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		if st, b := s.API(t, method, "/api/edge/speedtest", nil, s.AdminBasic()); st != http.StatusUnauthorized {
			t.Errorf("%s /api/edge/speedtest with a password = %d: %s, want 401", method, st, b)
		}
	}
	s.CreateUser(t, "bob", "bob-password-1")
	bob := s.SignIn(t, "bob", "bob-password-1")
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		if st, b := s.API(t, method, "/api/edge/speedtest", nil, bob); st != http.StatusForbidden {
			t.Errorf("%s /api/edge/speedtest without edge.manage = %d: %s, want 403", method, st, b)
		}
	}

	// A server on the local disk has no home bucket for a pass.
	local := StartServer(t, ServerOpts{})
	if st, b := local.API(t, http.MethodPost, "/api/edge/speedtest", nil, local.Admin()); st != http.StatusConflict || !strings.Contains(string(b), "edge-not-configured") {
		t.Errorf("pass on fs = %d: %s, want 409 edge-not-configured", st, b)
	}
}
