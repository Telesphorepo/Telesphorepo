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
			"ListTrustsInRepos": "sorts the policies that list a repo of the caller's scope (a seek per repo of the scope through publish_trust_repos_repo), never all policies", "CountTrustsUpTo": "a bounded count: it stops at its cap (the Settings overview says 10,000+)",
		},
	}
}
