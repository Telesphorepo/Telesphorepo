// Package telesphorepo holds the files of the repo root that the programs embed. It imports
// nothing, so the server and the CLI can both use it.
package telesphorepo

import _ "embed"

// ChangesTOML is the change log changes.toml. Package changelog reads it.
//
//go:embed changes.toml
var ChangesTOML []byte
