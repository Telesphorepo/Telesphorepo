package api

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"

	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
)

// registerSecurity mounts the two-step sign-in and the second-factor routes (spec §7.6):
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
//	POST   /api/security/passkeys/assert     → passkey options for a fresh check
//	DELETE /api/security/passkeys/{id}
//	POST   /api/security/recovery-codes      → {codes}
//
// The /api/security routes need a UI session of a local account; a session without a second
// factor reaches them (auth.Identify lets it through).
func (a *API) registerSecurity(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/auth/login", a.loginPassword)
	mux.HandleFunc("POST /api/auth/login/passkey", a.loginPasskey)
	mux.HandleFunc("POST /api/auth/login/second", a.loginSecondFactor)

	mux.HandleFunc("GET /api/security", a.getSecurity)
	mux.HandleFunc("POST /api/security/totp/begin", a.beginTOTP)
	mux.HandleFunc("POST /api/security/totp/confirm", a.confirmTOTP)
	mux.HandleFunc("DELETE /api/security/totp/{id}", a.deleteTOTP)
	mux.HandleFunc("POST /api/security/passkeys/begin", a.beginPasskeyRegistration)
	mux.HandleFunc("POST /api/security/passkeys/finish", a.finishPasskeyRegistration)
	mux.HandleFunc("POST /api/security/passkeys/assert", a.beginPasskeyAssertion)
	mux.HandleFunc("DELETE /api/security/passkeys/{id}", a.deletePasskey)
	mux.HandleFunc("POST /api/security/recovery-codes", a.generateRecoveryCodes)
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
		fail(w, r, err)
	}
}

// loginUser is a signed-in user. SecondFactorRequired is true for a setup-only session.
type loginUser struct {
	*auth.User
	SecondFactorRequired bool `json:"second_factor_required"`
}

// loginPassword is the password step. A user with a second factor gets
// {"second_factor": true, "methods": ["totp","passkey","recovery"]} (the kinds the user has) and
// the pending sign-in cookie; anyone else gets the user and a session.
func (a *API) loginPassword(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !httpx.DecodeJSON(w, r, a.cfg().MaxJSONBytes, &in) {
		return
	}
	res, err := a.auth.Login(w, r, in.Username, in.Password)
	if err != nil {
		if errors.Is(err, auth.ErrNotFound) || errors.Is(err, auth.ErrInvalid) {
			err = auth.ErrBadCredentials
		}
		fail(w, r, err)
		return
	}
	if res.User == nil {
		httpx.WriteJSON(w, http.StatusOK, struct {
			SecondFactor bool     `json:"second_factor"`
			Methods      []string `json:"methods"`
		}{true, res.SecondFactor})
		return
	}
	httpx.WriteJSON(w, http.StatusOK, loginUser{res.User, res.SetupOnly})
}

func (a *API) loginPasskey(w http.ResponseWriter, r *http.Request) {
	opts, err := a.auth.LoginPasskeyOptions(r)
	if err != nil {
		failSecondFactor(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, opts)
}

func (a *API) loginSecondFactor(w http.ResponseWriter, r *http.Request) {
	var in auth.SecondFactorInput
	if !httpx.DecodeJSON(w, r, a.cfg().MaxJSONBytes, &in) {
		return
	}
	u, err := a.auth.LoginSecondFactor(w, r, in)
	if err != nil {
		failSecondFactor(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, loginUser{u, false})
}

// securityUser requires a UI session of a local account: a password or token alone must not
// add or remove second factors.
func (a *API) securityUser(w http.ResponseWriter, r *http.Request) (*auth.Principal, bool) {
	p, ok := a.user(w, r)
	if !ok {
		return nil, false
	}
	if !p.Session {
		auth.SessionRequired(w)
		return nil, false
	}
	if p.Kind != auth.KindLocal {
		httpx.Error(w, http.StatusForbidden, auth.ErrSSOAccount.Error())
		return nil, false
	}
	return p, true
}

func (a *API) getSecurity(w http.ResponseWriter, r *http.Request) {
	p, ok := a.securityUser(w, r)
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

func (a *API) beginTOTP(w http.ResponseWriter, r *http.Request) {
	p, ok := a.securityUser(w, r)
	if !ok {
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if !httpx.DecodeJSON(w, r, a.cfg().MaxJSONBytes, &in) {
		return
	}
	setup, err := a.auth.BeginTOTP(r.Context(), p.UserID, in.Name)
	if err != nil {
		failSecondFactor(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, setup)
}

func (a *API) confirmTOTP(w http.ResponseWriter, r *http.Request) {
	p, ok := a.securityUser(w, r)
	if !ok {
		return
	}
	var in struct {
		SetupID string `json:"setup_id"`
		Code    string `json:"code"`
	}
	if !httpx.DecodeJSON(w, r, a.cfg().MaxJSONBytes, &in) {
		return
	}
	f, err := a.auth.ConfirmTOTP(r.Context(), p.UserID, in.SetupID, in.Code)
	if err != nil {
		failSecondFactor(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, f)
}

func (a *API) deleteTOTP(w http.ResponseWriter, r *http.Request) {
	p, ok := a.securityUser(w, r)
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

func (a *API) beginPasskeyRegistration(w http.ResponseWriter, r *http.Request) {
	p, ok := a.securityUser(w, r)
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

func (a *API) finishPasskeyRegistration(w http.ResponseWriter, r *http.Request) {
	p, ok := a.securityUser(w, r)
	if !ok {
		return
	}
	var in struct {
		Name       string          `json:"name"`
		Credential json.RawMessage `json:"credential"`
	}
	if !httpx.DecodeJSON(w, r, a.cfg().MaxJSONBytes, &in) {
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
func (a *API) beginPasskeyAssertion(w http.ResponseWriter, r *http.Request) {
	p, ok := a.securityUser(w, r)
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

func (a *API) deletePasskey(w http.ResponseWriter, r *http.Request) {
	p, ok := a.securityUser(w, r)
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

func (a *API) generateRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	p, ok := a.securityUser(w, r)
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
