package repos

import (
	"context"
	"slices"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/namesearch"
)

// The lists of GET /api/repos. Each one reads one index (migration 0005_performance.sql): every
// repo, the public repos (anonymous read), or the repos of a caller's grants (a lookup per id).
// A list with a text reads the grams of the names instead (migration 0008_name_search.sql,
// internal/namesearch): one gram's rows of the same index shape, in the ranges of
// namesearch.Steps. A caller merges the lists it may see (Merge).

// ListQuery selects a keyset page of repos: the names after After that hold Text (folded; ""
// for any), of Format and Kind ("" for any), at most Limit. Without a text the page is in name
// order; with one, the names that start with it come first (namesearch.Compare).
type ListQuery struct {
	After, Text, Format, Kind string
	Limit                     int
}

// Order is the order of the pages of q: by name, or as a search of q.Text lists them.
func (q ListQuery) Order(a, b *Repo) int {
	if q.Text == "" {
		return strings.Compare(a.Name, b.Name)
	}
	return namesearch.Compare(q.Text, a.Name, b.Name)
}

// nameRange is the range [from, to) of the names after after.
func nameRange(after string) (from, to string) {
	from, to = "", "\x7f" // every name is below DEL
	if after != "" {
		from = after + "\x00" // the first string after after
	}
	return from, to
}

// gram returns the gram whose rows a search of q.Text reads (namesearch.Gram); ok is false when
// no name holds the text.
func (s *Service) gram(ctx context.Context, q ListQuery) (string, bool, error) {
	db := dbq.New(s.db.R)
	return namesearch.Gram(ctx, q.Text, func(ctx context.Context, g string, limit int64) (int64, error) {
		return db.CountRepoGram(ctx, dbq.CountRepoGramParams{Gram: g, Limit: limit})
	})
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
	db := dbq.New(s.db.R)
	if q.Text == "" {
		from, to := nameRange(q.After)
		n := int64(q.Limit)
		switch {
		case q.Format != "":
			return s.rows(as(func(r dbq.ListReposOfFormatRow) row { return row(r) })(db.ListReposOfFormat(ctx, dbq.ListReposOfFormatParams{Format: q.Format, From: from, To: to, Kind: q.Kind, Limit: n})))
		case q.Kind != "":
			return s.rows(as(func(r dbq.ListReposOfKindRow) row { return row(r) })(db.ListReposOfKind(ctx, dbq.ListReposOfKindParams{Kind: q.Kind, From: from, To: to, Limit: n})))
		}
		return s.rows(as(func(r dbq.ListReposInRangeRow) row { return row(r) })(db.ListReposInRange(ctx, dbq.ListReposInRangeParams{From: from, To: to, Limit: n})))
	}
	g, ok, err := s.gram(ctx, q)
	if err != nil || !ok {
		return nil, err
	}
	return namesearch.Page(ctx, q.Text, q.After, q.Limit, func(ctx context.Context, st namesearch.Step, limit int) ([]*Repo, error) {
		from, to, n := st.Lower(), st.To, int64(limit)
		switch {
		case q.Format != "":
			return s.rows(as(func(r dbq.ListRepoGramsOfFormatRow) row { return row(r) })(db.ListRepoGramsOfFormat(ctx, dbq.ListRepoGramsOfFormatParams{
				Gram: g, Format: q.Format, From: from, To: to, Text: q.Text, Kind: q.Kind, Limit: n})))
		case q.Kind != "":
			return s.rows(as(func(r dbq.ListRepoGramsOfKindRow) row { return row(r) })(db.ListRepoGramsOfKind(ctx, dbq.ListRepoGramsOfKindParams{
				Gram: g, Kind: q.Kind, From: from, To: to, Text: q.Text, Limit: n})))
		}
		return s.rows(as(func(r dbq.ListRepoGramsRow) row { return row(r) })(db.ListRepoGrams(ctx, dbq.ListRepoGramsParams{
			Gram: g, From: from, To: to, Text: q.Text, Limit: n})))
	})
}

