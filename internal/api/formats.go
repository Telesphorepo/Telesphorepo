package api

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/ci"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
)

// formatJSON is the UI descriptor of a format (GET /api/formats): everything the web UI shows or
// chooses per format, so no shared UI code names a format.
type formatJSON struct {
	Name          string                `json:"name"`
	Title         string                `json:"title"`
	Description   string                `json:"description"`
	Icon          string                `json:"icon,omitempty"`       // inline SVG
	KindIcons     map[string]string     `json:"kind_icons,omitempty"` // Artifact.Kind -> inline SVG
	Kinds         []string              `json:"kinds"`                // repo kinds the format takes: hosted, proxy, group
	Config        configJSON            `json:"config"`
	RepoFields    map[string][]string   `json:"repo_fields"` // repo kind -> module fields, e.g. hosted: [signing_key]
	RepoTabs      []string              `json:"repo_tabs"`   // tabs of other modules in its repo settings, e.g. signing
	Upload        *uploadJSON           `json:"upload"`      // null: no uploads through the web UI
	DeleteNotes   []format.DeleteNote   `json:"delete_notes"`
	FileDetails   []format.FileDetail   `json:"file_details"`
	Browse        browseJSON            `json:"browse"`
	RemotePresets []format.RemotePreset `json:"remote_presets"`
	ProxyNote     string                `json:"proxy_note,omitempty"`
	PublicKey     string                `json:"public_key,omitempty"`
	Labels        labelsJSON            `json:"labels"`
	ImportSources []string              `json:"import_sources"`
	Tools         []toolJSON            `json:"tools"`
	// Publish is the publish category: "cli" (telesphorepo publish in the project folder) or
	// "helper" (the native command of the tool, through a credential helper of the CLI).
	Publish string `json:"publish"`
}

type configJSON struct {
	Default json.RawMessage `json:"default"`
	Fields  []format.Field  `json:"fields"`
}

type uploadJSON struct {
	Single  bool              `json:"single"`
	Folder  bool              `json:"folder"`
	Accept  []string          `json:"accept"`
	Hint    string            `json:"hint"`
	Keep    []string          `json:"keep"`
	One     string            `json:"one"`
	Many    string            `json:"many"`
	Note    string            `json:"note,omitempty"`
	Ignored string            `json:"ignored,omitempty"`
	Empty   string            `json:"empty,omitempty"`
	Fields  []format.Field    `json:"fields"`
	Reads   []format.ReadRule `json:"reads"`
}

type browseJSON struct {
	Versions         bool `json:"versions"`
	ArtifactSnippets bool `json:"artifact_snippets"`
}

type labelsJSON struct {
	ArtifactKinds  map[string]string       `json:"artifact_kinds"`
	ArtifactName   map[string]string       `json:"artifact_name"`
	Tools          map[string]string       `json:"tools"`
	InstallHeading string                  `json:"install_heading,omitempty"`
	BuildFiles     []format.BuildFileLabel `json:"build_files"`
}

