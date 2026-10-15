package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/files"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

type formatJSON struct {
	Name          string            `json:"name"`
	Title         string            `json:"title"`
	DefaultConfig json.RawMessage   `json:"default_config"`
	SettingsGroup string            `json:"settings_group,omitempty"`
	Icon          string            `json:"icon,omitempty"`       // inline SVG (format.Iconic)
	KindIcons     map[string]string `json:"kind_icons,omitempty"` // Artifact.Kind -> inline SVG
	Kinds         []string          `json:"kinds"`                // repo kinds the format takes: hosted, proxy, group
}

// listFormats lists the registered formats in registration order. SettingsGroup names the
// format's group in GET /api/settings when it contributes settings. Icon and KindIcons are the
// format's SVG icons, inline. Kinds are the repo kinds the format takes.
func (a *API) listFormats(w http.ResponseWriter, r *http.Request) {
	out := []formatJSON{}
	for _, f := range format.All() {
		fj := formatJSON{Name: f.Name(), Title: f.Title(), DefaultConfig: f.DefaultConfig(), Kinds: format.Kinds(f)}
		if len(fj.DefaultConfig) == 0 {
			fj.DefaultConfig = json.RawMessage("{}")
		}
		if c, ok := f.(format.Configurable); ok && len(c.Settings()) > 0 {
			fj.SettingsGroup = f.Name()
		}
		if ic, ok := f.(format.Iconic); ok {
			fj.Icon = string(ic.Icon())
			for k, svg := range ic.KindIcons() {
				if fj.KindIcons == nil {
					fj.KindIcons = map[string]string{}
				}
				fj.KindIcons[k] = string(svg)
			}
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
// base_url is the base_url setting, else the URL the request came in on. A proxy or a group takes
// no uploads, so it gets no publish snippets.
func (a *API) repoUsage(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := a.auth.RepoGate(w, r, r.PathValue("name"), "", auth.Read, true)
	if !ok {
		return
	}
	base := a.baseURL(r)
	snippets := []format.Snippet{}
	if d, ok := format.Get(repo.Format).(format.Describer); ok {
		for _, s := range d.Usage(repo, base) {
			if repo.Kind == repos.KindHosted || s.Purpose != format.SnippetPublish {
				snippets = append(snippets, s)
			}
		}
	}
	httpx.WriteJSON(w, http.StatusOK, struct {
		BaseURL  string           `json:"base_url"`
		Snippets []format.Snippet `json:"snippets"`
	}{base, snippets})
}

// artifactSnippets returns the snippets that use one artifact (format.ArtifactDescriber):
// ?path=<artifact or version folder>&version_mode=pinned|latest -> {base_url, artifact, version,
// version_mode, snippets}. artifact is null, and snippets empty, when the path is not an
// artifact or the format has no artifact snippets.
func (a *API) artifactSnippets(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := a.auth.RepoGate(w, r, r.PathValue("name"), "", auth.Read, true)
	if !ok {
		return
	}
	q := r.URL.Query()
	mode := q.Get("version_mode")
	switch mode {
	case "":
		mode = format.VersionPinned
	case format.VersionPinned, format.VersionLatest:
	default:
		httpx.Error(w, http.StatusBadRequest, `version_mode must be "pinned" or "latest"`)
		return
	}
	p := strings.Trim(q.Get("path"), "/")
	if p != "" && !files.ValidPath(p) {
		httpx.Error(w, http.StatusBadRequest, "invalid path")
		return
	}
	base := a.baseURL(r)
	type out struct {
		BaseURL  string           `json:"base_url"`
		Artifact *format.Artifact `json:"artifact"`
		Version  string           `json:"version"`
		Mode     string           `json:"version_mode"`
		Snippets []format.Snippet `json:"snippets"`
	}
	res := out{BaseURL: base, Mode: mode, Snippets: []format.Snippet{}}
	if d, ok := format.Get(repo.Format).(format.ArtifactDescriber); ok && p != "" {
		u, err := d.ArtifactUsage(r.Context(), repo, base, p, mode)
		if err != nil {
			fail(w, r, err)
			return
		}
		if u != nil {
			res.Artifact, res.Version, res.Mode = &u.Artifact, u.Version, u.Mode
			if u.Snippets != nil {
				res.Snippets = u.Snippets
			}
		}
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

// versionedPath reads ?path= for the versions and builds routes and the format's Versioned (nil
// when the format has none).
func (a *API) versionedPath(w http.ResponseWriter, r *http.Request) (*repos.Repo, format.Versioned, string, bool) {
	repo, _, ok := a.auth.RepoGate(w, r, r.PathValue("name"), "", auth.Read, true)
	if !ok {
		return nil, nil, "", false
	}
	p := strings.Trim(r.URL.Query().Get("path"), "/")
	if p == "" || !files.ValidPath(p) {
		httpx.Error(w, http.StatusBadRequest, "give the path of a folder")
		return nil, nil, "", false
	}
	v, _ := format.Get(repo.Format).(format.Versioned)
	return repo, v, p + "/", true
}

// versions lists the versions of an artifact folder, newest first (format.Versioned):
// ?path=<artifact folder>&after=<version>&limit= -> {artifact, items: [Version], next}. artifact
// is null when the folder is not an artifact or the format does not group versions.
func (a *API) versions(w http.ResponseWriter, r *http.Request) {
	repo, v, p, ok := a.versionedPath(w, r)
	if !ok {
		return
	}
	type out struct {
		Artifact *format.Artifact `json:"artifact"`
		Items    []format.Version `json:"items"`
		Next     *string          `json:"next"`
	}
	res := out{Items: []format.Version{}}
	if v != nil {
		art, items, next, err := v.Versions(r.Context(), repo, p, r.URL.Query().Get("after"), httpx.Limit(r))
		if err != nil {
			fail(w, r, err)
			return
		}
		if art != nil {
			res.Artifact, res.Next = art, cursor(next)
			if items != nil {
				res.Items = items
			}
		}
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

// builds lists the builds of a version folder, newest first (format.Versioned):
// ?path=<version folder>&after=<build>&limit= -> {artifact, version, items: [Build], next}.
// artifact and version are null when the folder is not a version.
func (a *API) builds(w http.ResponseWriter, r *http.Request) {
	repo, v, p, ok := a.versionedPath(w, r)
	if !ok {
		return
	}
	type out struct {
		Artifact *format.Artifact `json:"artifact"`
		Version  *format.Version  `json:"version"`
		Items    []format.Build   `json:"items"`
		Next     *string          `json:"next"`
	}
	res := out{Items: []format.Build{}}
	if v != nil {
		art, ver, items, next, err := v.Builds(r.Context(), repo, p, r.URL.Query().Get("after"), httpx.Limit(r))
		if err != nil {
			fail(w, r, err)
			return
		}
		if art != nil && ver != nil {
			res.Artifact, res.Version, res.Next = art, ver, cursor(next)
			if items != nil {
				res.Items = items
			}
		}
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}
