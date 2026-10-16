package netrules

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
			"ListTrustedProxyLists":   "reads the whole table at startup; it holds at most one row per IP list",
			"DeleteTrustedProxyLists": "an admin replaces the whole table; it holds at most one row per IP list",
		},
	}
}
