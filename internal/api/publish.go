package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/netrules"
	"github.com/Telesphorepo/Telesphorepo/internal/publish"
)

// registerPublish mounts the publisher-credential API (spec §7.6): device enrollment, publish
// sessions, CI trust policies (pipelines on their repos) and the publish log (activity).
func (a *API) registerPublish(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/devices/enroll", a.enrollDevice)
	mux.HandleFunc("POST /api/devices/enroll/poll", a.pollDevice)
	mux.HandleFunc("GET /api/devices/enroll", a.pendingEnrollment)
	mux.HandleFunc("POST /api/devices/approve", a.approveDevice)
	mux.HandleFunc("GET /api/devices", a.listDevices)
	mux.HandleFunc("DELETE /api/devices/{id}", a.revokeDevice)

	mux.HandleFunc("POST /api/publish/challenge", a.publishChallenge)
	mux.HandleFunc("POST /api/publish/sessions", a.deviceSession)
	mux.HandleFunc("POST /api/publish/sessions/manual", a.manualSession)
	mux.HandleFunc("POST /api/publish/sessions/oidc", a.oidcSession)
	mux.HandleFunc("GET /api/publish/sessions", a.listMySessions)
	mux.HandleFunc("DELETE /api/publish/sessions/{id}", a.closeSession)

	mux.HandleFunc("GET /api/publish/trusts", a.listTrusts)
	mux.HandleFunc("POST /api/publish/trusts", a.createTrust)
	mux.HandleFunc("GET /api/publish/trusts/{id}", a.getTrust)
	mux.HandleFunc("PATCH /api/publish/trusts/{id}", a.updateTrust)
	mux.HandleFunc("DELETE /api/publish/trusts/{id}", a.deleteTrust)

	mux.HandleFunc("GET /api/publish/log", a.publishLog)
}

// publishFail maps publish service errors to status codes. A failed second-factor check is 401
// "second factor did not verify"; the web UI does not treat a 401 of the approval and
// manual-session routes as a lost sign-in.
func publishFail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, publish.ErrAuthorizationPending), errors.Is(err, publish.ErrSlowDown),
		errors.Is(err, publish.ErrExpiredToken), errors.Is(err, publish.ErrInvalid):
		httpx.Error(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, publish.ErrNotFound):
		httpx.Error(w, http.StatusNotFound, err.Error())
	case errors.Is(err, publish.ErrConflict):
		httpx.Error(w, http.StatusConflict, err.Error())
	case errors.Is(err, publish.ErrDenied):
		httpx.Error(w, http.StatusUnauthorized, err.Error())
	case errors.Is(err, publish.ErrForbidden):
		httpx.Error(w, http.StatusForbidden, err.Error())
	case errors.Is(err, publish.ErrLimited):
		w.Header().Set("Retry-After", "30")
		httpx.Error(w, http.StatusTooManyRequests, err.Error())
	case errors.Is(err, auth.ErrNoSecondFactor), errors.Is(err, auth.ErrPasskeysUnavailable):
		httpx.Error(w, http.StatusForbidden, err.Error())
	case errors.Is(err, auth.ErrBadCredentials):
		httpx.Error(w, http.StatusUnauthorized, "second factor did not verify")
	case errors.Is(err, auth.ErrRateLimited):
		if d, ok := auth.RetryAfter(err); ok {
			w.Header().Set("Retry-After", strconv.Itoa(max(1, int(d.Seconds()+0.999))))
		}
		fail(w, r, err)
	default:
		fail(w, r, err)
	}
}

// person requires a signed-in user that is not a read token: a UI session, or a username and
// password on a GET route (a.user already refuses publish-session tokens, and auth.Identify
// refuses a password on a route that changes state).
func (a *API) person(w http.ResponseWriter, r *http.Request) (*auth.Principal, bool) {
	p, ok := a.user(w, r)
	if !ok {
		return nil, false
	}
	if p.TokenID != 0 {
		httpx.Error(w, http.StatusForbidden, "sign in with your password for this")
		return nil, false
	}
	return p, true
}

func idNext(n int64) *string {
	if n == 0 {
		return nil
	}
	s := strconv.FormatInt(n, 10)
	return &s
}

// secondFactor is a fresh second-factor proof: a TOTP code or a passkey assertion (the
// browser's JSON).
type secondFactor struct {
	TOTPCode string          `json:"totp_code"`
	Passkey  json.RawMessage `json:"passkey"`
}

// --- devices ---

