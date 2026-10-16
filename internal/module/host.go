package module

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"sync"

	"github.com/Telesphorepo/Telesphorepo/internal/access"
	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/blobs"
	"github.com/Telesphorepo/Telesphorepo/internal/ci"
	"github.com/Telesphorepo/Telesphorepo/internal/config"
	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/jobs"
	"github.com/Telesphorepo/Telesphorepo/internal/ops"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/rebuild"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
	"github.com/Telesphorepo/Telesphorepo/internal/storage"
)

// Kernel is the set of services that the kernel builds before any module starts. Every module
// gets the same Kernel.
type Kernel struct {
	Boot     *config.Boot
	Dev      bool   // "telesphorepo-server dev"
	Version  string // the stamped version of the server binary, changelog.Dev without a stamp
	DB       *db.DB
	Settings *settings.Service // the live settings: Current(), then a settings.Key or View
	Perms    *perm.Registry
	Audit    *audit.Log
	Jobs     *jobs.Runner
	Storage  *storage.Live
	Blobs    *blobs.Store
	Assets   *assets.Pipeline // Commit, CommitAll, Remove
	Reader   *assets.Reader   // the read path of hosted, proxy and group repos
	Uploads  *assets.Uploads  // resumable uploads
	Rebuilds *rebuild.Queue
	Repos    *repos.Service
	Ops      *ops.Service
	Auth     *auth.Service
	Access   *access.Engine // repo access decisions and the access report
	Formats  *format.Registry
	CI       *ci.Registry // the CI providers
	Builds   *ci.Builds   // the build records
	API      *route.API   // the shared helpers of JSON API handlers
}

// Host is what a module may use at runtime: the kernel services, a log tagged with the module
// ID, and the services that the modules it requires provide.
type Host struct {
	*Kernel
	Module string
	Log    *slog.Logger

	shared   *Shared
	requires map[string]bool
}

// Shared holds the services that modules provide to the modules that require them.
type Shared struct {
	mu     sync.Mutex
	byType map[reflect.Type]provided
}

type provided struct {
	module string
	value  any
}

// NewShared returns an empty set of provided services.
func NewShared() *Shared { return &Shared{byType: map[reflect.Type]provided{}} }

// NewHost returns the host of module m.
func NewHost(k *Kernel, m Module, log *slog.Logger, shared *Shared) *Host {
	req := map[string]bool{}
	for _, r := range m.Requires {
		req[r] = true
	}
	return &Host{Kernel: k, Module: m.ID, Log: log.With("module", m.ID), shared: shared, requires: req}
}

// Provide makes v available to the modules that require this module, as its dynamic type. It
// panics when another module provided a value of that type.
func (h *Host) Provide(v any) { h.provide(reflect.TypeOf(v), v) }

// ProvideAs makes v available as type T, an interface that the module's service implements (e.g.
// publish.Minter), so that a module that requires this one calls Use[T].
func ProvideAs[T any](h *Host, v T) { h.provide(reflect.TypeFor[T](), v) }

func (h *Host) provide(t reflect.Type, v any) {
	h.shared.mu.Lock()
	defer h.shared.mu.Unlock()
	if p, ok := h.shared.byType[t]; ok {
		panic(fmt.Sprintf("module %s: %s is provided by module %s already", h.Module, t, p.module))
	}
	h.shared.byType[t] = provided{h.Module, v}
}

// Use returns the service of type T that a module that h requires provided. It panics when no
// such module provided one.
func Use[T any](h *Host) T {
	t := reflect.TypeFor[T]()
	h.shared.mu.Lock()
	p, ok := h.shared.byType[t]
	h.shared.mu.Unlock()
	if !ok || !h.requires[p.module] {
		panic(fmt.Sprintf("module %s: no required module provides %s", h.Module, t))
	}
	return p.value.(T)
}

// Go runs fn as an on-demand task of the module (jobs.Runner.Go).
func (h *Host) Go(name string, fn func(ctx context.Context)) { h.Jobs.Go(h.Module, name, fn) }

// Context returns the module's context: it ends when the module stops.
func (h *Host) Context() context.Context { return h.Jobs.Context(h.Module) }
