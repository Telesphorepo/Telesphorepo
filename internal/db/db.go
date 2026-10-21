// Package db opens the SQLite database, applies the migrations of each component (the kernel and
// each module with tables) and checks query plans. Queries live in queries/*.sql and are
// generated into package dbq by sqlc.
package db

import (
	"context"
	"database/sql"
	"log/slog"
	"net/url"
	"os"

	_ "modernc.org/sqlite"
)

// CoreComponent is the name of the kernel's schema component.
const CoreComponent = "core"

// DB holds a single-connection writer pool and a multi-connection reader pool.
// All writes (including read-then-write sequences) must go through W.
type DB struct {
	W *sql.DB
	R *sql.DB
}

// Open opens (creating if needed) the database file. Init applies the migrations.
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
	return &DB{W: w, R: r}, nil
}

// OpenReadOnly opens the database file for reading only, as one pool. It writes nothing to the
// file: a backup reads the live database of a running server with it, and a restore reads the
// database of a backup.
func OpenReadOnly(file string) (*sql.DB, error) {
	if _, err := os.Stat(file); err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("mode", "ro")
	q.Add("_pragma", "busy_timeout(5000)")
	return sql.Open("sqlite", "file:"+file+"?"+q.Encode())
}

// VacuumInto writes a copy of the database that r reads into the empty file: SQLite's VACUUM
// INTO, a consistent snapshot while the server keeps writing. The copy is in rollback journal
// mode (the copy of a WAL database starts in WAL mode), so it opens read-only without -wal and
// -shm files, also from read-only media; the server sets WAL again when it opens it.
func VacuumInto(ctx context.Context, r *sql.DB, file string) error {
	if _, err := r.ExecContext(ctx, "VACUUM INTO ?", file); err != nil {
		return err
	}
	w, err := sql.Open("sqlite", "file:"+file)
	if err != nil {
		return err
	}
	_, err = w.ExecContext(ctx, "PRAGMA journal_mode=DELETE")
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	return err
}

// Migration names one migration of a component: a row of schema_migrations, or a file of this
// binary.
type Migration struct {
	Component string `json:"component"`
	Version   int    `json:"version"`
	Name      string `json:"name"`
	Checksum  string `json:"checksum"`
}

// Files returns the migration files of c in version order.
func (c Component) Files() ([]Migration, error) {
	ms, err := c.migrations()
	if err != nil {
		return nil, err
	}
	out := make([]Migration, len(ms))
	for i, m := range ms {
		out[i] = Migration{Component: c.Name, Version: m.version, Name: m.file, Checksum: m.checksum}
	}
	return out, nil
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

// Optimize refreshes the planner statistics (PRAGMA optimize), so they follow the data on a
// long-running server.
func (d *DB) Optimize(ctx context.Context) error {
	_, err := d.W.ExecContext(ctx, "PRAGMA optimize")
	return err
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
