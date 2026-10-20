package npm

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/Telesphorepo/Telesphorepo/internal/access"
	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/npm/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
)

// The sentences of the routes that Telesphorepo does not serve.
const (
	msgLogin  = "npm login is not used with Telesphorepo. To install, put a read token in .npmrc (see the repo page). To publish, run telesphorepo publish."
	msgSearch = "This registry has no search. Browse the repository in the web UI."
	msgAudit  = "This registry has no security advisories."
)

// The operations of a PUT or a DELETE (route.RepoRule Ops): a publish, a tag or a deprecation
// writes (repo.publish); an unpublish deletes (repo.delete).
var writeOrDelete = []route.RepoOp{{Op: access.Write, Perm: perm.RepoPublish}, {Op: access.Delete, Perm: perm.RepoDelete}}

const (
	opWrite  = 0
	opDelete = 1
)

// routes mounts the registry API under /npm/{repo}/. One route per method reads the rest of the
// path itself (parseTarget), because a scoped name comes as one segment with an encoded slash or
// as two segments.
func (h *Handler) routes(r *route.Router, _ format.Kit) {
	r.Handle("GET /npm/{repo}/-/ping", route.Public, func(w http.ResponseWriter, r *http.Request, _ *route.Call) {
		httpx.WriteJSON(w, http.StatusOK, struct{}{})
	})
	r.Handle("GET /npm/{repo}/-/whoami", route.FormatRead(name), h.whoami)
	r.Handle("POST /npm/{repo}/-/v1/login", route.Public, refuseWith(http.StatusUnauthorized, msgLogin))
	r.Handle("PUT /npm/{repo}/-/user/{user}", route.Public, refuseWith(http.StatusUnauthorized, msgLogin))
	r.Handle("GET /npm/{repo}/-/v1/search", route.Public, refuseWith(http.StatusNotFound, msgSearch))
	r.Handle("GET /npm/{repo}/-/npm/v1/security/{rest...}", route.Public, refuseWith(http.StatusNotFound, msgAudit))
	r.Handle("POST /npm/{repo}/-/npm/v1/security/{rest...}", route.Public, refuseWith(http.StatusNotFound, msgAudit))
	r.Handle("GET /npm/{repo}/{rest...}", route.FormatRead(name), h.get)
	r.Handle("PUT /npm/{repo}/{rest...}", route.Policy{Accept: auth.CredPublish,
		Repo: &route.RepoRule{Format: name, Ops: writeOrDelete, Pick: pickPut}}, h.put)
	r.Handle("DELETE /npm/{repo}/{rest...}", route.Policy{Accept: auth.CredPublish,
		Repo: &route.RepoRule{Format: name, Ops: writeOrDelete, Pick: pickDelete}}, h.delete)
}

// pickPut: PUT <name>/-rev/<rev> is the step of npm unpublish that drops versions (delete); every
// other PUT writes.
func pickPut(r *http.Request) int {
	if t, err := parseTarget(r); err == nil && !t.dash && len(t.rest) == 2 && t.rest[0] == "-rev" {
		return opDelete
	}
	return opWrite
}

// pickDelete: removing a dist-tag writes; every other DELETE (an unpublish) deletes.
func pickDelete(r *http.Request) int {
	if t, err := parseTarget(r); err == nil {
		if _, _, ok, _ := distTagsTarget(t); ok {
			return opWrite
		}
	}
	return opDelete
}

// refuseWith answers status with msg in npm's error form.
func refuseWith(status int, msg string) route.Handler {
	return func(w http.ResponseWriter, r *http.Request, _ *route.Call) { httpx.Error(w, r, status, msg) }
}

func notFound(w http.ResponseWriter, r *http.Request) {
	httpx.Error(w, r, http.StatusNotFound, "not found")
}

func ok(w http.ResponseWriter, status int) {
	httpx.WriteJSON(w, status, map[string]bool{"ok": true})
}

// whoami answers GET /-/whoami: the user of the credential.
func (h *Handler) whoami(w http.ResponseWriter, r *http.Request, c *route.Call) {
	if c.P.Anonymous() {
		httpx.Error(w, r, http.StatusUnauthorized, "no credential: put a token in .npmrc")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"username": c.P.Username})
}

// urlBase is the registry URL of the repo of the request, without a trailing slash.
func (h *Handler) urlBase(r *http.Request, repo *repos.Repo) string {
	return h.host.API.BaseURL(r) + "/npm/" + repo.Name
}

