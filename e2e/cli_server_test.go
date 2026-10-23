//go:build e2e

package e2e

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/clidist"
)

// The server side of the Telesphorepo CLI that needs no external tool: setup codes and
// POST /api/devices/setup, the self revoke of a device, sessions from a token exchange nonce and
// the device read proof, and the CLI downloads with the install scripts.

// wantProblem fails unless the answer is the named problem document (urn:telesphorepo:problem:
// name) with status, a title and a detail, and returns it.
func wantProblem(t *testing.T, what string, st int, b []byte, status int, name string) Problem {
	t.Helper()
	var p Problem
	if st != status || json.Unmarshal(b, &p) != nil || p.Status != status || p.Type != "urn:telesphorepo:problem:"+name ||
		p.Title == "" || p.Detail == "" {
		t.Fatalf("%s: status %d: %s, want %d and the problem %s with a title and a detail", what, st, b, status, name)
	}
	return p
}

// setupResult is the answer of POST /api/devices/setup.
type setupResult struct {
	DeviceID int64  `json:"device_id"`
	User     string `json:"user"`
	Repo     string `json:"repo"`
	Format   string `json:"format"`
	Kind     string `json:"kind"`
}

// newAuditEvents runs do and returns the events of the audit query q that do added.
func newAuditEvents(t *testing.T, s *Server, q string, do func()) []auditEntry {
	t.Helper()
	seen := map[int64]bool{}
	for _, e := range auditAll(t, s, q) {
		seen[e.ID] = true
	}
	do()
	var added []auditEntry
	for _, e := range auditAll(t, s, q) {
		if !seen[e.ID] {
			added = append(added, e)
		}
	}
	return added
}

// signText signs msg with key, as base64url without padding.
func signText(key ed25519.PrivateKey, msg string) string { return b64(ed25519.Sign(key, []byte(msg))) }

