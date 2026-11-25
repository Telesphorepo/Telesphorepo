//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// Bulk actions: the write routes that take a list (see Bulk actions in SPEC.md).

// rawRepoWithFile creates a private raw repo that holds the file f.txt (body "<repo> file").
func rawRepoWithFile(t *testing.T, s *Server, prefix string) string {
	t.Helper()
	repo := RandName(prefix)
	s.CreateRepo(t, repo, "raw", false, nil)
	resp, body := rawPut(t, s, "/raw/"+repo+"/f.txt", []byte(repo+" file"), s.PublishAuth(t, s.AdminUser, repo))
	wantStatus(t, resp, body, http.StatusCreated)
	return repo
}

// deleteRepos deletes the repos of names as the admin: POST /api/repos/delete answers 202 with
// a running delete operation per repo, in the order of names, and the test waits until each repo
// answers 404. A delete that fails fails the test.
func deleteRepos(t *testing.T, s *Server, names ...string) {
	t.Helper()
	var out struct {
		Deleting []struct {
			Name      string `json:"name"`
			Operation struct {
				Type  string `json:"type"`
				State string `json:"state"`
			} `json:"operation"`
		} `json:"deleting"`
	}
	st, b := s.API(t, http.MethodPost, "/api/repos/delete", map[string]any{"names": names}, s.Admin())
	if st != http.StatusAccepted {
		t.Fatalf("delete %v: status %d, want 202: %s", names, st, b)
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("delete %v: %v: %s", names, err, b)
	}
	if len(out.Deleting) != len(names) {
		t.Fatalf("delete %v: %s, want one entry per repo", names, b)
	}
	for i, d := range out.Deleting {
		if d.Name != names[i] || d.Operation.Type != "delete" || d.Operation.State != "running" {
			t.Fatalf("delete %v: entry %d is %+v, want %s with a running delete", names, i, d, names[i])
		}
	}
	for _, name := range names {
		waitRepoGone(t, s, name)
	}
}

