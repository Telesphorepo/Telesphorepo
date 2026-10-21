package publish

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/access"
	"github.com/Telesphorepo/Telesphorepo/internal/activity"
	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/ci"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/jobs"
	"github.com/Telesphorepo/Telesphorepo/internal/module"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
)

// PermPipelines adds and edits the CI pipelines (trust policies) of a repository.
const PermPipelines perm.ID = "ci.pipelines"

// The audit actions of the publish module.
const (
	ActionSessionCreate   = "publish.session.create"
	ActionSessionClose    = "publish.session.close"
	ActionDeviceApprove   = "device.approve"
	ActionDeviceRevoke    = "device.revoke"
	ActionDeviceSetup     = "device.setup"
	ActionSetupCodeCreate = "setup_code.create"
	ActionPipelineCreate  = "ci.pipeline.create"
	ActionPipelineUpdate  = "ci.pipeline.update"
	ActionPipelineDelete  = "ci.pipeline.delete"
)

// The settings of the group "publishing".
var (
	SessionTTL = settings.Duration(&settings.Def{Key: "publish.session_ttl", Default: "15m", Label: "Publish session lifetime",
		Help: "A publish session, the credential that every upload needs, lasts at most this time, for example 15m. A client ends its session when the publish is done. The maximum is 1h. A publish must end within it.",
		Validate: func(v any) error {
			if v.(time.Duration) > time.Hour {
				return errors.New("must be at most 1h")
			}
			return nil
		}})
	DeviceTTL = settings.Duration(&settings.Def{Key: "publish.device_ttl", Default: "2160h", Label: "Device lifetime",
		Help: "An enrolled device key of the Telesphorepo CLI can start publish sessions for this time, for example 2160h (90 days). After this time, the developer must enroll the device again."})
)

// Module returns the publish module: devices, setup codes, publish sessions (the token kind
// TokenPrefix), the Minter, CI trust policies, the publish log, the cleanup job, and the install
// scripts and downloads of the Telesphorepo CLI (/install.sh, /install.ps1, /cli/). It provides
// *Service and Minter to the modules that require it.
func Module() module.Module {
	m := &mod{}
	return module.Module{
		ID: "publish", Title: "Publishing", Requires: []string{"audit"},
		Register: func(x *module.Extender) {
			x.Schema(component())
			x.Permissions(perm.Subsystem{ID: "ci", Scope: perm.ScopeRepo, Label: "CI pipelines",
				Help: "The CI pipelines that may publish to a repository with the identity token of their job."},
				perm.Def{ID: PermPipelines, Label: "Manage CI pipelines", Help: "Add, change and delete the CI pipelines that may publish to the repository.",
					Needs: []perm.ID{perm.RepoPublish}, SecondFactor: true})
			x.AuditActions(
				audit.ActionDef{ID: ActionSessionCreate, Label: "Publish session started", Subsystem: "repo"},
				audit.ActionDef{ID: ActionSessionClose, Label: "Publish session closed", Subsystem: "repo"},
				audit.ActionDef{ID: ActionDeviceApprove, Label: "Device approved", Subsystem: "people"},
				audit.ActionDef{ID: ActionDeviceRevoke, Label: "Device revoked", Subsystem: "people"},
				audit.ActionDef{ID: ActionDeviceSetup, Label: "Project set up", Subsystem: "repo"},
				audit.ActionDef{ID: ActionSetupCodeCreate, Label: "Setup line created", Subsystem: "repo"},
				audit.ActionDef{ID: ActionPipelineCreate, Label: "CI pipeline created", Subsystem: "ci"},
				audit.ActionDef{ID: ActionPipelineUpdate, Label: "CI pipeline changed", Subsystem: "ci"},
				audit.ActionDef{ID: ActionPipelineDelete, Label: "CI pipeline deleted", Subsystem: "ci"},
			)
			x.CIProvider(GitHubActions())
			x.CIProvider(Jenkins())
			x.Settings(&settings.Group{ID: "publishing", Label: "Publishing", Perm: perm.SettingsGeneral,
				Defs: []*settings.Def{SessionTTL.Def(), DeviceTTL.Def()}})
			x.Init(func(ctx context.Context, h *module.Host) error {
				m.h = h
				h.Repos.OnPurge(purge(h.DB.W)) // empties the module's rows of a repo in batches before the repo row goes
				m.svc = New(Deps{DB: h.DB, Auth: h.Auth, Access: h.Access, Repos: h.Repos, Settings: h.Settings,
					Audit: h.Audit, CI: h.CI, Builds: h.Builds, Log: h.Log})
				h.Auth.SetSessionLoader(m.svc.LoadSession)
				h.Auth.SetDeviceLoader(m.svc.DevicePrincipal)
				h.Auth.SetDeviceProofVerifier(m.svc.VerifyDeviceProof)
				h.Access.SetPipelines(m.svc.RepoPipelines)
				h.Provide(m.svc)
				module.ProvideAs[Minter](h, m.svc)
				return nil
			})
			x.TokenKind(TokenPrefix, func(ctx context.Context, publicID, secret string) (*auth.Principal, error) {
				return m.svc.Verify(ctx, publicID, secret)
			})
			x.Job(jobs.Job{Name: "publish.cleanup", Every: cleanupEvery, Tick: func(ctx context.Context) error {
				return m.svc.Cleanup(ctx)
			}})
			x.RootPrefix("/install.sh")
			x.RootPrefix("/install.ps1")
			x.RootPrefix("/cli/")
			x.Routes(m.routes)
			x.Routes(m.installRoutes)
		},
	}
}

