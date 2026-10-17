//go:build e2e

package e2e

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

type permSubsystem struct {
	ID          string `json:"id"`
	Scope       string `json:"scope"`
	Label       string `json:"label"`
	Help        string `json:"help"`
	Module      string `json:"module"`
	Permissions []struct {
		ID           string   `json:"id"`
		Label        string   `json:"label"`
		Help         string   `json:"help"`
		Needs        []string `json:"needs"`
		SecondFactor bool     `json:"second_factor"`
		AllRepos     bool     `json:"all_repos"`
	} `json:"permissions"`
}

// TestPermissionRegistry: GET /api/permissions lists every subsystem with its permissions, and a
// write of an unknown ID or an ID of the other scope is refused.
func TestPermissionRegistry(t *testing.T) {
	t.Parallel()
	s := StartServer(t, ServerOpts{})
	var reg struct {
		Subsystems []permSubsystem `json:"subsystems"`
	}
	s.MustAPI(t, http.MethodGet, "/api/permissions", nil, nil, &reg)
	by := map[string]permSubsystem{}
	var server []string
	for _, sub := range reg.Subsystems {
		if sub.Label == "" || sub.Help == "" || sub.Module == "" {
			t.Fatalf("subsystem %+v: label, help and module are required", sub)
		}
		by[sub.ID] = sub
		for _, p := range sub.Permissions {
			if !strings.HasPrefix(p.ID, sub.ID+".") || p.Label == "" || p.Help == "" || p.Needs == nil {
				t.Fatalf("permission %+v of %s: the ID starts with the subsystem, label, help and needs are set", p, sub.ID)
			}
			if sub.Scope == "server" {
				server = append(server, p.ID)
			}
		}
	}
	if !slices.Equal(server, AllServerPerms) {
		t.Fatalf("server permissions %v, want %v (module start order)", server, AllServerPerms)
	}
	repo := by["repo"]
	if repo.Scope != "repo" || repo.Module != "core" || len(repo.Permissions) != 5 {
		t.Fatalf("subsystem repo: %+v", repo)
	}
	pub := repo.Permissions[1]
	if pub.ID != "repo.publish" || !slices.Equal(pub.Needs, []string{"repo.download"}) || pub.SecondFactor {
		t.Fatalf("repo.publish: %+v", pub)
	}
	if ci := by["ci"]; ci.Module != "publish" || ci.Scope != "repo" || len(ci.Permissions) != 1 ||
		ci.Permissions[0].ID != "ci.pipelines" || !ci.Permissions[0].SecondFactor {
		t.Fatalf("subsystem ci: %+v", ci)
	}
	if all := by["repositories"]; len(all.Permissions) != 1 || !all.Permissions[0].AllRepos {
		t.Fatalf("subsystem repositories: %+v", all)
	}

	r := RandName("reg")
	s.CreateRepo(t, r, "raw", false, nil)
	u := RandName("u")
	s.CreateUser(t, u, "u-password-1")
	for _, bad := range [][]string{{"repo.nope"}, {"people.manage"}} {
		st, b := s.API(t, http.MethodPut, "/api/grants", map[string]any{"users": []string{u}, "repos": []string{r}, "perms": bad}, s.Admin())
		wantAPIStatus(t, "PUT /api/grants "+strings.Join(bad, ","), st, b, http.StatusBadRequest)
	}
	for _, bad := range [][]string{{"people.nope"}, {"repo.download"}} {
		st, b := s.API(t, http.MethodPut, "/api/users/"+u+"/permissions", map[string]any{"permissions": bad}, s.Admin())
		wantAPIStatus(t, "PUT server permissions "+strings.Join(bad, ","), st, b, http.StatusBadRequest)
	}
	// A grant reads back with IDs.
	s.SetPerms(t, r, u, "repo.download", "repo.publish")
	var grants []struct {
		Username string   `json:"username"`
		Perms    []string `json:"perms"`
	}
	s.MustAPI(t, http.MethodGet, "/api/repos/"+r+"/grants", nil, s.Admin(), &grants)
	if len(grants) != 1 || grants[0].Username != u || strings.Join(grants[0].Perms, ",") != "repo.download,repo.publish" {
		t.Fatalf("grants of %s: %+v", r, grants)
	}
}

