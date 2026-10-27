// Package server builds the kernel, runs the lifecycle of every module (internal/modules) and
// serves HTTP.
package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Telesphorepo/Telesphorepo/changelog"
	"github.com/Telesphorepo/Telesphorepo/internal/access"
	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/blobs"
	"github.com/Telesphorepo/Telesphorepo/internal/ci"
	"github.com/Telesphorepo/Telesphorepo/internal/config"
	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/jobs"
	"github.com/Telesphorepo/Telesphorepo/internal/module"
	"github.com/Telesphorepo/Telesphorepo/internal/modules"
	"github.com/Telesphorepo/Telesphorepo/internal/ops"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/proxy"
	"github.com/Telesphorepo/Telesphorepo/internal/rebuild"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
	"github.com/Telesphorepo/Telesphorepo/internal/stats"
	"github.com/Telesphorepo/Telesphorepo/internal/storage"
	"github.com/Telesphorepo/Telesphorepo/web"
)

// Services exposes the kernel and the module manifests to Options.Init and to the offline
// commands.
type Services struct {
	Boot      *config.Boot
	Settings  *settings.Service
	Auth      *auth.Service
	Repos     *repos.Service
	DB        *db.DB
	Assets    *assets.Pipeline
	Builds    *ci.Builds
	Formats   *format.Registry
	Manifests []*module.Manifest
}

// Options customises RunWith. "telesphorepo-server serve" sets Version only.
type Options struct {
	// Dev applies the Dev default of every setting (see settings.Options.Dev) and makes the second
	// factor optional (auth.Service.SetDevMode). Only "telesphorepo-server dev" sets it.
	Dev bool
	// Init runs after every module's Init and routes, and before the default first-run admin
	// bootstrap (so it may create its own admin), the start of the jobs and listening.
	Init func(ctx context.Context, s *Services) error
	// Ready runs once the listener is bound.
	Ready func(addr net.Addr)
	// TextLogs uses human-readable text logs instead of JSON.
	TextLogs bool
	// Version is the stamped version of the server binary. "" is changelog.Dev.
	Version string
}

// VersionHeader is the response header that carries the version of the server on every answer.
const VersionHeader = "Telesphorepo-Version"

// Built is what the module list declares: the manifests in start order, the formats, the
// permission registry, the audit actions, the settings groups and the CI providers.
type Built struct {
	Manifests []*module.Manifest
	Formats   *format.Registry
	Perms     *perm.Registry
	Actions   *audit.Actions
	Groups    []*settings.Group
	CI        *ci.Registry
}

var (
	buildOnce sync.Once
	built     *Built
	buildErr  error
)

// Build runs every module's Register once per process: it builds the manifests, the permission
// registry (then fixed), the audit actions, the settings groups, the format registry and the CI
// provider registry. Later calls return the same result.
func Build() (*Built, error) {
	buildOnce.Do(func() {
		mfs := module.Build(modules.All())
		perms := perm.NewRegistry()
		for _, m := range mfs {
			for _, p := range m.Permissions {
				if err := perms.Add(m.Module.ID, p.Subsystem, p.Defs...); err != nil {
					buildErr = fmt.Errorf("module %s: %w", m.Module.ID, err)
					return
				}
			}
		}
		perms.Fix()
		actions := audit.NewActions()
		providers := ci.NewRegistry()
		var specs []format.Spec
		var groups []*settings.Group
		for _, m := range mfs {
			if err := actions.Add(m.AuditActions, perms.HasSubsystem); err != nil {
				buildErr = fmt.Errorf("module %s: %w", m.Module.ID, err)
				return
			}
			for _, p := range m.CIProviders {
				if err := providers.Add(p); err != nil {
					buildErr = fmt.Errorf("module %s: %w", m.Module.ID, err)
					return
				}
			}
			groups = append(groups, m.Settings...)
			specs = append(specs, m.Formats...)
		}
		reg, err := format.NewRegistry(specs)
		if err != nil {
			buildErr = err
			return
		}
		built = &Built{Manifests: mfs, Formats: reg, Perms: perms, Actions: actions, Groups: groups, CI: providers}
	})
	return built, buildErr
}

