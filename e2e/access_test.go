//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"slices"
	"strings"
	"testing"
)

// Effective access (GET /api/repos/{name}/access, /api/users/{username}/access), the cross-links
// (GET /api/users/{username}/access, /api/me/access) and the repository permissions: settings,
// access, the dependency rules, the last holder of people and the second-factor rule.

type accessPerson struct {
	Username string   `json:"username"`
	Perms    []string `json:"perms"`
	Via      string   `json:"via"`
}

// peopleOf returns each person as "username:perm,perm/via", for comparisons.
func peopleOf(ps []accessPerson) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Username + ":" + strings.Join(p.Perms, ",") + "/" + p.Via
	}
	return out
}

// accessNetworks is the detail of the network guard in the access report.
type accessNetworks struct {
	Any   bool `json:"any"`
	Lists []struct {
		Name  string `json:"name"`
		Title string `json:"title"`
	} `json:"lists"`
	CIDRs []struct {
		CIDR string `json:"cidr"`
		Note string `json:"note"`
	} `json:"cidrs"`
}

// accessGuards are the guard reports of the access report; the network module's guard is
// "network".
type accessGuards struct {
	Network struct {
		Restricted bool           `json:"restricted"`
		Summary    string         `json:"summary"`
		Detail     accessNetworks `json:"detail"`
	} `json:"network"`
}