// toolJSON is a build tool of a format that the CI files name.
type toolJSON struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// toolLabel is the label of a snippet tool.
type toolLabel struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// listFormats lists the UI descriptors of the formats in module order.
func (a *API) listFormats(w http.ResponseWriter, r *http.Request, c *route.Call) {
	out := []formatJSON{}
	for _, f := range a.formats.All() {
		out = append(out, a.formatOf(f))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (a *API) formatOf(f *format.Spec) formatJSON {
	nonNil := func(xs []string) []string {
		if xs == nil {
			return []string{}
		}
		return xs
	}
	fj := formatJSON{Name: f.Name, Title: f.Title, Description: f.Description, Kinds: f.Kinds, Icon: string(f.Icon),
		Config:      configJSON{Default: f.Config.Default, Fields: f.Config.Fields},
		RepoFields:  map[string][]string{},
		RepoTabs:    nonNil(f.RepoTabs),
		DeleteNotes: f.UI.DeleteNotes, FileDetails: f.UI.FileDetails, RemotePresets: f.UI.RemotePresets,
		ProxyNote: f.UI.ProxyNote, PublicKey: f.UI.PublicKey,
		Browse: browseJSON{Versions: f.Versions != nil, ArtifactSnippets: f.ArtifactUsage != nil},
		Labels: labelsJSON{ArtifactKinds: f.UI.ArtifactKinds, ArtifactName: f.UI.ArtifactName, Tools: a.toolLabels(f),
			InstallHeading: f.UI.InstallHeading, BuildFiles: f.UI.BuildFiles},
		ImportSources: []string{}, Tools: []toolJSON{}, Publish: f.PublishCategory(),
	}
	if len(fj.Config.Default) == 0 {
		fj.Config.Default = json.RawMessage("{}")
	}
	if fj.Config.Fields == nil {
		fj.Config.Fields = []format.Field{}
	}
	for _, k := range f.Kinds {
		fj.RepoFields[k] = nonNil(a.repos.FieldNames(f.Name, k))
	}
	if fj.DeleteNotes == nil {
		fj.DeleteNotes = []format.DeleteNote{}
	}
	if fj.FileDetails == nil {
		fj.FileDetails = []format.FileDetail{}
	}
	if fj.RemotePresets == nil {
		fj.RemotePresets = []format.RemotePreset{}
	}
	if fj.Labels.ArtifactKinds == nil {
		fj.Labels.ArtifactKinds = map[string]string{}
	}
	if fj.Labels.ArtifactName == nil {
		fj.Labels.ArtifactName = map[string]string{}
	}
	if fj.Labels.BuildFiles == nil {
		fj.Labels.BuildFiles = []format.BuildFileLabel{}
	}
	for k, svg := range f.KindIcons {
		if fj.KindIcons == nil {
			fj.KindIcons = map[string]string{}
		}
		fj.KindIcons[k] = string(svg)
	}
	if u := f.Upload; u != nil {
		fj.Upload = &uploadJSON{Single: u.Single, Folder: u.Folder, Accept: nonNil(u.Accept), Hint: u.Hint, Keep: nonNil(u.Keep),
			One: u.One, Many: u.Many, Note: u.Note, Ignored: u.Ignored, Empty: u.Empty, Fields: u.Fields, Reads: u.Reads}
		if fj.Upload.Fields == nil {
			fj.Upload.Fields = []format.Field{}
		}
		if fj.Upload.Reads == nil {
			fj.Upload.Reads = []format.ReadRule{}
		}
	}
	if f.Importer != nil {
		fj.ImportSources = f.Importer.Sources
	}
	for _, t := range f.Tools {
		fj.Tools = append(fj.Tools, toolJSON{ID: t.ID, Label: t.Label})
	}
	return fj
}

// toolLabels are the words for every snippet tool of format f: its own, then the CI systems.
func (a *API) toolLabels(f *format.Spec) map[string]string {
	out := map[string]string{}
	for k, v := range f.UI.SnippetTools {
		out[k] = v
	}
	for _, sys := range ci.Systems {
		if _, ok := out[sys]; !ok {
			out[sys] = a.h.CI.Title(sys)
		}
	}
	return out
}

// toolsOf returns the label of each tool of snippets, in the order the tools first appear.
func (a *API) toolsOf(f *format.Spec, snippets []format.Snippet) []toolLabel {
	labels := map[string]string{}
	if f != nil {
		labels = a.toolLabels(f)
	}
	out := []toolLabel{}
	for _, s := range snippets {
		if slices.ContainsFunc(out, func(t toolLabel) bool { return t.ID == s.Tool }) {
			continue
		}
		l := labels[s.Tool]
		if l == "" {
			l = s.Tool
		}
		out = append(out, toolLabel{s.Tool, l})
	}
	return out
}

// repoStatus answers {pending: n}: the units of the repo that wait for a rebuild of its indexes
// (internal/rebuild), the ones being rebuilt included.
func (a *API) repoStatus(w http.ResponseWriter, r *http.Request, c *route.Call) {
	n, err := a.h.Rebuilds.Pending(r.Context(), c.Repo.ID)
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]int{"pending": n})
}

