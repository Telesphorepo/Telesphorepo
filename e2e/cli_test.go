//go:build e2e

package e2e

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
)

// TestZeroConfigStartup: no TR_BOOTSTRAP_* and no TR_GPG_KEY_FILE. The server must print a
// one-time admin password banner and auto-generate its Debian signing key.
func TestZeroConfigStartup(t *testing.T) {
	s := StartServer(t, ServerOpts{NoBootstrapAdmin: true, NoSigningKey: true})

	logs := s.Logs(t)
	if !strings.Contains(logs, "password: "+s.AdminPass) {
		t.Fatalf("banner does not contain %q:\n%s", "password: "+s.AdminPass, logs)
	}

	sess := s.Login(t, "admin", s.AdminPass)
	var me struct {
		Username string `json:"username"`
		IsAdmin  bool   `json:"is_admin"`
	}
	s.MustAPI(t, http.MethodGet, "/api/me", nil, sess, &me)
	if me.Username != "admin" || !me.IsAdmin {
		t.Fatalf("/api/me = %+v, want admin with is_admin", me)
	}

	st, body := s.API(t, http.MethodGet, "/keys/debian.asc", nil, nil)
	if st != http.StatusOK {
		t.Fatalf("GET /keys/debian.asc: status %d: %s", st, body)
	}
	if !bytes.Contains(body, []byte("-----BEGIN PGP PUBLIC KEY BLOCK-----")) {
		t.Fatalf("GET /keys/debian.asc: not an armored public key:\n%s", body)
	}
	ents, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(body))
	if err != nil || len(ents) == 0 {
		t.Fatalf("parse auto-generated public key: %v (entities: %d)", err, len(ents))
	}
	if ents[0].PrivateKey != nil {
		t.Fatal("/keys/debian.asc exposes private key material")
	}
}

// TestCLIUser drives "telesphorepo user add/passwd" inside the running container with the
// password piped on stdin, exactly as an operator would: "<engine> exec -i <ctr> ...".
func TestCLIUser(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	const bin = "/telesphorepo"

	code, out := s.Exec(t, "s3cretpass1\n", bin, "user", "add", "bob")
	if code != 0 {
		t.Fatalf("user add bob: exit %d:\n%s", code, out)
	}
	var me struct {
		Username string `json:"username"`
		IsAdmin  bool   `json:"is_admin"`
	}
	s.MustAPI(t, http.MethodGet, "/api/me", nil, Basic{"bob", "s3cretpass1"}, &me)
	if me.Username != "bob" || me.IsAdmin {
		t.Fatalf("/api/me as bob = %+v, want non-admin bob", me)
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

	code, out = s.Exec(t, "adminpass123\n", bin, "user", "add", "carol", "--admin")
	if code != 0 {
		t.Fatalf("user add carol --admin: exit %d:\n%s", code, out)
	}
	s.MustAPI(t, http.MethodGet, "/api/me", nil, Basic{"carol", "adminpass123"}, &me)
	if !me.IsAdmin {
		t.Fatalf("/api/me as carol = %+v, want is_admin", me)
	}

	if code, out := s.Exec(t, "whatever123\n", bin, "user", "passwd", "nobody"); code == 0 {
		t.Fatalf("user passwd nobody: exit 0, want failure:\n%s", out)
	}
}

func TestCLIVersion(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	code, out := s.Exec(t, "", "/telesphorepo", "version")
	if code != 0 || strings.TrimSpace(out) == "" {
		t.Fatalf("version: exit %d, output %q", code, out)
	}
}
