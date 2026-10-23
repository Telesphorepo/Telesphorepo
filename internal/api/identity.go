package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
)

// The identity of the server: the name and the logo that the UI shows (the settings server_name
// and server_logo of the general group). Without a name the server is called defaultName.

const defaultName = "Telesphorepo"

// logo is the logo of a snapshot: its bytes with their media type and ETag. A zero logo has no
// bytes.
type logo struct {
	doc         format.Document
	contentType string
}

var logoView = settings.NewView(func(s *settings.Snapshot) logo {
	b := settings.ServerLogo.Get(s)
	if len(b) == 0 {
		return logo{}
	}
	ct, _, _, err := settings.RasterImage(b)
	if err != nil {
		return logo{} // the setting's Validate admits only raster images
	}
	return logo{doc: format.NewDocument(b), contentType: ct}
})

// identityJSON is GET /api/identity: the name, the URL of the logo (null without one) and
// whether either is set.
type identityJSON struct {
	Name       string  `json:"name"`
	Logo       *string `json:"logo"`
	Customized bool    `json:"customized"`
}

func (a *API) identityOf(s *settings.Snapshot) identityJSON {
	out := identityJSON{Name: settings.ServerName.Get(s)}
	if out.Name != "" {
		out.Customized = true
	} else {
		out.Name = defaultName
	}
	if l := logoView.Get(s); l.contentType != "" {
		// The ETag in the query gives each logo its own URL, so a browser never shows an old one.
		u := "/identity/logo?v=" + l.doc.ETag[1:17]
		out.Logo, out.Customized = &u, true
	}
	return out
}

// registerIdentity mounts the identity routes: the identity and the logo for everyone, the
// logo's change for a holder of settings.general.
func (a *API) registerIdentity(mux *route.Router) {
	mux.Handle("GET /api/identity", route.Public, a.identity)
	mux.Handle("GET /identity/logo", route.Public, a.logo)
	mux.Handle("PUT /api/identity/logo", route.UIPerm(perm.SettingsGeneral), a.putLogo)
	mux.Handle("DELETE /api/identity/logo", route.UIPerm(perm.SettingsGeneral), a.deleteLogo)
}

func (a *API) identity(w http.ResponseWriter, r *http.Request, _ *route.Call) {
	httpx.WriteJSON(w, http.StatusOK, a.identityOf(a.settings.Current()))
}

// logo answers the logo with its media type, its ETag (the SHA-256 of the bytes), nosniff and a
// sandbox CSP. A browser asks again each time (no-cache) and gets 304 while the logo is the same.
func (a *API) logo(w http.ResponseWriter, r *http.Request, _ *route.Call) {
	l := logoView.Get(a.settings.Current())
	if l.contentType == "" {
		httpx.Error(w, r, http.StatusNotFound, "this server has no logo")
		return
	}
	w.Header().Set("Cache-Control", "public, no-cache")
	format.WriteDocument(w, r, l.contentType, l.doc)
}

// putLogo replaces the logo with the request body: the bytes of a PNG, JPEG or WebP image of at
// most settings.MaxLogoBytes. The type comes from the bytes, never from a header or a file name.
// The settings service saves it like a PATCH of server_logo, with one settings.update event.
// Answers the identity.
func (a *API) putLogo(w http.ResponseWriter, r *http.Request, c *route.Call) {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, settings.MaxLogoBytes))
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		httpx.Error(w, r, http.StatusRequestEntityTooLarge, fmt.Sprintf("the logo must be at most %d KiB", settings.MaxLogoBytes>>10))
		return
	}
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "could not read the logo")
		return
	}
	if len(b) == 0 {
		httpx.Error(w, r, http.StatusBadRequest, "the body must hold the logo image")
		return
	}
	raw, err := json.Marshal(b) // standard base64, the JSON form of an image setting
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	a.saveLogo(w, r, c, raw)
}

// deleteLogo removes the logo. Answers the identity.
func (a *API) deleteLogo(w http.ResponseWriter, r *http.Request, c *route.Call) {
	a.saveLogo(w, r, c, json.RawMessage("null"))
}

func (a *API) saveLogo(w http.ResponseWriter, r *http.Request, c *route.Call, raw json.RawMessage) {
	if _, err := a.settings.Patch(r.Context(), map[string]json.RawMessage{settings.ServerLogo.Name(): raw}, c.P.UserID); err != nil {
		a.settingsError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, a.identityOf(a.settings.Current()))
}
