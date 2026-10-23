//go:build e2e

package e2e

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The download statistics (GET /api/stats) and the Activity filters by person and by time.

type downloadStats struct {
	Days []struct {
		Day       string `json:"day"`
		Downloads int64  `json:"downloads"`
		Bytes     int64  `json:"bytes"`
	} `json:"days"`
	Repos []struct {
		Name      string `json:"name"`
		Format    string `json:"format"`
		Downloads int64  `json:"downloads"`
		Bytes     int64  `json:"bytes"`
	} `json:"repos"`
	Downloads int64 `json:"downloads"`
	Bytes     int64 `json:"bytes"`
}

func TestDownloadStats(t *testing.T) {
	t.Parallel()
	s := StartServer(t, ServerOpts{})
	pub, priv := RandName("pub"), RandName("priv")
	s.CreateRepo(t, pub, "raw", true, nil)
	s.CreateRepo(t, priv, "raw", false, nil)
	w, pw := RandName("writer"), "writer-pass-1234"
	s.CreateUser(t, w, pw)
	s.SetPerms(t, pub, w, "repo.download", "repo.publish")
	s.SetPerms(t, priv, w, "repo.download", "repo.publish")
	x, y, z := strings.Repeat("x", 100), "seven b", strings.Repeat("z", 50)
	for _, f := range []struct{ repo, path, body string }{{pub, "x.bin", x}, {pub, "y.bin", y}, {priv, "z.bin", z}} {
		resp, body := rawPut(t, s, "/raw/"+f.repo+"/"+f.path, []byte(f.body), s.MintSession(t, w, f.repo).Auth())
		wantStatus(t, resp, body, http.StatusCreated)
	}

	// Counted: each GET that gets the bytes (a 200, a 206 counts its range).
	for range 3 {
		resp, body := authGet(t, s, "/raw/"+pub+"/x.bin", nil)
		wantStatus(t, resp, body, http.StatusOK)
	}
	resp, body := authGet(t, s, "/raw/"+pub+"/y.bin", nil)
	wantStatus(t, resp, body, http.StatusOK)
	for range 2 {
		resp, body := authGet(t, s, "/raw/"+priv+"/z.bin", Basic{w, pw})
		wantStatus(t, resp, body, http.StatusOK)
	}
	req := s.NewRequest(t, http.MethodGet, "/raw/"+pub+"/x.bin", nil)
	req.Header.Set("Range", "bytes=0-9")
	resp, body = s.Do(t, req, nil)
	wantStatus(t, resp, body, http.StatusPartialContent)
	etag := resp.Header.Get("ETag")

	// Not counted: a HEAD, a 304, a 404, a refused read.
	resp, body = s.Do(t, s.NewRequest(t, http.MethodHead, "/raw/"+pub+"/x.bin", nil), nil)
	wantStatus(t, resp, body, http.StatusOK)
	req = s.NewRequest(t, http.MethodGet, "/raw/"+pub+"/x.bin", nil)
	req.Header.Set("If-None-Match", etag)
	resp, body = s.Do(t, req, nil)
	wantStatus(t, resp, body, http.StatusNotModified)
	resp, body = authGet(t, s, "/raw/"+pub+"/missing.bin", nil)
	wantStatus(t, resp, body, http.StatusNotFound)
	resp, body = authGet(t, s, "/raw/"+priv+"/z.bin", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous GET of a private file: status %d: %s", resp.StatusCode, body)
	}

	// The read writes the counters first, so it holds every download served before it. Two days,
	// so a test that runs over midnight UTC still finds them.
	var st downloadStats
	s.MustAPI(t, http.MethodGet, "/api/stats?days=2", nil, s.Admin(), &st)
	pubN, pubB := int64(5), int64(3*len(x)+len(y)+10)
	privN, privB := int64(2), int64(2*len(z))
	if st.Downloads != pubN+privN || st.Bytes != pubB+privB {
		t.Fatalf("stats totals %d downloads, %d bytes; want %d, %d: %+v", st.Downloads, st.Bytes, pubN+privN, pubB+privB, st)
	}
	if len(st.Days) != 2 {
		t.Fatalf("?days=2 gave %d days: %+v", len(st.Days), st.Days)
	}
	if today := time.Now().UTC().Format(time.DateOnly); st.Days[1].Day != today {
		t.Fatalf("the last day is %s, want today %s", st.Days[1].Day, today)
	}
	var perDay int64
	for _, d := range st.Days {
		perDay += d.Downloads
	}
	if perDay != st.Downloads {
		t.Fatalf("the days hold %d downloads, the total is %d", perDay, st.Downloads)
	}
	if len(st.Repos) != 2 || st.Repos[0].Name != pub || st.Repos[0].Downloads != pubN || st.Repos[0].Bytes != pubB ||
		st.Repos[0].Format != "raw" || st.Repos[1].Name != priv || st.Repos[1].Downloads != privN || st.Repos[1].Bytes != privB {
		t.Fatalf("top repos %+v: want %s (%d, %d bytes) then %s (%d, %d bytes)", st.Repos, pub, pubN, pubB, priv, privN, privB)
	}

	// The repository list carries the downloads of each repo it shows, to a visitor too; the
	// stats read above wrote the counters.
	listed := func(a Auth) map[string]*int64 {
		var list []struct {
			Name      string `json:"name"`
			Downloads *int64 `json:"downloads"`
		}
		s.MustAPI(t, http.MethodGet, "/api/repos", nil, a, &list)
		out := map[string]*int64{}
		for _, r := range list {
			out[r.Name] = r.Downloads
		}
		return out
	}
	anon := listed(nil)
	if d := anon[pub]; d == nil || *d != pubN {
		t.Fatalf("GET /api/repos as a visitor: %s has downloads %v, want %d", pub, d, pubN)
	}
	if _, ok := anon[priv]; ok {
		t.Fatalf("GET /api/repos as a visitor lists the private %s", priv)
	}
	if d := listed(Basic{w, pw})[priv]; d == nil || *d != privN {
		t.Fatalf("GET /api/repos as %s: %s has downloads %v, want %d", w, priv, d, privN)
	}

	// Later downloads add to the same day.
	resp, body = authGet(t, s, "/raw/"+priv+"/z.bin", Basic{w, pw})
	wantStatus(t, resp, body, http.StatusOK)
	s.MustAPI(t, http.MethodGet, "/api/stats?days=2", nil, s.Admin(), &st)
	if st.Downloads != pubN+privN+1 {
		t.Fatalf("after one more download: %d downloads, want %d", st.Downloads, pubN+privN+1)
	}
	s.MustAPI(t, http.MethodGet, "/api/stats", nil, s.Admin(), &st)
	if len(st.Days) < 2 || st.Downloads != pubN+privN+1 {
		t.Fatalf("default window: %d days, %d downloads", len(st.Days), st.Downloads)
	}

	// A deleted repo leaves the statistics.
	deleteRepos(t, s, priv)
	s.MustAPI(t, http.MethodGet, "/api/stats?days=2", nil, s.Admin(), &st)
	if len(st.Repos) != 1 || st.Repos[0].Name != pub || st.Downloads != pubN {
		t.Fatalf("after the delete of %s: %+v", priv, st)
	}

	for _, q := range []string{"days=0", "days=abc", "days=-1"} {
		if code, b := s.API(t, http.MethodGet, "/api/stats?"+q, nil, s.Admin()); code != http.StatusBadRequest {
			t.Fatalf("GET /api/stats?%s: status %d, want 400: %s", q, code, b)
		}
	}
	// activity.read only, with a UI session.
	if code, b := s.API(t, http.MethodGet, "/api/stats", nil, s.SignIn(t, w, pw)); code != http.StatusForbidden {
		t.Fatalf("GET /api/stats without activity.read: status %d, want 403: %s", code, b)
	}
	if code, b := s.API(t, http.MethodGet, "/api/stats", nil, s.AdminBasic()); code != http.StatusUnauthorized {
		t.Fatalf("GET /api/stats without a UI session: status %d, want 401: %s", code, b)
	}
}

