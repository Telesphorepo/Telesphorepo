// Package modules is the one list of the modules of the server, in start order. It is the only
// place that names a module: a new format is its package plus one line here.
package modules

import (
	"github.com/Telesphorepo/Telesphorepo/internal/activity"
	"github.com/Telesphorepo/Telesphorepo/internal/api"
	"github.com/Telesphorepo/Telesphorepo/internal/cargo"
	"github.com/Telesphorepo/Telesphorepo/internal/debian"
	"github.com/Telesphorepo/Telesphorepo/internal/docker"
	"github.com/Telesphorepo/Telesphorepo/internal/edge"
	"github.com/Telesphorepo/Telesphorepo/internal/maven"
	"github.com/Telesphorepo/Telesphorepo/internal/module"
	"github.com/Telesphorepo/Telesphorepo/internal/move"
	"github.com/Telesphorepo/Telesphorepo/internal/netrules"
	"github.com/Telesphorepo/Telesphorepo/internal/nexus"
	"github.com/Telesphorepo/Telesphorepo/internal/npm"
	"github.com/Telesphorepo/Telesphorepo/internal/oidc"
	"github.com/Telesphorepo/Telesphorepo/internal/publish"
	"github.com/Telesphorepo/Telesphorepo/internal/raw"
	"github.com/Telesphorepo/Telesphorepo/internal/signing"
)

// All returns the modules in start order. A module starts after the modules it requires and
// stops before them.
func All() []module.Module {
	return []module.Module{
		api.Module(),      // core
		activity.Module(), // audit
		move.Module(),     // storage
		netrules.Module(), // network
		signing.Module(),
		publish.Module(),
		edge.Module(),
		oidc.Module(), // sso
		nexus.Module(),
		maven.Module(),
		debian.Module(),
		raw.Module(),
		docker.Module(),
		cargo.Module(),
		npm.Module(),
	}
}