// TestSetupCodes: a person with publish on the repo and a fresh second factor makes a setup code;
// the code enrolls a new device key, or takes the proof of the device that the person has, and
// answers the repo with its format and kind; a used, unknown or expired code answers the problem
// setup-code-used with a link; the device of another person leaves the code for its owner.
func TestSetupCodes(t *testing.T) {
	t.Parallel()
	vol := NewDataVolume(t)
	s := StartServer(t, ServerOpts{DataVolume: vol})
	repo, img := RandName("app"), RandName("img")
	s.CreateRepo(t, repo, "maven", false, nil)
	s.CreateRepo(t, img, "docker", false, nil)
	dev, reader := RandName("dev"), RandName("reader")
	s.CreateUser(t, dev, "dev-pass-1234")
	s.CreateUser(t, reader, "reader-pass-1234")
	for _, r := range []string{repo, img} {
		s.SetPerms(t, r, dev, "repo.download", "repo.publish")
	}
	s.SetPerms(t, repo, reader, "repo.download")
	setup := func(body map[string]any) (int, []byte) {
		t.Helper()
		return s.API(t, http.MethodPost, "/api/devices/setup", body, nil)
	}
	parse := func(what string, st int, b []byte) setupResult {
		t.Helper()
		var res setupResult
		if st != http.StatusOK || json.Unmarshal(b, &res) != nil || res.DeviceID <= 0 {
			t.Fatalf("%s: status %d: %s, want 200 with a device id", what, st, b)
		}
		return res
	}

	t.Run("create", func(t *testing.T) {
		st, b := s.API(t, http.MethodPost, "/api/publish/setup-codes", map[string]string{"repo": repo}, s.LoginTOTP(t, dev))
		wantAPIStatus(t, "a setup code without the second factor", st, b, http.StatusBadRequest)
		st, b = s.API(t, http.MethodPost, "/api/publish/setup-codes", map[string]string{"repo": repo}, s.TOTPProof(t, s.LoginTOTP(t, reader), reader))
		wantAPIStatus(t, "a setup code of a person who only downloads", st, b, http.StatusForbidden)
		var c SetupCode
		es := newAuditEvents(t, s, "action=setup_code.create", func() { c = s.NewSetupCode(t, dev, repo) })
		// A minute either way leaves room for the clock of the container.
		if left := time.Until(c.ExpiresAt); left < auth.SetupCodeTTL-time.Minute || left > auth.SetupCodeTTL+time.Minute {
			t.Fatalf("setup code expires at %s, want in %s", c.ExpiresAt, auth.SetupCodeTTL)
		}
		if len(es) != 1 || es[0].Repo != repo || es[0].Actor != dev || es[0].Detail["expires_at"] != float64(c.ExpiresAt.Unix()) {
			t.Fatalf("audit setup_code.create: %+v, want one new event of %s on %s that expires at %d", es, dev, repo, c.ExpiresAt.Unix())
		}
	})

	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var d *Device
	t.Run("a new device", func(t *testing.T) {
		c := s.NewSetupCode(t, dev, repo)
		body := map[string]any{"code": c.Code, "name": "laptop", "public_key": b64(pub)}
		var st int
		var b []byte
		es := newAuditEvents(t, s, "action=device.setup", func() { st, b = setup(body) })
		res := parse("set up with a new device", st, b)
		if len(es) != 1 || es[0].Actor != dev || es[0].Detail["new"] != true {
			t.Fatalf("audit device.setup: %+v, want one new event of %s with a new device", es, dev)
		}
		if res.User != dev || res.Repo != repo || res.Format != "maven" || res.Kind != "hosted" {
			t.Fatalf("set up with a new device: %+v, want %s, %s, maven, hosted", res, dev, repo)
		}
		d = &Device{ID: res.DeviceID, User: dev, Key: key}
		ps := mustSession(t, "mint with the device of the setup code")(s.Mint(t, d, repo))
		resp, b := pubPut(t, s, repo, ps.Auth())
		wantStatus(t, resp, b, http.StatusCreated)

		st, b = setup(body)
		p := wantProblem(t, "the setup code again", st, b, http.StatusUnauthorized, "setup-code-used")
		if p.Remedy == nil || p.Remedy.Action != "open_url" || p.Remedy.URL != s.InternalURL+"/" {
			t.Fatalf("the setup code again: remedy %+v, want open_url %s/", p.Remedy, s.InternalURL)
		}
	})
	if d == nil {
		t.FailNow()
	}

	t.Run("the device of this computer", func(t *testing.T) {
		c := s.NewSetupCode(t, dev, img)
		id := strconv.FormatInt(d.ID, 10)
		_, otherKey, _ := ed25519.GenerateKey(rand.Reader)
		nonce := s.Challenge(t, d.ID)
		st, b := setup(map[string]any{"code": c.Code, "device_id": d.ID, "nonce": nonce, "signature": signText(otherKey, "telesphorepo setup v1\n"+nonce+"\n"+id)})
		wantAPIStatus(t, "set up with the signature of another key", st, b, http.StatusUnauthorized)

		// The device of another person refuses, and the code stays for its owner.
		other := s.EnrollDevice(t, reader)
		nonce = s.Challenge(t, other.ID)
		st, b = setup(map[string]any{"code": c.Code, "device_id": other.ID, "nonce": nonce,
			"signature": signText(other.Key, "telesphorepo setup v1\n"+nonce+"\n"+strconv.FormatInt(other.ID, 10))})
		wantAPIStatus(t, "set up with the device of another person", st, b, http.StatusConflict)

		nonce = s.Challenge(t, d.ID)
		st, b = setup(map[string]any{"code": c.Code, "device_id": d.ID, "nonce": nonce, "signature": signText(key, "telesphorepo setup v1\n"+nonce+"\n"+id)})
		res := parse("set up with the device", st, b)
		if res.DeviceID != d.ID || res.Repo != img || res.Format != "docker" || res.Kind != "hosted" {
			t.Fatalf("set up with the device: %+v, want device %d, %s, docker, hosted", res, d.ID, img)
		}
	})

	t.Run("an unknown code", func(t *testing.T) {
		st, b := setup(map[string]any{"code": "trsu_" + randHex(32), "name": "laptop", "public_key": b64(make([]byte, 32))})
		p := wantProblem(t, "an unknown setup code", st, b, http.StatusUnauthorized, "setup-code-used")
		if p.Remedy == nil || p.Remedy.URL != s.InternalURL+"/" {
			t.Fatalf("an unknown setup code: remedy %+v, want the server root", p.Remedy)
		}
	})

	t.Run("an expired code", func(t *testing.T) {
		c := s.NewSetupCode(t, dev, repo)
		s.RestartAfter(t, func() {
			editDB(t, vol, "UPDATE auth_challenges SET expires_at = 1 WHERE kind = 'setup_code'")
		})
		newKey, _, _ := ed25519.GenerateKey(rand.Reader)
		st, b := setup(map[string]any{"code": c.Code, "name": "laptop", "public_key": b64(newKey)})
		p := wantProblem(t, "an expired setup code", st, b, http.StatusUnauthorized, "setup-code-used")
		if !strings.Contains(p.Detail, "expired") || p.Remedy == nil || p.Remedy.URL != s.InternalURL+"/#/repo/"+repo {
			t.Fatalf("an expired setup code: %+v, want a detail that says expired and the link to %s", p, repo)
		}
	})
}

