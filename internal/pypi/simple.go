package pypi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"html"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/pypi/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
)

// The simple repository API. Both forms of a page render from one model; every URL in a page is
// relative to the page, so a group serves the page of its owning member unchanged and a client
// follows each file to the repo it asked.

// A form of a page (PEP 691 content negotiation).
type form int

const (
	formJSON form = iota
	formHTML
	formTextHTML
)

func (f form) contentType() string {
	switch f {
	case formJSON:
		return ctJSON
	case formHTML:
		return ctHTML
	}
	return ctTextHTML
}

// negotiate picks the form of the answer from the Accept header: the acceptable form with the
// highest q, the server's order (JSON, HTML, text/html) breaking a tie. No Accept header is
// text/html. ok is false when the client accepts none of them (406).
func negotiate(r *http.Request) (form, bool) {
	accept := r.Header.Values("Accept")
	if strings.TrimSpace(strings.Join(accept, "")) == "" {
		return formTextHTML, true
	}
	var q [3]float64
	for _, h := range accept {
		for _, part := range strings.Split(h, ",") {
			mt, params, err := mime.ParseMediaType(strings.TrimSpace(part))
			if err != nil {
				continue
			}
			weight := 1.0
			if s, ok := params["q"]; ok {
				if weight, err = strconv.ParseFloat(s, 64); err != nil {
					continue
				}
			}
			var forms []form
			switch mt {
			case ctJSON, "application/vnd.pypi.simple.latest+json":
				forms = []form{formJSON}
			case ctHTML, "application/vnd.pypi.simple.latest+html":
				forms = []form{formHTML}
			case "text/html":
				forms = []form{formTextHTML}
			case "*/*":
				forms = []form{formJSON, formHTML, formTextHTML}
			}
			for _, f := range forms {
				q[f] = max(q[f], weight)
			}
		}
	}
	best := -1
	for f := range q {
		if q[f] > 0 && (best < 0 || q[f] > q[best]) {
			best = f
		}
	}
	return form(best), best >= 0
}

// meta is the "meta" of a JSON page.
type meta struct {
	APIVersion string `json:"api-version"`
}

// model is a project page: the files and the versions of one project.
type model struct {
	name     string
	files    []mfile
	versions []string
}

// mfile is one file of a page.
type mfile struct {
	filename       string
	href           string            // relative to the page, escaped, without a fragment
	hashes         map[string]string // e.g. "sha256" -> hex
	size           int64             // -1: unknown (an HTML page of the remote)
	requiresPython string
	uploadTime     time.Time // zero: unknown
	metadata       bool      // a .metadata file exists
	metaSHA        string    // its SHA-256; "" when unknown
	yanked         any       // nil (false), true or the reason
}

// apiVersion is 1.1 when every file has its size (PEP 700), else 1.0.
func (m *model) apiVersion() string {
	for _, f := range m.files {
		if f.size < 0 {
			return "1.0"
		}
	}
	return "1.1"
}