// enrollDevice starts the device flow (no auth): {name, kind, public_key} ->
// {device_code, user_code, verification_url, expires_in, interval}. Without the base_url
// setting, verification_url starts with the URL the device used.
func (a *API) enrollDevice(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name      string `json:"name"`
		Kind      string `json:"kind"`
		PublicKey string `json:"public_key"`
	}
	if !httpx.DecodeJSON(w, r, a.cfg().MaxJSONBytes, &in) {
		return
	}
	e, err := a.publish.Enroll(r.Context(), httpx.ClientIP(r), in.Name, in.Kind, in.PublicKey)
	if err != nil {
		publishFail(w, r, err)
		return
	}
	if strings.HasPrefix(e.VerificationURL, "/") {
		e.VerificationURL = strings.TrimSuffix(a.baseURL(r), "/") + e.VerificationURL
	}
	httpx.WriteJSON(w, http.StatusOK, e)
}

// pollDevice answers the device's poll (no auth): {device_code} -> {device_id}, or 400 with
// {"error": "authorization_pending" | "slow_down" | "expired_token"}.
func (a *API) pollDevice(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DeviceCode string `json:"device_code"`
	}
	if !httpx.DecodeJSON(w, r, a.cfg().MaxJSONBytes, &in) {
		return
	}
	id, err := a.publish.Poll(r.Context(), in.DeviceCode)
	if err != nil {
		publishFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, struct {
		DeviceID int64 `json:"device_id"`
	}{id})
}

// pendingEnrollment returns {user_code, name, kind, expires_at} for the approval page:
// ?user_code=ABCD-EFGH.
func (a *API) pendingEnrollment(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.person(w, r); !ok {
		return
	}
	e, err := a.publish.PendingEnrollment(r.Context(), r.URL.Query().Get("user_code"))
	if err != nil {
		publishFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, e)
}

// approveDevice approves an enrollment for the caller: {user_code, totp_code | passkey} for a
// local account, {user_code, grant} for an SSO account -> 201 with the device.
func (a *API) approveDevice(w http.ResponseWriter, r *http.Request) {
	p, ok := a.person(w, r)
	if !ok {
		return
	}
	var in struct {
		UserCode string `json:"user_code"`
		Grant    string `json:"grant"`
		secondFactor
	}
	if !httpx.DecodeJSON(w, r, a.cfg().MaxJSONBytes, &in) {
		return
	}
	d, err := a.publish.Approve(r.Context(), p, in.UserCode, in.TOTPCode, in.Passkey, in.Grant)
	if err != nil {
		publishFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, d)
}

// listDevices returns the caller's devices, or every device with its owner for a holder of
// People, newest first: ?after=<id>&limit=.
func (a *API) listDevices(w http.ResponseWriter, r *http.Request) {
	p, ok := a.user(w, r)
	if !ok {
		return
	}
	after, ok := idCursor(w, r)
	if !ok {
		return
	}
	var items []publish.Device
	var next int64
	var err error
	if p.Perms.Has(auth.PermPeople) {
		items, next, err = a.publish.Devices(r.Context(), after, httpx.Limit(r))
	} else {
		items, next, err = a.publish.UserDevices(r.Context(), p.UserID, after, httpx.Limit(r))
	}
	if err != nil {
		publishFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, list[publish.Device]{items, idNext(next)})
}

// revokeDevice revokes one of the caller's devices (any device for a holder of People) and ends its open
// sessions: 204.
func (a *API) revokeDevice(w http.ResponseWriter, r *http.Request) {
	p, ok := a.user(w, r)
	if !ok {
		return
	}
	id, ok := nexusID(w, r)
	if !ok {
		return
	}
	if err := a.publish.RevokeDevice(r.Context(), p, id); err != nil {
		publishFail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- sessions ---

// publishChallenge returns a single-use nonce for a device (no auth): {device_id} -> {nonce}.
func (a *API) publishChallenge(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DeviceID int64 `json:"device_id"`
	}
	if !httpx.DecodeJSON(w, r, a.cfg().MaxJSONBytes, &in) {
		return
	}
	nonce, err := a.publish.Challenge(r.Context(), in.DeviceID)
	if err != nil {
		publishFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, struct {
		Nonce string `json:"nonce"`
	}{nonce})
}

// deviceSession mints a session from a device signature (no auth):
// {device_id, repo, nonce, signature} -> 201 {id, token, repo, expires_at}.
func (a *API) deviceSession(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DeviceID  int64  `json:"device_id"`
		Repo      string `json:"repo"`
		Nonce     string `json:"nonce"`
		Signature string `json:"signature"`
	}
	if !httpx.DecodeJSON(w, r, a.cfg().MaxJSONBytes, &in) {
		return
	}
	m, err := a.publish.DeviceSession(r.Context(), httpx.ClientIP(r), in.DeviceID, in.Repo, in.Nonce, in.Signature)
	if err != nil {
		a.mintFail(w, r, err)
		return
	}
	a.recordMint(r, m)
	httpx.WriteJSON(w, http.StatusCreated, m)
}

