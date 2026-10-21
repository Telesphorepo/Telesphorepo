package dbq

// Hand-written queries that sqlc cannot express. Each one is a named constant in
// Extra, so the plan guard (db.CheckPlans) checks it like a generated query.

import "context"

// Extra maps each hand-written query name to its SQL.
var Extra = map[string]string{
	"SearchPackageIDs": searchPackageIDs,
}

// The FTS5 trigram search walks nuget_packages_fts in rowid order, which is the keyset order:
// sqlc cannot reference rowid or "nuget_packages_fts MATCH".
const searchPackageIDs = `SELECT p.id, p.lower_id
FROM nuget_packages_fts f JOIN nuget_packages p ON p.id = f.rowid
WHERE nuget_packages_fts MATCH ? AND f.rowid > ? AND p.repo_id = ?
ORDER BY f.rowid LIMIT ?`

type SearchPackageIDsParams struct {
	Match  string // an FTS5 query, e.g. the search text quoted as a phrase
	After  int64  // the id of the last hit of the previous page; 0 for the first page
	RepoID int64
	Limit  int64
}

type SearchPackageIDsRow struct {
	ID      int64
	LowerID string
}

// SearchPackageIDs returns the packages of a repo whose lowered id matches, in row id order.
func (q *Queries) SearchPackageIDs(ctx context.Context, arg SearchPackageIDsParams) ([]SearchPackageIDsRow, error) {
	rows, err := q.db.QueryContext(ctx, searchPackageIDs, arg.Match, arg.After, arg.RepoID, arg.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SearchPackageIDsRow
	for rows.Next() {
		var i SearchPackageIDsRow
		if err := rows.Scan(&i.ID, &i.LowerID); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}
