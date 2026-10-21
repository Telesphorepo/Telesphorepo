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
	searchHitCap  = 10000 // the most package ids of one repo that the trigram index gives a search
	searchHitPage = 500
)

// search answers SearchQueryService: the packages of the hosted repo, or of the hosted members of
// a group that the caller reads, whose id holds q (a substring of 3 or more characters through the
// trigram index of the package ids, else a prefix of the id). Unlisted versions are left out, and
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
	vis := visibility{prerelease: b2i(prerelease), semver2: b2i(semver2)}
	data := []any{}
	total := 0
	for _, ht := range hits {
		// The newest visible version only: whether the package counts, and its sort key.
		key, err := h.q.LatestVisibleVersion(ctx, dbq.LatestVisibleVersionParams{RepoID: ht.repo.ID, LowerID: ht.lid,
			Prerelease: vis.prerelease, Semver2: vis.semver2})
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			route.Fail(w, r, err)
			return
		}
		total++
		if total <= skip || len(data) >= take {
			continue
		}
		item, err := h.searchItem(ctx, u, ht.repo.ID, ht.lid, key, vis, semver2)
		if err != nil {
			route.Fail(w, r, err)
			return
		}
		data = append(data, item)
	}
	format.WriteDocument(w, r, jsonType, format.NewDocument(mustJSON(map[string]any{"totalHits": total, "data": data})))
}

// visibility is which versions a search shows besides the listed releases: 1 to show the
// prereleases, 1 to show the SemVer 2.0 versions.
type visibility struct{ prerelease, semver2 int64 }

// searchItem is one package of a search answer: its newest visible version (sort key latest) with
// its metadata, and every visible version.
func (h *Handler) searchItem(ctx context.Context, u urls, repoID int64, lid, latest string, vis visibility, semver2 bool) (map[string]any, error) {
	row, err := h.q.GetVersion(ctx, dbq.GetVersionParams{RepoID: repoID, LowerID: lid, SortKey: latest})
	if err != nil {
		return nil, err
	}
	rows, err := h.q.ListVisibleVersions(ctx, dbq.ListVisibleVersionsParams{RepoID: repoID, LowerID: lid,
		Prerelease: vis.prerelease, Semver2: vis.semver2, Limit: maxVersions})
	if err != nil {
		return nil, err
	}
	show := func(full, normalized string) string {
		if semver2 {
			return full
		}
		return normalized
	}
	versions := make([]any, len(rows))
	for i, v := range rows {
		versions[i] = map[string]any{"version": show(v.Version, v.Normalized), "downloads": 0, "@id": u.leaf(lid, strings.ToLower(v.Normalized))}
	}
	out := map[string]any{"@id": u.index(lid), "@type": "Package", "registration": u.index(lid), "id": row.PackageID,
		"version": show(row.Version, row.Normalized), "versions": versions, "totalDownloads": 0, "verified": false, "vulnerabilities": []any{}}
	var e map[string]any
	_ = json.Unmarshal([]byte(row.Meta), &e)
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
	return out, nil
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
// ids that hold q for a longer q (the trigram index of the package ids, one hit per package; at
// most searchHitCap of them).
func (h *Handler) searchIDs(ctx context.Context, repoID int64, q string, limit int) ([]string, error) {
	if utf8.RuneCountInString(q) < 3 {
		upper := "\x7f"
		if q != "" {
			upper, _ = assets.PrefixEnd(q)
		}
		return h.q.ListPackageIDs(ctx, dbq.ListPackageIDsParams{RepoID: repoID, LowerID: q, LowerID_2: upper, Limit: int64(limit)})
	}
	match := `"` + strings.ReplaceAll(q, `"`, `""`) + `"`
	var out []string
	var after int64
	for len(out) < searchHitCap {
		hits, err := h.q.SearchPackageIDs(ctx, dbq.SearchPackageIDsParams{Match: match, After: after, RepoID: repoID, Limit: searchHitPage})
		if err != nil {
			return nil, err
		}
		for _, hit := range hits {
			if strings.Contains(hit.LowerID, q) {
				out = append(out, hit.LowerID)
			}
		}
		if len(hits) < searchHitPage {
			break
		}
		after = hits[len(hits)-1].ID
	}
	slices.Sort(out)
	return out[:min(len(out), limit)], nil
}

// mustJSON encodes v, a value of maps, slices and strings that always encodes.
func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