// manualSession mints a session for the signed-in caller after a fresh second-factor check:
// {repo, totp_code | passkey} -> 201 {id, token, repo, expires_at}.
func (a *API) manualSession(w http.ResponseWriter, r *http.Request) {
	p, ok := a.person(w, r)
	if !ok {
		return
	}
	var in struct {
		Repo string `json:"repo"`
		secondFactor
	}
	if !httpx.DecodeJSON(w, r, a.cfg().MaxJSONBytes, &in) {
		return
	}
	m, err := a.publish.ManualSession(r.Context(), httpx.ClientIP(r), p, in.Repo, in.TOTPCode, in.Passkey)
	if err != nil {
		a.mintFail(w, r, err)
		return
	}
	a.recordMint(r, m)
	httpx.WriteJSON(w, http.StatusCreated, m)
}

// oidcSession exchanges a CI identity token for a session (no auth): {token, repo} ->
// 201 {id, token, repo, expires_at}.
func (a *API) oidcSession(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token string `json:"token"`
		Repo  string `json:"repo"`
	}
	if !httpx.DecodeJSON(w, r, a.cfg().MaxJSONBytes, &in) {
		return
	}
	m, err := a.publish.OIDCSession(r.Context(), httpx.ClientIP(r), in.Token, in.Repo)
	if err != nil {
		a.mintFail(w, r, err)
		return
	}
	a.recordMint(r, m)
	httpx.WriteJSON(w, http.StatusCreated, m)
}

// listMySessions returns the caller's publish sessions, newest first: ?after=<id>&limit=.
func (a *API) listMySessions(w http.ResponseWriter, r *http.Request) {
	p, ok := a.user(w, r)
	if !ok {
		return
	}
	after, ok := idCursor(w, r)
	if !ok {
		return
	}
	items, next, err := a.publish.UserSessions(r.Context(), p.UserID, after, httpx.Limit(r))
	if err != nil {
		publishFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, list[publish.SessionInfo]{items, idNext(next)})
}

// requestToken returns the credential of an "Authorization: Basic" (the password) or
// "Authorization: Bearer" header.
func requestToken(r *http.Request) string {
	if _, pass, ok := r.BasicAuth(); ok {
		return pass
	}
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// closeSession ends session {id}; the request authenticates with that session's token (Basic
// password or Bearer): 204.
func (a *API) closeSession(w http.ResponseWriter, r *http.Request) {
	id, ok := nexusID(w, r)
	if !ok {
		return
	}
	tok := requestToken(r)
	if tok == "" {
		httpx.Error(w, http.StatusUnauthorized, "authenticate with the publish session token")
		return
	}
	cl, err := a.publish.CloseSession(r.Context(), id, tok)
	if err != nil {
		publishFail(w, r, err)
		return
	}
	if cl != nil {
		a.audit.Record(r.Context(), r, audit.Event{
			Actor: audit.Actor{UserID: cl.UserID, Name: cl.Subject}, Action: audit.SessionClose,
			RepoID: cl.RepoID, Repo: cl.Repo, Target: strconv.FormatInt(id, 10), Detail: map[string]any{"via": cl.Via},
		})
	}
	w.WriteHeader(http.StatusNoContent)
}

// mintFail answers a failed mint. An address that the repo's publish rules refuse gets 403 and an
// ip.denied audit event.
func (a *API) mintFail(w http.ResponseWriter, r *http.Request, err error) {
	var d *netrules.DeniedError
	if errors.As(err, &d) {
		a.rules.Refuse(w, r, d)
		return
	}
	publishFail(w, r, err)
}

// recordMint writes a new publish session to the audit log.
func (a *API) recordMint(r *http.Request, m *publish.Minted) {
	a.audit.Record(r.Context(), r, audit.Event{
		Actor: audit.Actor{UserID: m.UserID, Name: m.Subject}, Action: audit.SessionCreate,
		RepoID: m.RepoID, Repo: m.Repo, Target: strconv.FormatInt(m.ID, 10), Detail: map[string]any{"via": m.Via},
	})
}

// publishLog returns every session, newest first (activity): ?after=<id>&limit=.
func (a *API) publishLog(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.can(w, r, auth.PermActivity); !ok {
		return
	}
	after, ok := idCursor(w, r)
	if !ok {
		return
	}
	items, next, err := a.publish.Log(r.Context(), after, httpx.Limit(r))
	if err != nil {
		publishFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, list[publish.SessionInfo]{items, idNext(next)})
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

// pipelines requires a UI session and returns its pipeline scope. 403 when the caller can see no
// pipeline at all.
func (a *API) pipelines(w http.ResponseWriter, r *http.Request) (*auth.Principal, pipelineScope, bool) {
	p, ok := a.session(w, r)
	if !ok {
		return nil, pipelineScope{}, false
	}
	ids, all, err := a.auth.ManagedRepoIDs(r.Context(), p, auth.RepoPipelines)
	if err != nil {
		fail(w, r, err)
		return nil, pipelineScope{}, false
	}
	s := pipelineScope{all: all, names: map[string]bool{}}
	for id := range ids {
		if repo, err := a.repos.ByID(r.Context(), id); err == nil {
			s.names[repo.Name] = true
		}
	}
	if !all && len(s.names) == 0 {
		httpx.Error(w, http.StatusForbidden, "permission required: pipelines on a repository")
		return nil, pipelineScope{}, false
	}
	return p, s, true
}

// trustInScope reads pipeline {id} and answers 404 when it is outside the scope.
func (a *API) trustInScope(w http.ResponseWriter, r *http.Request, s pipelineScope) (*publish.Trust, bool) {
	id, ok := nexusID(w, r)
	if !ok {
		return nil, false
	}
	t, err := a.publish.GetTrust(r.Context(), id)
	if err != nil {
		publishFail(w, r, err)
		return nil, false
	}
	if !s.covers(t.Repos) {
		httpx.Error(w, http.StatusNotFound, "trust policy not found")
		return nil, false
	}
	return t, true
}

// listTrusts returns a keyset page of the policies by name that the caller may see:
// ?after=<name>&limit=. It walks the pages of all policies until its page is full.
func (a *API) listTrusts(w http.ResponseWriter, r *http.Request) {
	_, scope, ok := a.pipelines(w, r)
	if !ok {
		return
	}
	limit := httpx.Limit(r)
	after := r.URL.Query().Get("after")
	out := []*publish.Trust{}
	for {
		items, next, err := a.publish.Trusts(r.Context(), after, limit)
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
				httpx.WriteJSON(w, http.StatusOK, list[*publish.Trust]{out, cursor(more)})
				return
			}
		}
		if next == "" {
			httpx.WriteJSON(w, http.StatusOK, list[*publish.Trust]{out, nil})
			return
		}
		after = next
	}
}

