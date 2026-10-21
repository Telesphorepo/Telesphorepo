//go:build e2e

package e2e

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// probeMigration is the next core migration of the newer build of TestMigrations: a new table
// that the migration fills from the rows the older build wrote.
const probeMigration = `-- E2E: the next core migration of TestMigrations.
CREATE TABLE e2e_probe (
  id   INTEGER PRIMARY KEY,
  note TEXT NOT NULL
) STRICT;
INSERT INTO e2e_probe (note) SELECT 'repo ' || name FROM repos;
INSERT INTO e2e_probe (note) SELECT 'user ' || username FROM users;
`

// TestMigrations: a real upgrade. The server of this tree writes a repo, a user, a grant and a
// file; a newer build with one more core migration starts on the same data volume, applies only
// that migration, logs it, and reads every row back; the new table holds the rows of the older
// build. A build whose applied migration 0001 has another content refuses to start and names the
// component and the file, and the older build refuses the database of the newer one. Neither
// refusal changes the database: the newer build starts on it again.
func TestMigrations(t *testing.T) {
	t.Parallel()
	const dir, initFile = "internal/db/migrations", "internal/db/migrations/0001_init.sql"
	initial, err := os.ReadFile(filepath.Join(repoRoot(t), filepath.FromSlash(initFile)))
	if err != nil {
		t.Fatal(err)
	}
	// The probe is the version after the last core migration of this tree.
	files, err := filepath.Glob(filepath.Join(repoRoot(t), filepath.FromSlash(dir), "*.sql"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range files {
		names = append(names, filepath.Base(f))
	}
	slices.Sort(names)
	version := fmt.Sprintf("%04d", len(names)+1)
	probeName := version + "_e2e_probe.sql"
	probeFile := dir + "/" + probeName
	older := BuildServer(t, "", "", "")
	newer := BuildServerWith(t, "", "", "", map[string][]byte{probeFile: []byte(probeMigration)})
	edited := BuildServerWith(t, "", "", "", map[string][]byte{
		probeFile: []byte(probeMigration),
		initFile:  append(initial, "\n-- E2E: an edit of an applied migration.\n"...),
	})

	vol := NewDataVolume(t)
	first := StartServer(t, ServerOpts{DataVolume: vol, Files: []ContainerFile{ServerBinary(older)}})
	repo, user, pass := RandName("mig"), RandName("migu"), "migration-pass-1"
	first.CreateRepo(t, repo, "raw", false, nil)
	first.CreateUser(t, user, pass)
	first.SetPerms(t, repo, user, "repo.download")
	path := "/raw/" + repo + "/notes/upgrade.txt"
	resp, body := rawPut(t, first, path, []byte("written before the upgrade"), first.PublishAuth(t, first.AdminUser, repo))
	wantStatus(t, resp, body, http.StatusCreated)
	stopServer(t, first)

	readBack := func(s *Server) {
		t.Helper()
		resp, body := authGet(t, s, path, Basic{user, pass})
		wantStatus(t, resp, body, http.StatusOK)
		if string(body) != "written before the upgrade" {
			t.Fatalf("GET %s after the upgrade: %q", path, body)
		}
		var me struct {
			Username        string              `json:"username"`
			RepoPermissions map[string][]string `json:"repo_permissions"`
		}
		s.MustAPI(t, http.MethodGet, "/api/me", nil, s.SignIn(t, user, pass), &me)
		if me.Username != user || !slices.Equal(me.RepoPermissions[repo], []string{"repo.download"}) {
			t.Fatalf("GET /api/me of %s after the upgrade: %+v, want repo.download on %s", user, me, repo)
		}
	}

	second := StartServer(t, ServerOpts{DataVolume: vol, Files: []ContainerFile{ServerBinary(newer)}})
	logs := second.Logs(t)
	if !strings.Contains(logs, `"msg":"applied migration","component":"core","migration":"`+probeName+`"`) {
		t.Fatalf("the newer build did not log the migration %s:\n%s", probeName, logs)
	}
	if strings.Contains(logs, `"migration":"0001_init.sql"`) {
		t.Fatalf("the newer build applied a migration 0001 again:\n%s", logs)
	}
	readBack(second)
	stopServer(t, second)

	d, err := sql.Open("sqlite", exportDB(t, vol))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if core := dbColumn(t, d, `SELECT name FROM schema_migrations WHERE component = 'core' ORDER BY version`); !slices.Equal(core, append(names, probeName)) {
		t.Fatalf("core migrations of the database: %v, want %v", core, append(names, probeName))
	}
	notes := dbColumn(t, d, `SELECT note FROM e2e_probe`)
	for _, want := range []string{"repo " + repo, "user " + user, "user " + first.AdminUser} {
		if !slices.Contains(notes, want) {
			t.Fatalf("e2e_probe holds %v, want %q from the rows of the older build", notes, want)
		}
	}

	wantRefusal(t, vol, edited, "the database applied the core migration 0001_init.sql, and that file has changed since; an applied migration must not change")
	wantRefusal(t, vol, older, "the database is from a newer Telesphorepo (core migration "+version+"); run that version or newer")

	third := StartServer(t, ServerOpts{DataVolume: vol, Files: []ContainerFile{ServerBinary(newer)}})
	if logs := third.Logs(t); strings.Contains(logs, `"msg":"applied migration"`) {
		t.Fatalf("the newer build applied a migration on its own database:\n%s", logs)
	}
	readBack(third)
}

// dbColumn returns the first column of every row of query on d.
func dbColumn(t *testing.T, d *sql.DB, query string) []string {
	t.Helper()
	rows, err := d.Query(query)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return out
}

// stopServer stops the server container gracefully; its data volume stays.
func stopServer(t *testing.T, s *Server) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := s.Stop(ctx, nil); err != nil {
		t.Fatalf("stop server %s: %v", s.Alias, err)
	}
}

// wantRefusal runs "telesphorepo-server serve" of the binary bin on data volume vol and wants it to
// exit with an error whose output holds want.
func wantRefusal(t *testing.T, vol, bin, want string) {
	t.Helper()
	r := RunClientResult(t, ServerImage, ClientOpts{
		NoShell: true,
		Files:   []ContainerFile{ServerBinary(bin)},
		Mounts:  map[string]string{vol: "/data"},
		Timeout: 2 * time.Minute,
	})
	if r.ExitCode == 0 || !strings.Contains(r.Output, want) {
		t.Fatalf("serve on the database: exit %d, want an error with %q:\n%s", r.ExitCode, want, r.Output)
	}
}
