//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
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

// wantGet requires status want for GET /raw/{repo}/f.txt with auth.
func wantGet(t *testing.T, s *Server, repo string, auth Auth, want int) {
	t.Helper()
	resp, body := authGet(t, s, "/raw/"+repo+"/f.txt", auth)
	// On S3 storage an allowed read answers 302 to a presigned URL.
	if want == http.StatusOK && resp.StatusCode == http.StatusFound {
		return
	}
	wantStatus(t, resp, body, want)
}

// lockRepo makes repo a mirror of a Nexus source, so it gets the operation "mirror". The Nexus source is
// a raw repo of the same server that serves the repository list that Nexus would serve.
func lockRepo(t *testing.T, s *Server, repo string) {
	t.Helper()
	fake := RandName("fakenexus")
	s.CreateRepo(t, fake, "raw", false, nil)
	list := `[{"name":"src","format":"raw","type":"hosted"}]`
	resp, body := rawPut(t, s, "/raw/"+fake+"/service/rest/v1/repositorySettings", []byte(list), s.PublishAuth(t, s.AdminUser, fake))
	wantStatus(t, resp, body, http.StatusCreated)
	var src struct {
		ID int64 `json:"id"`
	}
	s.MustAPI(t, http.MethodPost, "/api/nexus/sources", map[string]string{
		"name": RandName("nexus"), "base_url": s.InternalURL + "/raw/" + fake,
		"username": s.AdminUser, "password": s.AdminPass,
	}, s.Admin(), &src)
	s.MustAPI(t, http.MethodPut, "/api/nexus/sources/"+strconv.FormatInt(src.ID, 10)+"/repos",
		map[string]any{"repos": []map[string]any{{"source_repo": "src", "target": repo}}}, s.Admin(), nil)
	var r struct {
		Operation *struct {
			Type    string `json:"type"`
			State   string `json:"state"`
			Message string `json:"message"`
		} `json:"operation"`
	}
	s.MustAPI(t, http.MethodGet, "/api/repos/"+repo, nil, s.Admin(), &r)
	if r.Operation == nil || r.Operation.Type != "mirror" || r.Operation.State != "held" || !strings.Contains(r.Operation.Message, "Uploads and deletes start after the switch-over from Nexus server ") {
		t.Fatalf("repo %s after the Nexus mapping: operation %+v, want a held mirror", repo, r.Operation)
	}
}