type mod struct {
	h   *module.Host
	svc *Service
}

// routes mounts the publisher-credential API: device enrollment, project setup, publish
// sessions, CI trust policies (pipelines on their repos) and the publish log (activity).
func (m *mod) routes(r *route.Router) {
	fresh := route.Policy{Accept: auth.CredSession, FreshFactor: true}
	r.Handle("POST /api/devices/enroll", route.Public, m.enrollDevice)
	r.Handle("POST /api/devices/enroll/poll", route.Public, m.pollDevice)
	r.Handle("GET /api/devices/enroll", route.Policy{Accept: auth.CredPassword | auth.CredSession}, m.pendingEnrollment)
	r.Handle("POST /api/devices/approve", fresh, m.approveDevice)
	r.Handle("GET /api/devices", route.AnyUser, m.listDevices)
	r.Handle("DELETE /api/devices/{id}", route.UI, m.revokeDevice)
	r.Handle("DELETE /api/devices/self", route.Public, m.revokeSelf)
	r.Handle("POST /api/devices/setup", route.Public, m.setupDevice)
	r.Handle("POST /api/publish/setup-codes", route.Policy{Accept: auth.CredSession, FreshFactor: true,
		Repo: &route.RepoRule{Name: bodyRepo, Op: access.Mint, Perm: perm.RepoPublish}}, m.createSetupCode)

	r.Handle("POST /api/publish/challenge", route.Public, m.publishChallenge)
	r.Handle("POST /api/publish/sessions", route.Public, m.deviceSession)
	r.Handle("POST /api/publish/sessions/manual", fresh, m.manualSession)
	r.Handle("POST /api/publish/sessions/oidc", route.Public, m.oidcSession)
	r.Handle("POST /api/publish/sessions/exchange", route.Public, m.exchangeSession)
	r.Handle("GET /api/publish/sessions", route.AnyUser, m.listMySessions)
	r.Handle("DELETE /api/publish/sessions/{id}", route.Policy{Accept: auth.CredPublish}, m.closeSession)

	pipelines := route.Policy{Accept: auth.CredSession, SomeRepo: PermPipelines}
	r.Handle("GET /api/publish/trusts", pipelines, m.listTrusts)
	r.Handle("POST /api/publish/trusts", pipelines, m.createTrust)
	r.Handle("GET /api/publish/trusts/{id}", pipelines, m.getTrust)
	r.Handle("PATCH /api/publish/trusts/{id}", pipelines, m.updateTrust)
	r.Handle("DELETE /api/publish/trusts/{id}", pipelines, m.deleteTrust)

	r.Handle("GET /api/publish/log", route.UIPerm(activity.PermRead), m.publishLog)
}