// InitSchema applies the migrations of the kernel and of every module with a schema, in start
// order (db.DB.Init). A database that does not fit them is a db.IsIncompatible error.
func InitSchema(ctx context.Context, d *db.DB) error {
	comps, err := Components()
	if err != nil {
		return err
	}
	return d.Init(ctx, comps)
}

// Components returns the schema components: the kernel first, then every module with a schema,
// in start order.
func Components() ([]db.Component, error) {
	b, err := Build()
	if err != nil {
		return nil, err
	}
	out := []db.Component{db.Core()}
	for _, m := range b.Manifests {
		if m.Schema != nil {
			out = append(out, *m.Schema)
		}
	}
	return out, nil
}

// RunWith starts the HTTP server and blocks until ctx is cancelled (graceful shutdown). Every
// answer carries VersionHeader with Options.Version. The startup order:
//
//  1. Logger, data dirs, db.Open.
//  2. Build: every module's Register, the settings groups, the format registry.
//  3. The migrations of the kernel and of each module, in start order (InitSchema).
//  4. settings.Load (master key, stored settings, env overrides).
//  5. The kernel services: live storage (swapped when the storage settings change), repos,
//     audit, auth, assets, proxy, access, the job runner.
//  6. Each module's Init, in start order; an error stops startup. Then the token kinds, the
//     guards of the access engine and the jobs of every module.
//  7. The routes of every module on one route table, plus GET /healthz and the web UI, behind
//     the host check (hosts.ServeHTTP: repo domains, 421 for a host the server does not answer).
//  8. Options.Init, then the bootstrap admin when there are no users.
//  9. The Start of every job, in start order; an error stops startup.
//  10. Listen; then the Run and Tick loops of every job.
//
// Shutdown: http.Server.Shutdown (30 s), then the runner stops each module in reverse start
// order and waits for its work, then the database closes. Access logging never logs
// Authorization headers, cookies or query strings.
func RunWith(ctx context.Context, boot *config.Boot, opts Options) error {
	level := new(slog.LevelVar)
	hopts := &slog.HandlerOptions{Level: level}
	var log *slog.Logger
	if opts.TextLogs {
		log = slog.New(slog.NewTextHandler(os.Stderr, hopts))
	} else {
		log = slog.New(slog.NewJSONHandler(os.Stderr, hopts))
	}
	slog.SetDefault(log)

	for _, d := range []string{boot.DataDir, boot.TmpDir()} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}

	d, err := db.Open(ctx, boot.DBFile())
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer d.Close()
	b, err := Build()
	if err != nil {
		return err
	}
	if err := InitSchema(ctx, d); err != nil {
		return err
	}

	set, err := settings.Load(ctx, d, boot, b.Groups, settings.Options{Dev: opts.Dev, Log: log.With("component", "settings")})
	if err != nil {
		return err
	}
	level.Set(settings.Level(set.Current()))

	st, err := storage.Open(ctx, storage.ConfigView.Get(set.Current()))
	if err != nil {
		return fmt.Errorf("storage: %w", err)
	}
	live := storage.NewLive(st)
	set.Subscribe(func(old, new *settings.Snapshot) {
		level.Set(settings.Level(new))
		cfg := storage.ConfigView.Get(new)
		if storage.SameBackend(storage.ConfigView.Get(old), cfg) {
			return
		}
		st, err := storage.Open(context.WithoutCancel(ctx), cfg)
		if err != nil {
			log.Error("storage settings changed but the new store could not be opened; keeping the previous one", "error", err.Error())
			return
		}
		live.Swap(st)
		log.Info("storage switched", "storage", cfg.Backend)
	})

	// base ends after the HTTP server shut down: a write that runs detached from its request (the
	// asset pipeline) finishes unless the shutdown timed out.
	base, endBase := context.WithCancel(context.WithoutCancel(ctx))
	defer endBase()
	version := opts.Version
	if version == "" {
		version = changelog.Dev
	}
	k, err := kernel(base, d, boot, set, live, b, opts.Dev, version, log)
	if err != nil {
		return err
	}
	// Stop and Wait run before d.Close (defers run in reverse): no module work and no kernel
	// write outlives the database.
	defer k.Auth.Wait()
	defer k.Jobs.Stop()
	// The repo domains answer before any Init: the edge module reads them when it starts on its
	// saved state.
	var extraHosts []func(*settings.Snapshot) string
	var domainPaths []string
	for _, m := range b.Manifests {
		extraHosts = append(extraHosts, m.Hosts...)
		domainPaths = append(domainPaths, m.DomainPaths...)
	}
	hostCheck := newHosts(set, k.Repos, b.Formats, extraHosts, domainPaths)
	k.RepoDomains = hostCheck.domains
	shared := module.NewShared()
	hosts := make([]*module.Host, len(b.Manifests))
	for i, m := range b.Manifests {
		hosts[i] = module.NewHost(k, m.Module, log, shared)
		for _, fn := range m.Inits {
			// An Init records audit events as the system actor of its module.
			if err := fn(audit.System(ctx, m.Module.ID), hosts[i]); err != nil {
				return fmt.Errorf("module %s: %w", m.Module.ID, err)
			}
		}
		// A module's repo fields serve once its Init ran.
		for _, f := range m.RepoFields {
			k.Repos.AddField(f)
		}
	}
	var resolve func(*http.Request) httpx.Client
	for _, m := range b.Manifests {
		for _, t := range m.TokenKinds {
			k.Auth.AddTokenKind(t.Prefix, t.Verify)
		}
		for _, g := range m.Guards {
			k.Access.AddGuard(g)
		}
		for _, j := range m.Jobs {
			k.Jobs.Add(m.Module.ID, j)
		}
		if m.ResolveClient != nil {
			resolve = m.ResolveClient
		}
	}

	mux := route.NewMux(k.Auth, k.Access, k.Repos)
	for _, m := range b.Manifests {
		for _, p := range m.Prefixes() {
			mux.Claim(m.Module.ID, p)
		}
	}
	for _, m := range b.Manifests {
		r := mux.Router(m.Module.ID)
		kit := format.Kit{Reader: k.Reader, Assets: k.Assets}
		for _, f := range m.Formats {
			if f.Routes == nil {
				format.Standard(f.Name, r, kit)
			} else {
				f.Routes(r, kit)
			}
		}
		for _, fn := range m.Routes {
			fn(r)
		}
	}
	mux.Kernel("GET /healthz", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := d.R.PingContext(r.Context()); err != nil {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, "ok\n")
	}))
	mux.Kernel("/", web.Handler())
	hostCheck.attach(mux)
	set.AddCheck("general", hostCheck.checkDomains)
	hostCheck.warnShadowed(log, set.Current())
	set.Subscribe(func(_, new *settings.Snapshot) { hostCheck.warnShadowed(log, new) })

	if opts.Init != nil {
		s := &Services{Boot: boot, Settings: set, Auth: k.Auth, Repos: k.Repos, DB: d, Assets: k.Assets,
			Builds: k.Builds, Formats: b.Formats, Manifests: b.Manifests}
		if err := opts.Init(audit.System(ctx, "core"), s); err != nil {
			return err
		}
	}
	if err := bootstrapAdmin(ctx, k.Auth, boot, log); err != nil {
		return err
	}

	if err := k.Jobs.Start(); err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              boot.Listen,
		Handler:           versionHeader(version, resolveClient(resolve, accessLog(log, hostCheck))), // the client first, then the log, then the host
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
		BaseContext:       func(net.Listener) context.Context { return context.WithoutCancel(ctx) },
	}
	// HTTP/1.1 and HTTP/2: by ALPN over TLS, and with prior knowledge (h2c) on a plain listener,
	// for a reverse proxy that speaks h2c.
	srv.Protocols = new(http.Protocols)
	srv.Protocols.SetHTTP1(true)
	srv.Protocols.SetHTTP2(true)
	srv.Protocols.SetUnencryptedHTTP2(true)
	snap := set.Current()
	tlsOn := settings.TLSCertFile.Get(snap) != "" && settings.TLSKeyFile.Get(snap) != ""
	if tlsOn {
		certs := &certLoader{set: set.Current}
		srv.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: certs.get}
	}
	ln, err := net.Listen("tcp", boot.Listen)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	k.Jobs.Loop()

	errc := make(chan error, 1)
	go func() {
		if tlsOn {
			errc <- srv.ServeTLS(ln, "", "")
			return
		}
		errc <- srv.Serve(ln)
	}()
	log.Info("listening", "addr", ln.Addr().String(), "tls", tlsOn, "storage", storage.ConfigView.Get(set.Current()).Backend)
	if opts.Ready != nil {
		opts.Ready(ln.Addr())
	}

	select {
	case err := <-errc:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}
	log.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		endBase()
		srv.Close()
		return fmt.Errorf("shutdown: %w", err)
	}
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// kernel builds the kernel services that every module gets. base ends after the HTTP server shut
// down.
func kernel(base context.Context, d *db.DB, boot *config.Boot, set *settings.Service, live *storage.Live, b *Built, dev bool, version string, log *slog.Logger) (*module.Kernel, error) {
	opsSvc := ops.New(d)
	for _, m := range b.Manifests {
		for _, t := range m.OpTypes {
			opsSvc.Register(t)
		}
	}
	au := audit.New(d, b.Actions, log.With("component", "audit"))
	set.SetAudit(au)
	rs := repos.New(d, b.Formats.ValidateConfig, b.Formats.SupportsKind)
	rs.SetSealer(set) // proxy credentials are sealed with the master key
	rs.SetAudit(au)
	rs.SetChangeHook(b.Formats.OnRepoChange)
	rs.SetCreateHook(b.Formats.OnRepoCreate)
	if bad, err := rs.Load(base); err != nil {
		return nil, err
	} else if len(bad) > 0 {
		log.Error("the credentials of proxy repos do not decrypt (was the master key changed?); enter them again", "repo_ids", bad)
	}
	as := auth.New(d, set.Current, rs)
	as.SetSealer(set) // TOTP secrets are sealed with the master key
	as.SetAudit(au)
	as.SetDevMode(dev) // only "telesphorepo-server dev" makes the second factor optional
	as.SetBearerKey(set.DeriveKey(auth.BearerKeyInfo))
	as.SetEdgeKey(ed25519.NewKeyFromSeed(set.DeriveKey(auth.EdgeKeyInfo)))
	bs := blobs.New(d, live)
	pipe := assets.New(base, d, bs, set.Current, au)
	pipe.SetHooks(b.Formats.Hooks)
	engine := access.New(d, rs, as, au, set.Current, opsSvc)
	reader := assets.NewReader(pipe, rs, engine, func() time.Duration { return storage.PresignTTL.Get(set.Current()) },
		log.With("component", "assets"))
	reader.SetFetcher(proxy.New(rs, pipe, log.With("component", "proxy")))
	downloads := stats.New(d)
	reader.SetDownloads(downloads.Add)
	rs.OnRedirectUse(downloads.UseRedirect)
	ids := make([]string, len(b.Manifests))
	for i, m := range b.Manifests {
		ids[i] = m.Module.ID
	}
	runner := jobs.NewRunner(log.With("component", "jobs"), ids)
	rq := rebuild.New(d, rs, set, log.With("component", "rebuild"),
		func(name string, fn func(context.Context)) { runner.Go("core", name, fn) }, b.Formats.RebuildRun)
	return &module.Kernel{
		Boot: boot, Dev: dev, Version: version, DB: d, Settings: set, Perms: b.Perms, Audit: au,
		Jobs: runner, Storage: live, Blobs: bs, Assets: pipe, Reader: reader, Downloads: downloads, Uploads: assets.NewUploads(d, set.Current),
		Rebuilds: rq, Repos: rs, Ops: opsSvc, Auth: as, Formats: b.Formats, Access: engine,
		CI: b.CI, Builds: ci.NewBuilds(d, b.CI),
		API: &route.API{Settings: set.Current},
	}, nil
}