// render renders m in form f.
func (m *model) render(f form) []byte {
	if f == formJSON {
		type jsonFile struct {
			Filename         string            `json:"filename"`
			URL              string            `json:"url"`
			Hashes           map[string]string `json:"hashes"`
			RequiresPython   string            `json:"requires-python,omitempty"`
			Yanked           any               `json:"yanked"`
			Size             *int64            `json:"size,omitempty"`
			UploadTime       string            `json:"upload-time,omitempty"`
			CoreMetadata     any               `json:"core-metadata,omitempty"`
			DistInfoMetadata any               `json:"dist-info-metadata,omitempty"`
		}
		files := make([]jsonFile, len(m.files))
		for i, x := range m.files {
			j := jsonFile{Filename: x.filename, URL: x.href, Hashes: x.hashes, RequiresPython: x.requiresPython, Yanked: x.yanked}
			if j.Hashes == nil {
				j.Hashes = map[string]string{}
			}
			if j.Yanked == nil {
				j.Yanked = false
			}
			if x.size >= 0 {
				j.Size = &x.size
			}
			if !x.uploadTime.IsZero() {
				j.UploadTime = x.uploadTime.UTC().Format("2006-01-02T15:04:05.000000Z")
			}
			if x.metadata {
				var cm any = true
				if x.metaSHA != "" {
					cm = map[string]string{"sha256": x.metaSHA}
				}
				j.CoreMetadata, j.DistInfoMetadata = cm, cm
			}
			files[i] = j
		}
		versions := m.versions
		if versions == nil {
			versions = []string{}
		}
		b, _ := json.Marshal(struct {
			Meta     meta       `json:"meta"`
			Name     string     `json:"name"`
			Versions []string   `json:"versions"`
			Files    []jsonFile `json:"files"`
		}{meta{m.apiVersion()}, m.name, versions, files})
		return b
	}
	var b strings.Builder
	title := html.EscapeString(m.name)
	b.WriteString("<!DOCTYPE html>\n<html>\n  <head>\n    <meta name=\"pypi:repository-version\" content=\"" + m.apiVersion() + "\">\n")
	b.WriteString("    <title>Links for " + title + "</title>\n  </head>\n  <body>\n    <h1>Links for " + title + "</h1>\n")
	for _, x := range m.files {
		href := x.href
		if s := x.hashes["sha256"]; s != "" {
			href += "#sha256=" + s
		}
		b.WriteString(`    <a href="` + html.EscapeString(href) + `"`)
		if x.requiresPython != "" {
			b.WriteString(` data-requires-python="` + html.EscapeString(x.requiresPython) + `"`)
		}
		if x.metadata {
			v := "true"
			if x.metaSHA != "" {
				v = "sha256=" + x.metaSHA
			}
			b.WriteString(` data-dist-info-metadata="` + v + `" data-core-metadata="` + v + `"`)
		}
		switch y := x.yanked.(type) {
		case bool:
			if y {
				b.WriteString(` data-yanked=""`)
			}
		case string:
			b.WriteString(` data-yanked="` + html.EscapeString(y) + `"`)
		}
		b.WriteString(">" + html.EscapeString(x.filename) + "</a><br/>\n")
	}
	b.WriteString("  </body>\n</html>\n")
	return []byte(b.String())
}

// project is one entry of a project list.
type project struct{ name, display string }

// renderIndex renders a project list in form f.
func renderIndex(list []project, f form) []byte {
	if f == formJSON {
		type entry struct {
			Name string `json:"name"`
		}
		out := make([]entry, len(list))
		for i, p := range list {
			out[i] = entry{p.display}
		}
		b, _ := json.Marshal(struct {
			Meta     meta    `json:"meta"`
			Projects []entry `json:"projects"`
		}{meta{"1.1"}, out})
		return b
	}
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html>\n  <head>\n    <meta name=\"pypi:repository-version\" content=\"1.1\">\n    <title>Simple index</title>\n  </head>\n  <body>\n")
	for _, p := range list {
		b.WriteString(`    <a href="` + html.EscapeString(url.PathEscape(p.name)) + `/">` + html.EscapeString(p.display) + "</a><br/>\n")
	}
	b.WriteString("  </body>\n</html>\n")
	return []byte(b.String())
}

// toIndex sends /simple to /simple/.
func (h *Handler) toIndex(w http.ResponseWriter, r *http.Request, c *route.Call) {
	http.Redirect(w, r, "/pypi/"+url.PathEscape(c.Repo.Name)+"/simple/", http.StatusMovedPermanently)
}

// index answers the project list: the projects of a hosted repo, the projects a proxy has
// cached, the union of the members of a group.
func (h *Handler) index(w http.ResponseWriter, r *http.Request, c *route.Call) {
	f, ok := negotiate(r)
	if !ok {
		notAcceptable(w, r)
		return
	}
	list, err := h.projects(r.Context(), r, c, c.Repo)
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	format.WriteDocument(w, r, f.contentType(), renderIndex(list, f), "Accept")
}

// projects returns the project list of repo, sorted by name.
func (h *Handler) projects(ctx context.Context, r *http.Request, c *route.Call, repo *repos.Repo) ([]project, error) {
	switch repo.Kind {
	case repos.KindHosted:
		rows, err := h.q.ListProjects(ctx, repo.ID)
		if err != nil {
			return nil, err
		}
		out := make([]project, len(rows))
		for i, x := range rows {
			out[i] = project{x.Project, x.DisplayName}
		}
		return out, nil
	case repos.KindProxy:
		var out []project
		after := ""
		for {
			items, next, err := h.assets.List(ctx, repo.ID, ".simple/", after, 1000)
			if err != nil {
				return nil, err
			}
			for _, a := range items {
				n := strings.TrimPrefix(a.Path, ".simple/")
				out = append(out, project{n, n})
			}
			if next == "" {
				return out, nil
			}
			after = next
		}
	}
	members, err := h.host.Access.GroupMembers(ctx, r, c.P, repo)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []project
	for _, m := range members {
		list, err := h.projects(ctx, r, c, m)
		if err != nil {
			return nil, err
		}
		for _, p := range list {
			if !seen[p.name] {
				seen[p.name] = true
				out = append(out, p)
			}
		}
	}
	slices.SortFunc(out, func(a, b project) int { return strings.Compare(a.name, b.name) })
	return out, nil
}

