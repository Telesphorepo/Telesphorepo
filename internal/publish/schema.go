package publish

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
			"CountTrustsUpTo": "a bounded count: it stops at its cap (the Settings overview says 10,000+)",
		},
	}
}