// repoUsage returns the snippets of the repo: {base_url, tools: [{id, label}], snippets}. The
// format gives the use snippets and the publish snippets for a computer and by hand; the core
// adds the CI files of each of the format's tools (default trigger). base_url is the base_url
// setting, else the URL the request came in on. A proxy or a group takes no uploads, so it gets
// no publish snippets.
func (a *API) repoUsage(w http.ResponseWriter, r *http.Request, c *route.Call) {
	repo := c.Repo
	base := a.kit.BaseURL(r)
	snippets := []format.Snippet{}
	f := a.formats.Get(repo.Format)
	if f != nil && f.Usage != nil {
		for _, s := range f.Usage(repo, base) {
			if repo.Kind == repos.KindHosted || s.Purpose != format.SnippetPublish {
				snippets = append(snippets, s)
			}
		}
	}
	if f != nil && repo.Kind == repos.KindHosted {
		for _, sys := range ci.Systems {
			for _, t := range f.Tools {
				snippets = append(snippets, format.CISnippet(repo, base, t, sys, ci.Trigger{}))
			}
		}
	}
	httpx.WriteJSON(w, http.StatusOK, struct {
		BaseURL  string           `json:"base_url"`
		Tools    []toolLabel      `json:"tools"`
		Snippets []format.Snippet `json:"snippets"`
	}{base, a.toolsOf(f, snippets), snippets})
}

// maxPatterns is the most branches and the most tags of a CI file.
const maxPatterns = 20

// repoCI returns the CI file that publishes to a hosted repo:
// ?system=github-actions|jenkins|jenkins-freestyle&tool=<id>&branches=a,b&tags=v* -> Snippet.
// tool is a tool of the format (default: the first); branches and tags are comma-separated names
// or globs (none: a push to main on GitHub Actions, every run of a Jenkins job). A Jenkins
// freestyle job gets the shell build step and takes no branches or tags. The publish session of
// the repo reads it too, so "telesphorepo setup" writes the CI file.
func (a *API) repoCI(w http.ResponseWriter, r *http.Request, c *route.Call) {
	repo := c.Repo
	q := r.URL.Query()
	f := a.formats.Get(repo.Format)
	if f == nil || repo.Kind != repos.KindHosted || len(f.Tools) == 0 {
		httpx.Error(w, r, http.StatusBadRequest, "this repository takes no publishes from CI")
		return
	}
	system := q.Get("system")
	if !slices.Contains(ci.Systems, system) && system != ci.JenkinsFreestyleSystem {
		httpx.Error(w, r, http.StatusBadRequest, "system must be one of "+strings.Join(ci.Systems, ", ")+", "+ci.JenkinsFreestyleSystem)
		return
	}
	tool := f.Tools[0]
	if id := q.Get("tool"); id != "" {
		i := slices.IndexFunc(f.Tools, func(t format.Tool) bool { return t.ID == id })
		if i < 0 {
			ids := make([]string, len(f.Tools))
			for i, t := range f.Tools {
				ids[i] = t.ID
			}
			httpx.Error(w, r, http.StatusBadRequest, "tool must be one of "+strings.Join(ids, ", "))
			return
		}
		tool = f.Tools[i]
	}
	var t ci.Trigger
	for _, p := range []struct {
		key string
		out *[]string
	}{{"branches", &t.Branches}, {"tags", &t.Tags}} {
		for _, v := range strings.Split(q.Get(p.key), ",") {
			if v = strings.TrimSpace(v); v == "" {
				continue
			}
			if !ci.ValidPattern(v) {
				httpx.Error(w, r, http.StatusBadRequest, "invalid "+p.key+" pattern "+strconv.Quote(v)+"; use letters, digits and . _ / * -")
				return
			}
			if !slices.Contains(*p.out, v) {
				*p.out = append(*p.out, v)
			}
		}
		if len(*p.out) > maxPatterns {
			httpx.Error(w, r, http.StatusBadRequest, "name at most 20 "+p.key)
			return
		}
	}
	httpx.WriteJSON(w, http.StatusOK, format.CISnippet(repo, a.kit.BaseURL(r), tool, system, t))
}