func notAcceptable(w http.ResponseWriter, r *http.Request) {
	httpx.Error(w, r, http.StatusNotAcceptable, "this index answers "+ctJSON+", "+ctHTML+" or text/html")
}

// page answers the page of a project. A name that is not normalised, or that lacks the trailing
// slash, is sent to the normalised form.
func (h *Handler) page(w http.ResponseWriter, r *http.Request, c *route.Call) {
	raw := r.PathValue("project")
	n := normalize(raw)
	if !validName(raw) {
		httpx.Error(w, r, http.StatusNotFound, "not found")
		return
	}
	if raw != n || !strings.HasSuffix(r.URL.Path, "/") {
		http.Redirect(w, r, "/pypi/"+url.PathEscape(c.Repo.Name)+"/simple/"+url.PathEscape(n)+"/", http.StatusMovedPermanently)
		return
	}
	f, ok := negotiate(r)
	if !ok {
		notAcceptable(w, r)
		return
	}
	body, err := h.renderPage(r.Context(), r, c, c.Repo, n, f)
	switch {
	case errors.Is(err, assets.ErrRemoteFailed):
		httpx.Error(w, r, http.StatusBadGateway, "The remote repository did not answer.")
	case err != nil:
		route.Fail(w, r, err)
	case body == nil:
		httpx.Error(w, r, http.StatusNotFound, "not found")
	default:
		format.WriteDocument(w, r, f.contentType(), body, "Accept")
	}
}

// renderPage renders the page of project n of repo in form f; nil when repo has no such project.
// A group renders the page of the first member that has it (assets.Reader.Owner).
func (h *Handler) renderPage(ctx context.Context, r *http.Request, c *route.Call, repo *repos.Repo, n string, f form) ([]byte, error) {
	switch repo.Kind {
	case repos.KindHosted:
		m, err := h.hostedModel(ctx, repo, n)
		if err != nil || m == nil {
			return nil, err
		}
		return m.render(f), nil
	case repos.KindProxy:
		a, err := h.upstreamPage(ctx, r, c, repo, n)
		if err != nil || a == nil {
			return nil, err
		}
		key := format.DocumentKey("page", strconv.Itoa(int(f)), strconv.FormatInt(repo.ID, 10), a.SHA256, a.ContentType,
			h.remoteURL(repo), filesBase(repo), n)
		return h.docs.Render(key, func() ([]byte, error) {
			m, err := h.proxyModel(ctx, repo, n, a)
			if err != nil {
				return nil, err
			}
			return m.render(f), nil
		})
	}
	owner, err := h.owner(ctx, r, c, repo, n)
	if err != nil || owner == nil {
		return nil, err
	}
	return h.renderPage(ctx, r, c, owner, n, f)
}

// owner returns the member of group that owns project n, or nil.
func (h *Handler) owner(ctx context.Context, r *http.Request, c *route.Call, group *repos.Repo, n string) (*repos.Repo, error) {
	return h.host.Reader.Owner(ctx, r, c.P, group, func(ctx context.Context, m *repos.Repo) (bool, error) {
		switch m.Kind {
		case repos.KindHosted:
			_, err := h.q.GetProject(ctx, dbq.GetProjectParams{RepoID: m.ID, Project: n})
			if errors.Is(err, sql.ErrNoRows) {
				return false, nil
			}
			return err == nil, err
		case repos.KindProxy:
			a, err := h.upstreamPage(ctx, r, c, m, n)
			return a != nil, err
		}
		return false, nil
	})
}

