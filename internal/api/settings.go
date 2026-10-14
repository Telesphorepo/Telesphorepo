package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"

	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/move"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
)

// ssoKeys are the settings whose change must pass the OIDC discovery check before it is saved.
var ssoKeys = func() map[string]bool {
	m := map[string]bool{}
	for _, k := range settings.SSOKeys {
		m[k] = true
	}
	return m
}()

type settingsGroup struct {
	Name     string          `json:"name"`
	Label    string          `json:"label"`
	Settings []settings.Item `json:"settings"`
}

type settingsJSON struct {
	Groups []settingsGroup `json:"groups"`
}

func (a *API) settingsList(r *http.Request) settingsJSON {
	items := a.settings.List(r.Context())
	byGroup := map[string][]settings.Item{}
	for _, it := range items {
		byGroup[it.Group] = append(byGroup[it.Group], it)
	}
	out := settingsJSON{Groups: []settingsGroup{}}
	for _, g := range settings.Groups {
		if s := byGroup[g]; len(s) > 0 {
			out.Groups = append(out.Groups, settingsGroup{g, settings.GroupLabels[g], s})
		}
	}
	return out
}

// authConfig tells the UI which sign-in methods exist. password_login is always true (publishers
// sign in with a password); admin_password_login false means local admins must use SSO.
func (a *API) authConfig(w http.ResponseWriter, r *http.Request) {
	c := a.cfg()
	httpx.WriteJSON(w, http.StatusOK, map[string]bool{
		"password_login":       true,
		"oidc":                 c.OIDCEnabled(),
		"admin_password_login": c.AdminPasswordLogin,
	})
}

func (a *API) getSettings(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.admin(w, r); !ok {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, a.settingsList(r))
}

// decodePatch reads a {key: value} body. An empty string for a secret means "leave unchanged"
// and is dropped (the UI sends it for untouched password fields); null clears a setting.
func (a *API) decodePatch(w http.ResponseWriter, r *http.Request) (map[string]json.RawMessage, bool) {
	var patch map[string]json.RawMessage
	if !httpx.DecodeJSON(w, r, a.cfg().MaxJSONBytes, &patch) {
		return nil, false
	}
	for k, raw := range patch {
		if def := settings.Lookup(k); def != nil && def.Secret && string(bytes.TrimSpace(raw)) == `""` {
			delete(patch, k)
		}
	}
	return patch, true
}

// changesAny reports whether patch would change the effective value of one of the keys isKey accepts.
func changesAny(items []settings.Item, patch map[string]json.RawMessage, isKey func(string) bool) bool {
	for _, it := range items {
		raw, ok := patch[it.Key]
		if !ok || !isKey(it.Key) || it.Locked {
			continue
		}
		if string(bytes.TrimSpace(raw)) == "null" {
			if it.Source == settings.SourceDB {
				return true
			}
			continue
		}
		if it.Secret {
			return true // a new secret value; we cannot compare it
		}
		var v any
		if json.Unmarshal(raw, &v) != nil {
			return true // let validation report it
		}
		cur, _ := json.Marshal(it.Value)
		var c any
		_ = json.Unmarshal(cur, &c)
		if !reflect.DeepEqual(v, c) {
			return true
		}
	}
	return false
}

// settingsError writes a settings validation error: 409 if it concerns a setting forced by the
// environment, else 400. Body: {error, fields:{key: msg}}.
func (a *API) settingsError(w http.ResponseWriter, r *http.Request, err error, items []settings.Item) {
	var ve *settings.ValidationError
	if !errors.As(err, &ve) {
		fail(w, r, err)
		return
	}
	status := http.StatusBadRequest
	for _, it := range items {
		if _, bad := ve.Fields[it.Key]; bad && it.Locked {
			status = http.StatusConflict
		}
	}
	httpx.WriteJSON(w, status, map[string]any{"error": ve.Error(), "fields": ve.Fields})
}

