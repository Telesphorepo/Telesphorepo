package nuget

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/nuget/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/version"
)

// The model of a package is its versions (leaves), oldest first, each with its catalog entry: the
// rows of a hosted repo, or the registration of a proxy's remote. The registration, its pages and
// leaves, and the version list render from it with the URLs of the repo that answers.

// maxVersions is the most versions of one package that a hosted repo reads.
const maxVersions = 20000

// leaf is one version of a package.
type leaf struct {
	ver       version.NuGet
	id        string // the package id as the package writes it
	full      string // the version as the catalog shows it (with metadata)
	listed    bool
	published time.Time
	entry     map[string]any // the other fields of the catalog entry
}

// model is the versions of one package, oldest first.
type model struct {
	id     string
	leaves []leaf
}

// versionRow is one version of a hosted package with its file.
type versionRow = dbq.GetVersionRow

// modelOf is the model of n version rows, oldest first (row(i) is the i-th): nil without a row
// that reads.
func modelOf(n int, row func(i int) versionRow) *model {
	m := &model{}
	for i := range n {
		l, err := leafOfRow(row(i))
		if err != nil {
			continue
		}
		m.id = l.id
		m.leaves = append(m.leaves, l)
	}
	if len(m.leaves) == 0 {
		return nil
	}
	return m
}

// part is the part of a package's registration that a document renders: the index, one page
// (lower and upper, lowered) or one leaf (lower, the lowered version).
type part struct {
	doc          string // "index", "page" or "leaf"
	semver2      bool
	lower, upper string
}

// hostedPart reads the versions of package lid of a hosted repo that the registration document
// p renders: errNotFound when there are none. An index that inlines its leaves reads them; a paged
// index reads only the versions, for the page bounds; a page reads its versions and a leaf its
// version.
func (h *Handler) hostedPart(ctx context.Context, repoID int64, lid string, p part) (*model, error) {
	sv2 := b2i(p.semver2)
	var m *model
	switch p.doc {
	case "index":
		rows, err := h.q.ListRegVersions(ctx, dbq.ListRegVersionsParams{RepoID: repoID, LowerID: lid, Semver2: sv2, Limit: inlineMax + 1})
		if err != nil {
			return nil, err
		}
		if len(rows) <= inlineMax {
			m = modelOf(len(rows), func(i int) versionRow { return versionRow(rows[i]) })
			break
		}
		names, err := h.q.ListRegVersionNames(ctx, dbq.ListRegVersionNamesParams{RepoID: repoID, LowerID: lid, Semver2: sv2, Limit: maxVersions})
		if err != nil {
			return nil, err
		}
		m = &model{id: lid}
		for _, n := range names {
			if v, err := version.ParseNuGet(n); err == nil {
				m.leaves = append(m.leaves, leaf{ver: v})
			}
		}
	case "page":
		lower, err1 := version.ParseNuGet(p.lower)
		upper, err2 := version.ParseNuGet(p.upper)
		if err1 != nil || err2 != nil {
			return nil, errNotFound
		}
		// A page holds pageSize versions, or every version of an index that inlines them.
		rows, err := h.q.ListRegVersionsBetween(ctx, dbq.ListRegVersionsBetweenParams{RepoID: repoID, LowerID: lid, Semver2: sv2,
			Lower: lower.SortKey(), Upper: upper.SortKey(), Limit: inlineMax + 1})
		if err != nil {
			return nil, err
		}
		if len(rows) <= inlineMax {
			m = modelOf(len(rows), func(i int) versionRow { return versionRow(rows[i]) })
		}
	case "leaf":
		v, err := version.ParseNuGet(p.lower)
		if err != nil {
			return nil, errNotFound
		}
		row, err := h.q.GetVersion(ctx, dbq.GetVersionParams{RepoID: repoID, LowerID: lid, SortKey: v.SortKey()})
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errNotFound
		}
		if err != nil {
			return nil, err
		}
		m = modelOf(1, func(int) versionRow { return row })
	}
	if m == nil || len(m.leaves) == 0 {
		return nil, errNotFound
	}
	return m, nil
}

// leafOfRow is the leaf of a version row.
func leafOfRow(r versionRow) (leaf, error) {
	v, err := version.ParseNuGet(r.Version)
	if err != nil {
		return leaf{}, err
	}
	var entry map[string]any
	if err := json.Unmarshal([]byte(r.Meta), &entry); err != nil {
		return leaf{}, err
	}
	delete(entry, "packageTypes")
	return leaf{ver: v, id: r.PackageID, full: r.Version, listed: r.Listed != 0, published: time.Unix(r.PublishedAt, 0).UTC(), entry: entry}, nil
}

// errNotFound is a package, a page or a version that the repo does not have (404).
var errNotFound = errors.New("not found")