// versionHeader sets VersionHeader to version on every answer.
func versionHeader(version string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(VersionHeader, version)
		next.ServeHTTP(w, r)
	})
}

// resolveClient resolves the client of every request once with fn (the network module) and
// attaches it (httpx.ClientIP and httpx.ClientScheme read it). Without fn the client is the TCP
// peer and the scheme is that of the connection.
func resolveClient(fn func(*http.Request) httpx.Client, next http.Handler) http.Handler {
	if fn == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, httpx.WithClient(r, fn(r)))
	})
}

// bootstrapAdmin creates the first admin when the users table is empty: from
// TR_BOOTSTRAP_ADMIN_* if set, otherwise "admin" with a random password printed once to stderr
// (outside the log stream).
func bootstrapAdmin(ctx context.Context, as *auth.Service, boot *config.Boot, log *slog.Logger) error {
	has, err := as.HasUsers(ctx)
	if err != nil {
		return err
	}
	if has {
		return nil
	}
	user, pass, generated := boot.BootstrapAdminUser, boot.BootstrapAdminPassword, false
	if user == "" || pass == "" {
		user, pass, generated = "admin", RandomPassword(), true
	}
	if _, err := as.CreateUser(audit.System(ctx, "core"), user, pass, perm.Current().AllServer()); err != nil {
		return fmt.Errorf("bootstrap admin: %w", err)
	}
	log.Info("bootstrap admin created", "username", user)
	if generated {
		PrintBox(os.Stderr,
			"First start: created admin user",
			"",
			"  username: "+user,
			"  password: "+pass,
			"",
			"This password is shown only once. Change it with:",
			"  telesphorepo-server user passwd "+user,
			"",
			"Sign in to the web UI and add an authenticator app or a",
			"passkey on the Security page. An admin sign-in reaches",
			"only that page until then.",
		)
	}
	return nil
}

