package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
)

var (
	migrationNameRE = regexp.MustCompile(`^[a-z0-9_]+$`)
	migrationFileRE = regexp.MustCompile(`^(\d{4})_[a-z0-9_]+\.sql$`)
)

// migration creates the next migration of a component, internal/<dir>/migrations/NNNN_<name>.sql.
// dir is the folder of the component under internal/ ("db", or "core", for the kernel).
func migration(dir, name string) error {
	if !migrationNameRE.MatchString(name) {
		return usageErr(fmt.Sprintf("the migration name %q may hold only a-z, 0-9 and _ (go tool task migration db add_repo_notes)", name))
	}
	if dir == "core" {
		dir = "db"
	}
	folder := filepath.Join("internal", dir, "migrations")
	entries, err := os.ReadDir(folder)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s does not exist: name the folder of a component under internal/ (db for the kernel)", filepath.ToSlash(folder))
	}
	if err != nil {
		return err
	}
	next := 1
	for _, e := range entries {
		if m := migrationFileRE.FindStringSubmatch(e.Name()); m != nil {
			if v, _ := strconv.Atoi(m[1]); v >= next {
				next = v + 1
			}
		}
	}
	file := filepath.Join(folder, fmt.Sprintf("%04d_%s.sql", next, name))
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(f, "-- %s. db.Init runs this file once, in one transaction with foreign keys off and\n"+
		"-- checked before the commit. Once a release has applied it, never change it: add the next\n"+
		"-- migration instead.\n\n", filepath.Base(file))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	out.line(green("✓ ") + "created " + filepath.ToSlash(file) + "; run go tool task gen after you write it")
	return nil
}
