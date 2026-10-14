//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// The audit log (spec §13): events of sign-ins, publish sessions, uploads, deletes, repos,
// permissions, users and settings; admin only; the only place that shows IP addresses.

type auditEntry struct {
	ID          int64          `json:"id"`
	Actor       string         `json:"actor"`
	ActorUserID *int64         `json:"actor_user_id"`
	Action      string         `json:"action"`
	Repo        string         `json:"repo"`
	Target      string         `json:"target"`
	Detail      map[string]any `json:"detail"`
	IP          string         `json:"ip"`
}

// auditAll lists the audit log with the query q (e.g. "action=upload"), following the pages.
func auditAll(t *testing.T, s *Server, q string) []auditEntry {
	t.Helper()
	var all []auditEntry
	after := ""
	for i := 0; i < 100; i++ {
		p := "/api/audit?limit=200"
		if q != "" {
			p += "&" + q
		}
		if after != "" {
			p += "&after=" + url.QueryEscape(after)
		}
		var page struct {
			Items []auditEntry `json:"items"`
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

func findAudit(entries []auditEntry, action, actor string) *auditEntry {
	for i := range entries {
		if entries[i].Action == action && (actor == "" || entries[i].Actor == actor) {
			return &entries[i]
		}
	}
	return nil
}

func TestAudit(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	repo, other := RandName("audit"), RandName("other")
	s.CreateRepo(t, repo, "maven", false, nil)
	s.CreateRepo(t, other, "raw", false, nil)
	w, pw := RandName("writer"), "writer-pass-1234"
	s.CreateUser(t, w, pw, false)
	s.SetPermission(t, repo, w, "write")
	s.SetPermission(t, other, w, "write")

	if st, b := s.API(t, http.MethodPost, "/api/auth/login", map[string]string{"username": w, "password": "wrong-password"}, nil); st != http.StatusUnauthorized {
		t.Fatalf("login with a wrong password: status %d: %s", st, b)
	}
	sess := s.Login(t, w, pw)
	ps := s.MintSession(t, w, repo)
	resp, body := authPut(t, s, "/maven/"+repo+"/g/a/1.0/a-1.0.jar", "jar", ps.Auth())
	wantStatus(t, resp, body, http.StatusCreated)
	resp, body = rawPut(t, s, "/raw/"+other+"/f.bin", []byte("x"), s.MintSession(t, w, other).Auth())
	wantStatus(t, resp, body, http.StatusCreated)
	mustDelete(t, s, repo, "path", "g/a/1.0/a-1.0.jar", ps.Auth(), 1)
	if st := s.CloseSession(t, ps); st/100 != 2 {
		t.Fatalf("close session: status %d", st)
	}
	setSetting(t, s, "session_ttl", "100h")
	s.MustAPI(t, http.MethodPatch, "/api/users/"+w, map[string]any{"password": "new-writer-pass-99"}, s.Admin(), nil)
	s.MustAPI(t, http.MethodPatch, "/api/repos/"+repo, map[string]any{"description": "audited repo"}, s.Admin(), nil)

	all := auditAll(t, s, "")
	for _, c := range []struct{ action, actor string }{
		{"signin.failed", w}, {"signin", w}, {"session.create", w}, {"upload", w}, {"delete", w},
		{"session.close", w}, {"settings.update", s.AdminUser}, {"user.create", s.AdminUser},
		{"user.update", s.AdminUser}, {"repo.create", s.AdminUser}, {"repo.update", s.AdminUser},
		{"permission.set", s.AdminUser},
	} {
		if findAudit(all, c.action, c.actor) == nil {
			t.Fatalf("audit log has no %s event by %s", c.action, c.actor)
		}
	}
	for i := 1; i < len(all); i++ {
		if all[i].ID >= all[i-1].ID {
			t.Fatalf("audit log is not newest first at %d: %d after %d", i, all[i].ID, all[i-1].ID)
		}
	}

	failed := findAudit(all, "signin.failed", w)
	if failed.IP == "" {
		t.Fatalf("failed sign-in without an IP address: %+v", failed)
	}
	ip := failed.IP
	var up *auditEntry
	for i := range all {
		if all[i].Action == "upload" && all[i].Repo == repo {
			up = &all[i]
		}
	}
	if up == nil || up.Actor != w || up.Target != "g/a/1.0/a-1.0.jar" || up.ActorUserID == nil {
		t.Fatalf("upload event %+v: want actor %s, target g/a/1.0/a-1.0.jar and the user id", up, w)
	}
	if del := findAudit(all, "delete", w); del.Detail["count"] != float64(1) {
		t.Fatalf("delete event %+v: want count 1", del)
	}
	// Settings and user changes name the keys and fields, never the values.
	raw, _ := json.Marshal(all)
	for _, secret := range []string{"100h", "new-writer-pass-99", "wrong-password", ps.Token} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("audit log holds the value %q", secret)
		}
	}
	if set := findAudit(all, "settings.update", s.AdminUser); fmt.Sprint(set.Detail["keys"]) != "[session_ttl]" {
		t.Fatalf("settings event %+v: want keys [session_ttl]", set)
	}

	// Filters and pages.
	for _, e := range auditAll(t, s, "action=upload") {
		if e.Action != "upload" {
			t.Fatalf("?action=upload returned %s", e.Action)
		}
	}
	byRepo := auditAll(t, s, "repo="+repo)
	if len(byRepo) == 0 {
		t.Fatal("?repo= returned no event")
	}
	for _, e := range byRepo {
		if e.Repo != repo {
			t.Fatalf("?repo=%s returned an event of %q: %+v", repo, e.Repo, e)
		}
	}
	if got := auditAll(t, s, "repo="+repo+"&action=delete"); len(got) != 1 || got[0].Action != "delete" {
		t.Fatalf("?repo=&action=delete: %+v", got)
	}
	var page struct {
		Items []auditEntry `json:"items"`
		Next  *string      `json:"next"`
	}
	s.MustAPI(t, http.MethodGet, "/api/audit?limit=2", nil, s.Admin(), &page)
	if len(page.Items) != 2 || page.Next == nil {
		t.Fatalf("first page of 2: %d items, next %v", len(page.Items), page.Next)
	}

	// Admin only.
	user := Basic{w, "new-writer-pass-99"}
	sess = s.Login(t, w, "new-writer-pass-99")
	if st, b := s.API(t, http.MethodGet, "/api/audit", nil, sess); st != http.StatusForbidden {
		t.Fatalf("GET /api/audit as a user: status %d, want 403: %s", st, b)
	}
	for _, a := range []Auth{user, s.AdminBasic(), nil} {
		if st, b := s.API(t, http.MethodGet, "/api/audit", nil, a); st != http.StatusUnauthorized {
			t.Fatalf("GET /api/audit without a UI session: status %d, want 401: %s", st, b)
		}
	}

	// No other API shows an IP address.
	for _, c := range []struct {
		path string
		auth Auth
	}{
		{"/api/me", sess}, {"/api/devices", sess}, {"/api/publish/sessions", sess}, {"/api/tokens", user},
		{"/api/repos", user}, {"/api/repos/" + repo, user}, {"/api/repos/" + repo + "/tree", user},
		{"/api/repos/" + repo + "/assets", user}, {"/api/publish/log", s.Admin()}, {"/api/devices", s.Admin()},
		{"/api/users", s.Admin()},
	} {
		st, b := s.API(t, http.MethodGet, c.path, nil, c.auth)
		if st != http.StatusOK {
			t.Fatalf("GET %s: status %d: %s", c.path, st, b)
		}
		if strings.Contains(string(b), ip) || strings.Contains(string(b), `"ip"`) {
			t.Fatalf("GET %s shows an IP address: %s", c.path, b)
		}
	}
}