// failRead answers an error of a read: 404 for a name the repo does not have, 502 for a remote
// that did not answer.
func (h *Handler) failRead(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errNoPackage):
		notFound(w, r)
	case errors.Is(err, assets.ErrRemoteFailed):
		httpx.Error(w, r, http.StatusBadGateway, "The remote registry did not answer.")
	default:
		route.Fail(w, r, err)
	}
}

// get serves the packument, the manifest of a version, a tarball and the dist-tags.
func (h *Handler) get(w http.ResponseWriter, r *http.Request, c *route.Call) {
	t, err := parseTarget(r)
	if err != nil {
		notFound(w, r)
		return
	}
	if t.dash {
		pkg, tag, isTags, err := distTagsTarget(t)
		switch {
		case !isTags || tag != "":
			notFound(w, r)
		case err != nil:
			httpx.Error(w, r, http.StatusBadRequest, err.Error())
		default:
			h.getTags(w, r, c, pkg)
		}
		return
	}
	switch {
	case len(t.rest) == 0:
		h.packument(w, r, c, t.name)
	case len(t.rest) == 1 && t.rest[0] != "-":
		h.versionManifest(w, r, c, t.name, t.rest[0])
	case len(t.rest) == 2 && t.rest[0] == "-":
		h.file(w, r, c, t.name, t.rest[1])
	default:
		notFound(w, r)
	}
}