type timedAudit struct {
	ID     int64     `json:"id"`
	At     time.Time `json:"at"`
	Actor  string    `json:"actor"`
	Action string    `json:"action"`
	Repo   string    `json:"repo"`
}

// auditPages lists the audit log with the query q, following the pages.
func auditPages(t *testing.T, s *Server, q string) []timedAudit {
	t.Helper()
	var all []timedAudit
	after := ""
	for range 100 {
		p := "/api/audit?limit=3&" + q
		if after != "" {
			p += "&after=" + url.QueryEscape(after)
		}
		var page struct {
			Items []timedAudit `json:"items"`
			Next  *string      `json:"next"`
		}
		s.MustAPI(t, http.MethodGet, p, nil, s.Admin(), &page)
		all = append(all, page.Items...)
		if page.Next == nil {
			return all
		}
		after = *page.Next
	}
	t.Fatal("audit log: more than 100 pages")
	return nil
}

func TestAuditFilters(t *testing.T) {
	t.Parallel()
	s := StartServer(t, ServerOpts{})
	r1, r2 := RandName("one"), RandName("two")
	s.CreateRepo(t, r1, "raw", false, nil)
	s.CreateRepo(t, r2, "raw", false, nil)
	p1, p2, pw := RandName("ann"), RandName("bob"), "person-pass-1234"
	for _, p := range []string{p1, p2} {
		s.CreateUser(t, p, pw)
		s.SetPerms(t, r1, p, "repo.download", "repo.publish")
		s.SetPerms(t, r2, p, "repo.download", "repo.publish")
	}
	upload := func(user, repo, path string) {
		t.Helper()
		resp, body := rawPut(t, s, "/raw/"+repo+"/"+path, []byte(path), s.MintSession(t, user, repo).Auth())
		wantStatus(t, resp, body, http.StatusCreated)
	}

	// Before: ann uploads to both repos, bob to one.
	upload(p1, r1, "a1.bin")
	upload(p1, r2, "a2.bin")
	upload(p2, r1, "b1.bin")
	last := auditPages(t, s, "")[0].At
	mid := last.Add(time.Second)
	time.Sleep(time.Until(mid) + 100*time.Millisecond)
	// After: ann uploads once more to each repo, bob to the other.
	upload(p1, r1, "a3.bin")
	upload(p1, r2, "a4.bin")
	upload(p2, r2, "b2.bin")
	since := url.QueryEscape(mid.Format(time.RFC3339))

	uploads := func(es []timedAudit) []string {
		var out []string
		for _, e := range es {
			if e.Action == "file.upload" {
				out = append(out, e.Actor+" "+e.Repo)
			}
		}
		return out
	}
	check := func(q string, want ...string) {
		t.Helper()
		es := auditPages(t, s, q)
		for i := 1; i < len(es); i++ {
			if es[i].ID >= es[i-1].ID {
				t.Fatalf("?%s is not newest first: %d after %d", q, es[i].ID, es[i-1].ID)
			}
		}
		if got := uploads(es); strings.Join(got, ", ") != strings.Join(want, ", ") {
			t.Fatalf("?%s: uploads %q, want %q", q, got, want)
		}
	}

	check("actor="+p1, p1+" "+r2, p1+" "+r1, p1+" "+r2, p1+" "+r1)
	for _, e := range auditPages(t, s, "actor="+p1) {
		if e.Actor != p1 {
			t.Fatalf("?actor=%s returned an event of %s: %+v", p1, e.Actor, e)
		}
	}
	check("actor="+p1+"&action=file.upload", p1+" "+r2, p1+" "+r1, p1+" "+r2, p1+" "+r1)
	if got := auditPages(t, s, "actor="+p1+"&action=file.upload"); len(got) != 4 {
		t.Fatalf("?actor=&action=file.upload: %d events, want 4", len(got))
	}
	check("actor="+p1+"&repo="+r1, p1+" "+r1, p1+" "+r1)
	check("actor="+p2+"&repo="+r1+"&action=file.upload", p2+" "+r1)
	if got := auditPages(t, s, "actor="+RandName("nobody")); len(got) != 0 {
		t.Fatalf("?actor= of no one: %+v", got)
	}

	// since: at or after; until: before.
	check("since="+since, p2+" "+r2, p1+" "+r2, p1+" "+r1)
	for _, e := range auditPages(t, s, "since="+since) {
		if e.At.Before(mid) {
			t.Fatalf("?since=%s returned an event at %s", mid, e.At)
		}
	}
	for _, e := range auditPages(t, s, "until="+since) {
		if !e.At.Before(mid) {
			t.Fatalf("?until=%s returned an event at %s", mid, e.At)
		}
	}
	check("until="+since+"&action=file.upload", p2+" "+r1, p1+" "+r2, p1+" "+r1)
	check("since="+since+"&actor="+p1, p1+" "+r2, p1+" "+r1)
	check("since="+since+"&actor="+p1+"&repo="+r2+"&action=file.upload", p1+" "+r2)
	check("until="+since+"&actor="+p1+"&repo="+r2, p1+" "+r2)
	check("since="+since+"&repo="+r1+"&action=file.upload", p1+" "+r1)
	check("since="+since+"&until="+since)
	future := url.QueryEscape(time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	check("since=" + future)
	if got := auditPages(t, s, "until="+future); len(got) == 0 {
		t.Fatal("?until= in the future returned no event")
	}
	for _, q := range []string{"since=yesterday", "until=2026-10-22"} {
		if code, b := s.API(t, http.MethodGet, "/api/audit?"+q, nil, s.Admin()); code != http.StatusBadRequest {
			t.Fatalf("GET /api/audit?%s: status %d, want 400: %s", q, code, b)
		}
	}

	// The people of the filter.
	var people struct {
		Items []struct {
			Username string `json:"username"`
		} `json:"items"`
	}
	s.MustAPI(t, http.MethodGet, "/api/audit/people?q="+url.QueryEscape(p1[:5]), nil, s.Admin(), &people)
	found := false
	for _, p := range people.Items {
		found = found || p.Username == p1
		if !strings.HasPrefix(p.Username, p1[:5]) {
			t.Fatalf("?q=%s returned %s", p1[:5], p.Username)
		}
	}
	if !found {
		t.Fatalf("GET /api/audit/people?q=%s: %+v, want %s", p1[:5], people.Items, p1)
	}
	if code, b := s.API(t, http.MethodGet, "/api/audit/people", nil, s.SignIn(t, p2, pw)); code != http.StatusForbidden {
		t.Fatalf("GET /api/audit/people without activity.read: status %d, want 403: %s", code, b)
	}
}