// pkgSource is how a document of one package of one repo renders: key names what the model comes
// from (the revision of a hosted package, the SHA-256s of the remote documents of a proxy), load
// reads the model of a part of the registration: in a hosted repo the versions that the part
// renders, in a proxy every version. Only load reads the rows or the documents.
type pkgSource struct {
	key  []string
	load func(p part) (*model, error)
}

// source returns the pkgSource of package lid in repo (hosted or proxy); errNotFound when repo
// does not have it. A proxy's model comes from the registration of its remote: the index, and
// each page that the index does not inline.
func (h *Handler) source(ctx context.Context, r *http.Request, p *auth.Principal, repo *repos.Repo, lid string) (*pkgSource, error) {
	if repo.Kind != repos.KindProxy {
		rev, err := h.hostedRev(ctx, repo.ID, lid)
		if err != nil {
			return nil, err
		}
		return &pkgSource{key: []string{"hosted", strconv.FormatInt(repo.ID, 10), rev}, load: func(p part) (*model, error) {
			return h.hostedPart(ctx, repo.ID, lid, p)
		}}, nil
	}
	up, err := h.upstreamOf(ctx, r, p, repo)
	if err != nil {
		return nil, err
	}
	idx, err := h.remoteDoc(ctx, r, p, repo, upstreamDir+"registration/"+lid+"/index.json")
	if err != nil {
		return nil, err
	}
	if idx == nil {
		return nil, errNotFound
	}
	// The page URLs of an index, parsed once per index.
	refsDoc, err := h.docs.Render(format.DocumentKey("nuget-refs", idx.SHA256), func() ([]byte, error) {
		body, err := h.readDoc(ctx, idx)
		if err != nil {
			return nil, err
		}
		refs, err := pageRefs(body)
		if err != nil {
			return nil, errNotFound
		}
		return json.Marshal(refs)
	})
	if err != nil {
		return nil, err
	}
	var refs []string
	_ = json.Unmarshal(refsDoc.Body, &refs)
	key := []string{"proxy", strconv.FormatInt(repo.ID, 10), up.sha, idx.SHA256}
	pages := map[string]*assets.Asset{}
	for _, ref := range refs {
		local, ok := up.pagePath(ref)
		if !ok {
			h.host.Log.Warn("nuget proxy: a registration page is outside the registration base of the remote", "repo", repo.Name)
			continue
		}
		a, err := h.remoteDoc(ctx, r, p, repo, local)
		if err != nil {
			return nil, err
		}
		if a == nil {
			continue
		}
		key = append(key, a.SHA256)
		pages[ref] = a
	}
	return &pkgSource{key: key, load: func(part) (*model, error) {
		body, err := h.readDoc(ctx, idx)
		if err != nil {
			return nil, err
		}
		bodies := make(map[string][]byte, len(pages))
		for ref, a := range pages {
			if bodies[ref], err = h.readDoc(ctx, a); err != nil {
				return nil, err
			}
		}
		return remoteModel(up, body, bodies)
	}}, nil
}

