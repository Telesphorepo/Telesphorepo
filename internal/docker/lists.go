package docker

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/Telesphorepo/Telesphorepo/internal/access"
	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/docker/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
)

const (
	defaultPage = 1000  // n of a list without n
	maxPage     = 10000 // the largest n
)

// pageSize reads ?n=: absent is defaultPage, above maxPage is maxPage. A value that is not a
// number or is negative answers 400.
func pageSize(r *http.Request) (int, *ociError) {
	s := r.URL.Query().Get("n")
	if s == "" {
		return defaultPage, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, ociErr(http.StatusBadRequest, codePaginationInvalid, "n must be a number of 0 or more")
	}
	return min(n, maxPage), nil
}

// nextLink sets the Link header of the next page of path.
func nextLink(w http.ResponseWriter, path string, n int, last string) {
	w.Header().Set("Link", "<"+path+"?n="+strconv.Itoa(n)+"&last="+url.QueryEscape(last)+`>; rel="next"`)
}

// tagsList answers GET <name>/tags/list?n=&last=: the tags of the image in name order after
// last. A full page adds the Link of the next one. A proxy lists the remote's tags; a group
// merges the tags of its readable members.
func (f *Format) tagsList(w http.ResponseWriter, r *http.Request, c *route.Call, t target) {
	n, oe := pageSize(r)
	if oe != nil {
		writeOCI(w, oe)
		return
	}
	switch c.Repo.Kind {
	case repos.KindProxy:
		if n == 0 {
			writeTags(w, t.name, []string{})
			return
		}
		f.proxyTags(w, r, c, t, n)
	case repos.KindGroup:
		members, err := f.readableMembers(r, c)
		if err != nil {
			fail(w, r, err)
			return
		}
		f.localTags(w, r, members, t, n)
	default:
		f.localTags(w, r, []*repos.Repo{c.Repo}, t, n)
	}
}

// writeTags writes the tags list answer of name.
func writeTags(w http.ResponseWriter, name string, tags []string) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"name": name, "tags": tags})
}

// readableMembers returns the members of the group c.Repo that the caller reads, in read order.
func (f *Format) readableMembers(r *http.Request, c *route.Call) ([]*repos.Repo, error) {
	members, err := f.h.Repos.MemberRepos(r.Context(), c.Repo)
	if err != nil {
		return nil, err
	}
	var out []*repos.Repo
	for _, m := range members {
		ok, err := f.h.Access.Allows(r.Context(), r, c.P, m, access.Read, perm.RepoDownload)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, m)
		}
	}
	return out, nil
}

// catalog answers GET /v2/_catalog?n=&last=: the images of the repos that the credential behind
// the request downloads, by full name after last. The scopes of a bearer token do not limit it.
// A row of a repo the caller cannot read moves the bound past that repo, one seek per repo.
func (f *Format) catalog(w http.ResponseWriter, r *http.Request, c *route.Call) {
	w.Header().Set(apiVersionHeader, apiVersion)
	n, oe := pageSize(r)
	if oe != nil {
		writeOCI(w, oe)
		return
	}
	ctx := r.Context()
	readable, all, err := f.downloadable(r, c.P)
	if err != nil {
		fail(w, r, err)
		return
	}
	out := []string{}
	after := r.URL.Query().Get("last")
	for len(out) < n {
		want := n - len(out)
		rows, err := f.q.ListCatalog(ctx, dbq.ListCatalogParams{After: after, Limit: int64(want)})
		if err != nil {
			fail(w, r, err)
			return
		}
		skipped := false
		for _, row := range rows {
			if all || readable[row.RepoID] {
				out = append(out, row.FullName)
				after = row.FullName
				continue
			}
			after, _ = assets.PrefixEnd(repoOfName(row.FullName) + "/")
			skipped = true
			break
		}
		if !skipped && len(rows) < want {
			break // the end of the images
		}
	}
	if n > 0 && len(out) == n {
		nextLink(w, "/v2/_catalog", n, out[n-1])
	}
	httpx.WriteJSON(w, http.StatusOK, map[string][]string{"repositories": out})
}

// downloadable returns the repos whose files the credential behind p downloads, without the
// repos a guard refuses to r: anonymous (and a bearer token without a credential) reads the
// anonymous repos, a publish session also its own repo, a password, a read token or a device the
// grants of its user.
func (f *Format) downloadable(r *http.Request, p *auth.Principal) (map[int64]bool, bool, error) {
	ctx := r.Context()
	var ids []int64
	var all bool
	var err error
	switch {
	case p.Is(auth.CredPublish):
		if ids, _, err = f.h.Auth.DownloadableRepoIDs(ctx, nil); err != nil {
			return nil, false, err
		}
		ids = append(ids, p.Publish.RepoID)
	case p.Anonymous():
		ids, all, err = f.h.Auth.DownloadableRepoIDs(ctx, nil)
	default:
		ids, all, err = f.h.Auth.DownloadableRepoIDs(ctx, p)
	}
	if err != nil {
		return nil, false, err
	}
	refused := f.h.Access.RefusedRepos(r)
	if all && len(refused) == 0 {
		return nil, true, nil
	}
	out := map[int64]bool{}
	if all {
		list, err := f.h.Repos.List(ctx)
		if err != nil {
			return nil, false, err
		}
		for _, repo := range list {
			ids = append(ids, repo.ID)
		}
	}
	for _, id := range ids {
		if !refused[id] {
			out[id] = true
		}
	}
	return out, false, nil
}