// versionManifest answers GET <name>/<version or dist-tag>: the manifest of that version from the
// full packument, as registry.npmjs.org does. pnpm, run through corepack, fetches its own binary
// package this way from the registry of the project.
func (h *Handler) versionManifest(w http.ResponseWriter, r *http.Request, c *route.Call, pkg, ref string) {
	s, err := h.resolve(r.Context(), r, c.P, c.Repo, pkg, false)
	if err != nil {
		h.failRead(w, r, err)
		return
	}
	body, err := h.document(r.Context(), s, pkg, h.urlBase(r, c.Repo), false)
	if err != nil {
		h.failRead(w, r, err)
		return
	}
	var doc struct {
		Tags     map[string]string          `json:"dist-tags"`
		Versions map[string]json.RawMessage `json:"versions"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		h.failRead(w, r, err)
		return
	}
	v := ref
	if tv, ok := doc.Tags[ref]; ok {
		v = tv
	}
	m, ok := doc.Versions[v]
	if !ok {
		httpx.Error(w, r, http.StatusNotFound, "version "+ref+" of "+pkg+" not found")
		return
	}
	format.WriteDocument(w, r, "application/json", m)
}

// packument answers the full or the abbreviated packument of name (?write=true is the same).
func (h *Handler) packument(w http.ResponseWriter, r *http.Request, c *route.Call, pkg string) {
	abbreviated := wantsAbbreviated(r)
	s, err := h.resolve(r.Context(), r, c.P, c.Repo, pkg, abbreviated)
	if err != nil {
		h.failRead(w, r, err)
		return
	}
	body, err := h.document(r.Context(), s, pkg, h.urlBase(r, c.Repo), abbreviated)
	if err != nil {
		h.failRead(w, r, err)
		return
	}
	ct := "application/json"
	if abbreviated {
		ct = abbreviatedType
	}
	format.WriteDocument(w, r, ct, body, "Accept")
}

// file serves <name>/-/<file>. A group serves it from the member that owns the name.
func (h *Handler) file(w http.ResponseWriter, r *http.Request, c *route.Call, pkg, file string) {
	p, err := repopath.Parse(pkg + "/-/" + file)
	if err != nil {
		notFound(w, r)
		return
	}
	if c.Repo.Kind != repos.KindGroup {
		h.host.Reader.Serve(w, r, c, p)
		return
	}
	s, err := h.resolve(r.Context(), r, c.P, c.Repo, pkg, true)
	if err != nil {
		h.failRead(w, r, err)
		return
	}
	a, err := h.host.Reader.Resolve(r.Context(), r, c.P, s.repo, p)
	switch {
	case err != nil:
		h.failRead(w, r, err)
	case a == nil:
		notFound(w, r)
	default:
		h.host.Reader.ServeAsset(w, r, a, assets.ServeOptions{})
	}
}

// getTags answers the dist-tags of name.
func (h *Handler) getTags(w http.ResponseWriter, r *http.Request, c *route.Call, pkg string) {
	s, err := h.resolve(r.Context(), r, c.P, c.Repo, pkg, true)
	if err != nil {
		h.failRead(w, r, err)
		return
	}
	tags, err := h.distTagsOf(r.Context(), s, pkg)
	if err != nil {
		h.failRead(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, tags)
}

// put serves a publish or a metadata update (PUT <name>), the version removal of an unpublish (PUT
// <name>/-rev/<rev>) and a dist-tag (PUT /-/package/<name>/dist-tags/<tag>).
func (h *Handler) put(w http.ResponseWriter, r *http.Request, c *route.Call) {
	t, err := parseTarget(r)
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, err.Error())
		return
	}
	if t.dash {
		pkg, tag, isTags, err := distTagsTarget(t)
		switch {
		case err != nil:
			httpx.Error(w, r, http.StatusBadRequest, err.Error())
		case !isTags || tag == "":
			notFound(w, r)
		default:
			h.setTag(w, r, c, pkg, tag)
		}
		return
	}
	switch {
	case len(t.rest) == 0:
		h.putPackage(w, r, c, t.name, "")
	case len(t.rest) == 2 && t.rest[0] == "-rev":
		h.putPackage(w, r, c, t.name, t.rest[1])
	default:
		notFound(w, r)
	}
}

// delete serves npm unpublish (DELETE <name>/-rev/<rev>, DELETE <name>/-/<file>/-rev/<rev>) and
// the removal of a dist-tag.
func (h *Handler) delete(w http.ResponseWriter, r *http.Request, c *route.Call) {
	t, err := parseTarget(r)
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, err.Error())
		return
	}
	if t.dash {
		pkg, tag, isTags, err := distTagsTarget(t)
		switch {
		case err != nil:
			httpx.Error(w, r, http.StatusBadRequest, err.Error())
		case !isTags || tag == "":
			notFound(w, r)
		default:
			h.removeTag(w, r, c, pkg, tag)
		}
		return
	}
	if c.Repo.Kind != repos.KindHosted {
		w.Header().Set("Allow", "GET, HEAD")
		httpx.Error(w, r, http.StatusMethodNotAllowed, access.ReadOnly(c.Repo))
		return
	}
	switch {
	case len(t.rest) == 2 && t.rest[0] == "-rev":
		h.unpublishAll(w, r, c, t.name, t.rest[1])
	case len(t.rest) == 4 && t.rest[0] == "-" && t.rest[2] == "-rev":
		h.unpublishFile(w, r, c, t.name, t.rest[1], t.rest[3])
	default:
		notFound(w, r)
	}
}

// setTag points dist-tag tag of name at the version of the body (a JSON string).
func (h *Handler) setTag(w http.ResponseWriter, r *http.Request, c *route.Call, pkg, tag string) {
	var v string
	b, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil || json.Unmarshal(b, &v) != nil || v == "" {
		httpx.Error(w, r, http.StatusBadRequest, "the body must be the version as a JSON string, e.g. \"1.2.0\"")
		return
	}
	err = h.change(r.Context(), c.Repo, pkg, func(q *dbq.Queries) (bool, error) {
		if _, err := q.GetVersionTarball(r.Context(), dbq.GetVersionTarballParams{RepoID: c.Repo.ID, Name: pkg, Version: v}); err != nil {
			if isNoRows(err) {
				return false, assets.Refuse(http.StatusNotFound, "version %s of %s does not exist", v, pkg)
			}
			return false, err
		}
		return true, q.UpsertTag(r.Context(), dbq.UpsertTagParams{RepoID: c.Repo.ID, Name: pkg, Tag: tag, Version: v})
	})
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	ok(w, http.StatusCreated)
}

// removeTag removes dist-tag tag of name. latest stays: point it at another version instead.
func (h *Handler) removeTag(w http.ResponseWriter, r *http.Request, c *route.Call, pkg, tag string) {
	if tag == "latest" {
		httpx.Error(w, r, http.StatusBadRequest, "the latest tag cannot be removed; point it at another version with npm dist-tag add")
		return
	}
	err := h.change(r.Context(), c.Repo, pkg, func(q *dbq.Queries) (bool, error) {
		tags, err := q.ListTags(r.Context(), dbq.ListTagsParams{RepoID: c.Repo.ID, Name: pkg})
		if err != nil {
			return false, err
		}
		for _, t := range tags {
			if t.Tag == tag {
				return true, q.DeleteTag(r.Context(), dbq.DeleteTagParams{RepoID: c.Repo.ID, Name: pkg, Tag: tag})
			}
		}
		return false, assets.Refuse(http.StatusNotFound, "%s has no tag %s", pkg, tag)
	})
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	ok(w, http.StatusOK)
}
