package db

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
)

//go:embed queries/*.sql
var queriesFS embed.FS

// QuerySet is every query one owner (core or a format plugin) runs, for the plan guard.
type QuerySet struct {
	Component string
	Files     fs.FS             // *.sql files with sqlc "-- name: X :kind" blocks at the root
	Extra     map[string]string // hand-written query name -> SQL
	Allow     map[string]string // query name -> why a scan or temp B-tree is acceptable
}

// CoreQueries is the core QuerySet: the sqlc queries in queries/ and the hand-written ones
// in dbq/extra.go.
func CoreQueries() QuerySet {
	files, err := fs.Sub(queriesFS, "queries")
	if err != nil {
		panic(err) // the embedded directory always exists
	}
	return QuerySet{
		Component: CoreComponent,
		Files:     files,
		Extra:     dbq.Extra,
		Allow: map[string]string{
			"ListSettings":    "loads every stored setting at startup and after a settings write, never on a request path; at most one row per registered setting",
			"HasUsers":        "EXISTS stops at the first row; startup check for the first admin",
			"ListPermissions": "admin-only list of one repo's grants sorted by username; at most one row per user",
			"CountAssets":     "the total of a storage move; runs once when an admin starts a move, never per request",
		},
	}
}

var (
	nameRE  = regexp.MustCompile(`^--\s*name:\s*(\w+)\s+:\w+`)
	sqlcRE  = regexp.MustCompile(`sqlc\.(arg|narg|slice)\s*\([^)]*\)`)
	indexRE = regexp.MustCompile(`\bINDEX (\S+)`)
)

type namedQuery struct {
	name, origin, sql string
}

// parseQueries reads the "-- name:" blocks of every *.sql file at the root of fsys. Comment
// lines are dropped and sqlc.arg/narg/slice(...) become a single "?".
func parseQueries(fsys fs.FS) ([]namedQuery, error) {
	files, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return nil, err
	}
	var out []namedQuery
	for _, file := range files {
		body, err := fs.ReadFile(fsys, file)
		if err != nil {
			return nil, err
		}
		var cur *namedQuery
		var b strings.Builder
		flush := func() {
			if cur != nil {
				cur.sql = sqlcRE.ReplaceAllString(strings.TrimSpace(b.String()), "?")
				out = append(out, *cur)
			}
			b.Reset()
		}
		for _, line := range strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n") {
			t := strings.TrimSpace(line)
			if m := nameRE.FindStringSubmatch(t); m != nil {
				flush()
				cur = &namedQuery{name: m[1], origin: file}
				continue
			}
			if t == "" || strings.HasPrefix(t, "--") {
				continue
			}
			b.WriteString(line)
			b.WriteByte('\n')
		}
		flush()
	}
	return out, nil
}

// CheckPlans runs EXPLAIN QUERY PLAN on every query of sets against d, which must be a
// freshly migrated database (core and every plugin track). It fails on a SCAN of a real table
// (a partial index or a virtual table is fine) or a temp B-tree, unless the query is in its
// set's Allow list. The error lists every offending query with its plan.
func CheckPlans(ctx context.Context, d *DB, sets []QuerySet) error {
	partial, err := partialIndexes(ctx, d)
	if err != nil {
		return err
	}
	var problems []string
	seen := map[string]string{}
	for _, set := range sets {
		var qs []namedQuery
		if set.Files != nil {
			if qs, err = parseQueries(set.Files); err != nil {
				return fmt.Errorf("%s: %w", set.Component, err)
			}
		}
		for name, q := range set.Extra {
			qs = append(qs, namedQuery{name: name, origin: "extra", sql: q})
		}
		sort.Slice(qs, func(i, j int) bool { return qs[i].name < qs[j].name })

		names := map[string]bool{}
		for _, q := range qs {
			label := set.Component + "." + q.name + " (" + q.origin + ")"
			if prev, ok := seen[set.Component+"."+q.name]; ok {
				problems = append(problems, fmt.Sprintf("%s: name already used by %s", label, prev))
				continue
			}
			seen[set.Component+"."+q.name] = label
			names[q.name] = true

			plan, err := explain(ctx, d, q.sql)
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s: %v", label, err))
				continue
			}
			bad := false
			for _, step := range plan {
				if badStep(step, partial) {
					bad = true
					break
				}
			}
			_, allowed := set.Allow[q.name]
			switch {
			case bad && !allowed:
				problems = append(problems, fmt.Sprintf("%s: full scan or temp B-tree:\n    %s", label, strings.Join(plan, "\n    ")))
			case !bad && allowed:
				problems = append(problems, fmt.Sprintf("%s: in the allow-list but its plan is clean; remove the entry", label))
			}
		}
		for name := range set.Allow {
			if !names[name] {
				problems = append(problems, fmt.Sprintf("%s.%s: allow-list entry for an unknown query", set.Component, name))
			}
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return errors.New("query plan check failed:\n  " + strings.Join(problems, "\n  "))
	}
	return nil
}

// explain returns the detail column of EXPLAIN QUERY PLAN, with every parameter unbound (NULL).
func explain(ctx context.Context, d *DB, query string) ([]string, error) {
	args := make([]any, strings.Count(query, "?"))
	rows, err := d.R.QueryContext(ctx, "EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, notused int64
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			return nil, err
		}
		plan = append(plan, detail)
	}
	return plan, rows.Err()
}

// badStep reports whether one plan step is a full scan of a real table or a temp B-tree.
func badStep(detail string, partial map[string]bool) bool {
	if strings.Contains(detail, "TEMP B-TREE") {
		return true
	}
	rest, ok := strings.CutPrefix(detail, "SCAN ")
	if !ok {
		return false
	}
	switch {
	case strings.HasPrefix(rest, "CONSTANT ROW"), strings.HasPrefix(rest, "("), strings.HasPrefix(rest, "subquery"):
		return false
	case strings.Contains(rest, "VIRTUAL TABLE"):
		return false
	}
	// A partial index holds only the rows its WHERE selects, so scanning it is bounded by them.
	if m := indexRE.FindStringSubmatch(rest); m != nil && partial[m[1]] {
		return false
	}
	return true
}

// partialIndexes returns the names of the indexes that have a WHERE clause.
func partialIndexes(ctx context.Context, d *DB) (map[string]bool, error) {
	rows, err := d.R.QueryContext(ctx, `SELECT name, sql FROM sqlite_schema WHERE type = 'index' AND sql IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name, def string
		if err := rows.Scan(&name, &def); err != nil {
			return nil, err
		}
		if strings.Contains(strings.ToUpper(def), " WHERE ") {
			out[name] = true
		}
	}
	return out, rows.Err()
}
