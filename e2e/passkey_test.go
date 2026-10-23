//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"testing"

	"github.com/descope/virtualwebauthn"
)

// securityKey is a software security key: one credential, signed the way a browser hands a
// hardware key's answer to the server.
type securityKey struct {
	rp   virtualwebauthn.RelyingParty
	auth virtualwebauthn.Authenticator
	cred virtualwebauthn.Credential
}

func newSecurityKey(t *testing.T, s *Server) *securityKey {
	t.Helper()
	// The server's base_url, whose host is the passkeys' relying party.
	u, err := url.Parse(s.InternalURL)
	if err != nil {
		t.Fatal(err)
	}
	return &securityKey{
		rp:   virtualwebauthn.RelyingParty{ID: u.Hostname(), Name: "Telesphorepo", Origin: u.Scheme + "://" + u.Host},
		cred: virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2),
	}
}

// register adds the key as a passkey named name on sess.
func (k *securityKey) register(t *testing.T, s *Server, sess *Session, name string) {
	t.Helper()
	st, b := s.API(t, http.MethodPost, "/api/security/passkeys/begin", nil, sess)
	if st != http.StatusOK {
		t.Fatalf("POST /api/security/passkeys/begin: status %d: %s", st, b)
	}
	opts, err := virtualwebauthn.ParseAttestationOptions(string(b))
	if err != nil {
		t.Fatalf("parse creation options %s: %v", b, err)
	}
	k.auth = virtualwebauthn.NewAuthenticatorWithOptions(virtualwebauthn.AuthenticatorOptions{UserHandle: []byte(opts.UserID)})
	resp := virtualwebauthn.CreateAttestationResponse(k.rp, k.auth, k.cred, *opts)
	s.MustAPI(t, http.MethodPost, "/api/security/passkeys/finish", map[string]any{"name": name, "credential": json.RawMessage(resp)}, sess, nil)
	k.auth.AddCredential(k.cred)
}

// signIn signs user in with the password and then the key, in a new cookie jar (another browser
// or a phone), and returns the status of the second step.
func (k *securityKey) signIn(t *testing.T, s *Server, user string) (*Session, int, []byte) {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	sess := &Session{Jar: jar}
	var step struct {
		SecondFactor bool `json:"second_factor"`
	}
	s.MustAPI(t, http.MethodPost, "/api/auth/login", map[string]string{"username": user, "password": s.Password(t, user)}, sess, &step)
	if !step.SecondFactor {
		t.Fatalf("POST /api/auth/login for %s: no second step asked", user)
	}
	st, b := s.API(t, http.MethodPost, "/api/auth/login/passkey", nil, sess)
	if st != http.StatusOK {
		t.Fatalf("POST /api/auth/login/passkey: status %d: %s", st, b)
	}
	opts, err := virtualwebauthn.ParseAssertionOptions(string(b))
	if err != nil {
		t.Fatalf("parse request options %s: %v", b, err)
	}
	resp := virtualwebauthn.CreateAssertionResponse(k.rp, k.auth, k.cred, *opts)
	st, b = s.API(t, http.MethodPost, "/api/auth/login/second", map[string]any{"passkey": json.RawMessage(resp)}, sess)
	return sess, st, b
}

// signInOK signs in with the key and checks that the session is a full one; the key's counter
// goes up first, as a hardware key's does.
func (k *securityKey) signInOK(t *testing.T, s *Server, user string) *Session {
	t.Helper()
	k.cred.Counter++
	sess, st, b := k.signIn(t, s, user)
	if st != http.StatusOK {
		t.Fatalf("sign-in with a passkey: status %d: %s", st, b)
	}
	apiWant(t, s, http.MethodGet, "/api/users", nil, sess, http.StatusOK)
	return sess
}

// A passkey signs in after a sign-out, also with a counter that did not go up: a security key
// keeps one counter per protocol, and a phone may use the other one.
func TestPasskeySignIn(t *testing.T) {
	t.Parallel()
	// A passkey's relying party is a domain name, so the server's name has a dot.
	s := StartServer(t, ServerOpts{Alias: RandName("passkeys") + ".test"})
	user := RandName("pat")
	s.CreateUser(t, user, "pat-password-1", s.ServerPerms(t)...)

	key := newSecurityKey(t, s)
	key.register(t, s, s.Login(t, user, s.Password(t, user)), "Key")
	key.signInOK(t, s, user)
	key.signInOK(t, s, user)

	key.cred.Counter = 1
	if _, st, b := key.signIn(t, s, user); st != http.StatusOK {
		t.Fatalf("sign-in with a counter that did not go up: status %d: %s", st, b)
	}
}
