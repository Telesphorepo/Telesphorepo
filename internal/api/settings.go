package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"slices"

	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
)

// groupJSON is a settings group as GET /api/settings shows it.
type groupJSON struct {
	ID       string          `json:"id"`
	Label    string          `json:"label"`
	Module   string          `json:"module"`
	Perm     perm.ID         `json:"perm"`
	Page     string          `json:"page"`
	Settings []settings.Item `json:"settings"`
}

// settingsPerms returns the permissions of the settings groups: a holder of one of them reaches
// the settings API.
func (a *API) settingsPerms() []perm.ID {
	var out []perm.ID
	for _, g := range a.h.Settings.Groups() {
		if !slices.Contains(out, g.Perm) {
			out = append(out, g.Perm)
		}
	}
	return out
}

// settingsList returns the groups whose permission p holds, in module start order.
func (a *API) settingsList(p *auth.Principal) map[string][]groupJSON {
	out := []groupJSON{}
	for _, g := range a.settings.Groups() {
		if p.Perms.Has(g.Perm) {
			out = append(out, groupJSON{g.ID, g.Label, g.Module, g.Perm, g.Page, a.settings.Items(g)})
		}
	}
	return map[string][]groupJSON{"groups": out}
}

// authConfig tells the UI which sign-in methods exist: {oidc, password_login,
// second_factor_optional}. password_login is "all", or "publishers" when people who manage sign
// in with single sign-on only. second_factor_optional is true only under "telesphorepo dev".
func (a *API) authConfig(w http.ResponseWriter, r *http.Request, _ *route.Call) {
	httpx.WriteJSON(w, http.StatusOK, struct {
		OIDC                 bool   `json:"oidc"`
		PasswordLogin        string `json:"password_login"`
		SecondFactorOptional bool   `json:"second_factor_optional"`
	}{a.auth.SSOEnabled(), auth.PasswordLogin.Get(a.settings.Current()), a.auth.DevMode()})
}

// getSettings returns the settings groups whose permission the caller holds:
// {groups: [{id, label, module, perm, page, settings: [Item]}]}. Secret values stay hidden.
func (a *API) getSettings(w http.ResponseWriter, r *http.Request, c *route.Call) {
	httpx.WriteJSON(w, http.StatusOK, a.settingsList(c.P))
}

// decodePatch reads a {key: value} body. An empty string for a secret means "leave unchanged"
// and is dropped (the UI sends it for untouched password fields); null clears a setting.
func (a *API) decodePatch(w http.ResponseWriter, r *http.Request) (map[string]json.RawMessage, bool) {
	var patch map[string]json.RawMessage
	if !a.kit.Decode(w, r, &patch) {
		return nil, false
	}
	for k, raw := range patch {
		if def := a.settings.Def(k); def != nil && def.Secret && string(bytes.TrimSpace(raw)) == `""` {
			delete(patch, k)
		}
	}
	return patch, true
}

// settingsError writes a refusal of a settings change. A *settings.HookError answers its status
// with {error, fields?}. A *settings.ValidationError answers 409 when it concerns a setting that
// the environment sets, else 400, with {error, fields: {key: msg}}.
func (a *API) settingsError(w http.ResponseWriter, r *http.Request, err error) {
	var he *settings.HookError
	if errors.As(err, &he) {
		body := map[string]any{"error": he.Message}
		if he.Fields != nil {
			body["fields"] = he.Fields
		}
		httpx.WriteJSON(w, he.Status, body)
		return
	}
	var ve *settings.ValidationError
	if !errors.As(err, &ve) {
		route.Fail(w, r, err)
		return
	}
	status := http.StatusBadRequest
	snap := a.settings.Current()
	for k := range ve.Fields {
		if snap.Source(k) == settings.SourceEnv {
			status = http.StatusConflict
		}
	}
	httpx.WriteJSON(w, status, map[string]any{"error": ve.Error(), "fields": ve.Fields})
}

// patchSettings saves a settings patch. Each key needs the permission of its group (403). The
// settings service runs the Validate of every touched group, then their Apply hooks (an Apply
// may hold keys back and answer itself, e.g. 202 {move} while a storage move copies the blobs),
// then saves. The service records one settings.update event that names the saved keys.
func (a *API) patchSettings(w http.ResponseWriter, r *http.Request, c *route.Call) {
	p := c.P
	patch, ok := a.decodePatch(w, r)
	if !ok {
		return
	}
	for k := range patch {
		def := a.settings.Def(k)
		if def == nil {
			continue // the service answers 400 for an unknown key
		}
		if g := def.Group(); !p.Perms.Has(g.Perm) {
			httpx.Error(w, http.StatusForbidden, "permission required for "+k+": "+perm.Current().Label(g.Perm))
			return
		}
	}
	applied, err := a.settings.Patch(r.Context(), patch, p.UserID)
	if err != nil {
		a.settingsError(w, r, err)
		return
	}
	if applied != nil {
		httpx.WriteJSON(w, applied.Status, applied.Body)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, a.settingsList(p))
}

// testSettings checks the values of group {group}: the body is merged over the current values
// and not saved. The group's Validate runs, then its Test (e.g. the probe of a blob store, the
// discovery check of an identity provider). {ok: true}, or 400 {ok: false, error, fields?}.
func (a *API) testSettings(w http.ResponseWriter, r *http.Request, c *route.Call) {
	g := a.settings.Group(r.PathValue("group"))
	if g == nil {
		httpx.Error(w, http.StatusNotFound, "no such settings group")
		return
	}
	if !c.P.Perms.Has(g.Perm) {
		httpx.Error(w, http.StatusForbidden, "permission required: "+perm.Current().Label(g.Perm))
		return
	}
	patch, ok := a.decodePatch(w, r)
	if !ok {
		return
	}
	if err := a.settings.Test(r.Context(), g, patch); err != nil {
		body := map[string]any{"ok": false, "error": err.Error()}
		var ve *settings.ValidationError
		var he *settings.HookError
		switch {
		case errors.As(err, &ve):
			body["fields"] = ve.Fields
		case errors.As(err, &he) && he.Fields != nil:
			body["fields"] = he.Fields
		}
		httpx.WriteJSON(w, http.StatusBadRequest, body)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
