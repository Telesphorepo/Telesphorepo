package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// migrationRE is the name of a migration file: a 4-digit version from 0001, then a lower-case name.
var migrationRE = regexp.MustCompile(`^(\d{4})_([a-z0-9_]+)\.sql$`)

// migration is one file of the migrations of a component.
type migration struct {
	version  int
	file     string // e.g. 0001_init.sql
	sql      string // with CRLF line ends read as LF
	checksum string // hex SHA-256 of sql
}

// migrations reads the migrations of c in version order. Every file at the root of c.Migrations
// is NNNN_name.sql, and the versions run from 0001 with no gap.
func (c Component) migrations() ([]migration, error) {
	if c.Migrations == nil {
		return nil, nil
	}
	entries, err := fs.ReadDir(c.Migrations, ".")
	if err != nil {
		return nil, fmt.Errorf("migrations of %s: %w", c.Name, err)
	}
	var out []migration
	for _, e := range entries {
		m := migrationRE.FindStringSubmatch(e.Name())
		if e.IsDir() || m == nil {
			return nil, fmt.Errorf("migrations of %s: %s is not a migration file NNNN_name.sql", c.Name, e.Name())
		}
		v, _ := strconv.Atoi(m[1])
		if v != len(out)+1 {
			return nil, fmt.Errorf("migrations of %s: %s has version %04d, want %04d (versions run from 0001 with no gap)", c.Name, e.Name(), v, len(out)+1)
		}
		body, err := fs.ReadFile(c.Migrations, e.Name())
		if err != nil {
			return nil, fmt.Errorf("migrations of %s: %w", c.Name, err)
		}
		text := strings.ReplaceAll(string(body), "\r\n", "\n")
		sum := sha256.Sum256([]byte(text))
		out = append(out, migration{version: v, file: e.Name(), sql: text, checksum: hex.EncodeToString(sum[:])})
	}
	return out, nil
}

// schema returns the text of every migration of c, in order.
func (c Component) schema() (string, error) {
	ms, err := c.migrations()
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, m := range ms {
		b.WriteString(m.sql)
		b.WriteByte('\n')
	}
	return b.String(), nil
}

// ErrMigrationChanged means the database applied a migration of Component whose file in this
// binary is another one (other content or another name). An applied migration never changes:
// "telesphorepo-server dev" deletes and recreates its own data dir; serve refuses to start.
type ErrMigrationChanged struct {
	Component string
	Version   int
	Name      string // the file the database applied, e.g. 0001_init.sql
}

func (e *ErrMigrationChanged) Error() string {
	return fmt.Sprintf("the database applied the %s migration %s, and that file has changed since; "+
		"an applied migration must not change, so restore it and add the next migration instead", e.Component, e.Name)
}

// ErrDatabaseNewer means the database applied a migration of Component, Version, that is newer
// than the newest migration of this binary.
type ErrDatabaseNewer struct {
	Component string
	Version   int
}

func (e *ErrDatabaseNewer) Error() string {
	return fmt.Sprintf("the database is from a newer Telesphorepo (%s migration %04d); run that version or newer", e.Component, e.Version)
}

// ErrDevDatabase means the database has the table schema_components of a development build that
// kept no migrations.
var ErrDevDatabase = errors.New("the database is from a development build of Telesphorepo without schema migrations " +
	"(it has the table schema_components); delete the data directory to start fresh")

// IsIncompatible reports whether err says that the database does not fit the migrations of this
// binary: an ErrMigrationChanged, an ErrDatabaseNewer or ErrDevDatabase.
func IsIncompatible(err error) bool {
	var changed *ErrMigrationChanged
	var newer *ErrDatabaseNewer
	return errors.As(err, &changed) || errors.As(err, &newer) || errors.Is(err, ErrDevDatabase)
}