// RandomPassword returns a random 20-character alphanumeric password.
func RandomPassword() string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	b := make([]byte, 20)
	n := big.NewInt(int64(len(alphabet)))
	for i := range b {
		v, err := rand.Int(rand.Reader, n) // crypto/rand.Reader never fails (Go 1.24+)
		if err != nil {
			panic(err)
		}
		b[i] = alphabet[v.Int64()]
	}
	return string(b)
}

// PrintBox writes lines inside an ASCII box (portable across terminals, including Windows).
func PrintBox(w io.Writer, lines ...string) {
	width := 0
	for _, l := range lines {
		width = max(width, len(l))
	}
	bar := "+" + strings.Repeat("-", width+2) + "+"
	var b strings.Builder
	b.WriteString("\n" + bar + "\n")
	for _, l := range lines {
		b.WriteString("| " + l + strings.Repeat(" ", width-len(l)) + " |\n")
	}
	b.WriteString(bar + "\n\n")
	io.WriteString(w, b.String())
}

// recorder captures status and byte count. It forwards ReadFrom (sendfile for FS downloads)
// and exposes Unwrap so http.ResponseController reaches Flush etc.
type recorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *recorder) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *recorder) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

func (w *recorder) ReadFrom(r io.Reader) (int64, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	var n int64
	var err error
	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		n, err = rf.ReadFrom(r)
	} else {
		n, err = io.Copy(w.ResponseWriter, r)
	}
	w.bytes += n
	return n, err
}

func (w *recorder) Flush() {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *recorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func accessLog(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		start := time.Now()
		w := &recorder{ResponseWriter: rw}
		defer func() {
			var abort bool
			if p := recover(); p != nil {
				if p != http.ErrAbortHandler {
					log.Error("panic", "method", r.Method, "path", r.URL.Path, "panic", fmt.Sprint(p))
				}
				if p == http.ErrAbortHandler || w.status != 0 {
					abort = true // headers already sent: abort the connection
				} else {
					httpx.Error(w, r, http.StatusInternalServerError, "internal error")
				}
			}
			defer func() {
				if abort {
					panic(http.ErrAbortHandler)
				}
			}()
			status := w.status
			if status == 0 {
				status = http.StatusOK
			}
			log.Info("http",
				"method", r.Method,
				"path", r.URL.Path,
				"status", status,
				"bytes", w.bytes,
				"duration_ms", time.Since(start).Milliseconds(),
				"ip", httpx.ClientIP(r).String(),
			)
		}()
		next.ServeHTTP(w, r)
	})
}
