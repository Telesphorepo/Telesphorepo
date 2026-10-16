package api

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"

	"github.com/Telesphorepo/Telesphorepo/internal/route"

	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
)

// registerSecurity mounts the two-step sign-in and the second-factor routes:
//
//	POST   /api/auth/login                   {username, password}
//	POST   /api/auth/login/passkey           → passkey options for the pending sign-in
//	POST   /api/auth/login/second            {totp_code | passkey | recovery_code}
//	GET    /api/security                     → factors overview
//	POST   /api/security/totp/begin          {name} → {setup_id, secret, otpauth_url}
//	POST   /api/security/totp/confirm        {setup_id, code}
//	DELETE /api/security/totp/{id}
//	POST   /api/security/passkeys/begin      → passkey creation options
//	POST   /api/security/passkeys/finish     {name, credential}
//	POST   /api/security/passkeys/assert     → passkey options for a fresh check (auth.ProofHeader)
//	DELETE /api/security/passkeys/{id}
//	POST   /api/security/recovery-codes      → {codes}
//
// The /api/security routes need a UI session of a local account (route.Setup): a session
// without the second factor it needs reaches them, to set the factor up.
func (a *API) registerSecurity(mux *route.Router) {
	mux.Handle("POST /api/auth/login", route.Public, a.loginPassword)
	mux.Handle("POST /api/auth/login/passkey", route.Public, a.loginPasskey)
	mux.Handle("POST /api/auth/login/second", route.Public, a.loginSecondFactor)

	mux.Handle("GET /api/security", route.Setup, a.getSecurity)
	mux.Handle("POST /api/security/totp/begin", route.Setup, a.beginTOTP)
	mux.Handle("POST /api/security/totp/confirm", route.Setup, a.confirmTOTP)
	mux.Handle("DELETE /api/security/totp/{id}", route.Setup, a.deleteTOTP)
	mux.Handle("POST /api/security/passkeys/begin", route.Setup, a.beginPasskeyRegistration)
	mux.Handle("POST /api/security/passkeys/finish", route.Setup, a.finishPasskeyRegistration)
	mux.Handle("POST /api/security/passkeys/assert", route.Setup, a.beginPasskeyAssertion)
	mux.Handle("DELETE /api/security/passkeys/{id}", route.Setup, a.deletePasskey)
	mux.Handle("POST /api/security/recovery-codes", route.Setup, a.generateRecoveryCodes)
}

// sessionAnswer is the answer of a finished sign-in step: {session: SessionView}.
type sessionAnswer struct {
	Session *auth.SessionView `json:"session"`
}

// failSecondFactor maps second-factor errors, then falls back to fail.
func failSecondFactor(w http.ResponseWriter, r *http.Request, err error) {
	if d, ok := auth.RetryAfter(err); ok {
		w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(d.Seconds())))))
	}
	switch {
	case errors.Is(err, auth.ErrLoginExpired):
		httpx.Error(w, http.StatusUnauthorized, "sign-in expired; enter your password again")
	case errors.Is(err, auth.ErrBadSecondFactor):
		httpx.Error(w, http.StatusUnauthorized, "second factor did not verify")
	case errors.Is(err, auth.ErrNoSecondFactor):
		httpx.Error(w, http.StatusForbidden, auth.ErrNoSecondFactor.Error())
	case errors.Is(err, auth.ErrPasskeysUnavailable):
		httpx.Error(w, http.StatusConflict, auth.ErrPasskeysUnavailable.Error())
	default:
		route.Fail(w, r, err)
	}
}

// loginPassword is the password step. A user with a second factor gets
// {"second_factor": true, "methods": ["totp","passkey","recovery"]} (the kinds the user has) and
// the pending sign-in cookie; anyone else gets {session: SessionView} and a session.
func (a *API) loginPassword(w http.ResponseWriter, r *http.Request, c *route.Call) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !a.kit.Decode(w, r, &in) {
		return
	}
	res, err := a.auth.Login(w, r, in.Username, in.Password)
	if err != nil {
		if errors.Is(err, auth.ErrNotFound) || errors.Is(err, auth.ErrInvalid) {
			err = auth.ErrBadCredentials
		}
		route.Fail(w, r, err)
		return
	}
	if res.Session == nil {
		httpx.WriteJSON(w, http.StatusOK, struct {
			SecondFactor bool     `json:"second_factor"`
			Methods      []string `json:"methods"`
		}{true, res.SecondFactor})
		return
	}
	httpx.WriteJSON(w, http.StatusOK, sessionAnswer{res.Session})
}

