package dbq

// Hand-written queries that sqlc cannot express. Each one is a named constant in
// Extra, so the plan guard (db.CheckPlans) checks it like a generated query.

import (
	"context"
	"iter"
)

// Extra maps each hand-written query name to its SQL.
var Extra = map[string]string{
	"StreamPackages": streamPackages,
}

// The deb_packages UNIQUE index (repo_id, distribution, component, architecture, package,
// version) delivers the rows in (package, version) order, so no sort is needed.
const streamPackages = `SELECT package, version, control, filename, size, md5, sha1, sha256
FROM deb_packages
WHERE repo_id = ? AND distribution = ? AND component = ? AND architecture = ?
ORDER BY package, version`

type StreamPackagesParams struct {
	RepoID       int64
	Distribution string
	Component    string
	Architecture string
}

type StreamPackagesRow struct {
	Package  string
	Version  string
	Control  string // control stanza without hash, Filename and Size fields
	Filename string
	Size     int64
	Md5      string
	Sha1     string
	Sha256   string
}

// StreamPackages yields the packages of one (dist, component, architecture) in (package,
// version) order without loading them all. A failed query or scan yields one error and stops.
func (q *Queries) StreamPackages(ctx context.Context, arg StreamPackagesParams) iter.Seq2[StreamPackagesRow, error] {
	return func(yield func(StreamPackagesRow, error) bool) {
		rows, err := q.db.QueryContext(ctx, streamPackages, arg.RepoID, arg.Distribution, arg.Component, arg.Architecture)
		if err != nil {
			yield(StreamPackagesRow{}, err)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var r StreamPackagesRow
			if err := rows.Scan(&r.Package, &r.Version, &r.Control, &r.Filename, &r.Size, &r.Md5, &r.Sha1, &r.Sha256); err != nil {
				yield(StreamPackagesRow{}, err)
				return
			}
			if !yield(r, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(StreamPackagesRow{}, err)
		}
	}
}
