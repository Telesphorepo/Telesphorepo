package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"

	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
)

// backendKeys are the settings that select or address the blob store; a PATCH that changes one
// of them must pass the storage probe before it is saved (spec §3).
var backendKeys = map[string]bool{
	settings.KeyStorage:           true,
	settings.KeyFSRoot:            true,
	settings.KeyS3Endpoint:        true,
	settings.KeyS3Region:          true,
	settings.KeyS3Bucket:          true,
	settings.KeyS3AccessKeyID:     true,
	settings.KeyS3SecretAccessKey: true,
	settings.KeyS3Prefix:          true,
	settings.KeyS3PathStyle:       true,
}

type settingsGroup struct {
	Name     string          `json:"name"`
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
			out.Groups = append(out.Groups, settingsGroup{g, s})
		}
	}
	return out
}

// authConfig tells the UI which sign-in methods exist (M2 adds OIDC).
func (a *API) authConfig(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]bool{"password_login": true})
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

// changesBackend reports whether patch would change a storage backend setting's effective value.
func changesBackend(items []settings.Item, patch map[string]json.RawMessage) bool {
	for _, it := range items {
		raw, ok := patch[it.Key]
		if !ok || !backendKeys[it.Key] || it.Locked {
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
	if changesBackend(items, patch) {
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
	}
	if err := a.settings.Update(r.Context(), patch, p.UserID); err != nil {
		a.settingsError(w, r, err, items)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, a.settingsList(r))
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
