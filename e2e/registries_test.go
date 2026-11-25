//go:build e2e

package e2e

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestAuditActions: GET /api/audit/actions lists the actions that the modules register, each
// with a label, a subsystem, a sentence and a field for every detail key it records, so no fact
// of Activity shows raw; and the log records the actions of the services.
func TestAuditActions(t *testing.T) {
	t.Parallel()
	s := StartServer(t, ServerOpts{})
	var actions []struct {
		ID        string `json:"id"`
		Label     string `json:"label"`
		Subsystem string `json:"subsystem"`
		Sentence  string `json:"sentence"`
		Fields    []struct {
			Key   string `json:"key"`
			Label string `json:"label"`
			Kind  string `json:"kind"`
		} `json:"fields"`
	}
	s.MustAPI(t, http.MethodGet, "/api/audit/actions", nil, s.Admin(), &actions)
	var reg struct {
		Subsystems []struct {
			ID string `json:"id"`
		} `json:"subsystems"`
	}
	s.MustAPI(t, http.MethodGet, "/api/permissions", nil, nil, &reg)
	subsystems := map[string]bool{}
	for _, sub := range reg.Subsystems {
		subsystems[sub.ID] = true
	}
	// The kinds of a field that the web UI words (AuditField in web/src/lib/api/types.ts).
	kinds := map[string]bool{"text": true, "count": true, "size": true, "bool": true, "list": true, "enum": true,
		"perms": true, "time": true, "duration": true, "hidden": true}
	listed := map[string]bool{}
	fields := map[string]map[string]bool{} // the field keys of each action
	for _, a := range actions {
		if a.Label == "" || !subsystems[a.Subsystem] || a.Sentence == "" {
			t.Fatalf("action %+v: a label, a subsystem of GET /api/permissions and a sentence are required", a)
		}
		listed[a.ID] = true
		fields[a.ID] = map[string]bool{}
		for _, f := range a.Fields {
			if f.Key == "" || f.Label == "" || !kinds[f.Kind] {
				t.Errorf("action %s: field %+v needs a key, a label and a known kind", a.ID, f)
			}
			fields[a.ID][f.Key] = true
		}
	}
	if st, b := s.API(t, http.MethodGet, "/api/audit/actions", nil, nil); st != http.StatusUnauthorized {
		t.Fatalf("anonymous GET /api/audit/actions: status %d, want 401: %s", st, b)
	}

	// The services record their own events: read tokens and CI pipelines.
	u, pw := RandName("tok"), "tok-password-1"
	s.CreateUser(t, u, pw)
	sess := s.SignIn(t, u, pw)
	var tok struct {
		ID int64 `json:"id"`
	}
	s.MustAPI(t, http.MethodPost, "/api/tokens", map[string]any{"name": "laptop"}, sess, &tok)
	s.MustAPI(t, http.MethodDelete, "/api/tokens/"+strconv.FormatInt(tok.ID, 10), nil, sess, nil)
	repo := RandName("ci")
	s.CreateRepo(t, repo, "raw", false, nil)
	var trust struct {
		ID int64 `json:"id"`
	}
	s.MustAPI(t, http.MethodPost, "/api/publish/trusts", map[string]any{
		"name": RandName("trust"), "issuer": "https://issuer.example", "audience": "telesphorepo",
		"claims": map[string][]string{"repository": {"acme/app"}}, "repos": []string{repo},
	}, s.Admin(), &trust)
	s.MustAPI(t, http.MethodDelete, "/api/publish/trusts/"+strconv.FormatInt(trust.ID, 10), nil, s.Admin(), nil)
	// A new Debian repo binds the default signing key.
	s.CreateRepo(t, RandName("deb"), "debian", false, nil)
	all := auditAll(t, s, "")
	for _, c := range []struct{ action, actor string }{
		{"token.create", u}, {"token.delete", u}, {"ci.pipeline.create", s.AdminUser}, {"ci.pipeline.delete", s.AdminUser},
		{"signing.key.bind", s.AdminUser},
		{"signing.key.create", "signing"}, // the key "default" of the first start, by the signing module
	} {
		if !listed[c.action] {
			t.Errorf("GET /api/audit/actions does not list %s, which the log records", c.action)
		}
		if findAudit(all, c.action, c.actor) == nil {
			t.Errorf("audit log has no %s event by %s", c.action, c.actor)
		}
	}
	// Every detail key of every event has the words of a field of its action.
	for _, e := range all {
		for k := range e.Detail {
			if !fields[e.Action][k] {
				t.Errorf("%s event %d records detail %q, which its action registers no field for", e.Action, e.ID, k)
			}
		}
	}
}