type repoAccess struct {
	Repo     string         `json:"repo"`
	Format   string         `json:"format"`
	Kind     string         `json:"kind"`
	People   []accessPerson `json:"people"`
	Download struct {
		Public   bool           `json:"public"`
		People   []accessPerson `json:"people"`
		Admins   int            `json:"admins"`
		Guards   accessGuards   `json:"guards"`
		Sentence string         `json:"sentence"`
	} `json:"download"`
	Publish struct {
		People []accessPerson `json:"people"`
		Admins int            `json:"admins"`
		CI     []struct {
			ID           int64    `json:"id"`
			Rule         string   `json:"rule"`
			System       string   `json:"system"`
			Repositories []string `json:"repositories"`
			Branches     []string `json:"branches"`
			Tags         []string `json:"tags"`
			Workflows    []string `json:"workflows"`
			Subjects     []string `json:"subjects"`
			Disabled     bool     `json:"disabled"`
		} `json:"ci"`
		Guards  accessGuards `json:"guards"`
		Blocked *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"blocked"`
		Sentence string `json:"sentence"`
	} `json:"publish"`
}

func getAccess(t *testing.T, s *Server, repo, query string) repoAccess {
	t.Helper()
	var out repoAccess
	s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/access"+query, nil, s.Admin(), &out)
	return out
}

func wantSentence(t *testing.T, what, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s sentence = %q, want %q", what, got, want)
	}
}

func TestEffectiveAccess(t *testing.T) {
	t.Parallel()
	s := StartServer(t, ServerOpts{})
	repo := RandName("acc")
	s.CreateRepo(t, repo, "maven", false, nil)
	reader, writer, gone, boss := RandName("reader"), RandName("writer"), RandName("gone"), RandName("boss")
	for _, u := range []string{reader, writer, gone} {
		s.CreateUser(t, u, u+"-password-1")
	}
	s.CreateUser(t, boss, boss+"-password-1", s.ServerPerms(t)...)
	s.SetPerms(t, repo, reader, "repo.download")
	s.SetPerms(t, repo, writer, "repo.download", "repo.publish")
	s.SetPerms(t, repo, gone, "repo.download", "repo.publish")
	s.SetPerms(t, repo, boss, "repo.download") // a grant of a holder of All repositories changes nothing
	s.MustAPI(t, http.MethodPatch, "/api/users", map[string]any{"usernames": []string{gone}, "disabled": true}, s.Admin(), nil)
	everyRepoPerm := strings.Join(s.RepoPerms(t), ",") // the set of the All repositories permission

	t.Run("people and admins", func(t *testing.T) {
		a := getAccess(t, s, repo, "")
		if a.Repo != repo || a.Format != "maven" || a.Download.Public {
			t.Fatalf("access: %+v", a)
		}
		want := []string{reader + ":repo.download/permission", writer + ":repo.download,repo.publish/permission"}
		slices.Sort(want)
		if got := peopleOf(a.Download.People); !slices.Equal(got, want) {
			t.Fatalf("download people = %v, want %v (no disabled user, no admin)", got, want)
		}
		if got := peopleOf(a.People); !slices.Equal(got, want) {
			t.Fatalf("people = %v, want %v", got, want)
		}
		if got := peopleOf(a.Publish.People); !slices.Equal(got, []string{writer + ":repo.download,repo.publish/permission"}) {
			t.Fatalf("publish people = %v, want only %s", got, writer)
		}
		// At least the bootstrap admin and boss; every admin here is a local account.
		if a.Download.Admins < 2 || a.Publish.Admins != a.Download.Admins {
			t.Fatalf("admins = %d / %d, want the same count, at least 2", a.Download.Admins, a.Publish.Admins)
		}
		if !a.Download.Guards.Network.Detail.Any || !a.Publish.Guards.Network.Detail.Any || len(a.Publish.CI) != 0 || a.Publish.Blocked != nil {
			t.Fatalf("networks, CI or blocked: %+v", a)
		}
		wantSentence(t, "download", a.Download.Sentence, "2 people.")
		wantSentence(t, "publish", a.Publish.Sentence, "1 person.")

		listed := getAccess(t, s, repo, "?admins=1")
		var admins []string
		for _, p := range listed.Download.People {
			if p.Via == "all_repos" {
				if strings.Join(p.Perms, ",") != everyRepoPerm {
					t.Fatalf("?admins=1: %s has %v, want every repository permission", p.Username, p.Perms)
				}
				admins = append(admins, p.Username)
			}
		}
		if !slices.Contains(admins, boss) || !slices.Contains(admins, s.AdminUser) || len(admins) != a.Download.Admins {
			t.Fatalf("?admins=1: admins in people = %v, want %s and %s", admins, s.AdminUser, boss)
		}
	})

	t.Run("CI pipeline and networks", func(t *testing.T) {
		office := "office-" + randHex(2)
		s.MustAPI(t, http.MethodPost, "/api/ip-lists", map[string]any{"name": office, "title": "Office",
			"ranges": []string{"203.0.113.0/24"}}, s.Admin(), nil)
		createTrust(t, s, trustPolicy{Name: "app-main-" + randHex(2), Issuer: "https://token.actions.githubusercontent.com",
			Claims: map[string][]string{"repository": {"acme/app"}, "ref": {"refs/heads/main"},
				"workflow_ref": {"acme/app/.github/workflows/release.yml@*"}}, Repos: []string{repo}})
		s.MustAPI(t, http.MethodPut, "/api/repos/"+repo+"/ip-rules", map[string]any{
			"read":    []map[string]string{{"kind": "cidr", "value": "10.0.0.0/8", "note": "VPN"}},
			"publish": []map[string]string{{"kind": "list", "value": office}},
		}, s.Admin(), nil)

		a := getAccess(t, s, repo, "")
		if len(a.Publish.CI) != 1 {
			t.Fatalf("publish CI = %+v, want one pipeline", a.Publish.CI)
		}
		ci := a.Publish.CI[0]
		if ci.System != "github-actions" || !slices.Equal(ci.Repositories, []string{"acme/app"}) ||
			!slices.Equal(ci.Branches, []string{"main"}) || !slices.Equal(ci.Workflows, []string{".github/workflows/release.yml"}) {
			t.Fatalf("pipeline = %+v", ci)
		}
		if dn := a.Download.Guards.Network; !dn.Restricted || dn.Detail.Any || len(dn.Detail.CIDRs) != 1 || dn.Detail.CIDRs[0].Note != "VPN" {
			t.Fatalf("download networks = %+v", a.Download.Guards.Network)
		}
		if pn := a.Publish.Guards.Network; !pn.Restricted || pn.Detail.Any || len(pn.Detail.Lists) != 1 || pn.Detail.Lists[0].Title != "Office" {
			t.Fatalf("publish networks = %+v", a.Publish.Guards.Network)
		}
		wantSentence(t, "download", a.Download.Sentence, "2 people, only from 10.0.0.0/8.")
		wantSentence(t, "publish", a.Publish.Sentence, "1 person and 1 CI pipeline (acme/app, branch main), only from Office.")

		s.MustAPI(t, http.MethodPatch, "/api/repos", map[string]any{"names": []string{repo}, "anonymous_read": true}, s.Admin(), nil)
		a = getAccess(t, s, repo, "")
		wantSentence(t, "public download", a.Download.Sentence, "anyone, only from 10.0.0.0/8.")
		s.MustAPI(t, http.MethodPut, "/api/repos/"+repo+"/ip-rules", map[string]any{"read": []any{}, "publish": []any{}}, s.Admin(), nil)
		a = getAccess(t, s, repo, "")
		wantSentence(t, "public download", a.Download.Sentence, "anyone on the internet.")
		wantSentence(t, "publish", a.Publish.Sentence, "1 person and 1 CI pipeline (acme/app, branch main).")
	})

	t.Run("only admins", func(t *testing.T) {
		bare := RandName("bare")
		s.CreateRepo(t, bare, "raw", false, nil)
		a := getAccess(t, s, bare, "")
		wantSentence(t, "download", a.Download.Sentence, "only admins.")
		wantSentence(t, "publish", a.Publish.Sentence, "only admins.")
	})

	t.Run("person", func(t *testing.T) {
		var u struct {
			Username    string   `json:"username"`
			Permissions []string `json:"permissions"`
			Kind        string   `json:"kind"`
		}
		s.MustAPI(t, http.MethodGet, "/api/users/"+writer, nil, s.Admin(), &u)
		if u.Username != writer || len(u.Permissions) != 0 || u.Kind != "local" {
			t.Fatalf("GET /api/users/%s: %+v", writer, u)
		}
		s.MustAPI(t, http.MethodGet, "/api/users/"+boss, nil, s.Admin(), &u)
		if strings.Join(u.Permissions, ",") != strings.Join(s.ServerPerms(t), ",") {
			t.Fatalf("GET /api/users/%s: %+v, want every server permission", boss, u)
		}
		st, b := s.API(t, http.MethodGet, "/api/users/"+RandName("nobody"), nil, s.Admin())
		wantAPIStatus(t, "GET an unknown user", st, b, http.StatusNotFound)
	})

	t.Run("cross-links", func(t *testing.T) {
		type userAccess struct {
			Username    string   `json:"username"`
			Permissions []string `json:"permissions"`
			CanPublish  bool     `json:"can_publish"`
			Items       []struct {
				Repo  string   `json:"repo"`
				Perms []string `json:"perms"`
				Via   string   `json:"via"`
			} `json:"items"`
			Next *string `json:"next"`
		}
		permsOf := func(u userAccess, name string) string {
			for _, it := range u.Items {
				if it.Repo == name {
					return strings.Join(it.Perms, ",") + "/" + it.Via
				}
			}
			return ""
		}
		private := RandName("priv")
		s.CreateRepo(t, private, "raw", false, nil)

		var w userAccess
		s.MustAPI(t, http.MethodGet, "/api/users/"+writer+"/access?limit=500", nil, s.Admin(), &w)
		if !w.CanPublish || len(w.Permissions) != 0 || permsOf(w, repo) != "repo.download,repo.publish/permission" || permsOf(w, private) != "" {
			t.Fatalf("writer's access: %+v", w)
		}
		var r userAccess
		s.MustAPI(t, http.MethodGet, "/api/users/"+reader+"/access?limit=500", nil, s.Admin(), &r)
		if permsOf(r, repo) != "repo.download/permission" {
			t.Fatalf("reader's access: %+v", r)
		}
		var b userAccess
		s.MustAPI(t, http.MethodGet, "/api/users/"+boss+"/access?limit=500", nil, s.Admin(), &b)
		if strings.Join(b.Permissions, ",") != strings.Join(s.ServerPerms(t), ",") ||
			permsOf(b, private) != everyRepoPerm+"/all_repos" || permsOf(b, repo) != everyRepoPerm+"/all_repos" {
			t.Fatalf("admin's access: %+v", b)
		}
		var g userAccess
		s.MustAPI(t, http.MethodGet, "/api/users/"+gone+"/access", nil, s.Admin(), &g)
		if len(g.Items) != 0 || g.CanPublish {
			t.Fatalf("a disabled user's access: %+v", g)
		}

		var me userAccess
		s.MustAPI(t, http.MethodGet, "/api/me/access?limit=500", nil, Basic{writer, writer + "-password-1"}, &me)
		if me.Username != writer || permsOf(me, repo) != "repo.download,repo.publish/permission" {
			t.Fatalf("/api/me/access: %+v", me)
		}
		// A public repo without a grant is "repo.download/public".
		pub := RandName("pub")
		s.CreateRepo(t, pub, "raw", true, nil)
		s.MustAPI(t, http.MethodGet, "/api/me/access?limit=500", nil, Basic{reader, reader + "-password-1"}, &me)
		if permsOf(me, pub) != "repo.download/public" {
			t.Fatalf("reader's access to a public repo: %+v", me)
		}
		// Paging walks every repo once.
		var all []string
		after := ""
		for range 100 {
			var page userAccess
			s.MustAPI(t, http.MethodGet, "/api/users/"+boss+"/access?limit=2&after="+after, nil, s.Admin(), &page)
			for _, it := range page.Items {
				all = append(all, it.Repo)
			}
			if page.Next == nil {
				break
			}
			after = *page.Next
		}
		if !slices.IsSorted(all) || len(slices.Compact(slices.Clone(all))) != len(all) || !slices.Contains(all, pub) {
			t.Fatalf("admin access pages: %v", all)
		}
	})

	t.Run("a person without the permission", func(t *testing.T) {
		ui := s.SignIn(t, writer, writer+"-password-1")
		for _, p := range []string{"/api/repos/" + repo + "/access", "/api/users/" + writer, "/api/users/" + writer + "/access"} {
			st, b := s.API(t, http.MethodGet, p, nil, Basic{writer, writer + "-password-1"})
			wantAPIStatus(t, fmt.Sprintf("GET %s with a password", p), st, b, http.StatusUnauthorized)
			st, b = s.API(t, http.MethodGet, p, nil, ui)
			wantAPIStatus(t, fmt.Sprintf("GET %s with the UI session of a publisher", p), st, b, http.StatusForbidden)
		}
	})
}

// TestRepoSettingsPermission: a person with settings on one repository edits it, and gets 403 on
// another.
func TestRepoSettingsPermission(t *testing.T) {
	t.Parallel()
	s := StartServer(t, ServerOpts{})
	mine, other := RandName("mine"), RandName("other")
	s.CreateRepo(t, mine, "raw", false, nil)
	s.CreateRepo(t, other, "raw", false, nil)
	ed := RandName("ed")
	s.CreateUser(t, ed, "ed-password-1")
	s.SetPerms(t, mine, ed, "repo.settings")
	sess := s.SignIn(t, ed, "ed-password-1")

	var me struct {
		RepoPermissions map[string][]string `json:"repo_permissions"`
	}
	s.MustAPI(t, http.MethodGet, "/api/me", nil, sess, &me)
	if got := me.RepoPermissions[mine]; !slices.Equal(got, []string{"repo.settings"}) || len(me.RepoPermissions) != 1 {
		t.Fatalf("GET /api/me: repo_permissions %v, want only %s: [settings]", me.RepoPermissions, mine)
	}

	rules := map[string]any{"read": []map[string]string{{"kind": "cidr", "value": "10.0.0.0/8", "note": "VPN"}}, "publish": []any{}}
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/api/repos/%s", nil},
		{http.MethodPatch, "/api/repos/%s", map[string]any{"description": "edited by a person with settings"}},
		{http.MethodGet, "/api/repos/%s/ip-rules", nil},
		{http.MethodPut, "/api/repos/%s/ip-rules", rules},
		{http.MethodGet, "/api/repos/%s/access", nil},
	} {
		p := fmt.Sprintf(c.path, mine)
		st, b := s.API(t, c.method, p, c.body, sess)
		wantAPIStatus(t, c.method+" "+p+" with settings", st, b, http.StatusOK)
		p = fmt.Sprintf(c.path, other)
		st, b = s.API(t, c.method, p, c.body, sess)
		wantAPIStatus(t, c.method+" "+p+" without settings", st, b, http.StatusForbidden)
	}
	// The public switch needs settings on every repository of the list.
	st, b := s.API(t, http.MethodPatch, "/api/repos", map[string]any{"names": []string{mine, other}, "anonymous_read": true}, sess)
	wantAPIStatus(t, "PATCH /api/repos with a repository outside settings", st, b, http.StatusForbidden)
	s.MustAPI(t, http.MethodPatch, "/api/repos", map[string]any{"names": []string{mine}, "anonymous_read": true}, sess, nil)

	// Settings gives neither the permissions of the repository nor the server routes.
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/api/repos/" + mine + "/grants", nil},
		{http.MethodPut, "/api/grants", map[string]any{"users": []string{ed}, "repos": []string{mine}, "perms": []string{"repo.settings", "repo.delete"}}},
		{http.MethodPost, "/api/repos/delete", map[string]any{"names": []string{mine}}},
		{http.MethodGet, "/api/users", nil},
		{http.MethodGet, "/api/settings", nil},
	} {
		st, b := s.API(t, c.method, c.path, c.body, sess)
		wantAPIStatus(t, c.method+" "+c.path+" with settings on a repository", st, b, http.StatusForbidden)
	}
	var got struct {
		Description   string `json:"description"`
		AnonymousRead bool   `json:"anonymous_read"`
	}
	s.MustAPI(t, http.MethodGet, "/api/repos/"+mine, nil, s.Admin(), &got)
	if got.Description != "edited by a person with settings" || !got.AnonymousRead {
		t.Fatalf("repo %s after the edits: %+v", mine, got)
	}
}

// TestAccessPermission: a person with access on a repository gives and removes only the
// permissions they hold there; the dependency rules hold for everybody.
func TestAccessPermission(t *testing.T) {
	t.Parallel()
	s := StartServer(t, ServerOpts{})
	repo, other := RandName("acc"), RandName("other")
	s.CreateRepo(t, repo, "raw", false, nil)
	s.CreateRepo(t, other, "raw", false, nil)
	mgr, p, q := RandName("mgr"), RandName("p"), RandName("q")
	for _, u := range []string{mgr, p, q} {
		s.CreateUser(t, u, u+"-password-1")
	}
	s.SetPerms(t, repo, mgr, "repo.download", "repo.publish", "repo.access")
	s.SetPerms(t, repo, q, "repo.download", "repo.delete")
	sess := s.SignIn(t, mgr, mgr+"-password-1")
	set := func(users []string, repos []string, perms ...string) (int, []byte) {
		t.Helper()
		return s.API(t, http.MethodPut, "/api/grants", map[string]any{"users": users, "repos": repos, "perms": nonNil(perms)}, sess)
	}

	st, b := set([]string{p}, []string{repo}, "repo.download")
	wantAPIStatus(t, "give download", st, b, http.StatusOK)
	st, b = set([]string{p}, []string{repo}, "repo.download", "repo.publish", "repo.access")
	wantAPIStatus(t, "give download, publish and access", st, b, http.StatusOK)
	st, b = set([]string{p}, []string{repo}, "repo.download", "repo.delete")
	wantAPIStatus(t, "give delete, which mgr does not hold", st, b, http.StatusForbidden)
	st, b = set([]string{p}, []string{repo}, "repo.download", "repo.publish", "repo.settings")
	wantAPIStatus(t, "give settings, which mgr does not hold", st, b, http.StatusForbidden)
	// q holds delete, which mgr does not hold: mgr cannot take it away.
	st, b = set([]string{q}, []string{repo}, "repo.download")
	wantAPIStatus(t, "remove delete, which mgr does not hold", st, b, http.StatusForbidden)
	st, b = set([]string{q}, []string{repo})
	wantAPIStatus(t, "remove every permission of q", st, b, http.StatusForbidden)
	// No access on other: 403 for other alone and for a list with it.
	st, b = set([]string{p}, []string{other}, "repo.download")
	wantAPIStatus(t, "give on a repository without access", st, b, http.StatusForbidden)
	st, b = set([]string{p}, []string{repo, other})
	wantAPIStatus(t, "a list with a repository without access", st, b, http.StatusForbidden)

	var perms []struct {
		Username string   `json:"username"`
		Perms    []string `json:"perms"`
	}
	s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/grants", nil, sess, &perms)
	have := map[string]string{}
	for _, x := range perms {
		have[x.Username] = strings.Join(x.Perms, ",")
	}
	if have[p] != "repo.download,repo.publish,repo.access" || have[q] != "repo.download,repo.delete" || have[mgr] != "repo.download,repo.publish,repo.access" {
		t.Fatalf("GET /api/repos/%s/permissions: %v", repo, have)
	}
	st, b = s.API(t, http.MethodGet, "/api/repos/"+other+"/grants", nil, sess)
	wantAPIStatus(t, "GET the permissions of a repository without access", st, b, http.StatusForbidden)

	// Remove with an empty set.
	st, b = set([]string{p}, []string{repo})
	wantAPIStatus(t, "remove every permission of p", st, b, http.StatusOK)
	perms = nil
	s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/grants", nil, sess, &perms)
	for _, x := range perms {
		if x.Username == p {
			t.Fatalf("p still holds %v after the removal", x.Perms)
		}
	}

	// The registry rules: repo.publish needs repo.download and ci.pipelines needs repo.publish;
	// an unknown ID and a server permission are refused.
	for _, bad := range [][]string{{"repo.publish"}, {"repo.download", "ci.pipelines"}, {"ci.pipelines"}, {"bogus"}, {"people.manage"}} {
		st, b := s.API(t, http.MethodPut, "/api/grants", map[string]any{"users": []string{p}, "repos": []string{repo}, "perms": bad}, s.Admin())
		wantAPIStatus(t, fmt.Sprintf("perms %v", bad), st, b, http.StatusBadRequest)
	}
	s.SetPerms(t, repo, p, "repo.download", "repo.publish", "ci.pipelines")

	// One grant.set event per change, with the old and the new set.
	var found bool
	for _, e := range auditAll(t, s, "action=grant.set&repo="+repo) {
		if e.Actor == mgr && e.Target == p && fmt.Sprint(e.Detail["old"]) == "[repo.download]" && fmt.Sprint(e.Detail["new"]) == "[repo.download repo.publish repo.access]" {
			found = true
		}
	}
	if !found {
		t.Fatal("no grant.set event by mgr for p with old [repo.download] and new [repo.download repo.publish repo.access]")
	}
}

// TestLastPeople: the server keeps at least one enabled person with the people permission, and a
// person with people changes only the permissions they hold.
func TestLastPeople(t *testing.T) {
	t.Parallel()
	s := StartServer(t, ServerOpts{})
	allBut := func(drop string) []string {
		return slices.DeleteFunc(s.ServerPerms(t), func(p string) bool { return p == drop })
	}
	// The bootstrap admin is the only holder: removing people from themselves is refused.
	st, b := s.API(t, http.MethodPut, "/api/users/"+s.AdminUser+"/permissions", map[string]any{"permissions": allBut("people.manage")}, s.Admin())
	wantAPIStatus(t, "remove people from the last holder", st, b, http.StatusConflict)

	// A second holder: one of the two can give it up.
	boss := RandName("boss")
	s.CreateUser(t, boss, "boss-password-1", "people.manage")
	st, b = s.API(t, http.MethodPut, "/api/users/"+boss+"/permissions", map[string]any{"permissions": []string{}}, s.Admin())
	wantAPIStatus(t, "remove people from one of two holders", st, b, http.StatusOK)
	st, b = s.API(t, http.MethodPut, "/api/users/"+s.AdminUser+"/permissions", map[string]any{"permissions": allBut("people.manage")}, s.Admin())
	wantAPIStatus(t, "remove people from the last holder again", st, b, http.StatusConflict)

	// A person with people changes only the permissions they hold.
	s.SetServerPerms(t, boss, "people.manage")
	bossUI := s.SignIn(t, boss, "boss-password-1")
	st, b = s.API(t, http.MethodPost, "/api/users", map[string]any{"username": RandName("x"), "password": "x-password-1", "permissions": []string{"storage.manage"}}, bossUI)
	wantAPIStatus(t, "create a person with a permission that boss does not hold", st, b, http.StatusForbidden)
	helper := RandName("helper")
	st, b = s.API(t, http.MethodPost, "/api/users", map[string]any{"username": helper, "password": "helper-password-1", "permissions": []string{"people.manage"}}, bossUI)
	wantAPIStatus(t, "create a person with people", st, b, http.StatusCreated)
	st, b = s.API(t, http.MethodPut, "/api/users/"+helper+"/permissions", map[string]any{"permissions": []string{"people.manage", "settings.general"}}, bossUI)
	wantAPIStatus(t, "give general, which boss does not hold", st, b, http.StatusForbidden)
	st, b = s.API(t, http.MethodPut, "/api/users/"+s.AdminUser+"/permissions", map[string]any{"permissions": []string{"people.manage"}}, bossUI)
	wantAPIStatus(t, "take from the admin permissions that boss does not hold", st, b, http.StatusForbidden)
	// Boss does not cover the admin: no disable, no password change.
	st, b = s.API(t, http.MethodPatch, "/api/users", map[string]any{"usernames": []string{s.AdminUser}, "disabled": true}, bossUI)
	wantAPIStatus(t, "disable a person boss does not cover", st, b, http.StatusForbidden)
	st, b = s.API(t, http.MethodPatch, "/api/users/"+s.AdminUser, map[string]any{"password": "taken-over-1"}, bossUI)
	wantAPIStatus(t, "change the password of a person boss does not cover", st, b, http.StatusForbidden)
	// A disabled holder does not count: boss disables helper, then the admin removes people from
	// boss; the admin is then the last enabled holder.
	s.MustAPI(t, http.MethodPatch, "/api/users", map[string]any{"usernames": []string{helper}, "disabled": true}, bossUI, nil)
	s.MustAPI(t, http.MethodPut, "/api/users/"+boss+"/permissions", map[string]any{"permissions": []string{}}, s.Admin(), nil)
	st, b = s.API(t, http.MethodPut, "/api/users/"+s.AdminUser+"/permissions", map[string]any{"permissions": allBut("people.manage")}, s.Admin())
	wantAPIStatus(t, "remove people while the other holder is disabled", st, b, http.StatusConflict)

	// One user.permissions event per change, with the old and the new set.
	var found bool
	for _, e := range auditAll(t, s, "action=user.permissions") {
		if e.Target == boss && fmt.Sprint(e.Detail["old"]) == "[people.manage]" && fmt.Sprint(e.Detail["new"]) == "[]" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no user.permissions event for %s with old [people.manage] and new []", boss)
	}
}

// TestSecondFactorRule: a person needs the second factor for a UI session when they hold a server
// permission or a repository permission other than download and publish.
func TestSecondFactorRule(t *testing.T) {
	t.Parallel()
	s := StartServer(t, ServerOpts{})
	repo := RandName("tfa")
	s.CreateRepo(t, repo, "raw", false, nil)
	dl, ed := RandName("dl"), RandName("ed")
	s.CreateUser(t, dl, "dl-password-1")
	s.CreateUser(t, ed, "ed-password-1")
	s.SetPerms(t, repo, dl, "repo.download", "repo.publish")
	s.SetPerms(t, repo, ed, "repo.download", "repo.settings")

	type loginAnswer struct {
		SecondFactor         bool `json:"second_factor"`
		SecondFactorRequired bool `json:"second_factor_required"`
	}
	// login answers {second_factor, methods} or {session: SessionView}; it flattens both.
	login := func(user, pass string) (*Session, loginAnswer) {
		t.Helper()
		jar, _ := cookiejar.New(nil)
		sess := &Session{Jar: jar}
		var a struct {
			SecondFactor bool         `json:"second_factor"`
			Session      *loginAnswer `json:"session"`
		}
		s.MustAPI(t, http.MethodPost, "/api/auth/login", map[string]string{"username": user, "password": pass}, sess, &a)
		out := loginAnswer{SecondFactor: a.SecondFactor}
		if a.Session != nil {
			out.SecondFactorRequired = a.Session.SecondFactorRequired
		}
		return sess, out
	}

	t.Run("download and publish: the password alone", func(t *testing.T) {
		sess, a := login(dl, "dl-password-1")
		if a.SecondFactor || a.SecondFactorRequired {
			t.Fatalf("password sign-in of a publisher: %+v, want a full session", a)
		}
		var me loginAnswer
		s.MustAPI(t, http.MethodGet, "/api/me", nil, sess, &me)
		if me.SecondFactorRequired {
			t.Fatal("GET /api/me of a publisher: second_factor_required is true")
		}
		s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/tree", nil, sess, nil)
	})

	t.Run("settings without a factor: setup only", func(t *testing.T) {
		sess, a := login(ed, "ed-password-1")
		if a.SecondFactor || !a.SecondFactorRequired {
			t.Fatalf("password sign-in of a person with settings and no factor: %+v, want second_factor_required", a)
		}
		var me loginAnswer
		s.MustAPI(t, http.MethodGet, "/api/me", nil, sess, &me)
		if !me.SecondFactorRequired {
			t.Fatal("GET /api/me of a setup-only session: second_factor_required is false")
		}
		for _, c := range []struct {
			method, path string
			body         any
		}{
			{http.MethodGet, "/api/repos/" + repo + "/access", nil},
			{http.MethodPatch, "/api/repos/" + repo, map[string]any{"description": "with a password alone"}},
			{http.MethodGet, "/api/repos", nil},
		} {
			st, b := s.API(t, c.method, c.path, c.body, sess)
			if st != http.StatusForbidden || !strings.Contains(string(b), "second_factor_required") {
				t.Fatalf("%s %s with a setup-only session: status %d, want 403 second_factor_required: %s", c.method, c.path, st, b)
			}
		}
		s.MustAPI(t, http.MethodGet, "/api/security", nil, sess, nil)
	})

	t.Run("settings with a factor: the second step", func(t *testing.T) {
		s.EnrollTOTP(t, ed)
		_, a := login(ed, "ed-password-1")
		if !a.SecondFactor {
			t.Fatalf("password sign-in of a person with settings and a factor: %+v, want second_factor", a)
		}
		sess := s.SignIn(t, ed, "ed-password-1")
		s.MustAPI(t, http.MethodPatch, "/api/repos/"+repo, map[string]any{"description": "with both factors"}, sess, nil)
	})

	t.Run("a grant of settings ends the sessions of a password sign-in", func(t *testing.T) {
		sess := s.SignIn(t, dl, "dl-password-1")
		s.MustAPI(t, http.MethodGet, "/api/me", nil, sess, nil)
		s.SetPerms(t, repo, dl, "repo.download", "repo.publish", "repo.settings")
		st, b := s.API(t, http.MethodGet, "/api/me", nil, sess)
		wantAPIStatus(t, "GET /api/me after the grant of settings", st, b, http.StatusUnauthorized)
		// The next sign-in asks for a factor.
		if _, a := login(dl, "dl-password-1"); !a.SecondFactorRequired {
			t.Fatalf("password sign-in after the grant: %+v, want second_factor_required", a)
		}
	})

	t.Run("a grant of a server permission ends the sessions too", func(t *testing.T) {
		pub := RandName("pub")
		s.CreateUser(t, pub, "pub-password-1")
		sess := s.SignIn(t, pub, "pub-password-1")
		s.MustAPI(t, http.MethodGet, "/api/me", nil, sess, nil)
		s.SetServerPerms(t, pub, "activity.read")
		st, b := s.API(t, http.MethodGet, "/api/me", nil, sess)
		wantAPIStatus(t, "GET /api/me after the grant of activity", st, b, http.StatusUnauthorized)
		s.MustAPI(t, http.MethodGet, "/api/audit", nil, s.SignIn(t, pub, "pub-password-1"), nil)
	})
}

// TestCIProviderPipelines: the access report reads a trust policy through the CI provider of its
// issuer. A Jenkins policy (issuer <jenkins>/oidc) lists the job paths of its sub claims; a policy
// of an issuer that no provider matches is "other" with its subjects.
func TestCIProviderPipelines(t *testing.T) {
	t.Parallel()
	s := StartServer(t, ServerOpts{})
	repo := RandName("ci")
	s.CreateRepo(t, repo, "raw", false, nil)
	createTrust(t, s, trustPolicy{Name: "jenkins-" + randHex(2), Issuer: "https://jenkins.example.com/oidc",
		Claims: map[string][]string{"sub": {"https://jenkins.example.com/job/app/job/build/"}}, Repos: []string{repo}})
	createTrust(t, s, trustPolicy{Name: "other-" + randHex(2), Issuer: "https://ci.example.com",
		Claims: map[string][]string{"sub": {"pipeline:42"}}, Repos: []string{repo}})
	a := getAccess(t, s, repo, "")
	bySystem := map[string][]string{}
	for _, p := range a.Publish.CI {
		bySystem[p.System] = append(p.Repositories, p.Subjects...)
	}
	if !slices.Equal(bySystem["jenkins"], []string{"app/build"}) || !slices.Equal(bySystem["other"], []string{"pipeline:42"}) || len(bySystem) != 2 {
		t.Fatalf("pipelines by system: %v, want jenkins app/build and other pipeline:42", bySystem)
	}
}
