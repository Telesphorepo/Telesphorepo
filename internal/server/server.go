// Package server wires config, DB, storage, auth, formats, API and UI together.
package server

import (
	"context"
	"crypto/rand"
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

	"github.com/Telesphorepo/Telesphorepo/internal/api"
	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/config"
	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/files"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	_ "github.com/Telesphorepo/Telesphorepo/internal/formats" // registers the built-in formats
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/move"
	"github.com/Telesphorepo/Telesphorepo/internal/netrules"
	"github.com/Telesphorepo/Telesphorepo/internal/nexus"
	"github.com/Telesphorepo/Telesphorepo/internal/oidc"
	"github.com/Telesphorepo/Telesphorepo/internal/publish"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
	"github.com/Telesphorepo/Telesphorepo/internal/storage"
	"github.com/Telesphorepo/Telesphorepo/web"
)

// Services exposes the wired services to Options.Init.
type Services struct {
	Config   *config.Config // effective configuration at startup
	Settings *settings.Service
	Auth     *auth.Service
	Repos    *repos.Service
}

// Options customises Run (used by "telesphorepo dev"). The zero value is plain "serve".
type Options struct {
	// Dev applies the Dev default of every setting (see settings.Options.Dev) and makes the second
	// factor optional (auth.Service.SetDevMode). Only "telesphorepo dev" sets it.
	Dev bool
	// Init runs after the DB and services are ready and before the default first-run admin
	// bootstrap (so it may create its own admin) and before listening.
	Init func(ctx context.Context, s *Services) error
	// Ready runs once the listener is bound.
	Ready func(addr net.Addr)
	// TextLogs uses human-readable text logs instead of JSON.
	TextLogs bool
}

// Run starts the HTTP server and blocks until ctx is cancelled (graceful shutdown).
func Run(ctx context.Context, boot *config.Boot) error { return RunWith(ctx, boot, Options{}) }

// Migrate applies the migration track of every format that owns tables (format.Migrator).
// db.Open has already applied the core track.
func Migrate(ctx context.Context, d *db.DB) error {
	for _, f := range format.All() {
		if m, ok := f.(format.Migrator); ok {
			if err := d.Migrate(ctx, f.Name(), m.Migrations()); err != nil {
				return fmt.Errorf("migrate %s: %w", f.Name(), err)
			}
		}
	}
	return nil
}

// QuerySets returns the queries the plan guard checks: core first, then every
// format.Queryable.
func QuerySets() []db.QuerySet {
	sets := []db.QuerySet{db.CoreQueries()}
	for _, f := range format.All() {
		if q, ok := f.(format.Queryable); ok {
			sets = append(sets, q.QuerySet())
		}
	}
	return sets
}

var (
	settingsOnce sync.Once
	settingsErr  error
)

// registerSettings adds each format.Configurable's settings to the settings registry as a
// group named after the format. It runs once per process, before settings.Load.
func registerSettings() error {
	settingsOnce.Do(func() {
		for _, f := range format.All() {
			c, ok := f.(format.Configurable)
			if !ok {
				continue
			}
			if defs := c.Settings(); len(defs) > 0 {
				if err := settings.Register(f.Name(), f.Title(), defs); err != nil {
					settingsErr = fmt.Errorf("format %s: %w", f.Name(), err)
					return
				}
			}
		}
	})
	return settingsErr
}