// artifactSnippets returns the snippets that use one artifact (format.Spec ArtifactUsage):
// ?path=<artifact folder, version folder or file>&version=<v>&version_mode=pinned|latest ->
// {base_url, artifact, version, version_mode, tools, snippets}. version names a version of the
// artifact folder path (optional). artifact is null, and snippets empty, when the path is not an
// artifact or the format has no artifact snippets.
func (a *API) artifactSnippets(w http.ResponseWriter, r *http.Request, c *route.Call) {
	repo := c.Repo
	q := r.URL.Query()
	mode := q.Get("version_mode")
	switch mode {
	case "":
		mode = format.VersionPinned
	case format.VersionPinned, format.VersionLatest:
	default:
		httpx.Error(w, r, http.StatusBadRequest, `version_mode must be "pinned" or "latest"`)
		return
	}
	p := strings.Trim(q.Get("path"), "/")
	if _, err := repopath.Parse(p); p != "" && err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid path")
		return
	}
	base := a.kit.BaseURL(r)
	type out struct {
		BaseURL  string           `json:"base_url"`
		Artifact *format.Artifact `json:"artifact"`
		Version  string           `json:"version"`
		Mode     string           `json:"version_mode"`
		Tools    []toolLabel      `json:"tools"`
		Snippets []format.Snippet `json:"snippets"`
	}
	res := out{BaseURL: base, Mode: mode, Snippets: []format.Snippet{}}
	f := a.formats.Get(repo.Format)
	if f != nil && f.ArtifactUsage != nil && p != "" {
		u, err := f.ArtifactUsage(r.Context(), repo, base, p, q.Get("version"), mode)
		if err != nil {
			route.Fail(w, r, err)
			return
		}
		if u != nil {
			res.Artifact, res.Version, res.Mode = &u.Artifact, u.Version, u.Mode
			if u.Snippets != nil {
				res.Snippets = u.Snippets
			}
		}
	}
	res.Tools = a.toolsOf(f, res.Snippets)
	httpx.WriteJSON(w, http.StatusOK, res)
}

// versionedPath reads ?path= (an artifact folder) for the versions and builds routes, and the
// repo's format (whose Versions and Builds may be nil).
func (a *API) versionedPath(w http.ResponseWriter, r *http.Request, c *route.Call) (*repos.Repo, *format.Spec, string, bool) {
	repo := c.Repo
	p := strings.Trim(r.URL.Query().Get("path"), "/")
	if _, err := repopath.Parse(p); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "give the path of a folder")
		return nil, nil, "", false
	}
	return repo, a.formats.Get(repo.Format), p + "/", true
}

// versions lists the versions of an artifact folder, newest first (format.Spec Versions and Builds):
// ?path=<artifact folder>&after=<version>&limit= -> {artifact, items: [Version], next}. artifact
// is null when the folder is not an artifact or the format does not group versions.
func (a *API) versions(w http.ResponseWriter, r *http.Request, c *route.Call) {
	repo, v, p, ok := a.versionedPath(w, r, c)
	if !ok {
		return
	}
	type out struct {
		Artifact *format.Artifact `json:"artifact"`
		Items    []format.Version `json:"items"`
		Next     *string          `json:"next"`
	}
	res := out{Items: []format.Version{}}
	if v != nil && v.Versions != nil {
		art, items, next, err := v.Versions(r.Context(), repo, p, r.URL.Query().Get("after"), httpx.Limit(r))
		if err != nil {
			route.Fail(w, r, err)
			return
		}
		if art != nil {
			res.Artifact, res.Next = art, route.Cursor(next)
			if items != nil {
				res.Items = items
			}
		}
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

// builds lists the builds of one version of an artifact, newest first (format.Spec Versions and Builds):
// ?path=<artifact folder>&version=<v>&after=<build>&limit= -> {artifact, version, items: [Build],
// next}. artifact and version are null when the artifact has no such version.
func (a *API) builds(w http.ResponseWriter, r *http.Request, c *route.Call) {
	repo, v, p, ok := a.versionedPath(w, r, c)
	if !ok {
		return
	}
	q := r.URL.Query()
	if q.Get("version") == "" {
		httpx.Error(w, r, http.StatusBadRequest, "give the version")
		return
	}
	type out struct {
		Artifact *format.Artifact `json:"artifact"`
		Version  *format.Version  `json:"version"`
		Items    []format.Build   `json:"items"`
		Next     *string          `json:"next"`
	}
	res := out{Items: []format.Build{}}
	if v != nil && v.Builds != nil {
		art, ver, items, next, err := v.Builds(r.Context(), repo, p, q.Get("version"), q.Get("after"), httpx.Limit(r))
		if err != nil {
			route.Fail(w, r, err)
			return
		}
		if art != nil && ver != nil {
			res.Artifact, res.Version, res.Next = art, ver, route.Cursor(next)
			if items != nil {
				res.Items = items
			}
		}
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}
