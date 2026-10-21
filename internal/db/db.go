// Package db opens the SQLite database, creates the schema of each component (the kernel and
// each module with tables) and checks query plans. Queries live in queries/*.sql and are
// generated into package dbq by sqlc.
package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var coreSchema string

// CoreComponent is the name of the kernel's schema component.
const CoreComponent = "core"

// DB holds a single-connection writer pool and a multi-connection reader pool.
// All writes (including read-then-write sequences) must go through W.
type DB struct {
	W *sql.DB
	R *sql.DB
}

// Open opens (creating if needed) the database file. Init creates the schema.
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

// Fingerprint is the fingerprint of a schema text that schema_components holds: the hex SHA-256
// of the text with CRLF line ends read as LF.
func Fingerprint(schema string) string {
	sum := sha256.Sum256([]byte(strings.ReplaceAll(schema, "\r\n", "\n")))
	return hex.EncodeToString(sum[:])
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

// ErrSchemaChanged means the database holds a component whose schema text differs from the
// schema of this binary. The schema files are edited in place, so the database must be created
// again: "telesphorepo-server dev" deletes and recreates its own data dir; serve refuses to start.
type ErrSchemaChanged struct {
	Component string
}

func (e *ErrSchemaChanged) Error() string {
	return "the database was created with another schema of component " + e.Component +
		"; delete the data directory to start fresh"
}

// IsSchemaChanged reports whether err is an ErrSchemaChanged.
func IsSchemaChanged(err error) bool {
	var e *ErrSchemaChanged
	return errors.As(err, &e)
}

// Init creates the schema of each component, in order. schema_components holds one row per
// component with the SHA-256 of its schema text, with CRLF line ends read as LF. A component without a row gets its schema and
// its row in one transaction. A row with another fingerprint is an ErrSchemaChanged.
func (d *DB) Init(ctx context.Context, comps []Component) error {
	if _, err := d.W.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_components (
		component   TEXT PRIMARY KEY,
		fingerprint TEXT NOT NULL,
		applied_at  INTEGER NOT NULL) STRICT, WITHOUT ROWID`); err != nil {
		return err
	}
	for _, c := range comps {
		fp := Fingerprint(c.Schema)
		var stored string
		err := d.W.QueryRowContext(ctx, `SELECT fingerprint FROM schema_components WHERE component = ?`, c.Name).Scan(&stored)
		switch {
		case err == nil && stored == fp:
			continue
		case err == nil:
			return &ErrSchemaChanged{Component: c.Name}
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		err = d.Tx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, c.Schema); err != nil {
				return fmt.Errorf("schema %s: %w", c.Name, err)
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO schema_components (component, fingerprint, applied_at) VALUES (?, ?, ?)`,
				c.Name, fp, time.Now().Unix())
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}
