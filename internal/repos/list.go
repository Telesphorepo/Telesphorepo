package repos

import (
	"context"
	"regexp"

	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
)

// The lists of GET /api/repos. Each one reads one index (migration 0005_performance.sql): every
// repo, the public repos (anonymous read), or the repos of a caller's grants (a lookup per id).
// A caller merges the lists it may see.

// ListQuery selects a keyset page of repos by name: the names after After that start with Prefix,
// of Format and Kind ("" for any), at most Limit.
type ListQuery struct {
	After, Prefix, Format, Kind string
	Limit                       int
}

// prefixRE is what a name prefix can hold: the characters of NameRE. A prefix with other
// characters matches no repo.
var prefixRE = regexp.MustCompile(`^[a-z0-9._-]{0,64}$`)

// nameRange is the range [from, to) of the names after after that start with prefix. ok is false
// for a prefix that no name starts with.
func nameRange(after, prefix string) (from, to string, ok bool) {
	if !prefixRE.MatchString(prefix) {
		return "", "", false
	}
	from, to = prefix, "\x7f" // every name is below DEL
	if n := len(prefix); n > 0 {
		to = prefix[:n-1] + string([]byte{prefix[n-1] + 1})
	}
	if after != "" && after+"\x00" > from {
		from = after + "\x00" // the first string after after
	}
	return from, to, true
}

func (s *Service) rows(rs []row, err error) ([]*Repo, error) {
	if err != nil {
		return nil, err
	}
	out := make([]*Repo, 0, len(rs))
	for _, r := range rs {
		repo, _ := s.counted(fromRow(r, nil))
		out = append(out, repo)
	}
	return out, nil
}

// as turns the rows of a list query into rows of GetRepoByName, which have the same columns.
func as[T any](to func(T) row) func([]T, error) ([]row, error) {
	return func(rs []T, err error) ([]row, error) {
		if err != nil {
			return nil, err
		}
		out := make([]row, len(rs))
		for i, r := range rs {
			out[i] = to(r)
		}
		return out, nil
	}
}

// PageAll returns the first q.Limit repos of q among every repo.
func (s *Service) PageAll(ctx context.Context, q ListQuery) ([]*Repo, error) {
	from, to, ok := nameRange(q.After, q.Prefix)
	if !ok {
		return nil, nil
	}
	db, n := dbq.New(s.db.R), int64(q.Limit)
	switch {
	case q.Format != "":
		return s.rows(as(func(r dbq.ListReposOfFormatRow) row { return row(r) })(db.ListReposOfFormat(ctx, dbq.ListReposOfFormatParams{Format: q.Format, From: from, To: to, Kind: q.Kind, Limit: n})))
	case q.Kind != "":
		return s.rows(as(func(r dbq.ListReposOfKindRow) row { return row(r) })(db.ListReposOfKind(ctx, dbq.ListReposOfKindParams{Kind: q.Kind, From: from, To: to, Limit: n})))
	}
	return s.rows(as(func(r dbq.ListReposInRangeRow) row { return row(r) })(db.ListReposInRange(ctx, dbq.ListReposInRangeParams{From: from, To: to, Limit: n})))
}

// PageAnonymous returns the first q.Limit repos of q among the public repos.
func (s *Service) PageAnonymous(ctx context.Context, q ListQuery) ([]*Repo, error) {
	from, to, ok := nameRange(q.After, q.Prefix)
	if !ok {
		return nil, nil
	}
	db, n := dbq.New(s.db.R), int64(q.Limit)
	if q.Format != "" {
		return s.rows(as(func(r dbq.ListAnonymousReposOfFormatRow) row { return row(r) })(db.ListAnonymousReposOfFormat(ctx, dbq.ListAnonymousReposOfFormatParams{Format: q.Format, From: from, To: to, Kind: q.Kind, Limit: n})))
	}
	return s.rows(as(func(r dbq.ListAnonymousReposRow) row { return row(r) })(db.ListAnonymousRepos(ctx, dbq.ListAnonymousReposParams{From: from, To: to, Kind: q.Kind, Limit: n})))
}

// PageIn returns the first q.Limit repos of q among the repos of ids (a caller's grants).
func (s *Service) PageIn(ctx context.Context, ids []int64, q ListQuery) ([]*Repo, error) {
	from, to, ok := nameRange(q.After, q.Prefix)
	if !ok || len(ids) == 0 {
		return nil, nil
	}
	return s.rows(dbq.New(s.db.R).ListReposIn(ctx, ids, from, to, q.Format, q.Kind, int64(q.Limit)))
}

// Published returns the limit repos published last, among every repo (ids nil and anonymous
// false), the public repos (anonymous), or the repos of ids.
func (s *Service) Published(ctx context.Context, anonymous bool, ids []int64, limit int) ([]*Repo, error) {
	db, n := dbq.New(s.db.R), int64(limit)
	switch {
	case anonymous:
		return s.rows(as(func(r dbq.ListAnonymousReposByPublishedRow) row { return row(r) })(db.ListAnonymousReposByPublished(ctx, n)))
	case ids != nil:
		if len(ids) == 0 {
			return nil, nil
		}
		return s.rows(db.ListReposInByPublished(ctx, ids, n))
	}
	return s.rows(as(func(r dbq.ListReposByPublishedRow) row { return row(r) })(db.ListReposByPublished(ctx, n)))
}

// Named returns the repos of names that exist, in no order.
func (s *Service) Named(ctx context.Context, names []string) ([]*Repo, error) {
	if len(names) == 0 {
		return nil, nil
	}
	return s.rows(as(func(r dbq.ListReposNamedRow) row { return row(r) })(dbq.New(s.db.R).ListReposNamed(ctx, names)))
}

// Largest returns the limit repos that store the most bytes, among every repo (ids nil) or the
// repos of ids; a repo that stores nothing is left out.
func (s *Service) Largest(ctx context.Context, ids []int64, limit int) ([]*Repo, error) {
	db, n := dbq.New(s.db.R), int64(limit)
	if ids == nil {
		return s.rows(as(func(r dbq.ListLargestReposRow) row { return row(r) })(db.ListLargestRepos(ctx, n)))
	}
	if len(ids) == 0 {
		return nil, nil
	}
	return s.rows(db.ListLargestReposIn(ctx, ids, n))
}

// Totals are the repos of a set with their files and stored bytes.
type Totals struct {
	Repos, Files, Bytes int64
	Public              int64 // every repo only: the public repos that are not groups
}

// TotalsOf returns the totals of every repo (ids nil: the counter row that triggers keep) or of
// the repos of ids.
func (s *Service) TotalsOf(ctx context.Context, ids []int64) (Totals, error) {
	db := dbq.New(s.db.R)
	if ids == nil {
		t, err := db.GetRepoTotals(ctx)
		return Totals{Repos: t.Repos, Files: t.Files, Bytes: t.Bytes, Public: t.Public}, err
	}
	if len(ids) == 0 {
		return Totals{}, nil
	}
	t, err := db.SumReposIn(ctx, ids)
	return Totals{Repos: t.Repos, Files: t.Files, Bytes: t.Bytes}, err
}
