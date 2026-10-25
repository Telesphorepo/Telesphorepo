package dbq

// Hand-written queries that sqlc cannot express. Each one is a named constant in
// Extra, so the plan guard (db.CheckPlans) checks it like a generated query.

import "context"

// Extra maps each hand-written query name to its SQL.
var Extra = map[string]string{
	"SearchPackages":      searchPackages,
	"CountSearchPackages": countSearchPackages,
	"ListPackagesFrom":    listPackagesFrom,
	"CountPackagesFrom":   countPackagesFrom,
}

// The search index nuget_packages_search keys each package by repo_id << SearchShift | id, so
// the packages of one repo are the rowids [repo_id << SearchShift, (repo_id + 1) << SearchShift)
// and a search of one repo is a seek in the trigram index (sqlc cannot reference rowid or
// "nuget_packages_search MATCH").
const SearchShift = 40

// A search shows a package with its newest listed version of the variant (nuget_package_latest:
// 0 release, 1 release2, 2 pre, 3 pre2), and leaves out a package without one and a package that
// a repo of the JSON array of repo ids before it in the group has.
const (
	searchLatest = `CASE ?4 WHEN 0 THEN l.release WHEN 1 THEN l.release2 WHEN 2 THEN l.pre ELSE l.pre2 END AS latest`
	searchShown  = `latest IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM nuget_packages o WHERE o.repo_id IN (SELECT value FROM json_each(?5)) AND o.lower_id = p.lower_id)`
)

// The packages of a repo whose lowered id holds ?6 (?1, its FTS5 phrase), from the key ?2 below
// the key ?3, in key order.
const searchFrom = `FROM nuget_packages_search f JOIN nuget_packages p ON p.id = (f.rowid & 1099511627775)
  JOIN nuget_package_latest l ON l.package = p.id
WHERE nuget_packages_search MATCH ?1 AND f.rowid >= ?2 AND f.rowid < ?3 AND instr(p.lower_id, ?6) > 0 AND ` + searchShown

const searchPackages = `SELECT f.rowid, p.lower_id, ` + searchLatest + `
` + searchFrom + `
ORDER BY f.rowid LIMIT ?7`

const countSearchPackages = `SELECT count(*) FROM (SELECT ` + searchLatest + `
` + searchFrom + `
LIMIT ?7)`

// The packages of the repo ?1 with a lowered id from ?2 (inclusive) below ?3, in id order.
const prefixFrom = `FROM nuget_packages p JOIN nuget_package_latest l ON l.package = p.id
WHERE p.repo_id = ?1 AND p.lower_id >= ?2 AND p.lower_id < ?3 AND ` + searchShown

const listPackagesFrom = `SELECT p.id, p.lower_id, ` + searchLatest + `
` + prefixFrom + `
ORDER BY p.lower_id LIMIT ?6`

const countPackagesFrom = `SELECT count(*) FROM (SELECT ` + searchLatest + `
` + prefixFrom + `
LIMIT ?6)`

// SearchFilter is what a search shows of one repo.
type SearchFilter struct {
	// Match is an FTS5 query (the search text quoted as a phrase) and Q the lowered text it must
	// hold, for a search through the trigram index; From and To are then the bounds of the keys
	// (int64). With Match empty, From and To bound the lowered ids (string) of the repo RepoID.
	Match, Q string
	RepoID   int64
	From, To any
	Variant  int64  // 0 release, 1 release2, 2 pre, 3 pre2
	Shadow   string // JSON array of the ids of the repos whose packages are left out
}

// SearchHit is a package that a search shows: its key in the index (the trigram search) or its
// id, its lowered id and the sort key of its newest version of the variant.
type SearchHit struct {
	Key     int64
	LowerID string
	Latest  string
}

// query returns the query of f (list or count) and its arguments.
func (f SearchFilter) query(trigram, prefix string, limit int64) (string, []any) {
	if f.Match == "" {
		return prefix, []any{f.RepoID, f.From, f.To, f.Variant, f.Shadow, limit}
	}
	return trigram, []any{f.Match, f.From, f.To, f.Variant, f.Shadow, f.Q, limit}
}

// SearchPackages returns up to limit packages that f shows, in key order (the trigram search) or
// in id order.
func (q *Queries) SearchPackages(ctx context.Context, f SearchFilter, limit int64) ([]SearchHit, error) {
	query, args := f.query(searchPackages, listPackagesFrom, limit)
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SearchHit
	for rows.Next() {
		var i SearchHit
		if err := rows.Scan(&i.Key, &i.LowerID, &i.Latest); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// CountSearchPackages counts the packages that f shows, up to limit.
func (q *Queries) CountSearchPackages(ctx context.Context, f SearchFilter, limit int64) (int64, error) {
	query, args := f.query(countSearchPackages, countPackagesFrom, limit)
	var n int64
	err := q.db.QueryRowContext(ctx, query, args...).Scan(&n)
	return n, err
}
