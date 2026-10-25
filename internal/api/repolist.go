package api

import (
	"cmp"
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
)

// The list of repositories (GET /api/repos) and the figures of the Settings overview
// (GET /api/overview). A request reads only its answer: a keyset page merges at most three lists,
// each one range of an index (repos.Service list.go): every repo for a holder of all
// repositories, else the public repos and the repos of the caller's grants (a lookup per grant).

// maxPublished is the most repos of a list by last publish (?sort=published).
const maxPublished = 50

// visible is what one caller sees of the repos: every repo, or the public ones and the repos of
// the grants that hold a wanted permission.
type visible struct {
	all       bool    // a holder of all repositories
	anonymous bool    // the public repos count
	ids       []int64 // the repos of the caller's grants that count
}

// visibleTo returns what p sees of the repos where p holds a permission of want (empty: any
// permission), and ctx with p's grants (auth.Service.WithGrants).
func (a *API) visibleTo(ctx context.Context, p *auth.Principal, want perm.Set) (context.Context, visible, error) {
	if p != nil && perm.Current().AllRepos(p.Perms) {
		return ctx, visible{all: true}, nil
	}
	ctx, err := a.auth.WithGrants(ctx, p)
	if err != nil {
		return nil, visible{}, err
	}
	v := visible{anonymous: want.Empty() || want.Has(perm.RepoDownload), ids: []int64{}}
	for id, set := range auth.Granted(ctx, p) {
		if want.Empty() || !set.Intersect(want).Empty() {
			v.ids = append(v.ids, id)
		}
	}
	slices.Sort(v.ids)
	return ctx, v, nil
}

// listRepos answers GET /api/repos: a keyset page by name of the repos where the caller holds a
// permission or that are public. ?after=&limit= page it; ?q= is a name prefix, ?format= and
// ?kind= keep one format or kind, ?perm=a,b keeps the repos where the caller holds one of the
// repository permissions. ?sort=published gives the limit repos published last (at most
// maxPublished, with ?perm= only, no next page). ?name=a&name=b gives those repos (at most a page).
func (a *API) listRepos(w http.ResponseWriter, r *http.Request, c *route.Call) {
	p, ctx, q := c.P, r.Context(), r.URL.Query()
	var want perm.Set
	if s := strings.TrimSpace(q.Get("perm")); s != "" {
		var err error
		if want, err = perm.Current().Parse(perm.ScopeRepo, strings.Split(s, ",")); err != nil {
			httpx.Error(w, r, http.StatusBadRequest, err.Error())
			return
		}
	}
	kind := q.Get("kind")
	if kind != "" && kind != repos.KindHosted && kind != repos.KindProxy && kind != repos.KindGroup {
		httpx.Error(w, r, http.StatusBadRequest, `kind must be "hosted", "proxy" or "group"`)
		return
	}
	ctx, v, err := a.visibleTo(ctx, p, want)
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	limit := httpx.Limit(r)
	var page []*repos.Repo
	more := false
	switch {
	case len(q["name"]) > 0:
		names := q["name"]
		if len(names) > 200 {
			httpx.Error(w, r, http.StatusBadRequest, "at most 200 names")
			return
		}
		if page, err = a.repos.Named(ctx, names); err != nil {
			route.Fail(w, r, err)
			return
		}
		slices.SortFunc(page, func(x, y *repos.Repo) int { return strings.Compare(x.Name, y.Name) })
	case q.Get("sort") == "published":
		if q.Get("after") != "" || q.Get("q") != "" || q.Get("format") != "" || kind != "" {
			httpx.Error(w, r, http.StatusBadRequest, "sort=published takes perm and limit only")
			return
		}
		if page, err = a.published(ctx, v, min(limit, maxPublished)); err != nil {
			route.Fail(w, r, err)
			return
		}
	case q.Get("sort") == "" || q.Get("sort") == "name":
		lq := repos.ListQuery{After: q.Get("after"), Prefix: strings.ToLower(strings.TrimSpace(q.Get("q"))),
			Format: q.Get("format"), Kind: kind, Limit: limit + 1}
		if page, err = a.pageByName(ctx, v, lq); err != nil {
			route.Fail(w, r, err)
			return
		}
		if len(page) > limit {
			page, more = page[:limit], true
		}
	default:
		httpx.Error(w, r, http.StatusBadRequest, `sort must be "name" or "published"`)
		return
	}
	out, err := a.repoItems(r.WithContext(ctx), p, page)
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	next := ""
	if more && len(page) > 0 {
		next = page[len(page)-1].Name
	}
	httpx.WriteJSON(w, http.StatusOK, route.Page[repoJSON]{Items: out, Next: route.Cursor(next)})
}

// pageByName returns the first lq.Limit repos of lq that v sees, by name: the lists merged, each
// read up to lq.Limit, so the merge holds every name before the lq.Limit-th of the union.
func (a *API) pageByName(ctx context.Context, v visible, lq repos.ListQuery) ([]*repos.Repo, error) {
	if v.all {
		return a.repos.PageAll(ctx, lq)
	}
	var lists [][]*repos.Repo
	if v.anonymous {
		l, err := a.repos.PageAnonymous(ctx, lq)
		if err != nil {
			return nil, err
		}
		lists = append(lists, l)
	}
	l, err := a.repos.PageIn(ctx, v.ids, lq)
	if err != nil {
		return nil, err
	}
	lists = append(lists, l)
	return merge(lists, lq.Limit, func(x, y *repos.Repo) int { return strings.Compare(x.Name, y.Name) }), nil
}

