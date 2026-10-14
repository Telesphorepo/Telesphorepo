// Package db opens the SQLite database, applies the migration tracks, and checks query plans
// (spec §5.5). Queries live in queries/*.sql and are generated into package dbq by sqlc.
package db

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// CoreComponent is the schema_migrations track of the core tables.
const CoreComponent = "core"

// optimizeEvery is how often StartOptimizer refreshes the planner statistics.
const optimizeEvery = 6 * time.Hour

// DB holds a single-connection writer pool and a multi-connection reader pool.
// All writes (including read-then-write sequences) must go through W.
type DB struct {
	W *sql.DB
	R *sql.DB
}

// Open opens (creating if needed) the database file and applies the core migration track.
// Format plugins apply their own tracks afterwards with Migrate.
func Open(ctx context.Context, file string) (*DB, error) {
	// Create the file owner-only before SQLite does (it would use 0644); WAL/SHM files
	// inherit these permissions.
	if f, err := os.OpenFile(file, os.O_RDWR|os.O_CREATE, 0o600); err == nil {
		f.Close()
	} else {
		return nil, err
	}
	pragmas := []string{
		"journal_mode(WAL)",
		"synchronous(NORMAL)",
		"foreign_keys(ON)",
		"busy_timeout(5000)",
		"analysis_limit(1000)", // bounds the ANALYZE work of PRAGMA optimize on large tables
	}
	q := url.Values{}
	for _, p := range pragmas {
		q.Add("_pragma", p)
	}
	wq := url.Values{}
	for k, v := range q {
		wq[k] = v
	}
	wq.Set("_txlock", "immediate")

	w, err := sql.Open("sqlite", "file:"+file+"?"+wq.Encode())
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)

	r, err := sql.Open("sqlite", "file:"+file+"?"+q.Encode())
	if err != nil {
		w.Close()
		return nil, err
	}
	r.SetMaxOpenConns(8)

	d := &DB{W: w, R: r}
	core, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		d.Close()
		return nil, err
	}
	if err := d.Migrate(ctx, CoreComponent, core); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

// Close refreshes the planner statistics (PRAGMA optimize) and closes both pools.
func (d *DB) Close() error {
	if _, err := d.W.Exec("PRAGMA optimize"); err != nil {
		slog.Warn("PRAGMA optimize on close failed", "err", err)
	}
	err1 := d.R.Close()
	err2 := d.W.Close()
	if err1 != nil {
		return err1
	}
	return err2
}

// StartOptimizer runs PRAGMA optimize every 6 hours until ctx is done, so the planner
// statistics follow the data on a long-running server.
func (d *DB) StartOptimizer(ctx context.Context) {
	go func() {
		t := time.NewTicker(optimizeEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if _, err := d.W.ExecContext(ctx, "PRAGMA optimize"); err != nil && ctx.Err() == nil {
					slog.Warn("PRAGMA optimize failed", "err", err)
				}
			}
		}
	}()
}

// Tx runs fn in a write transaction (BEGIN IMMEDIATE), committing on nil error.
func (d *DB) Tx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := d.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Migrate applies the NNNN_name.sql files at the root of fsys in order, each in its own
// transaction, and records the version in schema_migrations under component. The core track
// creates schema_migrations in its first file, so it must be applied before any other track.
func (d *DB) Migrate(ctx context.Context, component string, fsys fs.FS) error {
	names, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return err
	}
	type migration struct {
		num  int
		name string
	}
	var ms []migration
	for _, name := range names {
		num, err := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
		if err != nil || num <= 0 {
			return fmt.Errorf("migration %s/%s: name must start with a positive NNNN_", component, name)
		}
		ms = append(ms, migration{num, name})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].num < ms[j].num })
	for i := 1; i < len(ms); i++ {
		if ms[i].num == ms[i-1].num {
			return fmt.Errorf("migration %s: %s and %s share version %d", component, ms[i-1].name, ms[i].name, ms[i].num)
		}
	}

	current, err := d.version(ctx, component)
	if err != nil {
		return err
	}
	if len(ms) > 0 && current > ms[len(ms)-1].num {
		return fmt.Errorf("migration %s: database is at version %d, newer than this binary (%d)",
			component, current, ms[len(ms)-1].num)
	}
	for _, m := range ms {
		if m.num <= current {
			continue
		}
		body, err := fs.ReadFile(fsys, m.name)
		if err != nil {
			return err
		}
		err = d.Tx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, string(body)); err != nil {
				return fmt.Errorf("migration %s/%s: %w", component, m.name, err)
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (component, version) VALUES (?, ?)
				ON CONFLICT (component) DO UPDATE SET version = excluded.version`, component, m.num)
			return err
		})
		if err != nil {
			return err
		}
		current = m.num
	}
	return nil
}

// version returns the applied version of a track; 0 when the track (or, before the core
// track's first file, schema_migrations itself) does not exist yet.
func (d *DB) version(ctx context.Context, component string) (int, error) {
	var exists bool
	if err := d.W.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM sqlite_schema WHERE type = 'table' AND name = 'schema_migrations')`).
		Scan(&exists); err != nil {
		return 0, err
	}
	if !exists {
		return 0, nil
	}
	var v int
	err := d.W.QueryRowContext(ctx, `SELECT version FROM schema_migrations WHERE component = ?`, component).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return v, err
}
