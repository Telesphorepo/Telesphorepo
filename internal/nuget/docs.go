package nuget

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/nuget/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
)

// The registration bases: RegistrationsBaseUrl/3.6.0 lists every version, the plain
// RegistrationsBaseUrl (and 3.0.0-beta, 3.0.0-rc, 3.4.0) leaves out the SemVer 2.0 versions.
const (
	regSemVer2 = "registration"
	regSemVer1 = "registration-semver1"
)

const jsonType = "application/json"

// repoBase is <base>/nuget/<repo> for the request.
func (h *Handler) repoBase(r *http.Request, repo *repos.Repo) string {
	return h.host.API.BaseURL(r) + "/nuget/" + repo.Name
}

// serviceIndex answers the service index of the repo: every resource is under /nuget/{repo}/,
// so dotnet sends the credentials of the source to each of them.
func (h *Handler) serviceIndex(w http.ResponseWriter, r *http.Request, c *route.Call) {
	base := h.repoBase(r, c.Repo)
	type res struct {
		ID      string `json:"@id"`
		Type    string `json:"@type"`
		Comment string `json:"comment,omitempty"`
	}
	var rs []res
	rs = append(rs, res{base + "/" + flatDir, "PackageBaseAddress/3.0.0", "The packages and their version lists"})
	rs = append(rs, res{base + "/" + regSemVer2 + "/", "RegistrationsBaseUrl/3.6.0", "The package metadata, SemVer 2.0 versions included"})
	for _, t := range []string{"RegistrationsBaseUrl", "RegistrationsBaseUrl/3.0.0-beta", "RegistrationsBaseUrl/3.0.0-rc", "RegistrationsBaseUrl/3.4.0"} {
		rs = append(rs, res{base + "/" + regSemVer1 + "/", t, "The package metadata without SemVer 2.0 versions"})
	}
	for _, t := range []string{"SearchQueryService", "SearchQueryService/3.0.0-beta", "SearchQueryService/3.0.0-rc", "SearchQueryService/3.5.0"} {
		rs = append(rs, res{base + "/query", t, "Search of the package ids of the hosted packages"})
	}
	rs = append(rs, res{base + "/package", "PackagePublish/2.0.0", "Push with a publish session as the API key"})
	rs = append(rs, res{base + "/vulnerabilities/index.json", "VulnerabilityInfo/6.7.0", "This server knows no vulnerabilities"})
	body, _ := json.Marshal(map[string]any{"version": "3.0.0", "resources": rs})
	format.WriteDocument(w, r, jsonType, format.NewDocument(body))
}

// vulnerabilityIndex lists one page of vulnerability data, which is empty: restore then finds
// the source audited and warns nothing.
func (h *Handler) vulnerabilityIndex(w http.ResponseWriter, r *http.Request, c *route.Call) {
	body, _ := json.Marshal([]map[string]string{{"@name": "base", "@id": h.repoBase(r, c.Repo) + "/vulnerabilities/base.json",
		"@updated": "2026-01-01T00:00:00Z", "comment": "This server knows no vulnerabilities."}})
	format.WriteDocument(w, r, jsonType, format.NewDocument(body))
}

// vulnerabilityPage is the empty page of vulnerability data: no package has a known
// vulnerability.
func (h *Handler) vulnerabilityPage(w http.ResponseWriter, r *http.Request, _ *route.Call) {
	format.WriteDocument(w, r, jsonType, format.NewDocument([]byte("{}")))
}

// lowerIDOf reads the {id} of the request, lowered; ok is false (and answered 404) for an id
// that is not valid.
func lowerIDOf(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if !validID(id) {
		httpx.Error(w, r, http.StatusNotFound, "not found")
		return "", false
	}
	return strings.ToLower(id), true
}

// fail answers an error of a document: 404 for a package the repo lacks, 502 for a remote that
// failed with nothing cached.
func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errNotFound):
		httpx.Error(w, r, http.StatusNotFound, "not found")
	case isRemoteFailed(err):
		httpx.Error(w, r, http.StatusBadGateway, "The remote repository did not answer.")
	default:
		route.Fail(w, r, err)
	}
}

// document renders a document of package lid for the repo of the request: from the repo, or in
// a group from the member that owns the name. parts name the document for the cache; build
// renders it from the model.
func (h *Handler) document(w http.ResponseWriter, r *http.Request, c *route.Call, lid string, parts []string, build func(m *model) ([]byte, error)) {
	ctx := r.Context()
	owner, err := h.owner(ctx, r, c.P, c.Repo, lid)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if owner == nil {
		h.fail(w, r, errNotFound)
		return
	}
	src, err := h.source(ctx, r, c.P, owner, lid)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	key := append(append([]string{h.repoBase(r, c.Repo), lid}, parts...), src.key...)
	err = h.docs.Serve(w, r, jsonType, format.DocumentKey(key...), func() ([]byte, error) {
		m, err := src.load()
		if err != nil {
			return nil, err
		}
		return build(m)
	})
	if err != nil {
		h.fail(w, r, err)
	}
}

// variant reads which registration base the request names, and its URLs.
func (h *Handler) variant(r *http.Request, c *route.Call) (urls, bool) {
	base := h.repoBase(r, c.Repo)
	if strings.Contains(r.URL.Path, "/"+regSemVer1+"/") {
		return urls{base: base, reg: base + "/" + regSemVer1}, false
	}
	return urls{base: base, reg: base + "/" + regSemVer2}, true
}

