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

	"telesphorepo/internal/api"
	"telesphorepo/internal/assets"
	"telesphorepo/internal/auth"
	"telesphorepo/internal/config"
	"telesphorepo/internal/db"
	"telesphorepo/internal/debian"
	"telesphorepo/internal/httpx"
	"telesphorepo/internal/maven"
	"telesphorepo/internal/repos"
	"telesphorepo/internal/signing"
	"telesphorepo/internal/storage"
	"telesphorepo/web"
)

// Services exposes the wired services to Options.Init.
type Services struct {
	Config *config.Config
	Auth   *auth.Service
	Repos  *repos.Service
}

// Options customises Run (used by "telesphorepo dev"). The zero value is plain "serve".
type Options struct {
	// Init runs after the DB and services are ready and before the default first-run admin
	// bootstrap (so it may create its own admin) and before listening.
	Init func(ctx context.Context, s *Services) error
	// Ready runs once the listener is bound.
	Ready func(addr net.Addr)
	// TextLogs uses human-readable text logs instead of JSON.
	TextLogs bool
}

// Run starts the HTTP server and blocks until ctx is cancelled (graceful shutdown).
func Run(ctx context.Context, cfg *config.Config) error { return RunWith(ctx, cfg, Options{}) }

// RunWith is Run with Options.
// Steps: slog JSON logger at cfg.LogLevel; mkdir DataDir/TmpDir; db.Open; storage (fs|s3);
// repos/auth/assets services; bootstrap admin if no users; debian.New + Start; maven.New;
// mux: GET /healthz, /api/ -> api.Handler(), formats Register, "/" -> web UI (SPA fallback).
// Access logging must never log Authorization headers, cookies, or query strings.
func RunWith(ctx context.Context, cfg *config.Config, opts Options) error {
	hopts := &slog.HandlerOptions{Level: cfg.LogLevel}
	var log *slog.Logger
	if opts.TextLogs {
		log = slog.New(slog.NewTextHandler(os.Stderr, hopts))
	} else {
		log = slog.New(slog.NewJSONHandler(os.Stderr, hopts))
	}
	slog.SetDefault(log)

	for _, d := range []string{cfg.DataDir, cfg.TmpDir()} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}

	d, err := db.Open(ctx, cfg.DBFile())
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer d.Close()

	var st storage.Store
	if cfg.Storage == "s3" {
		st, err = storage.NewS3(ctx, cfg)
	} else {
		st, err = storage.NewFS(cfg.FSRoot)
	}
	if err != nil {
		return fmt.Errorf("storage: %w", err)
	}

	rs := repos.New(d)
	as := auth.New(d, cfg, rs)
	ss := assets.New(d, st, cfg)

	if opts.Init != nil {
		if err := opts.Init(ctx, &Services{Config: cfg, Auth: as, Repos: rs}); err != nil {
			return err
		}
	}
	if err := bootstrapAdmin(ctx, as, cfg, log); err != nil {
		return err
	}
	if err := ensureSigningKey(cfg, log); err != nil {
		return err
	}

	deb, err := debian.New(d, as, ss, rs, cfg, log.With("component", "debian"))
	if err != nil {
		return fmt.Errorf("debian: %w", err)
	}
	mvn := maven.New(as, ss, cfg)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := d.R.PingContext(r.Context()); err != nil {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, "ok\n")
	})
	mux.Handle("/api/", api.New(as, rs, ss, deb, cfg).Handler())
	mvn.Register(mux)
	deb.Register(mux)
	mux.Handle("/", web.Handler())

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           accessLog(log, cfg, mux),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
		BaseContext:       func(net.Listener) context.Context { return context.WithoutCancel(ctx) },
	}
	ln, err := net.Listen("tcp", cfg.Listen)
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
func bootstrapAdmin(ctx context.Context, as *auth.Service, cfg *config.Config, log *slog.Logger) error {
	has, err := as.HasUsers(ctx)
	if err != nil {
		return fmt.Errorf("check users: %w", err)
	}
	if has {
		return nil
	}
	user, pass, generated := cfg.BootstrapAdminUser, cfg.BootstrapAdminPassword, false
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

// ensureSigningKey defaults TR_GPG_KEY_FILE to DataDir/signing.asc, generating it if missing.
// An explicitly configured key file that does not exist is an error.
func ensureSigningKey(cfg *config.Config, log *slog.Logger) error {
	if cfg.GPGKeyFile != "" {
		if _, err := os.Stat(cfg.GPGKeyFile); err != nil {
			return fmt.Errorf("TR_GPG_KEY_FILE %s: %w (unset TR_GPG_KEY_FILE to auto-generate one in the data dir)", cfg.GPGKeyFile, err)
		}
		return nil
	}
	cfg.GPGKeyFile = cfg.DefaultSigningKeyFile()
	_, err := os.Stat(cfg.GPGKeyFile)
	if err == nil {
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("signing key %s: %w", cfg.GPGKeyFile, err)
	}
	fp, err := signing.GenerateFile(cfg.GPGKeyFile, DefaultKeyName, DefaultKeyEmail)
	if err != nil {
		return fmt.Errorf("generate signing key %s: %w", cfg.GPGKeyFile, err)
	}
	log.Info("generated signing key", "file", cfg.GPGKeyFile, "fingerprint", fp)
	return nil
}

// Identity used for generated signing keys.
const (
	DefaultKeyName  = "Telesphorepo"
	DefaultKeyEmail = "telesphorepo@localhost"
)

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

func accessLog(log *slog.Logger, cfg *config.Config, next http.Handler) http.Handler {
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
				"ip", httpx.ClientIP(r, cfg.TrustedProxies).String(),
			)
		}()
		next.ServeHTTP(w, r)
	})
}
