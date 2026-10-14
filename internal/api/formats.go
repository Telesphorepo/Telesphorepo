package api

import (
	"encoding/json"
	"net/http"

	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
)

type formatJSON struct {
	Name          string          `json:"name"`
	Title         string          `json:"title"`
	DefaultConfig json.RawMessage `json:"default_config"`
	SettingsGroup string          `json:"settings_group,omitempty"`
}

// listFormats lists the registered formats in registration order. SettingsGroup names the
// format's group in GET /api/settings when it contributes settings.
func (a *API) listFormats(w http.ResponseWriter, r *http.Request) {
	out := []formatJSON{}
	for _, f := range format.All() {
		fj := formatJSON{Name: f.Name(), Title: f.Title(), DefaultConfig: f.DefaultConfig()}
		if len(fj.DefaultConfig) == 0 {
			fj.DefaultConfig = json.RawMessage("{}")
		}
		if c, ok := f.(format.Configurable); ok && len(c.Settings()) > 0 {
			fj.SettingsGroup = f.Name()
		}
		out = append(out, fj)
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// repoStatus returns the format's StatusReporter result for the repo, or {}.
func (a *API) repoStatus(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := a.auth.RepoGate(w, r, r.PathValue("name"), "", auth.Read, true)
	if !ok {
		return
	}
	s, ok := format.Get(repo.Format).(format.StatusReporter)
	if !ok {
		httpx.WriteJSON(w, http.StatusOK, struct{}{})
		return
	}
	st, err := s.Status(r.Context(), repo)
	if err != nil {
		fail(w, r, err)
		return
	}
	if st == nil {
		st = struct{}{}
	}
	httpx.WriteJSON(w, http.StatusOK, st)
}

// repoUsage returns the client setup snippets of the repo's format: {base_url, snippets}.
// base_url is the base_url setting, else the URL the request came in on.
func (a *API) repoUsage(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := a.auth.RepoGate(w, r, r.PathValue("name"), "", auth.Read, true)
	if !ok {
		return
	}
	base := a.baseURL(r)
	snippets := []format.Snippet{}
	if d, ok := format.Get(repo.Format).(format.Describer); ok {
		if s := d.Usage(repo, base); s != nil {
			snippets = s
		}
	}
	httpx.WriteJSON(w, http.StatusOK, struct {
		BaseURL  string           `json:"base_url"`
		Snippets []format.Snippet `json:"snippets"`
	}{base, snippets})
}
