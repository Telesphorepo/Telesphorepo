package backup

import (
	"fmt"

	"github.com/Telesphorepo/Telesphorepo/internal/db"
)

// Fit is how the database of a backup fits the migrations of a binary.
type Fit int

const (
	// FitSame: the database applied every migration of the binary, and only those.
	FitSame Fit = iota
	// FitOlder: the binary has migrations that the database lacks; it applies them at start.
	FitOlder
	// FitNewer: the database applied a migration that the binary lacks; the binary refuses to
	// start on it.
	FitNewer
	// FitChanged: an applied migration differs from the binary's file; the binary refuses to start.
	FitChanged
)

// CompareMigrations compares the migrations that a backup's database applied with the migration
// files of comps (the components of a binary). It returns the fit and the migration that decides
// it: the first newer or changed one, or the first that the binary would apply.
func CompareMigrations(applied []db.Migration, comps []db.Component) (Fit, *db.Migration, error) {
	byComp := map[string][]db.Migration{}
	for _, m := range applied {
		byComp[m.Component] = append(byComp[m.Component], m)
	}
	fit, first := FitSame, (*db.Migration)(nil)
	known := map[string]bool{}
	for _, c := range comps {
		known[c.Name] = true
		files, err := c.Files()
		if err != nil {
			return 0, nil, err
		}
		done := byComp[c.Name]
		for i, a := range done {
			if i >= len(files) {
				a := a
				return FitNewer, &a, nil
			}
			if f := files[i]; f.Version != a.Version || f.Name != a.Name || f.Checksum != a.Checksum {
				a := a
				return FitChanged, &a, nil
			}
		}
		if len(files) > len(done) && fit == FitSame {
			fit, first = FitOlder, &files[len(done)]
		}
	}
	for _, m := range applied {
		if !known[m.Component] {
			m := m
			return FitNewer, &m, nil
		}
	}
	return fit, first, nil
}

// MigrationText names migration m for a person: "core migration 0002_assets.sql".
func MigrationText(m *db.Migration) string {
	return fmt.Sprintf("%s migration %s", m.Component, m.Name)
}