// createTrust stores a policy: {name, issuer, audience, claims, jwks, repos, disabled} -> 201.
// The caller holds pipelines on every repo of it (403).
func (a *API) createTrust(w http.ResponseWriter, r *http.Request) {
	p, scope, ok := a.pipelines(w, r)
	if !ok {
		return
	}
	var in publish.TrustInput
	if !httpx.DecodeJSON(w, r, a.cfg().MaxJSONBytes, &in) {
		return
	}
	if !scope.covers(in.Repos) {
		httpx.Error(w, http.StatusForbidden, "a CI pipeline may list only repositories where you hold the CI pipelines permission")
		return
	}
	t, err := a.publish.CreateTrust(r.Context(), p.UserID, in)
	if err != nil {
		publishFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, t)
}

func (a *API) getTrust(w http.ResponseWriter, r *http.Request) {
	_, scope, ok := a.pipelines(w, r)
	if !ok {
		return
	}
	t, ok := a.trustInScope(w, r, scope)
	if !ok {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, t)
}

// updateTrust changes the fields that are set. The pipeline and its new repos stay in the
// caller's scope (404 for a pipeline outside it, 403 for new repos outside it).
func (a *API) updateTrust(w http.ResponseWriter, r *http.Request) {
	_, scope, ok := a.pipelines(w, r)
	if !ok {
		return
	}
	cur, ok := a.trustInScope(w, r, scope)
	if !ok {
		return
	}
	var in publish.TrustInput
	if !httpx.DecodeJSON(w, r, a.cfg().MaxJSONBytes, &in) {
		return
	}
	if in.Repos != nil && !scope.covers(in.Repos) {
		httpx.Error(w, http.StatusForbidden, "a CI pipeline may list only repositories where you hold the CI pipelines permission")
		return
	}
	t, err := a.publish.UpdateTrust(r.Context(), cur.ID, in)
	if err != nil {
		publishFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, t)
}

func (a *API) deleteTrust(w http.ResponseWriter, r *http.Request) {
	_, scope, ok := a.pipelines(w, r)
	if !ok {
		return
	}
	cur, ok := a.trustInScope(w, r, scope)
	if !ok {
		return
	}
	if err := a.publish.DeleteTrust(r.Context(), cur.ID); err != nil {
		publishFail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
