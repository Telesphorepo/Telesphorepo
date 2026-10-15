//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// Server permissions (one test per permission), the CI pipelines permission and the person
// picker (GET /api/people).

// permRoute is a route that one server permission reaches. body returns a new request body for
// each call (nil for none); ok are the answers of a holder.
type permRoute struct {
	method, path string
	body         func() any
	ok           []int
}

// TestServerPermissions gives each server permission to its own person. The holder reaches the
// routes of the permission; every other holder gets 403 there.
func TestServerPermissions(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	repo := RandName("perm")
	s.CreateRepo(t, repo, "raw", false, nil)
	none := func() any { return nil }
	cases := []struct {
		perm     string
		routes   []permRoute
		settings bool // GET /api/settings answers 200
	}{
		{"repos", []permRoute{
			{http.MethodPost, "/api/repos", func() any { return map[string]any{"name": RandName("new"), "format": "raw"} }, []int{http.StatusCreated}},
			{http.MethodGet, "/api/repos/" + repo + "/permissions", none, []int{http.StatusOK}},
		}, false},
		{"people", []permRoute{
			{http.MethodGet, "/api/users", none, []int{http.StatusOK}},
			{http.MethodGet, "/api/users/" + s.AdminUser + "/access", none, []int{http.StatusOK}},
			{http.MethodPost, "/api/users", func() any {
				return map[string]any{"username": RandName("new"), "password": "new-password-1", "permissions": []string{"people"}}
			}, []int{http.StatusCreated}},
		}, false},
		{"signin", []permRoute{
			{http.MethodPatch, "/api/settings", func() any { return map[string]any{"oidc.session_ttl": "11h"} }, []int{http.StatusOK}},
		}, true},
		{"storage", []permRoute{
			{http.MethodGet, "/api/storage/moves/current", none, []int{http.StatusOK, http.StatusNotFound}},
		}, true},
		{"network", []permRoute{
			// The list route also serves settings on a repo (which repos holds); one list needs network.
			{http.MethodGet, "/api/ip-lists/github-actions", none, []int{http.StatusOK}},
			{http.MethodGet, "/api/network/proxy-candidates", none, []int{http.StatusOK}},
			{http.MethodPatch, "/api/settings", func() any { return map[string]any{"trusted_proxies": []string{"192.0.2.1"}} }, []int{http.StatusOK}},
		}, true},
		{"keys", []permRoute{
			{http.MethodGet, "/api/signing-keys/default", none, []int{http.StatusOK}},
			{http.MethodPost, "/api/signing-keys", func() any { return map[string]any{"name": RandName("key")} }, []int{http.StatusCreated}},
		}, false},
		{"nexus", []permRoute{
			{http.MethodGet, "/api/nexus/sources", none, []int{http.StatusOK}},
		}, false},
		{"general", []permRoute{
			{http.MethodPatch, "/api/settings", func() any { return map[string]any{"session_ttl": "100h"} }, []int{http.StatusOK}},
		}, true},
		{"activity", []permRoute{
			{http.MethodGet, "/api/audit", none, []int{http.StatusOK}},
			{http.MethodGet, "/api/publish/log", none, []int{http.StatusOK}},
		}, false},
	}
	if len(cases) != len(AllServerPerms) {
		t.Fatalf("%d cases for %d server permissions", len(cases), len(AllServerPerms))
	}

	sessions := map[string]*Session{}
	for _, c := range cases {
		u := RandName(c.perm)
		s.CreateUser(t, u, u+"-password-1", c.perm)
		sessions[c.perm] = s.SignIn(t, u, u+"-password-1")
	}

	for _, c := range cases {
		t.Run(c.perm, func(t *testing.T) {
			holder := sessions[c.perm]
			for _, r := range c.routes {
				st, b := s.API(t, r.method, r.path, r.body(), holder)
				wantAPIStatus(t, fmt.Sprintf("%s %s with %s", r.method, r.path, c.perm), st, b, r.ok...)
				for _, o := range cases {
					if o.perm == c.perm {
						continue
					}
					st, b := s.API(t, r.method, r.path, r.body(), sessions[o.perm])
					wantAPIStatus(t, fmt.Sprintf("%s %s with %s only", r.method, r.path, o.perm), st, b, http.StatusForbidden)
				}
				// A password reaches no permission route.
				st, b = s.API(t, r.method, r.path, r.body(), s.AdminBasic())
				wantAPIStatus(t, fmt.Sprintf("%s %s with the admin's password", r.method, r.path), st, b, http.StatusUnauthorized)
			}
			want := http.StatusForbidden
			if c.settings {
				want = http.StatusOK
			}
			st, b := s.API(t, http.MethodGet, "/api/settings", nil, holder)
			wantAPIStatus(t, "GET /api/settings with "+c.perm, st, b, want)
		})
	}
}