func (a *API) patchSettings(w http.ResponseWriter, r *http.Request) {
	p, ok := a.admin(w, r)
	if !ok {
		return
	}
	patch, ok := a.decodePatch(w, r)
	if !ok {
		return
	}
	items := a.settings.List(r.Context())
	if changesAny(items, patch, func(k string) bool { return ssoKeys[k] }) {
		if err := a.settings.TestOIDC(r.Context(), patch); err != nil {
			a.settingsError(w, r, err, items)
			return
		}
	}
	if changesAny(items, patch, settings.IsStorageKey) {
		if a.moves.Running() {
			httpx.Error(w, http.StatusConflict, "a storage move is running; wait for it to end or cancel it")
			return
		}
		if err := a.settings.TestStorage(r.Context(), patch); err != nil {
			var ve *settings.ValidationError
			if errors.As(err, &ve) {
				a.settingsError(w, r, err, items)
				return
			}
			msg := "storage test failed: " + err.Error()
			httpx.WriteJSON(w, http.StatusBadRequest, map[string]any{
				"error": msg, "fields": map[string]string{settings.KeyStorage: msg},
			})
			return
		}
		if a.startMove(w, r, patch, items, p.UserID) {
			return
		}
	}
	if err := a.settings.Update(r.Context(), patch, p.UserID); err != nil {
		a.settingsError(w, r, err, items)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, a.settingsList(r))
}

// startMove handles a settings patch that changes the blob store (spec §11). When the new store
// is at another location and assets exist, it saves the patch's other settings, starts a move
// to the new store and answers 202 {move}; the storage settings change when the move is done.
// It returns false when the caller should save the whole patch at once (no assets, or the same
// location with other credentials or options).
func (a *API) startMove(w http.ResponseWriter, r *http.Request, patch map[string]json.RawMessage, items []settings.Item, userID int64) bool {
	ctx := r.Context()
	target, err := a.settings.Preview(patch)
	if err != nil {
		a.settingsError(w, r, err, items)
		return true
	}
	if move.SameLocation(a.cfg(), target) {
		return false
	}
	has, err := a.moves.HasAssets(ctx)
	if err != nil {
		fail(w, r, err)
		return true
	}
	if !has {
		return false
	}
	rest := map[string]json.RawMessage{}
	for k, v := range patch {
		if !settings.IsStorageKey(k) {
			rest[k] = v
		}
	}
	if len(rest) > 0 {
		if err := a.settings.Update(ctx, rest, userID); err != nil {
			a.settingsError(w, r, err, items)
			return true
		}
	}
	m, err := a.moves.Start(ctx, target, userID)
	if err != nil {
		moveFail(w, r, err)
		return true
	}
	httpx.WriteJSON(w, http.StatusAccepted, map[string]any{"move": m})
	return true
}

func (a *API) testStorage(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.admin(w, r); !ok {
		return
	}
	patch, ok := a.decodePatch(w, r)
	if !ok {
		return
	}
	if err := a.settings.TestStorage(r.Context(), patch); err != nil {
		body := map[string]any{"ok": false, "error": err.Error()}
		var ve *settings.ValidationError
		if errors.As(err, &ve) {
			body["fields"] = ve.Fields
		}
		httpx.WriteJSON(w, http.StatusBadRequest, body)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// testOIDC validates the SSO settings patch would produce (merged over the current values, not
// saved), including fetching the issuer's discovery document. {ok:true} or 400 {ok:false, error,
// fields}.
func (a *API) testOIDC(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.admin(w, r); !ok {
		return
	}
	patch, ok := a.decodePatch(w, r)
	if !ok {
		return
	}
	if err := a.settings.TestOIDC(r.Context(), patch); err != nil {
		body := map[string]any{"ok": false, "error": err.Error()}
		var ve *settings.ValidationError
		if errors.As(err, &ve) {
			body["fields"] = ve.Fields
		}
		httpx.WriteJSON(w, http.StatusBadRequest, body)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *API) getSigningKey(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.admin(w, r); !ok {
		return
	}
	info := a.settings.SigningKey()
	if info == nil {
		httpx.Error(w, http.StatusNotFound, "no signing key configured")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, info)
}

func (a *API) setSigningKey(w http.ResponseWriter, r *http.Request) {
	p, ok := a.admin(w, r)
	if !ok {
		return
	}
	var in struct {
		Armored string `json:"armored"`
	}
	// The body is optional: none (or {}) generates a new key.
	if r.ContentLength != 0 && !httpx.DecodeJSON(w, r, a.cfg().MaxJSONBytes, &in) {
		return
	}
	info, err := a.settings.SetSigningKey(r.Context(), in.Armored, p.UserID)
	if errors.Is(err, settings.ErrSigningKeyFromFile) {
		httpx.Error(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		a.settingsError(w, r, err, a.settings.List(r.Context()))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, info)
}
