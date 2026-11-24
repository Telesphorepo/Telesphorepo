package nuget

import (
	"database/sql"
	"encoding/json"
	"net/http"
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
	maxSkip      = 3000
	maxTake      = 1000
	defaultTake  = 20
	searchHitCap = 10000 // the most packages that totalHits counts
)

// search answers SearchQueryService: the packages of the hosted repo, or of the hosted members of
// a group that the caller reads (a member leaves out the ids of the members before it), whose id
// holds q: a substring of 3 or more characters through the trigram index of the package ids, in
// the order of the index (repo by repo, then the order the packages came in), else a prefix of
// the id, in id order. A package shows with its newest listed version that the client takes
// (prereleases and SemVer 2.0 versions only when it asks for them; nuget_package_latest), and a
// package without one is left out. A page reads skip + take packages at most, and totalHits
// counts up to searchHitCap. A proxy answers an empty result: nuget.org runs its search on another
// host.
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
	vis := visibility{prerelease: b2i(prerelease), semver2: b2i(semver2)}
	type hit struct {
		repo *repos.Repo
		dbq.SearchHit
	}
	var hits []hit
	var total int64
	shadow := []int64{}
	for _, repo := range list {
		f := searchFilter(repo.ID, q, vis)
		f.Shadow = string(mustJSON(shadow))
		shadow = append(shadow, repo.ID)
		counted := false
		if need := int64(skip + take - len(hits)); need > 0 {
			rows, err := h.q.SearchPackages(ctx, f, need)
			if err != nil {
				route.Fail(w, r, err)
				return
			}
			for _, row := range rows {
				hits = append(hits, hit{repo, row})
			}
			if int64(len(rows)) < need {
				total, counted = total+int64(len(rows)), true
			}
		}
		if !counted && total < searchHitCap {
			n, err := h.q.CountSearchPackages(ctx, f, searchHitCap-total)
			if err != nil {
				route.Fail(w, r, err)
				return
			}
			total += n
		}
	}
	total = min(total, searchHitCap)

	u := urls{base: h.repoBase(r, c.Repo), reg: h.repoBase(r, c.Repo) + "/" + regSemVer2}
	if !semver2 {
		u.reg = u.base + "/" + regSemVer1
	}
	// The packages of the page with their versions: one query.
	page := hits[min(skip, len(hits)):]
	keys := make([][3]any, len(page))
	for i, ht := range page {
		keys[i] = [3]any{ht.repo.ID, ht.LowerID, ht.Latest}
	}
	items, err := h.q.SearchItems(ctx, keys, vis.prerelease == 1, vis.semver2, maxVersions)
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	data := []any{}
	for i, ht := range page {
		it, ok := items[i]
		if !ok {
			route.Fail(w, r, sql.ErrNoRows) // the version went since the search read it
			return
		}
		data = append(data, searchItem(u, ht.LowerID, it, semver2))
	}
	format.WriteDocument(w, r, jsonType, format.NewDocument(mustJSON(map[string]any{"totalHits": total, "data": data})))
}

// searchFilter is the search of the hosted repo repoID for q (lowered): every id for an empty q,
// the ids that start with q for a q of one or two characters, and the ids that hold q for a
// longer q (the trigram index, in the key range of the repo).
func searchFilter(repoID int64, q string, vis visibility) dbq.SearchFilter {
	f := dbq.SearchFilter{RepoID: repoID, Variant: vis.prerelease*2 + vis.semver2}
	if utf8.RuneCountInString(q) < 3 {
		upper := "\x7f"
		if q != "" {
			upper, _ = assets.PrefixEnd(q)
		}
		f.From, f.To = q, upper
		return f
	}
	f.Match, f.Q = `"`+strings.ReplaceAll(q, `"`, `""`)+`"`, q
	f.From, f.To = repoID<<dbq.SearchShift, (repoID+1)<<dbq.SearchShift
	return f
}

// visibility is which versions a search shows besides the listed releases: 1 to show the
// prereleases, 1 to show the SemVer 2.0 versions.
type visibility struct{ prerelease, semver2 int64 }

// searchItem is one package of a search answer: its newest visible version with its metadata,
// and every visible version.
func searchItem(u urls, lid string, it dbq.SearchItem, semver2 bool) map[string]any {
	show := func(full, normalized string) string {
		if semver2 {
			return full
		}
		return normalized
	}
	versions := make([]any, len(it.Versions))
	for i, v := range it.Versions {
		versions[i] = map[string]any{"version": show(v.Version, v.Normalized), "downloads": 0, "@id": u.leaf(lid, strings.ToLower(v.Normalized))}
	}
	out := map[string]any{"@id": u.index(lid), "@type": "Package", "registration": u.index(lid), "id": it.PackageID,
		"version": show(it.Version, it.Normalized), "versions": versions, "totalDownloads": 0, "verified": false, "vulnerabilities": []any{}}
	var e map[string]any
	_ = json.Unmarshal([]byte(it.Meta), &e)
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

// mustJSON encodes v, a value of maps, slices and strings that always encodes.
func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