// flatIndex answers the version list of a package (PackageBaseAddress {id}/index.json).
func (h *Handler) flatIndex(w http.ResponseWriter, r *http.Request, c *route.Call) {
	lid, ok := lowerIDOf(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	owner, err := h.owner(ctx, r, c.P, c.Repo, lid)
	if err != nil || owner == nil {
		h.fail(w, r, errOr(err))
		return
	}
	if owner.Kind == repos.KindProxy {
		// The remote's list holds no URL: it serves as the remote wrote it.
		a, err := h.proxyFlat(ctx, r, c.P, owner, lid)
		if err != nil || a == nil {
			h.fail(w, r, errOr(err))
			return
		}
		if err := h.docs.Serve(w, r, jsonType, format.DocumentKey("flat-remote", a.SHA256), func() ([]byte, error) {
			return h.readDoc(ctx, a)
		}); err != nil {
			h.fail(w, r, err)
		}
		return
	}
	rev, err := h.hostedRev(ctx, owner.ID, lid)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	key := format.DocumentKey("flat", strconv.FormatInt(owner.ID, 10), lid, rev)
	if err := h.docs.Serve(w, r, jsonType, key, func() ([]byte, error) { return h.renderFlat(ctx, owner.ID, lid) }); err != nil {
		h.fail(w, r, err)
	}
}

// renderFlat is the version list of the flat container of package lid of a hosted repo: every
// version, unlisted ones included, lowered and normalised, oldest first.
func (h *Handler) renderFlat(ctx context.Context, repoID int64, lid string) ([]byte, error) {
	vs, err := h.q.ListVersionNames(ctx, dbq.ListVersionNamesParams{RepoID: repoID, LowerID: lid, Limit: maxVersions})
	if err != nil {
		return nil, err
	}
	if len(vs) == 0 {
		return nil, errNotFound
	}
	for i, v := range vs {
		vs[i] = strings.ToLower(v)
	}
	return json.Marshal(map[string][]string{"versions": vs})
}

// errOr is err, or errNotFound when err is nil.
func errOr(err error) error {
	if err == nil {
		return errNotFound
	}
	return err
}

// flatFile serves a .nupkg or a .nuspec of the flat container: the stored file of the repo, of
// the member that owns the package in a group, or of the remote of a proxy.
func (h *Handler) flatFile(w http.ResponseWriter, r *http.Request, c *route.Call) {
	lid, ok := lowerIDOf(w, r)
	if !ok {
		return
	}
	lver, file := strings.ToLower(r.PathValue("ver")), strings.ToLower(r.PathValue("file"))
	nupkg, nuspec := packagePaths(lid, lver)
	p := flatDir + lid + "/" + lver + "/" + file
	if p != nupkg && p != nuspec {
		httpx.Error(w, r, http.StatusNotFound, "not found")
		return
	}
	rp, err := repopath.Parse(p)
	if err != nil {
		httpx.Error(w, r, http.StatusNotFound, "not found")
		return
	}
	ctx := r.Context()
	owner, err := h.owner(ctx, r, c.P, c.Repo, lid)
	if err != nil || owner == nil {
		h.fail(w, r, errOr(err))
		return
	}
	if owner.Kind == repos.KindProxy {
		if _, err := h.upstreamOf(ctx, r, c.P, owner); err != nil {
			h.fail(w, r, err)
			return
		}
	}
	a, err := h.host.Reader.Resolve(ctx, r, c.P, owner, rp)
	if err != nil || a == nil {
		h.fail(w, r, errOr(err))
		return
	}
	h.host.Reader.ServeAsset(w, r, a, assets.ServeOptions{})
}

// registrationIndex answers the registration index of a package.
func (h *Handler) registrationIndex(w http.ResponseWriter, r *http.Request, c *route.Call) {
	lid, ok := lowerIDOf(w, r)
	if !ok {
		return
	}
	u, semver2 := h.variant(r, c)
	h.document(w, r, c, lid, []string{"index", strconv.FormatBool(semver2)}, func(m *model) ([]byte, error) {
		return m.renderIndex(u, semver2)
	})
}

// registrationPage answers a page of the registration of a package with more versions than the
// index inlines.
func (h *Handler) registrationPage(w http.ResponseWriter, r *http.Request, c *route.Call) {
	lid, ok := lowerIDOf(w, r)
	if !ok {
		return
	}
	lower, upper := strings.ToLower(r.PathValue("lower")), strings.ToLower(r.PathValue("upper"))
	upper, found := strings.CutSuffix(upper, ".json")
	if !found {
		httpx.Error(w, r, http.StatusNotFound, "not found")
		return
	}
	u, semver2 := h.variant(r, c)
	h.document(w, r, c, lid, []string{"page", strconv.FormatBool(semver2), lower, upper}, func(m *model) ([]byte, error) {
		return m.renderPageDoc(u, semver2, lower, upper)
	})
}

// registrationLeaf answers the registration leaf of one version.
func (h *Handler) registrationLeaf(w http.ResponseWriter, r *http.Request, c *route.Call) {
	lid, ok := lowerIDOf(w, r)
	if !ok {
		return
	}
	lver, found := strings.CutSuffix(strings.ToLower(r.PathValue("leaf")), ".json")
	if !found {
		httpx.Error(w, r, http.StatusNotFound, "not found")
		return
	}
	u, semver2 := h.variant(r, c)
	h.document(w, r, c, lid, []string{"leaf", strconv.FormatBool(semver2), lver}, func(m *model) ([]byte, error) {
		return m.renderLeafDoc(u, semver2, lver)
	})
}
