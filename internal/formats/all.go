// Package formats lists the repository formats built into the binary. It is the only place
// that names them; import it for its side effect.
package formats

import (
	"github.com/Telesphorepo/Telesphorepo/internal/debian"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/maven"
	"github.com/Telesphorepo/Telesphorepo/internal/raw"
)

func init() {
	format.Register(maven.New, debian.New, raw.New)
}