// published returns the limit repos that v sees, published last first.
func (a *API) published(ctx context.Context, v visible, limit int) ([]*repos.Repo, error) {
	if v.all {
		return a.repos.Published(ctx, false, nil, limit)
	}
	var lists [][]*repos.Repo
	if v.anonymous {
		l, err := a.repos.Published(ctx, true, nil, limit)
		if err != nil {
			return nil, err
		}
		lists = append(lists, l)
	}
	l, err := a.repos.Published(ctx, false, v.ids, limit)
	if err != nil {
		return nil, err
	}
	lists = append(lists, l)
	return merge(lists, limit, func(x, y *repos.Repo) int {
		return cmp.Or(y.LastPublishedAt.Compare(x.LastPublishedAt), cmp.Compare(y.ID, x.ID))
	}), nil
}

// merge merges lists, each sorted by order, into one, a repo once, at most limit.
func merge(lists [][]*repos.Repo, limit int, order func(x, y *repos.Repo) int) []*repos.Repo {
	var out []*repos.Repo
	seen := map[int64]bool{}
	for _, l := range lists {
		for _, r := range l {
			if !seen[r.ID] {
				seen[r.ID] = true
				out = append(out, r)
			}
		}
	}
	slices.SortFunc(out, order)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// repoItems is the JSON of the repos of one page that p sees (a repo without a permission is left
// out): the caller's permissions, the downloads of the window, and a group's counters. The config
// is left out: GET /api/repos/{name} has it.
func (a *API) repoItems(r *http.Request, p *auth.Principal, page []*repos.Repo) ([]repoJSON, error) {
	ctx := r.Context()
	ids := make([]int64, len(page))
	for i, repo := range page {
		ids[i] = repo.ID
	}
	recent, err := a.recentDownloads(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]repoJSON, 0, len(page))
	for _, repo := range page {
		perms, err := a.auth.RepoPerms(ctx, p, repo)
		if err != nil {
			return nil, err
		}
		if perms.Empty() {
			continue
		}
		j := a.toRepoJSON(repo, perms)
		j.Config = nil
		n := recent[repo.ID]
		j.Downloads = &n
		if repo.Kind == repos.KindGroup {
			if err := a.groupCounters(r, p, repo, &j); err != nil {
				return nil, err
			}
		}
		out = append(out, j)
	}
	return out, nil
}

// recentDownloads returns the downloads of the repos of ids in the window of the download counters
// (the last 30 days), as the last flush wrote them: one seek of download_window_repos per repo. A
// repo without downloads has no entry.
func (a *API) recentDownloads(ctx context.Context, ids []int64) (map[int64]int64, error) {
	return a.h.Downloads.Recent(ctx, ids)
}

// --- the Settings overview ---

// maxLargest is how many repos the overview's Largest repositories holds.
const maxLargest = 8

// countCap is where a bounded count stops: the overview says "10,000+".
const countCap = 10000

type boundedCount struct {
	Count int64 `json:"count"`
	More  bool  `json:"more"` // the count stopped at countCap
}

type overviewJSON struct {
	// Repos are the totals of every repo for a holder of all repositories (All), else of the repos
	// where the caller holds more than download.
	Repos struct {
		All   bool  `json:"all"`
		Count int64 `json:"count"`
		Files int64 `json:"files"`
		Size  int64 `json:"size"`
	} `json:"repos"`
	Largest     []repoJSON    `json:"largest"`          // the largest of those repos that store something
	PublicRepos int64         `json:"public_repos"`     // the public repos that are not groups
	People      *boundedCount `json:"people,omitempty"` // a holder of people.manage: the accounts
}

// overview answers GET /api/overview: the figures of the Settings overview, each a counter or a
// bounded query.
func (a *API) overview(w http.ResponseWriter, r *http.Request, c *route.Call) {
	p, ctx := c.P, r.Context()
	var mine perm.Set
	for _, id := range perm.Current().AllRepo().IDs() {
		if id != perm.RepoDownload {
			mine = mine.Union(perm.SetOf(id))
		}
	}
	ctx, v, err := a.visibleTo(ctx, p, mine)
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	var ids []int64 // nil: every repo
	if !v.all {
		ids = v.ids
	}
	var out overviewJSON
	out.Repos.All = v.all
	t, err := a.repos.TotalsOf(ctx, ids)
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	out.Repos.Count, out.Repos.Files, out.Repos.Size = t.Repos, t.Files, t.Bytes
	out.PublicRepos = t.Public
	if ids != nil {
		every, err := a.repos.TotalsOf(ctx, nil)
		if err != nil {
			route.Fail(w, r, err)
			return
		}
		out.PublicRepos = every.Public
	}
	largest, err := a.repos.Largest(ctx, ids, maxLargest)
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	if out.Largest, err = a.repoItems(r.WithContext(ctx), p, largest); err != nil {
		route.Fail(w, r, err)
		return
	}
	if p.Perms.Has(perm.PeopleManage) {
		n, err := dbq.New(a.h.DB.R).CountUsersUpTo(ctx, countCap+1)
		if err != nil {
			route.Fail(w, r, err)
			return
		}
		out.People = &boundedCount{Count: min(n, countCap), More: n > countCap}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
