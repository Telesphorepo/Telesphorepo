package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/publish"
)

// registerPublish mounts the publisher-credential API (spec §7.6): device enrollment, publish
// sessions, CI trust policies (admin) and the publish log (admin).
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

// person requires a signed-in user that is not a read token: a UI session or a username and
// password (a.user already refuses publish-session tokens).
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
	e, err := a.publish.Enroll(r.Context(), httpx.ClientIP(r, a.cfg().TrustedProxies), in.Name, in.Kind, in.PublicKey)
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

// approveDevice approves an enrollment for the caller: {user_code, totp_code | passkey} ->
// 201 with the device.
func (a *API) approveDevice(w http.ResponseWriter, r *http.Request) {
	p, ok := a.person(w, r)
	if !ok {
		return
	}
	var in struct {
		UserCode string `json:"user_code"`
		secondFactor
	}
	if !httpx.DecodeJSON(w, r, a.cfg().MaxJSONBytes, &in) {
		return
	}
	d, err := a.publish.Approve(r.Context(), p, in.UserCode, in.TOTPCode, in.Passkey)
	if err != nil {
		publishFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, d)
}

// listDevices returns the caller's devices, or every device with its owner for an admin, newest
// first: ?after=<id>&limit=.
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
	if p.IsAdmin {
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

// revokeDevice revokes one of the caller's devices (any device for an admin) and ends its open
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
	m, err := a.publish.DeviceSession(r.Context(), in.DeviceID, in.Repo, in.Nonce, in.Signature)
	if err != nil {
		publishFail(w, r, err)
		return
	}
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
	m, err := a.publish.ManualSession(r.Context(), p, in.Repo, in.TOTPCode, in.Passkey)
	if err != nil {
		publishFail(w, r, err)
		return
	}
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
	m, err := a.publish.OIDCSession(r.Context(), in.Token, in.Repo)
	if err != nil {
		publishFail(w, r, err)
		return
	}
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
	if err := a.publish.CloseSession(r.Context(), id, tok); err != nil {
		publishFail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// publishLog returns every session, newest first (admin): ?after=<id>&limit=.
func (a *API) publishLog(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.admin(w, r); !ok {
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

// --- trust policies (admin) ---

// listTrusts returns a keyset page of the policies by name: ?after=<name>&limit=.
func (a *API) listTrusts(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.admin(w, r); !ok {
		return
	}
	items, next, err := a.publish.Trusts(r.Context(), r.URL.Query().Get("after"), httpx.Limit(r))
	if err != nil {
		publishFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, list[*publish.Trust]{items, cursor(next)})
}

// createTrust stores a policy: {name, issuer, audience, claims, jwks, repos, disabled} -> 201.
func (a *API) createTrust(w http.ResponseWriter, r *http.Request) {
	p, ok := a.admin(w, r)
	if !ok {
		return
	}
	var in publish.TrustInput
	if !httpx.DecodeJSON(w, r, a.cfg().MaxJSONBytes, &in) {
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
	if _, ok := a.admin(w, r); !ok {
		return
	}
	id, ok := nexusID(w, r)
	if !ok {
		return
	}
	t, err := a.publish.GetTrust(r.Context(), id)
	if err != nil {
		publishFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, t)
}

// updateTrust changes the fields that are set.
func (a *API) updateTrust(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.admin(w, r); !ok {
		return
	}
	id, ok := nexusID(w, r)
	if !ok {
		return
	}
	var in publish.TrustInput
	if !httpx.DecodeJSON(w, r, a.cfg().MaxJSONBytes, &in) {
		return
	}
	t, err := a.publish.UpdateTrust(r.Context(), id, in)
	if err != nil {
		publishFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, t)
}

func (a *API) deleteTrust(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.admin(w, r); !ok {
		return
	}
	id, ok := nexusID(w, r)
	if !ok {
		return
	}
	if err := a.publish.DeleteTrust(r.Context(), id); err != nil {
		publishFail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