// TestSSOStartupGuard: with signin.password_login=publishers an account that needs a second
// factor cannot sign in with a password, and a publisher can. SSO settings whose client secret no
// longer decrypts (another master key) start the server without SSO and with password sign-in for
// everyone, so no admin is locked out. Sign-in cookies are Secure unless cookie.secure says no.
func TestSSOStartupGuard(t *testing.T) {
	t.Parallel()
	vol := NewDataVolume(t)
	key := func() string {
		b := make([]byte, 32)
		rand.Read(b)
		return base64.StdEncoding.EncodeToString(b)
	}
	s := StartServer(t, ServerOpts{DataVolume: vol, Env: map[string]string{"TR_MASTER_KEY": key()}})

	// The server serves the discovery document of its own issuer from a public raw repo.
	disc := RandName("disc")
	s.CreateRepo(t, disc, "raw", true, nil)
	issuer := s.InternalURL + "/raw/" + disc
	doc, _ := json.Marshal(map[string]any{
		"issuer": issuer, "authorization_endpoint": issuer + "/auth", "token_endpoint": issuer + "/token",
		"jwks_uri": issuer + "/jwks", "response_types_supported": []string{"code"},
		"subject_types_supported": []string{"public"}, "id_token_signing_alg_values_supported": []string{"RS256"},
	})
	resp, body := rawPut(t, s, "/raw/"+disc+"/.well-known/openid-configuration", doc, s.PublishAuth(t, s.AdminUser, disc))
	wantStatus(t, resp, body, http.StatusCreated)
	// A publisher: a local account with repository permissions and no server permission, so no
	// second factor.
	publisher, publisherPass := RandName("pub"), "publisher-pass-1"
	s.CreateUser(t, publisher, publisherPass)
	s.SetPerms(t, disc, publisher, "repo.download", "repo.publish")
	s.MustAPI(t, http.MethodPatch, "/api/settings", map[string]any{
		"oidc.issuer": issuer, "oidc.client_id": "telesphorepo", "oidc.client_secret": "s3cret",
		"signin.password_login": "publishers",
	}, s.Admin(), nil)
	var cfg struct {
		OIDC          bool   `json:"oidc"`
		PasswordLogin string `json:"password_login"`
	}
	s.MustAPI(t, http.MethodGet, "/api/auth/config", nil, nil, &cfg)
	if !cfg.OIDC || cfg.PasswordLogin != "publishers" {
		t.Fatalf("auth config with SSO: %+v, want oidc on and password sign-in for publishers", cfg)
	}
	if st, b := s.API(t, http.MethodPost, "/api/auth/login", map[string]string{"username": s.AdminUser, "password": s.AdminPass}, nil); st != http.StatusForbidden {
		t.Fatalf("admin password sign-in with SSO: status %d, want 403: %s", st, b)
	}
	if st, b := s.API(t, http.MethodPost, "/api/auth/login", map[string]string{"username": publisher, "password": publisherPass}, nil); st != http.StatusOK {
		t.Fatalf("publisher password sign-in with SSO: status %d, want 200: %s", st, b)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := s.Stop(ctx, nil); err != nil {
		t.Fatalf("stop the first server: %v", err)
	}
	// The second server runs with cookie.secure at its default: its sign-in cookies are Secure.
	s2 := StartServer(t, ServerOpts{DataVolume: vol, Env: map[string]string{"TR_MASTER_KEY": key(), "TR_COOKIE_SECURE": ""}})
	s2.MustAPI(t, http.MethodGet, "/api/auth/config", nil, nil, &cfg)
	if cfg.OIDC || cfg.PasswordLogin != "all" {
		t.Fatalf("auth config after a change of the master key: %+v, want oidc off and password sign-in for all", cfg)
	}
	login, _ := json.Marshal(map[string]string{"username": s.AdminUser, "password": s.AdminPass})
	req := s2.NewRequest(t, http.MethodPost, "/api/auth/login", strings.NewReader(string(login)))
	req.Header.Set("Content-Type", "application/json")
	resp, body = s2.Do(t, req, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin password sign-in after a change of the master key: status %d, want 200: %s", resp.StatusCode, body)
	}
	secure := len(resp.Cookies()) > 0
	for _, c := range resp.Cookies() {
		secure = secure && c.Secure
	}
	if !secure {
		t.Fatalf("sign-in with cookie.secure at its default: Set-Cookie %q, want Secure cookies", resp.Header.Values("Set-Cookie"))
	}
}