func TestBulkRepos(t *testing.T) {
	t.Parallel()
	ForEachStorage(t, func(t *testing.T, opts ServerOpts) {
		if opts.S3 == nil {
			opts.DataVolume = NewDataVolume(t)
		}
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

		// Delete two: both answer 404. Their blobs stay until the blob GC.
		var del struct {
			Deleted []string `json:"deleted"`
			Files   int      `json:"files"`
		}
		s.MustAPI(t, http.MethodPost, "/api/repos/delete", map[string]any{"names": []string{a, b}}, s.Admin(), &del)
		if !slices.Equal(del.Deleted, []string{a, b}) || del.Files != 2 {
			t.Fatalf("delete 2 repos: %+v, want deleted [%s %s] and 2 files", del, a, b)
		}
		for _, r := range []string{a, b} {
			apiWant(t, s, http.MethodGet, "/api/repos/"+r, nil, s.Admin(), http.StatusNotFound)
		}
		var gone []string
		for _, e := range auditAll(t, s, "action=repo.delete") {
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

		// A locked repository in the list: 409 that names it, and nothing is deleted.
		d := rawRepoWithFile(t, s, "bulk-d")
		lockRepo(t, s, c)
		st, body = s.API(t, http.MethodPost, "/api/repos/delete", map[string]any{"names": []string{d, c}}, s.Admin())
		if st != http.StatusConflict || !strings.Contains(string(body), c) {
			t.Fatalf("delete with a locked repo: status %d, want 409 that names %s: %s", st, c, body)
		}
		for _, r := range []string{c, d} {
			apiWant(t, s, http.MethodGet, "/api/repos/"+r, nil, s.Admin(), http.StatusOK)
		}
		// The mirror operation ends at the cut-over only: it cannot be resumed or cleared here, and
		// a repo without an operation has none to end.
		apiWant(t, s, http.MethodPost, "/api/repos/"+c+"/operation/resume", nil, s.Admin(), http.StatusConflict)
		apiWant(t, s, http.MethodDelete, "/api/repos/"+c+"/operation", nil, s.Admin(), http.StatusConflict)
		apiWant(t, s, http.MethodDelete, "/api/repos/"+d+"/operation", nil, s.Admin(), http.StatusConflict)
	})
}

func TestBulkFiles(t *testing.T) {
	t.Parallel()
	ForEachStorage(t, func(t *testing.T, opts ServerOpts) {
		if opts.S3 == nil {
			opts.DataVolume = NewDataVolume(t)
		}
		s := StartServer(t, opts)
		repo, other := RandName("files"), RandName("other")
		s.CreateRepo(t, repo, "raw", false, nil)
		s.CreateRepo(t, other, "raw", false, nil)
		w, reader := RandName("writer"), RandName("reader")
		s.CreateUser(t, w, "writer-pass-1234")
		s.CreateUser(t, reader, "reader-pass-1234")
		s.SetPerms(t, repo, w, "repo.download", "repo.publish", "repo.delete")
		s.SetPerms(t, other, w, "repo.download", "repo.publish", "repo.delete")
		s.SetPerms(t, repo, reader, "repo.download")

		ps := s.MintSession(t, w, repo)
		for _, f := range []string{"one.bin", "two.bin", "dir/a.bin", "dir/sub/b.bin", "keep.bin"} {
			resp, body := rawPut(t, s, "/raw/"+repo+"/"+f, []byte("raw "+f), ps.Auth())
			wantStatus(t, resp, body, http.StatusCreated)
		}
		route := "/api/repos/" + repo + "/files/delete"
		pick := map[string][]string{"paths": {"one.bin", "two.bin"}, "prefixes": {"dir"}}

		// A bad path deletes nothing.
		bad := map[string][]string{"paths": {"one.bin", "a/../b"}, "prefixes": {"dir"}}
		apiWant(t, s, http.MethodPost, route, bad, ps.Auth(), http.StatusBadRequest)
		apiWant(t, s, http.MethodPost, route, map[string][]string{"paths": {"one.bin"}, "prefixes": {"a//b"}}, ps.Auth(), http.StatusBadRequest)
		apiWant(t, s, http.MethodPost, route, map[string][]string{}, ps.Auth(), http.StatusBadRequest)

		// A read token gets 401; a publish session of another repository is refused.
		token := Basic{"x", s.CreateToken(t, reader, "reader-pass-1234", "read")}
		apiWant(t, s, http.MethodPost, route, pick, token, http.StatusUnauthorized)
		if st, b := s.API(t, http.MethodPost, route, pick, s.MintSession(t, w, other).Auth()); st != http.StatusUnauthorized && st != http.StatusForbidden {
			t.Fatalf("delete with a publish session of another repo: status %d, want 401 or 403: %s", st, b)
		}
		if got := nodeNames(treeAll(t, s, repo, "", 50, s.Admin())); !slices.Equal(got, []string{"dir:dir", "file:keep.bin", "file:one.bin", "file:two.bin"}) {
			t.Fatalf("root after the refused deletes: %v", got)
		}

		// Two files and one folder in one call.
		var out struct {
			Deleted int `json:"deleted"`
		}
		s.MustAPI(t, http.MethodPost, route, pick, ps.Auth(), &out)
		if out.Deleted != 4 {
			t.Fatalf("delete 2 files and a folder of 2: deleted %d, want 4", out.Deleted)
		}
		if got := nodeNames(treeAll(t, s, repo, "", 50, s.Admin())); !slices.Equal(got, []string{"file:keep.bin"}) {
			t.Fatalf("root after the delete: %v", got)
		}
		// Deleted files leave their blobs to the blob GC; every blob is still stored.
		keys := storedKeys(t, opts)
		for _, f := range []string{"one.bin", "two.bin", "dir/a.bin", "dir/sub/b.bin", "keep.bin"} {
			if !slices.Contains(keys, blobKey(sha256Hex([]byte("raw "+f)))) {
				t.Fatalf("blob of %s is gone before the blob GC", f)
			}
		}
		// One event per file and one per folder.
		var targets []string
		for _, e := range auditAll(t, s, "action=file.delete&repo="+repo) {
			targets = append(targets, e.Target)
		}
		slices.Sort(targets)
		if !slices.Equal(targets, []string{"dir/", "one.bin", "two.bin"}) {
			t.Fatalf("delete events: %v, want dir/, one.bin and two.bin", targets)
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
	if st != http.StatusBadRequest || !strings.Contains(string(body), "You cannot disable your own account.") {
		t.Fatalf("disable a list with the caller: status %d, want 400: %s", st, body)
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

// TestBulkSessionRule checks that every list route needs a UI session (a password over HTTP Basic
// is refused) and holds 1 to 1000 items.
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
		{http.MethodPut, "/api/grants", map[string]any{"users": []string{"erin"}, "repos": []string{repo}, "perms": []string{"repo.download", "repo.publish"}}},
		{http.MethodPut, "/api/grants", map[string]any{"users": []string{"erin"}, "repos": []string{repo}, "perms": []string{}}},
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
