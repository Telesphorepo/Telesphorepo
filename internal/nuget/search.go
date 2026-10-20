package nuget

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/nuget/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
)

// Limits of the search, as nuget.org has them.
const (
	maxSkip       = 3000
	maxTake       = 1000
	defaultTake   = 20
	searchHitCap  = 2000 // the most path hits of the trigram index one search reads
	searchHitPage = 200
)

// search answers SearchQueryService: the packages of the hosted repo, or of the hosted members of
// a group that the caller reads, whose id holds q (a substring of 3 or more characters through the
// trigram index of the asset paths, else a prefix of the id). Unlisted versions are left out, and
// prerelease and SemVer 2.0 versions unless the client asks for them. A proxy answers an empty
// result: nuget.org runs its search on another host.
func (h *Handler) search(w http.ResponseWriter, r *http.Request, c *route.Call) {
	ctx := r.Context()
	qv := r.URL.Query()
	q := strings.ToLower(strings.TrimSpace(qv.Get("q")))
	skip, _ := strconv.Atoi(qv.Get("skip"))
	take, err := strconv.Atoi(qv.Get("take"))
	if err != nil {
		take = defaultTake
	}
	skip, take = min(max(skip, 0), maxSkip), min(max(take, 0), maxTake)
	prerelease := strings.EqualFold(qv.Get("prerelease"), "true")
	semver2 := strings.HasPrefix(qv.Get("semVerLevel"), "2")

	var list []*repos.Repo
	switch c.Repo.Kind {
	case repos.KindHosted:
		list = []*repos.Repo{c.Repo}
	case repos.KindGroup:
		members, err := h.host.Access.GroupMembers(ctx, r, c.P, c.Repo)
		if err != nil {
			route.Fail(w, r, err)
			return
		}
		for _, m := range members {
			if m.Kind == repos.KindHosted {
				list = append(list, m)
			}
		}
	}
	type hit struct {
		repo *repos.Repo
		lid  string
	}
	seen := map[string]bool{}
	var hits []hit
	for _, repo := range list {
		ids, err := h.searchIDs(ctx, repo.ID, q, skip+take+50)
		if err != nil {
			route.Fail(w, r, err)
			return
		}
		for _, id := range ids {
			if !seen[id] {
				seen[id] = true
				hits = append(hits, hit{repo, id})
			}
		}
	}
	slices.SortFunc(hits, func(a, b hit) int { return strings.Compare(a.lid, b.lid) })

	u := urls{base: h.repoBase(r, c.Repo), reg: h.repoBase(r, c.Repo) + "/" + regSemVer2}
	if !semver2 {
		u.reg = u.base + "/" + regSemVer1
	}
	data := []any{}
	total := 0
	for _, ht := range hits {
		m, err := h.hostedModel(ctx, ht.repo.ID, ht.lid)
		if err != nil {
			route.Fail(w, r, err)
			return
		}
		if m == nil {
			continue
		}
		var vis []leaf
		for _, l := range m.leaves {
			if l.listed && (prerelease || !l.ver.Prerelease()) && (semver2 || !l.ver.IsSemVer2()) {
				vis = append(vis, l)
			}
		}
		if len(vis) == 0 {
			continue
		}
		total++
		if total <= skip || len(data) >= take {
			continue
		}
		data = append(data, searchItem(u, m, vis, semver2))
	}
	format.WriteDocument(w, r, jsonType, mustJSON(map[string]any{"totalHits": total, "data": data}))
}

// searchItem is one package of a search answer: its newest visible version and every visible
// version.
func searchItem(u urls, m *model, vis []leaf, semver2 bool) map[string]any {
	lid := strings.ToLower(m.id)
	latest := vis[len(vis)-1]
	show := func(l leaf) string {
		if semver2 {
			return l.full
		}
		return l.ver.Normalized()
	}
	versions := make([]any, len(vis))
	for i, l := range vis {
		versions[i] = map[string]any{"version": show(l), "downloads": 0, "@id": u.leaf(lid, strings.ToLower(l.ver.Normalized()))}
	}
	out := map[string]any{"@id": u.index(lid), "@type": "Package", "registration": u.index(lid), "id": latest.id,
		"version": show(latest), "versions": versions, "totalDownloads": 0, "verified": false, "vulnerabilities": []any{}}
	e := latest.entry
	for _, k := range []string{"description", "summary", "title", "iconUrl", "licenseUrl", "projectUrl", "tags"} {
		if v, ok := e[k]; ok {
			out[k] = v
		}
	}
	if a, ok := e["authors"].(string); ok && a != "" {
		out["authors"] = splitList(a)
	}
	if o, ok := e["owners"].(string); ok && o != "" {
		out["owners"] = splitList(o)
	}
	return out
}

// splitList splits a comma-separated list of the .nuspec.
func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// searchIDs returns up to limit lowered package ids of a hosted repo that match q, in id order:
// every id for an empty q, the ids that start with q for a q of one or two characters, and the
// ids that hold q for a longer q (the trigram index of the asset paths, through the .nuspec of
// each version).
func (h *Handler) searchIDs(ctx context.Context, repoID int64, q string, limit int) ([]string, error) {
	if utf8.RuneCountInString(q) < 3 {
		upper := "\x7f"
		if q != "" {
			upper, _ = assets.PrefixEnd(q)
		}
		return h.q.ListPackageIDs(ctx, dbq.ListPackageIDsParams{RepoID: repoID, LowerID: q, LowerID_2: upper, Limit: int64(limit)})
	}
	seen := map[string]bool{}
	var out []string
	var after int64
	for read := 0; read < searchHitCap && len(out) < limit; read += searchHitPage {
		hits, next, err := h.host.Assets.Search(ctx, q, []int64{repoID}, after, searchHitPage)
		if err != nil {
			return nil, err
		}
		for _, hit := range hits {
			lid, _, file, ok := flatFile(hit.Asset.Path)
			if ok && strings.HasSuffix(file, ".nuspec") && strings.Contains(lid, q) && !seen[lid] {
				seen[lid] = true
				out = append(out, lid)
			}
		}
		if next == 0 {
			break
		}
		after = next
	}
	slices.Sort(out)
	return out, nil
}

// mustJSON encodes v, a value of maps, slices and strings that always encodes.
func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
