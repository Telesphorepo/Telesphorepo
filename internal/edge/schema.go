package edge

import (
	"embed"

	"github.com/Telesphorepo/Telesphorepo/internal/db"
)

//go:embed schema.sql
var schema string

//go:embed queries/*.sql
var queriesFS embed.FS

// component is the schema of the module and its queries for the plan guard.
func component() db.Component {
	return db.Component{
		Schema:  schema,
		Queries: db.Sub(queriesFS, "queries"),
		Allow: map[string]string{
			"CountOutbox":  "the outbox job counts its rows once per pass for the status page, never on a request path; the table holds only the objects not written yet",
			"SumFileCount": "once per backfill start, for its progress; one row per repo",
		},
	}
}
