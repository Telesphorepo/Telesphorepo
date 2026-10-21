package edge

import (
	"embed"

	"github.com/Telesphorepo/Telesphorepo/internal/db"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

//go:embed queries/*.sql
var queriesFS embed.FS

// component is the migrations of the module and its queries for the plan guard.
func component() db.Component {
	return db.Component{
		Migrations: db.Sub(migrationsFS, "migrations"),
		Queries:    db.Sub(queriesFS, "queries"),
		Allow: map[string]string{
			"CountOutbox":  "the counter of the outbox job counts its rows for the status page, every 5 s only while the page polls it and never per request; the table holds only the objects not written yet",
			"SumFileCount": "once per backfill start, for its progress; one row per repo",
		},
	}
}