// Init applies the migrations of each component, in order. schema_migrations holds one row per
// applied migration: the component, the version, the file name, the SHA-256 of the file (with
// CRLF line ends read as LF) and the time. Per component, an applied migration whose file differs
// is an ErrMigrationChanged, and an applied version above the newest file is an ErrDatabaseNewer.
// Every file above the last applied version is applied in order, each in its own transaction with
// its row (see apply).
func (d *DB) Init(ctx context.Context, comps []Component) error {
	var dev int
	if err := d.W.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name = 'schema_components'`).Scan(&dev); err != nil {
		return err
	}
	if dev > 0 {
		return ErrDevDatabase
	}
	if _, err := d.W.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		component  TEXT NOT NULL,
		version    INTEGER NOT NULL,
		name       TEXT NOT NULL,
		checksum   TEXT NOT NULL,
		applied_at INTEGER NOT NULL,
		PRIMARY KEY (component, version)) STRICT, WITHOUT ROWID`); err != nil {
		return err
	}
	for _, c := range comps {
		if err := d.migrate(ctx, c); err != nil {
			return err
		}
	}
	return nil
}

// applied is a row of schema_migrations.
type applied struct {
	version        int
	name, checksum string
}

// migrate checks the applied migrations of c against its files and applies the new ones.
func (d *DB) migrate(ctx context.Context, c Component) error {
	ms, err := c.migrations()
	if err != nil {
		return err
	}
	rows, err := d.W.QueryContext(ctx, `SELECT version, name, checksum FROM schema_migrations WHERE component = ? ORDER BY version`, c.Name)
	if err != nil {
		return err
	}
	var done []applied
	for rows.Next() {
		var a applied
		if err := rows.Scan(&a.version, &a.name, &a.checksum); err != nil {
			rows.Close()
			return err
		}
		done = append(done, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	last := 0
	if len(done) > 0 {
		last = done[len(done)-1].version
	}
	if last > len(ms) {
		return &ErrDatabaseNewer{Component: c.Name, Version: last}
	}
	for _, a := range done {
		if a.version < 1 || ms[a.version-1].file != a.name || ms[a.version-1].checksum != a.checksum {
			return &ErrMigrationChanged{Component: c.Name, Version: a.version, Name: a.name}
		}
	}
	for _, m := range ms[last:] {
		if err := d.apply(ctx, c.Name, m); err != nil {
			return err
		}
		slog.Info("applied migration", "component", c.Name, "migration", m.file)
	}
	return nil
}

// apply runs migration m of component and records it in one transaction. The transaction runs
// on a connection of its own with foreign keys off, so a migration can rebuild a table (create
// the new table, copy the rows, drop the old table, rename the new one) without the drop
// deleting the rows that reference it. PRAGMA foreign_key_check runs before the commit: a row
// that the migration leaves without its parent fails the migration.
func (d *DB) apply(ctx context.Context, component string, m migration) (err error) {
	conn, err := d.W.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return err
	}
	defer func() {
		if _, ferr := conn.ExecContext(context.WithoutCancel(ctx), `PRAGMA foreign_keys = ON`); ferr != nil && err == nil {
			err = ferr
		}
	}()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return fmt.Errorf("%s migration %s: %w", component, m.file, err)
	}
	if err := foreignKeyCheck(ctx, tx); err != nil {
		return fmt.Errorf("%s migration %s: %w", component, m.file, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (component, version, name, checksum, applied_at) VALUES (?, ?, ?, ?, ?)`,
		component, m.version, m.file, m.checksum, time.Now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

// foreignKeyCheck fails when PRAGMA foreign_key_check finds a row whose parent is missing. The
// error names up to 10 of them.
func foreignKeyCheck(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var bad []string
	for rows.Next() && len(bad) < 10 {
		var table, parent string
		var rowid sql.NullInt64
		var fkid int64
		if err := rows.Scan(&table, &rowid, &parent, &fkid); err != nil {
			return err
		}
		row := "a row of " + table
		if rowid.Valid {
			row = fmt.Sprintf("%s rowid %d", table, rowid.Int64)
		}
		bad = append(bad, row+" references a missing "+parent+" row")
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(bad) > 0 {
		return errors.New("foreign key check failed: " + strings.Join(bad, "; "))
	}
	return nil
}