// TestPipelinesPermission: a person sees and edits a CI pipeline only when they hold pipelines on
// every repository of it.
func TestPipelinesPermission(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	mine, other := RandName("mine"), RandName("other")
	s.CreateRepo(t, mine, "raw", false, nil)
	s.CreateRepo(t, other, "raw", false, nil)
	ci, pub := RandName("ci"), RandName("pub")
	s.CreateUser(t, ci, "ci-password-1")
	s.CreateUser(t, pub, "pub-password-1")
	s.SetPerms(t, mine, ci, "download", "publish", "pipelines")
	s.SetPerms(t, other, ci, "download", "publish")
	s.SetPerms(t, mine, pub, "download", "publish")
	sess := s.SignIn(t, ci, "ci-password-1")

	policy := func(repos ...string) map[string]any {
		return map[string]any{
			"name": RandName("trust"), "issuer": "https://issuer.example", "audience": "telesphorepo",
			"claims": map[string][]string{"repository": {"acme/app"}}, "repos": repos,
		}
	}
	idOf := func(b []byte) int64 {
		t.Helper()
		var out struct {
			ID int64 `json:"id"`
		}
		if err := json.Unmarshal(b, &out); err != nil || out.ID == 0 {
			t.Fatalf("trust answer %s: no id (%v)", b, err)
		}
		return out.ID
	}

	// Create: only repositories where ci holds pipelines.
	st, b := s.API(t, http.MethodPost, "/api/publish/trusts", policy(mine), sess)
	wantAPIStatus(t, "create a pipeline for a repository with pipelines", st, b, http.StatusCreated)
	own := idOf(b)
	for _, repos := range [][]string{{other}, {mine, other}, {}} {
		st, b := s.API(t, http.MethodPost, "/api/publish/trusts", policy(repos...), sess)
		wantAPIStatus(t, fmt.Sprintf("create a pipeline for %v", repos), st, b, http.StatusForbidden)
	}

	// The admin's pipelines: one in scope, one outside it.
	var inScope, outside struct {
		ID int64 `json:"id"`
	}
	s.MustAPI(t, http.MethodPost, "/api/publish/trusts", policy(mine), s.Admin(), &inScope)
	s.MustAPI(t, http.MethodPost, "/api/publish/trusts", policy(mine, other), s.Admin(), &outside)

	var list struct {
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
	}
	s.MustAPI(t, http.MethodGet, "/api/publish/trusts?limit=200", nil, sess, &list)
	var ids []int64
	for _, it := range list.Items {
		ids = append(ids, it.ID)
	}
	if !slices.Contains(ids, own) || !slices.Contains(ids, inScope.ID) || slices.Contains(ids, outside.ID) {
		t.Fatalf("GET /api/publish/trusts: ids %v, want %d and %d, not %d", ids, own, inScope.ID, outside.ID)
	}
	s.MustAPI(t, http.MethodGet, "/api/publish/trusts?limit=200", nil, s.Admin(), &list)
	if len(list.Items) != 3 {
		t.Fatalf("GET /api/publish/trusts as the admin: %d pipelines, want 3", len(list.Items))
	}

	// A pipeline outside the scope answers 404 to every route.
	out := fmt.Sprintf("/api/publish/trusts/%d", outside.ID)
	for _, m := range []string{http.MethodGet, http.MethodPatch, http.MethodDelete} {
		var body any
		if m == http.MethodPatch {
			body = map[string]any{"disabled": true}
		}
		st, b := s.API(t, m, out, body, sess)
		wantAPIStatus(t, m+" a pipeline outside the scope", st, b, http.StatusNotFound)
	}

	// Edit in scope; a new repository outside the scope answers 403.
	mineURL := fmt.Sprintf("/api/publish/trusts/%d", own)
	s.MustAPI(t, http.MethodPatch, mineURL, map[string]any{"disabled": true}, sess, nil)
	st, b = s.API(t, http.MethodPatch, mineURL, map[string]any{"repos": []string{mine, other}}, sess)
	wantAPIStatus(t, "move a pipeline to a repository outside the scope", st, b, http.StatusForbidden)
	s.MustAPI(t, http.MethodDelete, fmt.Sprintf("/api/publish/trusts/%d", inScope.ID), nil, sess, nil)

	// Without pipelines anywhere: 403; with a password: 401.
	pubUI := s.SignIn(t, pub, "pub-password-1")
	st, b = s.API(t, http.MethodGet, "/api/publish/trusts", nil, pubUI)
	wantAPIStatus(t, "list pipelines without the permission", st, b, http.StatusForbidden)
	st, b = s.API(t, http.MethodPost, "/api/publish/trusts", policy(mine), pubUI)
	wantAPIStatus(t, "create a pipeline without the permission", st, b, http.StatusForbidden)
	st, b = s.API(t, http.MethodGet, "/api/publish/trusts", nil, Basic{ci, "ci-password-1"})
	wantAPIStatus(t, "list pipelines with a password", st, b, http.StatusUnauthorized)

	// A pipeline shows on the access page of its repository for a holder of pipelines.
	var acc repoAccess
	s.MustAPI(t, http.MethodGet, "/api/repos/"+mine+"/access", nil, sess, &acc)
	listed := false
	for _, c := range acc.Publish.CI {
		listed = listed || c.ID == own
	}
	if !listed {
		t.Fatalf("GET /api/repos/%s/access: CI %+v, want pipeline %d", mine, acc.Publish.CI, own)
	}
}

