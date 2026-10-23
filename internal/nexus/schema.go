package nexus

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
			"ListRepoRehearsals": "startup: loads every rehearsal repo once into memory; one row per rehearsal repo",
			"ListRepoMoves":      "startup: loads every moving repo once into memory; one row per moving repo",
		},
	}
}