// RunWith is Run with Options.
// Steps: logger; mkdir DataDir/TmpDir; db.Open (core migrations); format migrations; format
// settings; settings.Load (master key, DB, env overrides); query optimizer; live storage
// (hot-swapped on storage setting changes); repos/auth/publish/assets/move/nexus services;
// bootstrap admin if no users; signing key (setting, generated on first start); mux: GET
// /healthz, /api/ -> api.Handler(), each format's Register, "/" -> web UI; listen; each
// format.Starter's Start; resume a running storage move and the running Nexus syncs, start the
// Nexus sync scheduler and the publish cleanup job; serve.
// Access logging must never log Authorization headers, cookies, or query strings.
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
	if err := Migrate(ctx, d); err != nil {
		return err
	}
	if err := registerSettings(); err != nil {
		return err
	}

	set, err := settings.Load(ctx, d, boot, settings.Options{Dev: opts.Dev, Log: log.With("component", "settings")})
	if err != nil {
		return err
	}
	cfg := set.Config()
	level.Set(cfg.LogLevel)
	d.StartOptimizer(ctx)

	st, err := storage.Open(ctx, cfg)
	if err != nil {
		return fmt.Errorf("storage: %w", err)
	}
	live := storage.NewLive(st)

	set.Subscribe(func(old, new *settings.Snapshot) {
		level.Set(new.Config.LogLevel)
		if storage.SameBackend(old.Config, new.Config) {
			return
		}
		st, err := storage.Open(context.WithoutCancel(ctx), new.Config)
		if err != nil {
			log.Error("storage settings changed but the new store could not be opened; keeping the previous one", "error", err.Error())
			return
		}
		live.Swap(st)
		log.Info("storage switched", "storage", new.Config.Storage)
	})

	rs := repos.New(d, format.ValidateRepoConfig)
	au := audit.New(d, func() time.Duration { return set.Current().Duration(settings.KeyAuditRetention) }, log.With("component", "audit"))
	nr := netrules.New(d, set.Config, au, log.With("component", "netrules"))
	if err := nr.Load(ctx); err != nil {
		return err
	}
	as := auth.New(d, set.Config, rs)
	as.SetSealer(set) // TOTP secrets are sealed with the master key
	as.SetAudit(au)
	as.SetNetRules(nr)      // per-repo IP rules on the repo gates
	as.SetDevMode(opts.Dev) // only "telesphorepo dev" makes the second factor optional
	ps := publish.New(d, as, rs, set, log.With("component", "publish"))
	as.SetPublishTokens(ps) // publish-session tokens authenticate through the publish service
	ps.SetNetRules(nr)      // minting a session checks the repo's publish rules
	ss := assets.New(d, live, set.Config)
	fs := files.New(d, ss, rs, as, log.With("component", "files"))
	mv := move.New(d, set, live, ss, rs, log.With("component", "move"))
	nx := nexus.New(d, set, rs, log.With("component", "nexus"))
	// The move copier, the Nexus sync runners and scheduler, and the publish cleanup job live until
	// RunWith returns. They stop before the database closes (this defer runs before d.Close); a
	// move or sync stays running in the database.
	jobsCtx, stopJobs := context.WithCancel(ctx)
	defer func() {
		stopJobs()
		mv.Wait()
		nx.Wait()
		ps.Wait()
		au.Wait()
		nr.Wait()
	}()

	if opts.Init != nil {
		if err := opts.Init(ctx, &Services{Config: cfg, Settings: set, Auth: as, Repos: rs}); err != nil {
			return err
		}
	}
	if err := bootstrapAdmin(ctx, as, boot, log); err != nil {
		return err
	}
	if err := set.EnsureSigningKey(ctx); err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := d.R.PingContext(r.Context()); err != nil {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, "ok\n")
	})
	sso := oidc.New(as, set.Config, log.With("component", "oidc"))
	mux.Handle("/api/", api.New(as, rs, ss, set, mv, nx, ps, fs, au, nr, sso).Handler())
	for _, f := range format.All() {
		f.Register(mux, format.Host{
			DB:       d,
			Auth:     as,
			Assets:   ss,
			Repos:    rs,
			Config:   set.Config,
			Settings: set,
			Signer:   set.Signer,
			Log:      log.With("component", f.Name()),
			Audit:    au,
		})
	}
	mux.Handle("/", web.Handler())

	srv := &http.Server{
		Addr:              boot.Listen,
		Handler:           nr.Middleware(accessLog(log, mux)), // client address first, then the log
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
		BaseContext:       func(net.Listener) context.Context { return context.WithoutCancel(ctx) },
	}
	ln, err := net.Listen("tcp", boot.Listen)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	for _, f := range format.All() {
		if s, ok := f.(format.Starter); ok {
			s.Start(ctx)
		}
	}
	// A storage move or Nexus sync left running by the previous process continues from its
	// last saved position. Both stop with ctx and stay running in the database.
	if err := mv.Resume(jobsCtx); err != nil {
		ln.Close()
		return err
	}
	nx.Start(jobsCtx)
	ps.Start(jobsCtx)
	au.Start(jobsCtx)
	nr.Start(jobsCtx)

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	log.Info("listening", "addr", ln.Addr().String(), "storage", cfg.Storage)
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
		srv.Close()
		return fmt.Errorf("shutdown: %w", err)
	}
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// bootstrapAdmin creates the first admin when the users table is empty: from
// TR_BOOTSTRAP_ADMIN_* if set, otherwise "admin" with a random password printed once to stderr
// (outside the log stream).
func bootstrapAdmin(ctx context.Context, as *auth.Service, boot *config.Boot, log *slog.Logger) error {
	has, err := as.HasUsers(ctx)
	if err != nil {
		return fmt.Errorf("check users: %w", err)
	}
	if has {
		return nil
	}
	user, pass, generated := boot.BootstrapAdminUser, boot.BootstrapAdminPassword, false
	if user == "" || pass == "" {
		user, pass, generated = "admin", RandomPassword(), true
	}
	if _, err := as.CreateUser(ctx, user, pass, true); err != nil {
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
			"  telesphorepo user passwd "+user,
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
					httpx.Error(w, http.StatusInternalServerError, "internal error")
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
