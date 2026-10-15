//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

// signingKey is a key as GET /api/signing-keys/{name} returns it.
type signingKey struct {
	Name        string   `json:"name"`
	Fingerprint string   `json:"fingerprint"`
	PublicKey   string   `json:"public_key"`
	Repos       []string `json:"repos"`
	Error       string   `json:"error"`
}

func getSigningKey(t *testing.T, s *Server, name string) signingKey {
	t.Helper()
	var k signingKey
	s.MustAPI(t, http.MethodGet, "/api/signing-keys/"+name, nil, s.Admin(), &k)
	return k
}

// fingerprintOf returns the primary key fingerprint (upper-case hex) of an armored key.
func fingerprintOf(t *testing.T, armored string) string {
	t.Helper()
	ring, err := openpgp.ReadArmoredKeyRing(strings.NewReader(armored))
	if err != nil || len(ring) == 0 {
		t.Fatalf("parse armored key: %v (entities: %d)", err, len(ring))
	}
	return fmt.Sprintf("%X", ring[0].PrimaryKey.Fingerprint)
}

// encryptedSigningKey returns an armored Ed25519 private key encrypted with a passphrase.
func encryptedSigningKey(t *testing.T) (armored, passphrase string) {
	t.Helper()
	cfg := &packet.Config{Algorithm: packet.PubKeyAlgoEdDSA, Curve: packet.Curve25519}
	ent, err := openpgp.NewEntity("Telesphorepo E2E encrypted", "", "enc@telesphorepo.test", cfg)
	if err != nil {
		t.Fatal(err)
	}
	passphrase = randHex(12)
	if err := ent.EncryptPrivateKeys([]byte(passphrase), cfg); err != nil {
		t.Fatalf("encrypt key: %v", err)
	}
	var buf bytes.Buffer
	w, err := armor.Encode(&buf, openpgp.PrivateKeyType, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ent.SerializePrivateWithoutSigning(w, cfg); err != nil {
		t.Fatalf("serialize key: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.String(), passphrase
}

// TestSigningKeys covers the signing keys as a resource (spec §13.3): create, import, the
// usage list, delete refused while a repo uses a key, and a Debian repo signed with its own key
// that apt installs from, before and after the repo changes its key.
func TestSigningKeys(t *testing.T) {
	s := StartServer(t, ServerOpts{})

	// The first start created "default".
	var all []signingKey
	st, raw := s.API(t, http.MethodGet, "/api/signing-keys", nil, s.Admin())
	if st != http.StatusOK || json.Unmarshal(raw, &all) != nil {
		t.Fatalf("GET /api/signing-keys: status %d: %s", st, raw)
	}
	if !slices.ContainsFunc(all, func(k signingKey) bool { return k.Name == "default" && k.Fingerprint != "" }) {
		t.Fatalf("GET /api/signing-keys: %s, want the key \"default\"", raw)
	}
	def := getSigningKey(t, s, "default")

	// Create.
	var team signingKey
	st, raw = s.API(t, http.MethodPost, "/api/signing-keys", map[string]any{"name": "team"}, s.Admin())
	if st != http.StatusCreated || json.Unmarshal(raw, &team) != nil {
		t.Fatalf("create key team: status %d: %s", st, raw)
	}
	if team.Fingerprint == "" || team.Fingerprint == def.Fingerprint || fingerprintOf(t, team.PublicKey) != team.Fingerprint {
		t.Fatalf("created key team: %+v (default %s)", team, def.Fingerprint)
	}
	if st, b := s.API(t, http.MethodPost, "/api/signing-keys", map[string]any{"name": "team"}, s.Admin()); st != http.StatusConflict {
		t.Fatalf("create key team again: status %d, want 409: %s", st, b)
	}
	if st, b := s.API(t, http.MethodPost, "/api/signing-keys", map[string]any{"name": "Bad Name"}, s.Admin()); st != http.StatusBadRequest {
		t.Fatalf("create a key with a bad name: status %d, want 400: %s", st, b)
	}

	// Import: a key without a passphrase, and an encrypted one.
	var imported signingKey
	s.MustAPI(t, http.MethodPost, "/api/signing-keys", map[string]any{"name": "imported", "armored": SigningPrivateKey}, s.Admin(), &imported)
	if imported.Fingerprint != fingerprintOf(t, SigningPublicKey) {
		t.Fatalf("imported key fingerprint %s, want %s", imported.Fingerprint, fingerprintOf(t, SigningPublicKey))
	}
	enc, pass := encryptedSigningKey(t)
	if st, b := s.API(t, http.MethodPost, "/api/signing-keys", map[string]any{"name": "enc", "armored": enc}, s.Admin()); st != http.StatusBadRequest {
		t.Fatalf("import an encrypted key without its passphrase: status %d, want 400: %s", st, b)
	}
	if st, b := s.API(t, http.MethodPost, "/api/signing-keys", map[string]any{"name": "enc", "armored": enc, "passphrase": "wrong"}, s.Admin()); st != http.StatusBadRequest {
		t.Fatalf("import an encrypted key with a wrong passphrase: status %d, want 400: %s", st, b)
	}
	var encKey signingKey
	s.MustAPI(t, http.MethodPost, "/api/signing-keys", map[string]any{"name": "enc", "armored": enc, "passphrase": pass}, s.Admin(), &encKey)
	if encKey.Fingerprint != fingerprintOf(t, enc) {
		t.Fatalf("imported encrypted key fingerprint %s, want %s", encKey.Fingerprint, fingerprintOf(t, enc))
	}
	if st, b := s.API(t, http.MethodPost, "/api/signing-keys", map[string]any{"name": "junk", "armored": "not a key"}, s.Admin()); st != http.StatusBadRequest {
		t.Fatalf("import junk: status %d, want 400: %s", st, b)
	}

	// No private key material leaves the server; public.asc is the public key.
	_, raw = s.API(t, http.MethodGet, "/api/signing-keys", nil, s.Admin())
	if strings.Contains(string(raw), "PRIVATE KEY") {
		t.Fatal("GET /api/signing-keys leaks private key material")
	}
	resp, pub := s.Do(t, s.NewRequest(t, http.MethodGet, "/api/signing-keys/team/public.asc", nil), s.Admin())
	if resp.StatusCode != http.StatusOK || strings.TrimSpace(string(pub)) != strings.TrimSpace(team.PublicKey) {
		t.Fatalf("GET public.asc of team: status %d: %s", resp.StatusCode, pub)
	}
	if st, _ := s.API(t, http.MethodGet, "/api/signing-keys/nowhere", nil, s.Admin()); st != http.StatusNotFound {
		t.Fatalf("GET an unknown key: status %d, want 404", st)
	}

	// Admins only.
	s.CreateUser(t, "plain", "plainpass123")
	plain := s.SignIn(t, "plain", "plainpass123")
	if st, _ := s.API(t, http.MethodGet, "/api/signing-keys", nil, plain); st != http.StatusForbidden {
		t.Fatalf("non-admin GET /api/signing-keys: status %d, want 403", st)
	}
	if st, _ := s.API(t, http.MethodPost, "/api/signing-keys", map[string]any{"name": "x"}, plain); st != http.StatusForbidden {
		t.Fatalf("non-admin create: status %d, want 403", st)
	}

	// A Debian repo with its own key.
	pkgs := buildDebPackages(t)
	archs := []string{"amd64", "arm64"}
	if !slices.Contains(archs, pkgs.arch) {
		archs = append(archs, pkgs.arch)
	}
	repo := RandName("deb")
	if st, b := s.API(t, http.MethodPost, "/api/repos", map[string]any{"name": repo, "format": "debian",
		"config": map[string]any{"architectures": archs, "signing_key": "nowhere"}}, s.Admin()); st != http.StatusBadRequest {
		t.Fatalf("create a repo with an unknown signing key: status %d, want 400: %s", st, b)
	}
	s.CreateRepo(t, repo, "debian", false, map[string]any{"architectures": archs, "signing_key": "team"})
	reader, readerPass := RandName("reader"), randHex(12)
	s.CreateUser(t, reader, readerPass)
	s.SetPerms(t, repo, reader, "download")
	rauth := Basic{reader, readerPass}

	ps := s.MintSession(t, "admin", repo)
	debMustUpload(t, s, repo, pkgs.hello1, ps.Auth(), "e2e-hello", "1.0", "all")
	debMustUpload(t, s, repo, pkgs.native, ps.Auth(), "e2e-native", "1.0", pkgs.arch)
	s.WaitDebianIndexed(t, repo)

	if resp, _ := s.Do(t, s.NewRequest(t, http.MethodGet, "/debian/"+repo+"/key.asc", nil), nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous GET key.asc of a private repo: status %d, want 401", resp.StatusCode)
	}
	key := s.DebianKey(t, repo, rauth)
	if fingerprintOf(t, key) != team.Fingerprint {
		t.Fatalf("/debian/%s/key.asc is not the key team", repo)
	}
	out := debAptInstall(t, s, repo, reader, readerPass, key)
	if !strings.Contains(out, "RESULT: hello 1.0\n") || !strings.Contains(out, "NATIVE: native "+pkgs.arch) {
		t.Fatalf("apt install from a repo signed with team:\n%s", out)
	}
	debCheckIndexes(t, s, nil, repo, rauth, key)

	// The key lists the repo, and cannot be deleted while the repo uses it.
	if k := getSigningKey(t, s, "team"); !slices.Equal(k.Repos, []string{repo}) {
		t.Fatalf("key team repos: %v, want [%s]", k.Repos, repo)
	}
	st, raw = s.API(t, http.MethodDelete, "/api/signing-keys/team", nil, s.Admin())
	if st != http.StatusConflict || !strings.Contains(string(raw), repo) {
		t.Fatalf("delete a key in use: status %d, want 409 naming %s: %s", st, repo, raw)
	}

	// Changing the repo's key re-signs that repo.
	s.MustAPI(t, http.MethodPatch, "/api/repos/"+repo, map[string]any{"config": map[string]any{"architectures": archs, "signing_key": "imported"}}, s.Admin(), nil)
	s.WaitDebianIndexed(t, repo)
	key = s.DebianKey(t, repo, rauth)
	if fingerprintOf(t, key) != imported.Fingerprint {
		t.Fatalf("/debian/%s/key.asc after the key change is not the key imported", repo)
	}
	debCheckIndexes(t, s, nil, repo, rauth, key)
	out = debAptInstall(t, s, repo, reader, readerPass, key)
	if !strings.Contains(out, "RESULT: hello 1.0\n") {
		t.Fatalf("apt install after the key change:\n%s", out)
	}

	// No repo uses team now: it can be deleted.
	if k := getSigningKey(t, s, "team"); len(k.Repos) != 0 {
		t.Fatalf("key team repos after the change: %v, want none", k.Repos)
	}
	if st, b := s.API(t, http.MethodDelete, "/api/signing-keys/team", nil, s.Admin()); st != http.StatusNoContent {
		t.Fatalf("delete an unused key: status %d, want 204: %s", st, b)
	}
	if st, _ := s.API(t, http.MethodGet, "/api/signing-keys/team", nil, s.Admin()); st != http.StatusNotFound {
		t.Fatalf("GET a deleted key: status %d, want 404", st)
	}
	if st, b := s.API(t, http.MethodPatch, "/api/repos/"+repo, map[string]any{"config": map[string]any{"architectures": archs, "signing_key": "team"}}, s.Admin()); st != http.StatusBadRequest {
		t.Fatalf("PATCH a repo to a deleted key: status %d, want 400: %s", st, b)
	}

	// Keys survive a restart (sealed with the master key).
	s.Restart(t)
	if k := getSigningKey(t, s, "imported"); k.Fingerprint != imported.Fingerprint || k.Error != "" || !slices.Equal(k.Repos, []string{repo}) {
		t.Fatalf("key imported after a restart: %+v", k)
	}
}