// publishFail maps publish service errors to status codes. route.Fail maps every other error.
func publishFail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrAuthorizationPending), errors.Is(err, ErrSlowDown),
		errors.Is(err, ErrExpiredToken), errors.Is(err, ErrInvalid):
		httpx.Error(w, r, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, r, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrConflict):
		httpx.Error(w, r, http.StatusConflict, err.Error())
	case errors.Is(err, ErrDenied):
		httpx.Error(w, r, http.StatusUnauthorized, err.Error())
	case errors.Is(err, ErrForbidden):
		httpx.Error(w, r, http.StatusForbidden, err.Error())
	case errors.Is(err, ErrLimited):
		w.Header().Set("Retry-After", "30")
		httpx.Error(w, r, http.StatusTooManyRequests, err.Error())
	default:
		route.Fail(w, r, err)
	}
}

// --- devices ---

// enrollDevice starts the device flow (no auth): {name, public_key} ->
// {device_code, user_code, verification_url, expires_in, interval}. Without the base_url
// setting, verification_url starts with the URL the device used.
func (m *mod) enrollDevice(w http.ResponseWriter, r *http.Request, c *route.Call) {
	var in struct {
		Name      string `json:"name"`
		PublicKey string `json:"public_key"`
	}
	if !m.h.API.Decode(w, r, &in) {
		return
	}
	e, err := m.svc.Enroll(r.Context(), httpx.ClientIP(r), in.Name, in.PublicKey)
	if err != nil {
		publishFail(w, r, err)
		return
	}
	if strings.HasPrefix(e.VerificationURL, "/") {
		e.VerificationURL = strings.TrimSuffix(m.h.API.BaseURL(r), "/") + e.VerificationURL
	}
	httpx.WriteJSON(w, http.StatusOK, e)
}

// pollDevice answers the device's poll (no auth): {device_code} -> {device_id}, or 400 with
// {"error": "authorization_pending" | "slow_down" | "expired_token"}.
func (m *mod) pollDevice(w http.ResponseWriter, r *http.Request, c *route.Call) {
	var in struct {
		DeviceCode string `json:"device_code"`
	}
	if !m.h.API.Decode(w, r, &in) {
		return
	}
	id, err := m.svc.Poll(r.Context(), in.DeviceCode)
	if err != nil {
		publishFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, struct {
		DeviceID int64 `json:"device_id"`
	}{id})
}

// pendingEnrollment returns {user_code, name, expires_at} for the approval page:
// ?user_code=ABCD-EFGH.
func (m *mod) pendingEnrollment(w http.ResponseWriter, r *http.Request, c *route.Call) {
	e, err := m.svc.PendingEnrollment(r.Context(), r.URL.Query().Get("user_code"))
	if err != nil {
		publishFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, e)
}

// approveDevice approves an enrollment for the caller: {user_code} -> 201 with the device. The
// request carries a fresh second factor (auth.ProofHeader): a TOTP code or a passkey of a local
// account, or the grant of an SSO account that a fresh SSO sign-in issued for this user code.
func (m *mod) approveDevice(w http.ResponseWriter, r *http.Request, c *route.Call) {
	var in struct {
		UserCode string `json:"user_code"`
	}
	if !m.h.API.Decode(w, r, &in) {
		return
	}
	d, err := m.svc.Approve(r.Context(), r, c.P, c.Proof, in.UserCode)
	if err != nil {
		publishFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, d)
}

// listDevices returns the caller's devices, or every device with its owner for a holder of
// people.manage, newest first: ?after=<id>&limit=.
func (m *mod) listDevices(w http.ResponseWriter, r *http.Request, c *route.Call) {
	p := c.P
	after, ok := route.IDCursor(w, r)
	if !ok {
		return
	}
	var items []Device
	var next int64
	var err error
	if p.Perms.Has(perm.PeopleManage) {
		items, next, err = m.svc.Devices(r.Context(), after, httpx.Limit(r))
	} else {
		items, next, err = m.svc.UserDevices(r.Context(), p.UserID, after, httpx.Limit(r))
	}
	if err != nil {
		publishFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, route.Page[Device]{Items: items, Next: route.IDNext(next)})
}