// TestDeviceSelfRevoke: a device revokes itself with its own signature (DELETE
// /api/devices/self); its sessions end and the audit log names the device as the cause.
func TestDeviceSelfRevoke(t *testing.T) {
	t.Parallel()
	s := StartServer(t, ServerOpts{})
	repo := RandName("app")
	s.CreateRepo(t, repo, "maven", false, nil)
	dev := RandName("dev")
	s.CreateUser(t, dev, "dev-pass-1234")
	s.SetPerms(t, repo, dev, "repo.download", "repo.publish")
	d := s.EnrollDevice(t, dev)
	ps := mustSession(t, "mint")(s.Mint(t, d, repo))
	revoke := func(key ed25519.PrivateKey, nonce string) (int, []byte) {
		t.Helper()
		sig := signText(key, fmt.Sprintf("telesphorepo revoke v1\n%s\n%d", nonce, d.ID))
		return s.API(t, http.MethodDelete, "/api/devices/self", map[string]any{"device_id": d.ID, "nonce": nonce, "signature": sig}, nil)
	}

	_, otherKey, _ := ed25519.GenerateKey(rand.Reader)
	st, b := revoke(otherKey, s.Challenge(t, d.ID))
	wantAPIStatus(t, "revoke with the signature of another key", st, b, http.StatusUnauthorized)
	nonce := s.Challenge(t, d.ID)
	st, b = revoke(d.Key, nonce)
	wantAPIStatus(t, "revoke with the device signature", st, b, http.StatusNoContent)
	st, b = revoke(d.Key, nonce)
	wantAPIStatus(t, "revoke with a used nonce", st, b, http.StatusUnauthorized)

	resp, body := pubPut(t, s, repo, ps.Auth())
	wantPublishRequired(t, resp, body)
	st, b = s.API(t, http.MethodPost, "/api/publish/challenge", map[string]any{"device_id": d.ID}, nil)
	wantAPIStatus(t, "challenge for a device that revoked itself", st, b, http.StatusUnauthorized)
	var devices struct {
		Items []struct {
			ID    int64  `json:"id"`
			State string `json:"state"`
		} `json:"items"`
	}
	s.MustAPI(t, http.MethodGet, "/api/devices", nil, s.LoginTOTP(t, dev), &devices)
	if len(devices.Items) != 1 || devices.Items[0].ID != d.ID || devices.Items[0].State != "revoked" {
		t.Fatalf("GET /api/devices: %+v, want device %d revoked", devices.Items, d.ID)
	}
	if es := auditAll(t, s, "action=device.revoke"); len(es) != 1 || es[0].Actor != dev || es[0].Detail["by"] != "device" {
		t.Fatalf("audit device.revoke: %+v, want one event of %s by the device", es, dev)
	}
}

// bearer is an Authorization: Bearer credential.
type bearer string