// waitRepoGone waits until GET /api/repos/{name} answers 404; a failed operation of the repo
// fails the test.
func waitRepoGone(t *testing.T, s *Server, name string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		st, b := s.API(t, http.MethodGet, "/api/repos/"+name, nil, s.Admin())
		if st == http.StatusNotFound {
			return
		}
		var r struct {
			Operation *struct {
				State string `json:"state"`
				Error string `json:"error"`
			} `json:"operation"`
		}
		_ = json.Unmarshal(b, &r)
		switch {
		case st != http.StatusOK:
			t.Fatalf("GET %s while it is deleted: status %d: %s", name, st, b)
		case r.Operation != nil && r.Operation.State == "failed":
			t.Fatalf("the delete of %s failed: %s", name, r.Operation.Error)
		case time.Now().After(deadline):
			t.Fatalf("%s still exists 2 minutes after its delete: %s", name, b)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// wantGet requires status want for GET /raw/{repo}/f.txt with auth.
func wantGet(t *testing.T, s *Server, repo string, auth Auth, want int) {
	t.Helper()
	resp, body := authGet(t, s, "/raw/"+repo+"/f.txt", auth)
	wantStatus(t, resp, body, want)
}

func TestBulkRepos(t *testing.T) {
	t.Parallel()
	OnLocalDisk(t, func(t *testing.T, opts ServerOpts) {
		opts.DataVolume = NewDataVolume(t)
		s := StartServer(t, opts)
		a, b, c := rawRepoWithFile(t, s, "bulk-a"), rawRepoWithFile(t, s, "bulk-b"), rawRepoWithFile(t, s, "bulk-c")
		for _, r := range []string{a, b, c} {
			wantGet(t, s, r, nil, http.StatusUnauthorized)
		}

		// Make three repositories public in one call.
		var out struct {
			Changed int `json:"changed"`
		}
		s.MustAPI(t, http.MethodPatch, "/api/repos", map[string]any{"names": []string{a, b, c}, "anonymous_read": true}, s.Admin(), &out)
		if out.Changed != 3 {
			t.Fatalf("make 3 public: changed %d, want 3", out.Changed)
		}
		for _, r := range []string{a, b, c} {
			wantGet(t, s, r, nil, http.StatusOK)
			if n := len(auditAll(t, s, "action=repo.update&repo="+r)); n != 1 {
				t.Fatalf("repo.update events of %s: %d, want 1", r, n)
			}
		}
		// Again: nothing changes, and no event.
		s.MustAPI(t, http.MethodPatch, "/api/repos", map[string]any{"names": []string{a}, "anonymous_read": true}, s.Admin(), &out)
		if out.Changed != 0 {
			t.Fatalf("make a public repo public: changed %d, want 0", out.Changed)
		}

		// One unknown name: 404 with the name, and nothing changes.
		missing := RandName("missing")
		st, body := s.API(t, http.MethodPatch, "/api/repos", map[string]any{"names": []string{a, missing}, "anonymous_read": false}, s.Admin())
		if st != http.StatusNotFound || !strings.Contains(string(body), missing) {
			t.Fatalf("make private with an unknown name: status %d, want 404 that names %s: %s", st, missing, body)
		}
		wantGet(t, s, a, nil, http.StatusOK)

		// A delete with an unknown name deletes nothing.
		apiWant(t, s, http.MethodPost, "/api/repos/delete", map[string]any{"names": []string{a, missing}}, s.Admin(), http.StatusNotFound)
		wantGet(t, s, a, nil, http.StatusOK)

		// Delete two: 202, then both answer 404 once their deletes ran. Their blobs stay until the
		// blob GC.
		deleteRepos(t, s, a, b)
		var gone []string
		for _, e := range auditAll(t, s, "action=repo.delete") {
			if n, _ := e.Detail["count"].(float64); n != 1 {
				t.Fatalf("repo.delete event of %s counts %v files, want 1", e.Target, e.Detail["count"])
			}
			gone = append(gone, e.Target)
		}
		slices.Sort(gone)
		want := []string{a, b}
		slices.Sort(want)
		if !slices.Equal(gone, want) {
			t.Fatalf("repo.delete events: %v, want one each for %v", gone, want)
		}
		keys := storedKeys(t, opts)
		for _, r := range []string{a, b} {
			if k := blobKey(sha256Hex([]byte(r + " file"))); !slices.Contains(keys, k) {
				t.Fatalf("blob %s of a deleted repo is gone before the blob GC", k)
			}
		}
	})
}

func TestBulkPeople(t *testing.T) {
	t.Parallel()
	s := StartServer(t, ServerOpts{})
	repo := rawRepoWithFile(t, s, "people")
	u1, u2, u3 := RandName("dis"), RandName("dis"), RandName("keep")
	for _, u := range []string{u1, u2, u3} {
		s.CreateUser(t, u, u+"-password")
		s.SetPerms(t, repo, u, "repo.download")
	}
	sess := map[string]*Session{u1: s.SignIn(t, u1, u1+"-password"), u2: s.SignIn(t, u2, u2+"-password")}
	for u, ui := range sess {
		apiWant(t, s, http.MethodGet, "/api/me", nil, ui, http.StatusOK)
		wantGet(t, s, repo, Basic{u, u + "-password"}, http.StatusOK)
	}

	// A list with the caller gets 400 and changes nothing.
	st, body := s.API(t, http.MethodPatch, "/api/users", map[string]any{"usernames": []string{u3, s.AdminUser}, "disabled": true}, s.Admin())
	if st != http.StatusBadRequest {
		t.Fatalf("disable a list with the caller: status %d, want 400: %s", st, body)
	}
	for _, u := range []string{u3, s.AdminUser} {
		var got struct {
			Disabled bool `json:"disabled"`
		}
		s.MustAPI(t, http.MethodGet, "/api/users/"+u, nil, s.Admin(), &got)
		if got.Disabled {
			t.Fatalf("%s is disabled after the refused list with the caller", u)
		}
	}
	// An unknown name gets 404 and changes nothing.
	apiWant(t, s, http.MethodPatch, "/api/users", map[string]any{"usernames": []string{u3, RandName("nobody")}, "disabled": true}, s.Admin(), http.StatusNotFound)
	wantGet(t, s, repo, Basic{u3, u3 + "-password"}, http.StatusOK)

	// Disable two: their sessions end and HTTP Basic fails for them.
	var out struct {
		Changed int `json:"changed"`
	}
	s.MustAPI(t, http.MethodPatch, "/api/users", map[string]any{"usernames": []string{u1, u2}, "disabled": true}, s.Admin(), &out)
	if out.Changed != 2 {
		t.Fatalf("disable 2: changed %d, want 2", out.Changed)
	}
	for u, ui := range sess {
		apiWant(t, s, http.MethodGet, "/api/me", nil, ui, http.StatusUnauthorized)
		wantGet(t, s, repo, Basic{u, u + "-password"}, http.StatusUnauthorized)
	}
	wantGet(t, s, repo, Basic{u3, u3 + "-password"}, http.StatusOK)
	var disabled []string
	for _, e := range auditAll(t, s, "action=user.update") {
		if e.Detail["disabled"] == true {
			disabled = append(disabled, e.Target)
		}
	}
	slices.Sort(disabled)
	want := []string{u1, u2}
	slices.Sort(want)
	if !slices.Equal(disabled, want) {
		t.Fatalf("user.update events that disable: %v, want one each for %v", disabled, want)
	}

	// Enable one again.
	s.MustAPI(t, http.MethodPatch, "/api/users", map[string]any{"usernames": []string{u1, u3}, "disabled": false}, s.Admin(), &out)
	if out.Changed != 1 {
		t.Fatalf("enable 1 of 2: changed %d, want 1", out.Changed)
	}
	wantGet(t, s, repo, Basic{u1, u1 + "-password"}, http.StatusOK)
}

func TestBulkAccess(t *testing.T) {
	t.Parallel()
	s := StartServer(t, ServerOpts{})
	r1, r2, r3 := rawRepoWithFile(t, s, "acc"), rawRepoWithFile(t, s, "acc"), rawRepoWithFile(t, s, "acc")
	p1, p2, p3 := RandName("p"), RandName("p"), RandName("p")
	for _, p := range []string{p1, p2, p3} {
		s.CreateUser(t, p, p+"-password")
	}
	basic := func(p string) Basic { return Basic{p, p + "-password"} }
	var out struct {
		Changed int `json:"changed"`
	}

	// One person, download on three repositories.
	s.MustAPI(t, http.MethodPut, "/api/grants", map[string]any{"users": []string{p1}, "repos": []string{r1, r2, r3}, "perms": []string{"repo.download"}}, s.Admin(), &out)
	if out.Changed != 3 {
		t.Fatalf("download on 3 repos: changed %d, want 3", out.Changed)
	}
	for _, r := range []string{r1, r2, r3} {
		wantGet(t, s, r, basic(p1), http.StatusOK)
	}
	// The same set again changes nothing.
	s.MustAPI(t, http.MethodPut, "/api/grants", map[string]any{"users": []string{p1}, "repos": []string{r1, r2, r3}, "perms": []string{"repo.download"}}, s.Admin(), &out)
	if out.Changed != 0 {
		t.Fatalf("the same set again: changed %d, want 0", out.Changed)
	}

	// Three people, download and publish on one repository.
	s.MustAPI(t, http.MethodPut, "/api/grants", map[string]any{"users": []string{p1, p2, p3}, "repos": []string{r1}, "perms": []string{"repo.download", "repo.publish"}}, s.Admin(), &out)
	if out.Changed != 3 {
		t.Fatalf("publish for 3 people: changed %d, want 3", out.Changed)
	}
	for _, p := range []string{p1, p2, p3} {
		resp, body := rawPut(t, s, "/raw/"+r1+"/by-"+p+".txt", []byte(p), s.PublishAuth(t, p, r1))
		wantStatus(t, resp, body, http.StatusCreated)
	}
	events := auditAll(t, s, "action=grant.set&repo="+r1)
	if len(events) != 4 {
		t.Fatalf("grant.set events of %s: %d, want 4", r1, len(events))
	}
	// Each event holds the old and the new set; the newest event of p1 is the change to publish.
	var p1Event *auditEntry
	for i := range events {
		if events[i].Target == p1 {
			p1Event = &events[i]
			break
		}
	}
	if p1Event == nil || fmt.Sprint(p1Event.Detail["old"]) != "[repo.download]" || fmt.Sprint(p1Event.Detail["new"]) != "[repo.download repo.publish]" {
		t.Fatalf("grant.set event of %s: %+v, want old [repo.download] and new [repo.download repo.publish]", p1, p1Event)
	}

	// One unknown name changes nothing.
	nobody := RandName("nobody")
	st, body := s.API(t, http.MethodPut, "/api/grants", map[string]any{"users": []string{p2, nobody}, "repos": []string{r2}, "perms": []string{"repo.download"}}, s.Admin())
	if st != http.StatusNotFound || !strings.Contains(string(body), nobody) {
		t.Fatalf("grant with an unknown user: status %d, want 404 that names %s: %s", st, nobody, body)
	}
	apiWant(t, s, http.MethodPut, "/api/grants", map[string]any{"users": []string{p2}, "repos": []string{r2, RandName("gone")}, "perms": []string{"repo.download"}}, s.Admin(), http.StatusNotFound)
	wantGet(t, s, r2, basic(p2), http.StatusForbidden)
	// An unknown permission and a broken dependency get 400.
	apiWant(t, s, http.MethodPut, "/api/grants", map[string]any{"users": []string{p2}, "repos": []string{r2}, "perms": []string{"bogus"}}, s.Admin(), http.StatusBadRequest)
	apiWant(t, s, http.MethodPut, "/api/grants", map[string]any{"users": []string{p2}, "repos": []string{r2}, "perms": []string{"repo.publish"}}, s.Admin(), http.StatusBadRequest)

	// Remove access with an empty set: 403 after.
	s.MustAPI(t, http.MethodPut, "/api/grants", map[string]any{"users": []string{p1}, "repos": []string{r1, r2, r3}, "perms": []string{}}, s.Admin(), &out)
	if out.Changed != 3 {
		t.Fatalf("remove p1 from 3 repos: changed %d, want 3", out.Changed)
	}
	for _, r := range []string{r1, r2, r3} {
		wantGet(t, s, r, basic(p1), http.StatusForbidden)
	}
	wantGet(t, s, r1, basic(p2), http.StatusOK)
}

// TestBulkSessionRule checks that the list routes of repos, files and people need a UI session (a
// password over HTTP Basic is refused) and hold 1 to 1000 items.
func TestBulkSessionRule(t *testing.T) {
	t.Parallel()
	s := StartServer(t, ServerOpts{})
	repo := rawRepoWithFile(t, s, "rule")
	s.CreateUser(t, "erin", "erin-password-1")
	routes := []struct {
		method, path string
		body         map[string]any
	}{
		{http.MethodPatch, "/api/repos", map[string]any{"names": []string{repo}, "anonymous_read": true}},
		{http.MethodPost, "/api/repos/delete", map[string]any{"names": []string{repo}}},
		{http.MethodPost, "/api/repos/" + repo + "/files/delete", map[string]any{"paths": []string{"f.txt"}}},
		{http.MethodPatch, "/api/users", map[string]any{"usernames": []string{"erin"}, "disabled": true}},
	}
	for _, r := range routes {
		apiWant(t, s, r.method, r.path, r.body, s.AdminBasic(), http.StatusUnauthorized)
	}
	// Nothing changed.
	wantGet(t, s, repo, nil, http.StatusUnauthorized)
	wantGet(t, s, repo, s.AdminBasic(), http.StatusOK)

	// A list of 0 or 1001 items is refused.
	many := make([]string, 1001)
	for i := range many {
		many[i] = RandName("n")
	}
	for _, names := range [][]string{{}, many} {
		apiWant(t, s, http.MethodPatch, "/api/repos", map[string]any{"names": names, "anonymous_read": true}, s.Admin(), http.StatusBadRequest)
		apiWant(t, s, http.MethodPatch, "/api/users", map[string]any{"usernames": names, "disabled": true}, s.Admin(), http.StatusBadRequest)
	}
}
