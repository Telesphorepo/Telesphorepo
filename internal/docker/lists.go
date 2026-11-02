package docker

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/docker/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
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
	return f.h.Access.GroupMembers(r.Context(), r, c.P, c.Repo)
}

// catalog answers GET /v2/_catalog?n=&last=: the images of the repos that the credential behind
// the request downloads, by full name after last. The scopes of a bearer token do not limit it.
func (f *Format) catalog(w http.ResponseWriter, r *http.Request, c *route.Call) {
	w.Header().Set(apiVersionHeader, apiVersion)
	n, oe := pageSize(r)
	if oe != nil {
		writeOCI(w, oe)
		return
	}
	ids, all, err := f.downloadable(r, c.P)
	if err != nil {
		fail(w, r, err)
		return
	}
	last := r.URL.Query().Get("last")
	var out []string
	if all {
		out, err = f.catalogAll(r.Context(), last, n)
	} else {
		out, err = f.catalogOf(r.Context(), ids, last, n)
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	if n > 0 && len(out) == n {
		nextLink(w, "/v2/_catalog", n, out[n-1])
	}
	httpx.WriteJSON(w, http.StatusOK, map[string][]string{"repositories": out})
}

// catalogAll returns at most n images of every repo, by full name after last, through
// docker_images_full: one range read.
func (f *Format) catalogAll(ctx context.Context, last string, n int) ([]string, error) {
	out := []string{}
	if n == 0 {
		return out, nil
	}
	rows, err := f.q.ListCatalog(ctx, dbq.ListCatalogParams{After: last, Limit: int64(n)})
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		out = append(out, row.FullName)
	}
	return out, nil
}

// idBatch is how many repo ids one ReposWithImages reads.
const idBatch = 1000

// catalogOf returns at most n images of the repos ids, by full name after last. It walks the
// repos of ids that hold an image in the order of "<name>/" (a repo name has no '/', so that is
// the order of their full names) and reads each repo's images through the primary key: a page
// reads one query per repo it lists from, and never a row of a repo outside ids.
func (f *Format) catalogOf(ctx context.Context, ids []int64, last string, n int) ([]string, error) {
	out := []string{}
	if n == 0 || len(ids) == 0 {
		return out, nil
	}
	type repo struct {
		id     int64
		prefix string // "<name>/"
	}
	var list []repo
	for batch := range slices.Chunk(ids, idBatch) {
		rows, err := f.q.ReposWithImages(ctx, batch)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			list = append(list, repo{row.ID, row.Name + "/"})
		}
	}
	slices.SortFunc(list, func(a, b repo) int { return strings.Compare(a.prefix, b.prefix) })
	// The first repo with a full name after last: the repo of last, or the first one after it.
	i := sort.Search(len(list), func(i int) bool {
		return list[i].prefix > last || strings.HasPrefix(last, list[i].prefix)
	})
	for ; i < len(list) && len(out) < n; i++ {
		after := ""
		if image, ok := strings.CutPrefix(last, list[i].prefix); ok {
			after = image
		}
		names, err := f.q.ListRepoImages(ctx, dbq.ListRepoImagesParams{RepoID: list[i].id, After: after, Limit: int64(n - len(out))})
		if err != nil {
			return nil, err
		}
		out = append(out, names...)
	}
	return out, nil
}

// downloadable returns the repos whose files the credential behind p downloads from the client
// address of r: anonymous (and a bearer token without a credential) reads the public repos, a
// publish session also its own repo, a password, a read token or a device the grants of its
// user, and every caller the repos whose allowed networks hold the address. A holder of all
// repositories gets all=true.
func (f *Format) downloadable(r *http.Request, p *auth.Principal) (ids []int64, all bool, err error) {
	ctx := r.Context()
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
	if err != nil || all {
		return nil, all, err
	}
	ids = append(ids, f.h.Access.NetworkRepos(ctx)...)
	return slices.Compact(slices.Sorted(slices.Values(ids))), false, nil
}
