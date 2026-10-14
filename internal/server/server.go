// Package server wires config, DB, storage, auth, formats, API and UI together.
package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
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
	"telesphorepo/internal/storage"
	"telesphorepo/web"
)

// Run starts the HTTP server and blocks until ctx is cancelled (graceful shutdown).
// Steps: slog JSON logger at cfg.LogLevel; mkdir DataDir/TmpDir; db.Open; storage (fs|s3);
// repos/auth/assets services; bootstrap admin if no users; debian.New + Start; maven.New;
// mux: GET /healthz, /api/ -> api.Handler(), formats Register, "/" -> web UI (SPA fallback).
// Access logging must never log Authorization headers, cookies, or query strings.
func Run(ctx context.Context, cfg *config.Config) error {
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
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

	if err := bootstrapAdmin(ctx, as, cfg, log); err != nil {
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

func bootstrapAdmin(ctx context.Context, as *auth.Service, cfg *config.Config, log *slog.Logger) error {
	if cfg.BootstrapAdminUser == "" || cfg.BootstrapAdminPassword == "" {
		return nil
	}
	has, err := as.HasUsers(ctx)
	if err != nil {
		return fmt.Errorf("check users: %w", err)
	}
	if has {
		return nil
	}
	if _, err := as.CreateUser(ctx, cfg.BootstrapAdminUser, cfg.BootstrapAdminPassword, true); err != nil {
		return fmt.Errorf("bootstrap admin: %w", err)
	}
	log.Info("bootstrap admin created", "username", cfg.BootstrapAdminUser)
	return nil
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