// pageRefs returns the URLs of the pages of a registration index that do not inline their
// leaves.
func pageRefs(body []byte) ([]string, error) {
	var idx struct {
		Items []struct {
			ID    string            `json:"@id"`
			Items []json.RawMessage `json:"items"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &idx); err != nil {
		return nil, err
	}
	refs := []string{}
	for _, pg := range idx.Items {
		if pg.Items == nil && pg.ID != "" {
			refs = append(refs, pg.ID)
		}
	}
	return refs, nil
}

// regItem is a leaf of a remote registration.
type regItem struct {
	CatalogEntry map[string]any `json:"catalogEntry"`
}

// remoteModel reads the leaves of a remote registration index and its pages.
func remoteModel(up *upstream, index []byte, pages map[string][]byte) (*model, error) {
	var idx struct {
		Items []struct {
			ID    string    `json:"@id"`
			Items []regItem `json:"items"`
		} `json:"items"`
	}
	if err := json.Unmarshal(index, &idx); err != nil {
		return nil, errNotFound
	}
	var items []regItem
	for _, pg := range idx.Items {
		if pg.Items != nil {
			items = append(items, pg.Items...)
			continue
		}
		body, ok := pages[pg.ID]
		if !ok {
			continue
		}
		var page struct {
			Items []regItem `json:"items"`
		}
		if json.Unmarshal(body, &page) == nil {
			items = append(items, page.Items...)
		}
	}
	m := &model{}
	for _, it := range items {
		if l, ok := remoteLeaf(up, it.CatalogEntry); ok {
			m.id = l.id
			m.leaves = append(m.leaves, l)
		}
	}
	if len(m.leaves) == 0 {
		return nil, errNotFound
	}
	slices.SortStableFunc(m.leaves, func(a, b leaf) int { return a.ver.Compare(b.ver) })
	return m, nil
}

// remoteLeaf is the leaf of a remote catalog entry. Every value that points at the remote's host
// is dropped: the render sets this repo's URLs where a client needs them.
func remoteLeaf(up *upstream, e map[string]any) (leaf, bool) {
	id, _ := e["id"].(string)
	vs, _ := e["version"].(string)
	v, err := version.ParseNuGet(vs)
	if !validID(id) || err != nil {
		return leaf{}, false
	}
	l := leaf{ver: v, id: id, full: vs, listed: true}
	if b, ok := e["listed"].(bool); ok {
		l.listed = b
	}
	if s, ok := e["published"].(string); ok {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			l.published = t.UTC()
			if t.Year() == 1900 {
				l.listed = false
			}
		}
	}
	l.entry, _ = dropOrigin(e, up.origin).(map[string]any)
	for _, k := range []string{"@id", "@type", "id", "version", "listed", "published", "packageContent"} {
		delete(l.entry, k)
	}
	return l, true
}

// dropOrigin returns a copy of v without the string values that start with origin.
func dropOrigin(v any, origin string) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			if s, ok := e.(string); ok && strings.HasPrefix(strings.ToLower(s), origin) {
				continue
			}
			out[k] = dropOrigin(e, origin)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = dropOrigin(e, origin)
		}
		return out
	}
	return v
}

// deepCopy copies a decoded JSON value.
func deepCopy(v any) any { return dropOrigin(v, "\x00") }

// urls are the URLs of one repo's documents: base is <base>/nuget/<repo>, reg the registration
// base of the variant.
type urls struct {
	base string
	reg  string
}

func (u urls) index(lid string) string { return u.reg + "/" + lid + "/index.json" }

func (u urls) leaf(lid, lver string) string { return u.reg + "/" + lid + "/" + lver + ".json" }

func (u urls) page(lid, lower, upper string) string {
	return u.reg + "/" + lid + "/page/" + strings.ToLower(lower) + "/" + strings.ToLower(upper) + ".json"
}

func (u urls) content(lid, lver string) string {
	return u.base + "/" + flatDir + lid + "/" + lver + "/" + lid + "." + lver + ".nupkg"
}

// unlistedDate is the published date of an unlisted version, as nuget.org writes it.
const unlistedDate = "1900-01-01T00:00:00+00:00"

// renderLeaf is the registration leaf of l: the catalog entry inline, with this repo's URLs.
func (m *model) renderLeaf(u urls, l leaf) map[string]any {
	lid := strings.ToLower(m.id)
	lver := strings.ToLower(l.ver.Normalized())
	leafURL, content := u.leaf(lid, lver), u.content(lid, lver)
	entry, _ := deepCopy(l.entry).(map[string]any)
	if entry == nil {
		entry = map[string]any{}
	}
	published := unlistedDate
	if l.listed {
		published = l.published.Format(time.RFC3339)
	}
	entry["@id"], entry["@type"] = leafURL, "PackageDetails"
	entry["id"], entry["version"], entry["listed"], entry["published"] = l.id, l.full, l.listed, published
	entry["packageContent"] = content
	if groups, ok := entry["dependencyGroups"].([]any); ok {
		for _, g := range groups {
			gm, _ := g.(map[string]any)
			deps, _ := gm["dependencies"].([]any)
			for _, d := range deps {
				if dm, ok := d.(map[string]any); ok {
					if id, ok := dm["id"].(string); ok {
						dm["registration"] = u.index(strings.ToLower(id))
					}
				}
			}
		}
	}
	return map[string]any{"@id": leafURL, "@type": "Package", "catalogEntry": entry, "packageContent": content,
		"registration": u.index(lid)}
}

// visible returns the leaves of the variant: without the SemVer 2.0 versions for the plain
// registration.
func (m *model) visible(semver2 bool) []leaf {
	if semver2 {
		return m.leaves
	}
	var out []leaf
	for _, l := range m.leaves {
		if !l.ver.IsSemVer2() {
			out = append(out, l)
		}
	}
	return out
}

// Leaves are inlined in the index up to inlineMax versions; above that, the index lists pages
// of pageSize versions.
const (
	inlineMax = 128
	pageSize  = 64
)

// pagesOf splits leaves into the pages of the registration index.
func pagesOf(leaves []leaf) [][]leaf {
	if len(leaves) <= inlineMax {
		return [][]leaf{leaves}
	}
	var out [][]leaf
	for i := 0; i < len(leaves); i += pageSize {
		out = append(out, leaves[i:min(i+pageSize, len(leaves))])
	}
	return out
}

// renderPage is one page of the registration index: inline (with its leaves and parent) or a
// reference to the page document.
func (m *model) renderPage(u urls, pg []leaf, inline bool) map[string]any {
	lid := strings.ToLower(m.id)
	lower, upper := pg[0].ver.Normalized(), pg[len(pg)-1].ver.Normalized()
	out := map[string]any{"@type": "catalog:CatalogPage", "count": len(pg), "lower": lower, "upper": upper}
	if !inline {
		out["@id"] = u.page(lid, lower, upper)
		return out
	}
	out["@id"] = u.index(lid) + "#page/" + lower + "/" + upper
	out["parent"] = u.index(lid)
	items := make([]any, len(pg))
	for i, l := range pg {
		items[i] = m.renderLeaf(u, l)
	}
	out["items"] = items
	return out
}

// renderIndex is the registration index (RegistrationsBaseUrl).
func (m *model) renderIndex(u urls, semver2 bool) ([]byte, error) {
	leaves := m.visible(semver2)
	if len(leaves) == 0 {
		return nil, errNotFound
	}
	pages := pagesOf(leaves)
	items := make([]any, len(pages))
	for i, pg := range pages {
		items[i] = m.renderPage(u, pg, len(pages) == 1)
	}
	lid := strings.ToLower(m.id)
	return json.Marshal(map[string]any{"@id": u.index(lid),
		"@type": []string{"catalog:CatalogRoot", "PackageRegistration", "catalog:Permalink"},
		"count": len(items), "items": items})
}

// renderPageDoc is the page document of the version range lower..upper (lowered).
func (m *model) renderPageDoc(u urls, semver2 bool, lower, upper string) ([]byte, error) {
	for _, pg := range pagesOf(m.visible(semver2)) {
		if strings.ToLower(pg[0].ver.Normalized()) != lower || strings.ToLower(pg[len(pg)-1].ver.Normalized()) != upper {
			continue
		}
		out := m.renderPage(u, pg, true)
		out["@id"] = u.page(strings.ToLower(m.id), pg[0].ver.Normalized(), pg[len(pg)-1].ver.Normalized())
		return json.Marshal(out)
	}
	return nil, errNotFound
}

// renderLeafDoc is the leaf document of version lver (lowered, normalised).
func (m *model) renderLeafDoc(u urls, semver2 bool, lver string) ([]byte, error) {
	for _, l := range m.visible(semver2) {
		if strings.ToLower(l.ver.Normalized()) == lver {
			return json.Marshal(m.renderLeaf(u, l))
		}
	}
	return nil, errNotFound
}

// hostedRev returns the revision of package lid of a hosted repo; errNotFound when the repo has
// no version of it.
func (h *Handler) hostedRev(ctx context.Context, repoID int64, lid string) (string, error) {
	rev, err := h.q.GetPackageRev(ctx, dbq.GetPackageRevParams{RepoID: repoID, LowerID: lid})
	if errors.Is(err, sql.ErrNoRows) {
		return "", errNotFound
	}
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(rev, 10), nil
}

// hostedHas reports whether a hosted repo has package lid.
func (h *Handler) hostedHas(ctx context.Context, repoID int64, lid string) (bool, error) {
	_, err := h.hostedRev(ctx, repoID, lid)
	if errors.Is(err, errNotFound) {
		return false, nil
	}
	return err == nil, err
}

// owner returns the repo that answers for package lid in repo: repo itself, or in a group the
// member that owns the name (assets.Reader.Owner): a hosted member with a version of it, a proxy
// whose remote has its version list. nil when no member has it.
func (h *Handler) owner(ctx context.Context, r *http.Request, p *auth.Principal, repo *repos.Repo, lid string) (*repos.Repo, error) {
	if repo.Kind != repos.KindGroup {
		return repo, nil
	}
	return h.host.Reader.Owner(ctx, r, p, repo, func(ctx context.Context, m *repos.Repo) (bool, error) {
		if m.Kind != repos.KindProxy {
			return h.hostedHas(ctx, m.ID, lid)
		}
		a, err := h.proxyFlat(ctx, r, p, m, lid)
		return a != nil, err
	})
}

// proxyFlat returns the cached version list of package lid of proxy repo (the flat container
// index.json of its remote, fetched again after the repo's metadata_ttl): nil when the remote
// does not have the package.
func (h *Handler) proxyFlat(ctx context.Context, r *http.Request, p *auth.Principal, repo *repos.Repo, lid string) (*assets.Asset, error) {
	if _, err := h.upstreamOf(ctx, r, p, repo); err != nil {
		return nil, err
	}
	return h.remoteDoc(ctx, r, p, repo, upstreamDir+"flat/"+lid+"/index.json")
}

// isRemoteFailed reports whether err is a proxy remote that failed with nothing cached.
func isRemoteFailed(err error) bool { return errors.Is(err, assets.ErrRemoteFailed) }
