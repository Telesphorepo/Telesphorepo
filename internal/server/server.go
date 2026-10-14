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
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/api"
	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/config"
	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/debian"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/maven"
	"github.com/Telesphorepo/Telesphorepo/internal/oidc"
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
	// Defaults replaces built-in setting defaults (setting key -> text value), see
	// settings.Options.Defaults.
	Defaults map[string]string
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

// RunWith is Run with Options.
// Steps: logger; mkdir DataDir/TmpDir; db.Open; settings.Load (master key, DB, env overrides);
// live storage (hot-swapped on storage setting changes); repos/auth/assets services; bootstrap
// admin if no users; signing key (setting, generated on first start); debian.New + Start;
// maven.New; mux: GET /healthz, /api/ -> api.Handler(), formats Register, "/" -> web UI.
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

	set, err := settings.Load(ctx, d, boot, settings.Options{Defaults: opts.Defaults, Log: log.With("component", "settings")})
	if err != nil {
		return err
	}
	cfg := set.Config()
	level.Set(cfg.LogLevel)

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
		log.Info("storage switched; existing blobs were not moved", "storage", new.Config.Storage)
	})

	rs := repos.New(d)
	as := auth.New(d, set.Config, rs)
	ss := assets.New(d, live, set.Config)

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

	deb, err := debian.New(d, as, ss, rs, set.Config, set.Signer, log.With("component", "debian"))
	if err != nil {
		return fmt.Errorf("debian: %w", err)
	}
	// A new signing key re-signs every published Release (indexes are marked dirty).
	set.Subscribe(deb.SigningKeyChanged)
	mvn := maven.New(as, ss, set.Config)

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
	mux.Handle("/api/", api.New(as, rs, ss, deb, set, sso).Handler())
	mvn.Register(mux)
	deb.Register(mux)
	mux.Handle("/", web.Handler())

	srv := &http.Server{
		Addr:              boot.Listen,
		Handler:           accessLog(log, set.Config, mux),
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

	deb.Start(ctx)

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

func accessLog(log *slog.Logger, cfg config.Provider, next http.Handler) http.Handler {
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
				"ip", httpx.ClientIP(r, cfg().TrustedProxies).String(),
			)
		}()
		next.ServeHTTP(w, r)
	})
}