func (b bearer) apply(r *http.Request) { r.Header.Set("Authorization", "Bearer "+string(b)) }

// TestExchangeSession: a token exchange nonce needs the device signature over a recent time and
// is kept on request; its proof (trx.<device id>.<nonce>.<signature>) mints one session per repo
// (POST /api/publish/sessions/exchange) and answers the same session again; a format read takes
// the proof as the Basic password or the Bearer value, and a write does not.
func TestExchangeSession(t *testing.T) {
	t.Parallel()
	s := StartServer(t, ServerOpts{})
	repo, files := RandName("app"), RandName("files")
	s.CreateRepo(t, repo, "maven", false, nil)
	s.CreateRepo(t, files, "raw", false, nil)
	dev := RandName("dev")
	s.CreateUser(t, dev, "dev-pass-1234")
	for _, r := range []string{repo, files} {
		s.SetPerms(t, r, dev, "repo.download", "repo.publish")
	}
	d := s.EnrollDevice(t, dev)

	signTime := func(key ed25519.PrivateKey, ts int64) string {
		return signText(key, fmt.Sprintf("telesphorepo challenge v1\n%d\n%d", d.ID, ts))
	}
	now := time.Now().Unix()
	yearAgo := time.Now().AddDate(-1, 0, 0).Unix()
	_, otherKey, _ := ed25519.GenerateKey(rand.Reader)
	for _, c := range []struct {
		what string
		body map[string]any
	}{
		{"no signature", map[string]any{"device_id": d.ID, "exchange": true}},
		{"the signature of another key", map[string]any{"device_id": d.ID, "exchange": true, "ts": now, "signature": signTime(otherKey, now)}},
		{"a time a year ago", map[string]any{"device_id": d.ID, "exchange": true, "ts": yearAgo, "signature": signTime(d.Key, yearAgo)}},
	} {
		st, b := s.API(t, http.MethodPost, "/api/publish/challenge", c.body, nil)
		wantAPIStatus(t, "a token exchange nonce with "+c.what, st, b, http.StatusUnauthorized)
	}
	var signed, kept struct {
		Nonce string `json:"nonce"`
	}
	s.MustAPI(t, http.MethodPost, "/api/publish/challenge", map[string]any{"device_id": d.ID, "exchange": true, "ts": now, "signature": signTime(d.Key, now)}, nil, &signed)
	s.MustAPI(t, http.MethodPost, "/api/publish/challenge", map[string]any{"device_id": d.ID, "exchange": true, "nonce": signed.Nonce}, nil, &kept)
	if signed.Nonce == "" || kept.Nonce != signed.Nonce {
		t.Fatalf("token exchange nonce %q, kept %q: want a nonce and the same nonce again", signed.Nonce, kept.Nonce)
	}
	proof := fmt.Sprintf("trx.%d.%s.%s", d.ID, signed.Nonce, d.Sign(signed.Nonce, "*"))

	exchange := func(proof, repo string) (int, []byte) {
		t.Helper()
		return s.API(t, http.MethodPost, "/api/publish/sessions/exchange", map[string]string{"proof": proof, "repo": repo}, nil)
	}
	var shape struct {
		Format string `json:"format"`
		Kind   string `json:"kind"`
	}
	st, b := exchange(proof, repo)
	a := mustSession(t, "exchange the proof")(st, b)
	if json.Unmarshal(b, &shape) != nil || shape.Format != "maven" || shape.Kind != "hosted" {
		t.Fatalf("exchange the proof: %s, want format maven and kind hosted", b)
	}
	if again := mustSession(t, "exchange the proof again")(exchange(proof, repo)); again.ID != a.ID || again.Token != a.Token {
		t.Fatalf("exchange the proof again: session %s, want the open session %s with its token", again.ID, a.ID)
	}
	if f := mustSession(t, "exchange the proof for another repo")(exchange(proof, files)); f.ID == a.ID || f.Repo != files {
		t.Fatalf("exchange the proof for %s: session %s of %s, want a new session of %s", files, f.ID, f.Repo, files)
	}
	resp, body := pubPut(t, s, repo, a.Auth())
	wantStatus(t, resp, body, http.StatusCreated)

	forged := fmt.Sprintf("trx.%d.%s.%s", d.ID, signed.Nonce, (&Device{ID: d.ID, Key: otherKey}).Sign(signed.Nonce, "*"))
	st, b = exchange(forged, repo)
	wantAPIStatus(t, "exchange a proof of another key", st, b, http.StatusUnauthorized)
	st, b = exchange("trx.1", repo)
	wantAPIStatus(t, "exchange a proof that is not trx.<device id>.<nonce>.<signature>", st, b, http.StatusUnauthorized)

	// The proof reads with the grants of the device's user, and never writes.
	p := "/maven/" + repo + "/com/example/read/1.0/read.txt"
	resp, body = authPut(t, s, p, "read me", a.Auth())
	wantStatus(t, resp, body, http.StatusCreated)
	for _, auth := range []Auth{Basic{"telesphorepo-device", proof}, bearer(proof)} {
		resp, body = authGet(t, s, p, auth)
		wantStatus(t, resp, body, http.StatusOK)
		if string(body) != "read me" {
			t.Fatalf("read with the device proof: %q, want the file", body)
		}
	}
	resp, body = pubPut(t, s, repo, Basic{"telesphorepo-device", proof})
	wantPublishRequired(t, resp, body)
}