// TestAuditActions: GET /api/audit/actions lists the actions that the modules register, each
// with a label and a subsystem, and the log records the actions of the services.
func TestAuditActions(t *testing.T) {
	t.Parallel()
	s := StartServer(t, ServerOpts{})
	var actions []struct {
		ID        string `json:"id"`
		Label     string `json:"label"`
		Subsystem string `json:"subsystem"`
	}
	s.MustAPI(t, http.MethodGet, "/api/audit/actions", nil, s.Admin(), &actions)
	subs := map[string]string{}
	for _, a := range actions {
		if a.Label == "" || a.Subsystem == "" {
			t.Fatalf("action %+v: label and subsystem are required", a)
		}
		subs[a.ID] = a.Subsystem
	}
	for id, sub := range map[string]string{
		"file.upload": "repo", "grant.set": "repo", "token.create": "people", "storage.move.start": "storage",
		"network.denied": "network", "signing.key.create": "signing", "ci.pipeline.create": "ci",
		"device.approve": "people", "sso.signin": "signin", "nexus.cutover": "nexus",
	} {
		if subs[id] != sub {
			t.Errorf("action %s: subsystem %q, want %q", id, subs[id], sub)
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
		if findAudit(all, c.action, c.actor) == nil {
			t.Errorf("audit log has no %s event by %s", c.action, c.actor)
		}
	}
}

// TestSettingsS3Endpoint: an empty S3 endpoint means AWS, so the region is required; r2 is not a
// storage backend.
func TestSettingsS3Endpoint(t *testing.T) {
	t.Parallel()
	s := StartServer(t, ServerOpts{})
	aws := map[string]any{
		"storage": "s3", "s3.endpoint": "", "s3.region": "", "s3.bucket": "b",
		"s3.access_key_id": "a", "s3.secret_access_key": "s",
	}
	st, b := s.API(t, http.MethodPost, "/api/settings/storage/test", aws, s.Admin())
	var res struct {
		OK     bool              `json:"ok"`
		Fields map[string]string `json:"fields"`
	}
	_ = json.Unmarshal(b, &res)
	if st != http.StatusBadRequest || res.OK || res.Fields["s3.region"] == "" {
		t.Fatalf("S3 test without endpoint and region: status %d, want 400 with a field error of s3.region: %s", st, b)
	}
	if st, b := s.API(t, http.MethodPatch, "/api/settings", map[string]any{"storage": "r2"}, s.Admin()); st != http.StatusBadRequest {
		t.Fatalf("storage r2: status %d, want 400: %s", st, b)
	}
	if st, b := s.API(t, http.MethodPost, "/api/settings/nope/test", map[string]any{}, s.Admin()); st != http.StatusNotFound {
		t.Fatalf("test of an unknown group: status %d, want 404: %s", st, b)
	}
}

// TestSSOStartupGuard: with signin.password_login=publishers an account that needs a second
// factor cannot sign in with a password, and a publisher can. SSO settings whose client secret no
// longer decrypts (another master key) start the server without SSO and with password sign-in for everyone, so no admin is locked out.
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
	s2 := StartServer(t, ServerOpts{DataVolume: vol, Env: map[string]string{"TR_MASTER_KEY": key()}})
	s2.MustAPI(t, http.MethodGet, "/api/auth/config", nil, nil, &cfg)
	if cfg.OIDC || cfg.PasswordLogin != "all" {
		t.Fatalf("auth config after a change of the master key: %+v, want oidc off and password sign-in for all", cfg)
	}
	if st, b := s2.API(t, http.MethodPost, "/api/auth/login", map[string]string{"username": s.AdminUser, "password": s.AdminPass}, nil); st != http.StatusOK {
		t.Fatalf("admin password sign-in after a change of the master key: status %d, want 200: %s", st, b)
	}
	if logs := s2.Logs(t); !strings.Contains(logs, "SSO is off") {
		t.Fatalf("the log does not say that SSO is off:\n%s", logs)
	}
}
