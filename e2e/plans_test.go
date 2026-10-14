//go:build e2e

package e2e

import "testing"

// TestQueryPlans runs the plan guard ("telesphorepo dbcheck") inside the real image. It
// checks every core and format query on a fresh database, so a query that no other test
// reaches is still checked.
func TestQueryPlans(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	code, out := s.Exec(t, "", "/telesphorepo", "dbcheck")
	if code != 0 {
		t.Fatalf("telesphorepo dbcheck: exit %d:\n%s", code, out)
	}
	t.Logf("telesphorepo dbcheck:\n%s", out)
}