func (a *API) loginPasskey(w http.ResponseWriter, r *http.Request, c *route.Call) {
	opts, err := a.auth.LoginPasskeyOptions(r)
	if err != nil {
		failSecondFactor(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, opts)
}

func (a *API) loginSecondFactor(w http.ResponseWriter, r *http.Request, c *route.Call) {
	var in auth.SecondFactorInput
	if !a.kit.Decode(w, r, &in) {
		return
	}
	v, err := a.auth.LoginSecondFactor(w, r, in)
	if err != nil {
		failSecondFactor(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, sessionAnswer{v})
}

// localAccount requires the UI session (route.Setup) of a local account: an SSO account has no
// second factors of its own.
func localAccount(w http.ResponseWriter, c *route.Call) (*auth.Principal, bool) {
	p := c.P
	if p.Kind != auth.KindLocal {
		httpx.Error(w, http.StatusForbidden, auth.ErrSSOAccount.Error())
		return nil, false
	}
	return p, true
}

func (a *API) getSecurity(w http.ResponseWriter, r *http.Request, c *route.Call) {
	p, ok := localAccount(w, c)
	if !ok {
		return
	}
	f, err := a.auth.ListFactors(r.Context(), p.UserID)
	if err != nil {
		failSecondFactor(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, f)
}

func (a *API) beginTOTP(w http.ResponseWriter, r *http.Request, c *route.Call) {
	p, ok := localAccount(w, c)
	if !ok {
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if !a.kit.Decode(w, r, &in) {
		return
	}
	setup, err := a.auth.BeginTOTP(r.Context(), p.UserID, in.Name)
	if err != nil {
		failSecondFactor(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, setup)
}

func (a *API) confirmTOTP(w http.ResponseWriter, r *http.Request, c *route.Call) {
	p, ok := localAccount(w, c)
	if !ok {
		return
	}
	var in struct {
		SetupID string `json:"setup_id"`
		Code    string `json:"code"`
	}
	if !a.kit.Decode(w, r, &in) {
		return
	}
	f, err := a.auth.ConfirmTOTP(r.Context(), p.UserID, in.SetupID, in.Code)
	if err != nil {
		failSecondFactor(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, f)
}

func (a *API) deleteTOTP(w http.ResponseWriter, r *http.Request, c *route.Call) {
	p, ok := localAccount(w, c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := a.auth.DeleteTOTP(r.Context(), p.UserID, id); err != nil {
		failSecondFactor(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) beginPasskeyRegistration(w http.ResponseWriter, r *http.Request, c *route.Call) {
	p, ok := localAccount(w, c)
	if !ok {
		return
	}
	opts, err := a.auth.BeginPasskeyRegistration(r.Context(), p.UserID)
	if err != nil {
		failSecondFactor(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, opts)
}

func (a *API) finishPasskeyRegistration(w http.ResponseWriter, r *http.Request, c *route.Call) {
	p, ok := localAccount(w, c)
	if !ok {
		return
	}
	var in struct {
		Name       string          `json:"name"`
		Credential json.RawMessage `json:"credential"`
	}
	if !a.kit.Decode(w, r, &in) {
		return
	}
	pk, err := a.auth.FinishPasskeyRegistration(r.Context(), p.UserID, in.Name, in.Credential)
	if err != nil {
		failSecondFactor(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, pk)
}

// beginPasskeyAssertion starts a fresh passkey check, for a device approval or a manual
// publish session.
func (a *API) beginPasskeyAssertion(w http.ResponseWriter, r *http.Request, c *route.Call) {
	p, ok := localAccount(w, c)
	if !ok {
		return
	}
	opts, err := a.auth.BeginPasskeyAssertion(r.Context(), p.UserID)
	if err != nil {
		failSecondFactor(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, opts)
}

func (a *API) deletePasskey(w http.ResponseWriter, r *http.Request, c *route.Call) {
	p, ok := localAccount(w, c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := a.auth.DeletePasskey(r.Context(), p.UserID, id); err != nil {
		failSecondFactor(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) generateRecoveryCodes(w http.ResponseWriter, r *http.Request, c *route.Call) {
	p, ok := localAccount(w, c)
	if !ok {
		return
	}
	codes, err := a.auth.GenerateRecoveryCodes(r.Context(), p.UserID)
	if err != nil {
		failSecondFactor(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, struct {
		Codes []string `json:"codes"`
	}{codes})
}