// TestPeoplePicker: GET /api/people lists enabled usernames by prefix, in keyset pages, to a person
// with people or with access on a repository.
func TestPeoplePicker(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	repo := RandName("pick")
	s.CreateRepo(t, repo, "raw", false, nil)
	base := "pick" + randHex(3)
	anna, anton, bert, gone := base+"-anna", base+"-anton", base+"-bert", base+"-anita"
	for _, u := range []string{anna, anton, bert, gone} {
		s.CreateUser(t, u, u+"-password-1")
	}
	s.MustAPI(t, http.MethodPatch, "/api/users", map[string]any{"usernames": []string{gone}, "disabled": true}, s.Admin(), nil)
	mgr, plain := RandName("mgr"), RandName("plain")
	s.CreateUser(t, mgr, "mgr-password-1")
	s.CreateUser(t, plain, "plain-password-1")
	s.SetPerms(t, repo, mgr, "download", "access")
	s.SetPerms(t, repo, plain, "download", "publish")

	type page struct {
		Items []struct {
			Username string `json:"username"`
		} `json:"items"`
		Next *string `json:"next"`
	}
	names := func(p page) []string {
		var out []string
		for _, it := range p.Items {
			out = append(out, it.Username)
		}
		return out
	}

	for _, who := range []struct {
		what string
		sess *Session
	}{{"people", s.Admin()}, {"access on a repository", s.SignIn(t, mgr, "mgr-password-1")}} {
		var p page
		s.MustAPI(t, http.MethodGet, "/api/people?q="+base+"-an", nil, who.sess, &p)
		if got := names(p); !slices.Equal(got, []string{anna, anton}) || p.Next != nil {
			t.Fatalf("GET /api/people?q=%s-an with %s: %v next %v, want [%s %s] (no disabled person)", base, who.what, got, p.Next, anna, anton)
		}
	}

	// Keyset pages: one name per page, case does not matter in q.
	var all []string
	after := ""
	for range 10 {
		var p page
		path := "/api/people?limit=1&q=" + strings.ToUpper(base)
		if after != "" {
			path += "&after=" + after
		}
		s.MustAPI(t, http.MethodGet, path, nil, s.Admin(), &p)
		if len(p.Items) > 1 {
			t.Fatalf("GET %s: %d items, want at most 1", path, len(p.Items))
		}
		all = append(all, names(p)...)
		if p.Next == nil {
			break
		}
		after = *p.Next
	}
	if !slices.Equal(all, []string{anna, anton, bert}) {
		t.Fatalf("pages of /api/people?q=%s: %v, want [%s %s %s]", base, all, anna, anton, bert)
	}

	st, b := s.API(t, http.MethodGet, "/api/people?q="+base, nil, s.SignIn(t, plain, "plain-password-1"))
	wantAPIStatus(t, "GET /api/people without people or access", st, b, http.StatusForbidden)
	st, b = s.API(t, http.MethodGet, "/api/people?q="+base, nil, s.AdminBasic())
	wantAPIStatus(t, "GET /api/people with a password", st, b, http.StatusUnauthorized)
}