// TestCLIDownloads: the server serves the CLI archives of its build and their SHA256SUMS at
// /cli/, and writes its URL and the sums into the install scripts.
func TestCLIDownloads(t *testing.T) {
	t.Parallel()
	s := StartServer(t, ServerOpts{})
	get := func(path string) (*http.Response, []byte) {
		t.Helper()
		return s.Do(t, s.NewRequest(t, http.MethodGet, path, nil), nil)
	}

	resp, b := get("/cli/" + clidist.SumsFile)
	wantStatus(t, resp, b, http.StatusOK)
	sums := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		sum, name, ok := strings.Cut(line, "  ")
		if !ok || len(sum) != 64 {
			t.Fatalf("SHA256SUMS line %q, want <sha256>  <name>", line)
		}
		sums[name] = sum
	}
	var names []string
	for n := range sums {
		names = append(names, n)
	}
	slices.Sort(names)
	var want []string
	for _, tg := range clidist.Targets {
		want = append(want, clidist.Archive(tg.OS, tg.Arch))
	}
	slices.Sort(want)
	if !slices.Equal(names, want) {
		t.Fatalf("SHA256SUMS names %v, want %v", names, want)
	}

	resp, b = get("/cli/" + cliArchiveName)
	wantStatus(t, resp, b, http.StatusOK)
	if got := sha256.Sum256(b); hex.EncodeToString(got[:]) != sums[cliArchiveName] {
		t.Fatalf("the SHA-256 of %s is not the one of SHA256SUMS", cliArchiveName)
	}
	gz, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("%s: %v", cliArchiveName, err)
	}
	found := false
	for tr := tar.NewReader(gz); ; {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("%s: %v", cliArchiveName, err)
		}
		found = found || (h.Name == "telesphorepo" && h.Typeflag == tar.TypeReg && h.Mode&0o111 != 0)
	}
	if !found {
		t.Fatalf("%s holds no executable telesphorepo", cliArchiveName)
	}

	for _, script := range []string{"install.sh", "install.ps1"} {
		resp, b = get("/" + script)
		wantStatus(t, resp, b, http.StatusOK)
		if !strings.Contains(string(b), s.InternalURL) {
			t.Fatalf("%s does not name the URL %s:\n%s", script, s.InternalURL, b)
		}
		for name, sum := range sums {
			if !strings.Contains(string(b), sum) {
				t.Fatalf("%s does not hold the sum of %s:\n%s", script, name, b)
			}
		}
		if script == "install.ps1" && strings.Count(string(b), "\n") != strings.Count(string(b), "\r\n") {
			t.Fatalf("install.ps1 has a line end that is not the Windows one:\n%s", b)
		}
	}
}