// revokeDevice revokes one of the caller's devices (any device for a holder of people.manage) and ends its open
// sessions: 204.
func (m *mod) revokeDevice(w http.ResponseWriter, r *http.Request, c *route.Call) {
	p := c.P
	id, ok := route.PathID(w, r, "invalid id")
	if !ok {
		return
	}
	if err := m.svc.RevokeDevice(r.Context(), p, id); err != nil {
		publishFail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- sessions ---

// publishChallenge returns a nonce for a device (no auth): {device_id, exchange, nonce, ts,
// signature} -> {nonce, expires_at}. Without exchange the nonce serves one request within 60 s:
// a session for one repo (POST /api/publish/sessions), a project setup (POST /api/devices/setup)
// or the revoke of the device (DELETE /api/devices/self). With exchange it is a token exchange nonce
// (ExchangeChallenge): the device signs it over the repo "*" and a format's token endpoint takes
// the proof until it expires after publish.session_ttl. With exchange, a nonce in the request is
// one to keep: the answer is that nonce while it is a token exchange nonce of the device that has
// not expired. A new token exchange nonce needs ts and the device signature over it
// (ChallengeProof).
func (m *mod) publishChallenge(w http.ResponseWriter, r *http.Request, c *route.Call) {
	var in struct {
		DeviceID  int64  `json:"device_id"`
		Exchange  bool   `json:"exchange"`
		Nonce     string `json:"nonce"`
		TS        int64  `json:"ts"`
		Signature string `json:"signature"`
	}
	if !m.h.API.Decode(w, r, &in) {
		return
	}
	var nonce string
	var exp time.Time
	var err error
	if in.Exchange {
		nonce, exp, err = m.svc.ExchangeChallenge(r.Context(), in.DeviceID, in.Nonce, ChallengeProof{TS: in.TS, Signature: in.Signature})
	} else {
		nonce, exp, err = m.svc.Challenge(r.Context(), in.DeviceID)
	}
	if err != nil {
		publishFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, struct {
		Nonce     string    `json:"nonce"`
		ExpiresAt time.Time `json:"expires_at"`
	}{nonce, exp})
}

// deviceSession mints a session from a device signature (no auth):
// {device_id, repo, nonce, signature} -> 201 {id, token, repo, format, kind, expires_at}.
func (m *mod) deviceSession(w http.ResponseWriter, r *http.Request, c *route.Call) {
	var in struct {
		DeviceID  int64  `json:"device_id"`
		Repo      string `json:"repo"`
		Nonce     string `json:"nonce"`
		Signature string `json:"signature"`
	}
	if !m.h.API.Decode(w, r, &in) {
		return
	}
	m.mintRoute(w, r, MintCred{Device: &DeviceProof{DeviceID: in.DeviceID, Nonce: in.Nonce, Signature: in.Signature, Repo: in.Repo}}, in.Repo)
}

// manualSession mints a session for the signed-in caller, whose request carries a fresh second
// factor (auth.ProofHeader): {repo} -> 201 {id, token, repo, format, kind, expires_at}.
func (m *mod) manualSession(w http.ResponseWriter, r *http.Request, c *route.Call) {
	var in struct {
		Repo string `json:"repo"`
	}
	if !m.h.API.Decode(w, r, &in) {
		return
	}
	m.mintRoute(w, r, MintCred{Fresh: c.P}, in.Repo)
}

// oidcSession exchanges a CI identity token for a session (no auth): {token, repo, revision?} ->
// 201 {id, token, repo, format, kind, expires_at}. revision is the commit of the job.
func (m *mod) oidcSession(w http.ResponseWriter, r *http.Request, c *route.Call) {
	var in struct {
		Token    string `json:"token"`
		Repo     string `json:"repo"`
		Revision string `json:"revision"`
	}
	if !m.h.API.Decode(w, r, &in) {
		return
	}
	if in.Token == "" {
		publishFail(w, r, errorf(ErrInvalid, "token must be a CI identity token (JWT)"))
		return
	}
	if in.Revision != "" && !ci.ValidRevision(in.Revision) {
		publishFail(w, r, errorf(ErrInvalid, "revision must be a git commit hash: 40 or 64 lower-case hex digits"))
		return
	}
	m.mintRoute(w, r, MintCred{CIToken: in.Token, Revision: in.Revision}, in.Repo)
}

// listMySessions returns the caller's publish sessions, newest first: ?after=<id>&limit=.
func (m *mod) listMySessions(w http.ResponseWriter, r *http.Request, c *route.Call) {
	p := c.P
	after, ok := route.IDCursor(w, r)
	if !ok {
		return
	}
	items, next, err := m.svc.UserSessions(r.Context(), p.UserID, after, httpx.Limit(r))
	if err != nil {
		publishFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, route.Page[SessionInfo]{Items: items, Next: route.IDNext(next)})
}

// closeSession ends session {id}; the request authenticates with that session's token (Basic
// password or Bearer): 204.
func (m *mod) closeSession(w http.ResponseWriter, r *http.Request, c *route.Call) {
	id, ok := route.PathID(w, r, "invalid id")
	if !ok {
		return
	}
	if err := m.svc.CloseSession(r.Context(), c.P, id); err != nil {
		publishFail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// mintRoute mints a session for the repo name with cred and answers 201 with the session and its
// token. A mint that the access engine refuses answers its status and message (405 with Allow
// for a proxy or a group; the network guard has recorded its audit event).
func (m *mod) mintRoute(w http.ResponseWriter, r *http.Request, cred MintCred, name string) {
	mt, err := m.svc.MintRoute(r.Context(), r, cred, name)
	var refused *RefusedError
	switch {
	case errors.As(err, &refused):
		if refused.D.Code == access.CodeReadOnlyKind {
			w.Header().Set("Allow", "GET, HEAD")
		}
		httpx.Error(w, r, refused.D.Status, refused.D.Message)
	case err != nil:
		publishFail(w, r, err)
	default:
		httpx.WriteJSON(w, http.StatusCreated, mt)
	}
}

// publishLog returns every session, newest first (activity): ?after=<id>&limit=.
func (m *mod) publishLog(w http.ResponseWriter, r *http.Request, c *route.Call) {
	after, ok := route.IDCursor(w, r)
	if !ok {
		return
	}
	items, next, err := m.svc.Log(r.Context(), after, httpx.Limit(r))
	if err != nil {
		publishFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, route.Page[SessionInfo]{Items: items, Next: route.IDNext(next)})
}

// --- trust policies (CI pipelines) ---

// pipelineScope is what CI pipelines p may see and change: every one for a holder of All
// repositories (all), else those whose repos are all in names, the repos where p holds the
// pipelines permission.
type pipelineScope struct {
	all   bool
	names map[string]bool
}

// covers reports whether the scope holds every repo of a pipeline. A pipeline without repos
// belongs only to a holder of All repositories.
func (s pipelineScope) covers(repos []string) bool {
	if s.all {
		return true
	}
	if len(repos) == 0 {
		return false
	}
	for _, r := range repos {
		if !s.names[r] {
			return false
		}
	}
	return true
}

// pipelines returns the pipeline scope of the caller: the repos where the route policy found the
// pipelines permission (Call.Managed).
func (m *mod) pipelines(w http.ResponseWriter, r *http.Request, c *route.Call) (*auth.Principal, pipelineScope, bool) {
	s := pipelineScope{all: c.Managed.All, names: map[string]bool{}}
	for id := range c.Managed.IDs {
		repo, err := m.h.Repos.ByID(r.Context(), id)
		if errors.Is(err, repos.ErrNotFound) {
			continue
		}
		if err != nil {
			route.Fail(w, r, err)
			return nil, pipelineScope{}, false
		}
		s.names[repo.Name] = true
	}
	return c.P, s, true
}

// trustInScope reads pipeline {id} and answers 404 when it is outside the scope.
func (m *mod) trustInScope(w http.ResponseWriter, r *http.Request, s pipelineScope) (*Trust, bool) {
	id, ok := route.PathID(w, r, "invalid id")
	if !ok {
		return nil, false
	}
	t, err := m.svc.GetTrust(r.Context(), id)
	if err != nil {
		publishFail(w, r, err)
		return nil, false
	}
	if !s.covers(t.Repos) {
		httpx.Error(w, r, http.StatusNotFound, "trust policy not found")
		return nil, false
	}
	return t, true
}

// listTrusts returns a keyset page of the policies by name that the caller may see:
// ?after=<name>&limit=. It walks the pages of all policies until its page is full.
func (m *mod) listTrusts(w http.ResponseWriter, r *http.Request, c *route.Call) {
	_, scope, ok := m.pipelines(w, r, c)
	if !ok {
		return
	}
	limit := httpx.Limit(r)
	after := r.URL.Query().Get("after")
	out := []*Trust{}
	for {
		items, next, err := m.svc.Trusts(r.Context(), after, limit)
		if err != nil {
			publishFail(w, r, err)
			return
		}
		for i, t := range items {
			if !scope.covers(t.Repos) {
				continue
			}
			out = append(out, t)
			if len(out) == limit {
				more := ""
				if i < len(items)-1 || next != "" {
					more = t.Name
				}
				httpx.WriteJSON(w, http.StatusOK, route.Page[*Trust]{Items: out, Next: route.Cursor(more)})
				return
			}
		}
		if next == "" {
			httpx.WriteJSON(w, http.StatusOK, route.Page[*Trust]{Items: out, Next: nil})
			return
		}
		after = next
	}
}

// createTrust stores a policy: {name, issuer, audience, claims, jwks, repos, disabled} -> 201.
// The caller holds pipelines on every repo of it (403).
func (m *mod) createTrust(w http.ResponseWriter, r *http.Request, c *route.Call) {
	p, scope, ok := m.pipelines(w, r, c)
	if !ok {
		return
	}
	var in TrustInput
	if !m.h.API.Decode(w, r, &in) {
		return
	}
	if !scope.covers(in.Repos) {
		httpx.Error(w, r, http.StatusForbidden, "a CI pipeline may list only repositories where you hold the CI pipelines permission")
		return
	}
	t, err := m.svc.CreateTrust(r.Context(), p.UserID, in)
	if err != nil {
		publishFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, t)
}

func (m *mod) getTrust(w http.ResponseWriter, r *http.Request, c *route.Call) {
	_, scope, ok := m.pipelines(w, r, c)
	if !ok {
		return
	}
	t, ok := m.trustInScope(w, r, scope)
	if !ok {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, t)
}

// updateTrust changes the fields that are set. The pipeline and its new repos stay in the
// caller's scope (404 for a pipeline outside it, 403 for new repos outside it).
func (m *mod) updateTrust(w http.ResponseWriter, r *http.Request, c *route.Call) {
	_, scope, ok := m.pipelines(w, r, c)
	if !ok {
		return
	}
	cur, ok := m.trustInScope(w, r, scope)
	if !ok {
		return
	}
	var in TrustInput
	if !m.h.API.Decode(w, r, &in) {
		return
	}
	if in.Repos != nil && !scope.covers(in.Repos) {
		httpx.Error(w, r, http.StatusForbidden, "a CI pipeline may list only repositories where you hold the CI pipelines permission")
		return
	}
	t, err := m.svc.UpdateTrust(r.Context(), cur.ID, in)
	if err != nil {
		publishFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, t)
}

func (m *mod) deleteTrust(w http.ResponseWriter, r *http.Request, c *route.Call) {
	_, scope, ok := m.pipelines(w, r, c)
	if !ok {
		return
	}
	cur, ok := m.trustInScope(w, r, scope)
	if !ok {
		return
	}
	if err := m.svc.DeleteTrust(r.Context(), cur.ID); err != nil {
		publishFail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
