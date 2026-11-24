package dbq

// Hand-written queries that sqlc cannot express. Each one is a named constant in
// Extra, so the plan guard (db.CheckPlans) checks it like a generated query.

import "context"

// Extra maps each hand-written query name to its SQL.
var Extra = map[string]string{
	"CatalogPage": catalogPage,
}

// A page of the catalog of a list of repos (the JSON array ?1 of [repo id, the image the repo's
// part starts after], in catalog order): one repo after another, up to ?2 images of each by name
// through the primary key, while the images so far are fewer than ?2. The query reads fewer than
// 2 x ?2 rows and a seek per repo it reaches, whatever the repos hold.
const catalogPage = `WITH RECURSIVE w(k, rest, names) AS (
  SELECT 0, ?2, (SELECT json_group_array(full_name) FROM (SELECT i.full_name FROM docker_images i
    WHERE i.repo_id = json_extract(?1, '$[0][0]') AND i.image > json_extract(?1, '$[0][1]')
    ORDER BY i.image LIMIT ?2))
  UNION ALL
  SELECT w.k + 1, w.rest - json_array_length(w.names), (SELECT json_group_array(full_name) FROM (SELECT i.full_name FROM docker_images i
    WHERE i.repo_id = json_extract(?1, '$[' || (w.k + 1) || '][0]') AND i.image > json_extract(?1, '$[' || (w.k + 1) || '][1]')
    ORDER BY i.image LIMIT ?2))
  FROM w WHERE w.rest > json_array_length(w.names) AND w.k + 1 < json_array_length(?1)
)
SELECT k, names FROM w`

// CatalogRow is the images of one repo of CatalogPage: K is its place in the list, Names a JSON
// array of full names.
type CatalogRow struct {
	K     int64
	Names string
}

// CatalogPage returns the images of the repos of list (JSON, see catalogPage), up to limit of
// each, while the images so far are fewer than limit.
func (q *Queries) CatalogPage(ctx context.Context, list string, limit int64) ([]CatalogRow, error) {
	rows, err := q.db.QueryContext(ctx, catalogPage, list, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CatalogRow
	for rows.Next() {
		var r CatalogRow
		if err := rows.Scan(&r.K, &r.Names); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
