//go:build e2e

package e2e

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
)

// TestZeroConfigStartup: no TR_BOOTSTRAP_*. The server must print a one-time admin password
// banner and generate the signing key "default", which a new Debian repo uses.
func TestZeroConfigStartup(t *testing.T) {
	t.Parallel()
	s := StartServer(t, ServerOpts{NoBootstrapAdmin: true})

	logs := s.Logs(t)
	if !strings.Contains(logs, "password: "+s.AdminPass) {
		t.Fatalf("banner does not contain %q:\n%s", "password: "+s.AdminPass, logs)
	}

	sess := s.Login(t, "admin", s.AdminPass)
	var me struct {
		Username             string   `json:"username"`
		Permissions          []string `json:"permissions"`
		SecondFactorRequired bool     `json:"second_factor_required"`
	}
	s.MustAPI(t, http.MethodGet, "/api/me", nil, sess, &me)
	if me.Username != "admin" || strings.Join(me.Permissions, ",") != strings.Join(s.ServerPerms(t), ",") || !me.SecondFactorRequired {
		t.Fatalf("/api/me = %+v, want admin with every server permission and second_factor_required", me)
	}

	var keys []struct {
		Name        string `json:"name"`
		Fingerprint string `json:"fingerprint"`
		PublicKey   string `json:"public_key"`
	}
	s.MustAPI(t, http.MethodGet, "/api/signing-keys", nil, s.Admin(), &keys)
	if len(keys) != 1 || keys[0].Name != "default" || keys[0].Fingerprint == "" {
		t.Fatalf("GET /api/signing-keys on the first start: %+v, want the key \"default\"", keys)
	}
	repo := RandName("deb")
	s.CreateRepo(t, repo, "debian", true, nil)
	body := []byte(s.DebianKey(t, repo, nil))
	ents, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(body))
	if err != nil || len(ents) == 0 {
		t.Fatalf("parse generated public key: %v (entities: %d)", err, len(ents))
	}
	if ents[0].PrivateKey != nil {
		t.Fatalf("/debian/%s/key.asc exposes private key material", repo)
	}
	if strings.TrimSpace(string(body)) != strings.TrimSpace(keys[0].PublicKey) {
		t.Fatalf("/debian/%s/key.asc is not the public key of \"default\"", repo)
	}
}

// TestCLIUser drives "telesphorepo-server user add/passwd" inside the running container with the
// password piped on stdin, exactly as an operator would: "<engine> exec -i <ctr> ...".
func TestCLIUser(t *testing.T) {
	t.Parallel()
	s := StartServer(t, ServerOpts{})
	const bin = "/telesphorepo-server"

	code, out := s.Exec(t, "s3cretpass1\n", bin, "user", "add", "bob")
	if code != 0 {
		t.Fatalf("user add bob: exit %d:\n%s", code, out)
	}
	var me struct {
		Username    string   `json:"username"`
		Permissions []string `json:"permissions"`
	}
	s.MustAPI(t, http.MethodGet, "/api/me", nil, Basic{"bob", "s3cretpass1"}, &me)
	if me.Username != "bob" || len(me.Permissions) != 0 {
		t.Fatalf("/api/me as bob = %+v, want bob without server permissions", me)
	}

	// Adding an existing user must fail rather than silently reset the password.
	if code, out := s.Exec(t, "otherpass99\n", bin, "user", "add", "bob"); code == 0 {
		t.Fatalf("user add bob (duplicate): exit 0, want failure:\n%s", out)
	}

	code, out = s.Exec(t, "n3wpassword2\n", bin, "user", "passwd", "bob")
	if code != 0 {
		t.Fatalf("user passwd bob: exit %d:\n%s", code, out)
	}
	if st, b := s.API(t, http.MethodGet, "/api/me", nil, Basic{"bob", "n3wpassword2"}); st != http.StatusOK {
		t.Fatalf("/api/me with new password: status %d: %s", st, b)
	}
	if st, _ := s.API(t, http.MethodGet, "/api/me", nil, Basic{"bob", "s3cretpass1"}); st != http.StatusUnauthorized {
		t.Fatalf("/api/me with old password: status %d, want 401", st)
	}

	if code, out := s.Exec(t, "whatever123\n", bin, "user", "passwd", "nobody"); code == 0 {
		t.Fatalf("user passwd nobody: exit 0, want failure:\n%s", out)
	}
}

func TestCLIVersion(t *testing.T) {
	t.Parallel()
	s := StartServer(t, ServerOpts{})
	code, out := s.Exec(t, "", "/telesphorepo-server", "version")
	if code != 0 || strings.TrimSpace(out) == "" {
		t.Fatalf("version: exit %d, output %q", code, out)
	}
}
