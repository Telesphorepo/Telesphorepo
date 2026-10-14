package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/Telesphorepo/Telesphorepo/internal/config"
	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/server"
)

var queryNameRE = regexp.MustCompile(`(?m)^--\s*name:\s*\w+\s+:\w+`)

// dbcheckCmd applies every migration track to a temporary database and runs the plan guard
// on the queries of core and of every format.
func dbcheckCmd(args []string) error {
	if err := noArgs("dbcheck", args); err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "telesphorepo-dbcheck-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(dir, "check.db"))
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer d.Close()
	if err := server.Migrate(ctx, d); err != nil {
		return err
	}
	sets := server.QuerySets()
	if err := db.CheckPlans(ctx, d, sets); err != nil {
		return err
	}
	n := 0
	for _, set := range sets {
		n += len(set.Extra)
		if set.Files == nil {
			continue
		}
		files, err := fs.Glob(set.Files, "*.sql")
		if err != nil {
			return err
		}
		for _, f := range files {
			body, err := fs.ReadFile(set.Files, f)
			if err != nil {
				return err
			}
			n += len(queryNameRE.FindAll(body, -1))
		}
	}
	fmt.Printf("ok: %d queries\n", n)
	return nil
}

const (
	seedReposPerFormat = 10
	seedBatch          = 10000
)

// seedCmd bulk-inserts synthetic rows straight into the database. Every format that
// implements format.Seeder gets seed-<format>-NN repos and an equal share of the N rows.
func seedCmd(args []string) error {
	cmd := find(commands, "seed")
	if len(args) == 0 {
		return &usageError{"missing count N", cmd}
	}
	if len(args) > 1 {
		return &usageError{fmt.Sprintf("unexpected argument %q", args[1]), cmd}
	}
	n, err := strconv.Atoi(args[0])
	if err != nil || n < 1 {
		return &usageError{fmt.Sprintf("N must be a positive number (got %q)", args[0]), cmd}
	}

	dir, _ := config.CLIDataDir()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	ctx := context.Background()
	cfg := &config.Config{DataDir: dir}
	d, err := db.Open(ctx, cfg.DBFile())
	if err != nil {
		return fmt.Errorf("open database %s: %w", cfg.DBFile(), err)
	}
	defer d.Close()
	if err := server.Migrate(ctx, d); err != nil {
		return err
	}

	type target struct {
		seeder format.Seeder
		repo   *repos.Repo
	}
	rs := repos.New(d, format.ValidateRepoConfig)
	var targets []target
	for _, f := range format.All() {
		s, ok := f.(format.Seeder)
		if !ok {
			continue
		}
		for i := 0; i < seedReposPerFormat; i++ {
			r, err := ensureRepo(ctx, rs, fmt.Sprintf("seed-%s-%02d", f.Name(), i), f.Name())
			if err != nil {
				return err
			}
			targets = append(targets, target{s, r})
		}
	}
	if len(targets) == 0 {
		return errors.New("no registered format supports seeding")
	}

	for i, t := range targets {
		share := n / len(targets)
		if i < n%len(targets) {
			share++
		}
		for start := 0; start < share; start += seedBatch {
			cnt := min(seedBatch, share-start)
			err := d.Tx(ctx, func(tx *sql.Tx) error { return t.seeder.Seed(ctx, tx, t.repo, start, cnt) })
			if err != nil {
				return fmt.Errorf("seed repo %s: %w", t.repo.Name, err)
			}
		}
	}

	if _, err := d.W.ExecContext(ctx, `ANALYZE`); err != nil {
		return fmt.Errorf("analyze: %w", err)
	}
	fmt.Printf("seeded %d rows over %d repos in %s\n", n, len(targets), dir)
	return nil
}

// ensureRepo returns the repo called name, creating it (anonymous read, default config) when it
// does not exist.
func ensureRepo(ctx context.Context, rs *repos.Service, name, formatName string) (*repos.Repo, error) {
	r, err := rs.ByName(ctx, name)
	if errors.Is(err, repos.ErrNotFound) {
		if r, err = rs.Create(ctx, name, formatName, true, "", nil); err != nil {
			return nil, fmt.Errorf("create repo %s: %w", name, err)
		}
	} else if err != nil {
		return nil, err
	}
	if r.Format != formatName {
		return nil, fmt.Errorf("repo %s has format %s, want %s", name, r.Format, formatName)
	}
	return r, nil
}