// PageAnonymous returns the first q.Limit repos of q among the public repos.
func (s *Service) PageAnonymous(ctx context.Context, q ListQuery) ([]*Repo, error) {
	db := dbq.New(s.db.R)
	if q.Text == "" {
		from, to := nameRange(q.After)
		n := int64(q.Limit)
		if q.Format != "" {
			return s.rows(as(func(r dbq.ListAnonymousReposOfFormatRow) row { return row(r) })(db.ListAnonymousReposOfFormat(ctx, dbq.ListAnonymousReposOfFormatParams{Format: q.Format, From: from, To: to, Kind: q.Kind, Limit: n})))
		}
		return s.rows(as(func(r dbq.ListAnonymousReposRow) row { return row(r) })(db.ListAnonymousRepos(ctx, dbq.ListAnonymousReposParams{From: from, To: to, Kind: q.Kind, Limit: n})))
	}
	g, ok, err := s.gram(ctx, q)
	if err != nil || !ok {
		return nil, err
	}
	return namesearch.Page(ctx, q.Text, q.After, q.Limit, func(ctx context.Context, st namesearch.Step, limit int) ([]*Repo, error) {
		from, to, n := st.Lower(), st.To, int64(limit)
		if q.Format != "" {
			return s.rows(as(func(r dbq.ListAnonymousRepoGramsOfFormatRow) row { return row(r) })(db.ListAnonymousRepoGramsOfFormat(ctx, dbq.ListAnonymousRepoGramsOfFormatParams{
				Gram: g, Format: q.Format, From: from, To: to, Text: q.Text, Kind: q.Kind, Limit: n})))
		}
		return s.rows(as(func(r dbq.ListAnonymousRepoGramsRow) row { return row(r) })(db.ListAnonymousRepoGrams(ctx, dbq.ListAnonymousRepoGramsParams{
			Gram: g, From: from, To: to, Text: q.Text, Kind: q.Kind, Limit: n})))
	})
}

// PageIn returns the first q.Limit repos of q among the repos of ids (a caller's grants): one
// lookup per id for each range it reads.
func (s *Service) PageIn(ctx context.Context, ids []int64, q ListQuery) ([]*Repo, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	db := dbq.New(s.db.R)
	if q.Text == "" {
		from, to := nameRange(q.After)
		return s.rows(db.ListReposIn(ctx, ids, from, to, "", q.Format, q.Kind, int64(q.Limit)))
	}
	return namesearch.Page(ctx, q.Text, q.After, q.Limit, func(ctx context.Context, st namesearch.Step, limit int) ([]*Repo, error) {
		return s.rows(db.ListReposIn(ctx, ids, st.Lower(), st.To, q.Text, q.Format, q.Kind, int64(limit)))
	})
}

// Seen is what one caller sees of the repos: every repo (All), or the public ones (Public) and the
// repos of IDs.
type Seen struct {
	All    bool
	Public bool
	IDs    []int64
}

// PageSeen returns the first q.Limit repos of q that v sees: the lists merged, each read up to
// q.Limit, so the merge holds every repo before the q.Limit-th of the union.
func (s *Service) PageSeen(ctx context.Context, v Seen, q ListQuery) ([]*Repo, error) {
	if v.All {
		return s.PageAll(ctx, q)
	}
	var lists [][]*Repo
	if v.Public {
		l, err := s.PageAnonymous(ctx, q)
		if err != nil {
			return nil, err
		}
		lists = append(lists, l)
	}
	l, err := s.PageIn(ctx, v.IDs, q)
	if err != nil {
		return nil, err
	}
	lists = append(lists, l)
	return Merge(lists, q.Limit, q.Order), nil
}

// Merge merges lists, each sorted by order, into one, a repo once, at most limit.
func Merge(lists [][]*Repo, limit int, order func(x, y *Repo) int) []*Repo {
	var out []*Repo
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

// ByIDs returns the repos of ids that exist, by id: one query (the repos of a page of search
// counts, the groups of a member).
func (s *Service) ByIDs(ctx context.Context, ids []int64) (map[int64]*Repo, error) {
	out := make(map[int64]*Repo, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	found, err := s.rows(dbq.New(s.db.R).ListReposByIDs(ctx, slices.Compact(slices.Sorted(slices.Values(ids)))))
	if err != nil {
		return nil, err
	}
	for _, r := range found {
		out[r.ID] = r
	}
	return out, nil
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