// hostedModel builds the page of project n of a hosted repo from its rows; nil without files.
func (h *Handler) hostedModel(ctx context.Context, repo *repos.Repo, n string) (*model, error) {
	rows, err := h.q.ListProjectFiles(ctx, dbq.ListProjectFilesParams{RepoID: repo.ID, Project: n})
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	m := &model{name: n}
	seen := map[string]bool{}
	for _, x := range rows {
		if !seen[x.Version] {
			seen[x.Version] = true
			m.versions = append(m.versions, x.Version)
		}
		m.files = append(m.files, mfile{filename: x.Filename,
			href:   "../../packages/" + url.PathEscape(n) + "/" + url.PathEscape(x.Filename),
			hashes: map[string]string{"sha256": x.Sha256}, size: x.Size, requiresPython: x.RequiresPython,
			uploadTime: time.Unix(x.UploadedAt, 0), metadata: x.MetadataSha256 != "", metaSHA: x.MetadataSha256})
	}
	return m, nil
}

// remoteURL is the remote URL of proxy repo ("" when it has none).
func (h *Handler) remoteURL(repo *repos.Repo) string {
	if px, ok := h.host.Repos.Proxy(repo.ID); ok {
		return px.RemoteURL
	}
	return ""
}

// upstreamPage returns the cached upstream page of project n of proxy repo, fetched again after
// the repo's metadata_ttl; nil when the remote has no such project.
func (h *Handler) upstreamPage(ctx context.Context, r *http.Request, c *route.Call, repo *repos.Repo, n string) (*assets.Asset, error) {
	p, err := repopath.Parse(".simple/" + n)
	if err != nil {
		return nil, nil
	}
	return h.host.Reader.Resolve(ctx, r, c.P, repo, p)
}

// file returns the handler of the files under prefix ("packages/", "files/" or "remote/").
func (h *Handler) file(prefix string) route.Handler {
	return func(w http.ResponseWriter, r *http.Request, c *route.Call) {
		rest, err := httpx.RepoPath(r, "path")
		if err != nil {
			httpx.Error(w, r, http.StatusBadRequest, "invalid path")
			return
		}
		p := repopath.Path(prefix + string(rest))
		a, err := h.resolve(r.Context(), r, c, c.Repo, p)
		switch {
		case errors.Is(err, assets.ErrRemoteFailed):
			httpx.Error(w, r, http.StatusBadGateway, "The remote repository did not answer.")
		case err != nil:
			h.log.Error("pypi read failed", "repo", c.Repo.Name, "path", p, "err", err)
			httpx.Error(w, r, http.StatusInternalServerError, "internal error")
		case a == nil:
			httpx.Error(w, r, http.StatusNotFound, "not found")
		default:
			h.host.Reader.ServeAsset(w, r, a, assets.ServeOptions{})
		}
	}
}

// resolve returns the asset of file path p in repo: a hosted repo's stored file; a proxy's cached
// file of files/ or remote/, or the one its remote sends now; in a group, a packages/ file of the
// member that owns its project, and a files/ or remote/ file of the first proxy member that has
// it.
func (h *Handler) resolve(ctx context.Context, r *http.Request, c *route.Call, repo *repos.Repo, p repopath.Path) (*assets.Asset, error) {
	s := string(p)
	switch repo.Kind {
	case repos.KindHosted:
		if !strings.HasPrefix(s, "packages/") {
			return nil, nil
		}
		a, err := h.assets.Get(ctx, repo.ID, s)
		if errors.Is(err, assets.ErrNotFound) {
			return nil, nil
		}
		return a, err
	case repos.KindProxy:
		if strings.HasPrefix(s, "packages/") {
			return nil, nil
		}
		return h.host.Reader.Resolve(ctx, r, c.P, repo, p)
	}
	if strings.HasPrefix(s, "packages/") {
		proj, _, _ := strings.Cut(strings.TrimPrefix(s, "packages/"), "/")
		owner, err := h.owner(ctx, r, c, repo, normalize(proj))
		if err != nil || owner == nil {
			return nil, err
		}
		return h.resolve(ctx, r, c, owner, p)
	}
	members, err := h.host.Access.GroupMembers(ctx, r, c.P, repo)
	if err != nil {
		return nil, err
	}
	remoteFailed := false
	for _, m := range members {
		if m.Kind != repos.KindProxy {
			continue
		}
		a, err := h.host.Reader.Resolve(ctx, r, c.P, m, p)
		switch {
		case errors.Is(err, assets.ErrRemoteFailed):
			remoteFailed = true
		case err != nil:
			return nil, err
		case a != nil:
			return a, nil
		}
	}
	if remoteFailed {
		return nil, assets.ErrRemoteFailed
	}
	return nil, nil
}
