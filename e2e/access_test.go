//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"slices"
	"testing"
)

// Effective access (GET /api/repos/{name}/access, /api/users/{username}/access) and the cross-links
// (GET /api/users/{username}/access, /api/me/access).

type accessPerson struct {
	Username string `json:"username"`
	Level    string `json:"level"`
	Via      string `json:"via"`
}

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

type repoAccess struct {
	Repo     string `json:"repo"`
	Format   string `json:"format"`
	Download struct {
		Public   bool           `json:"public"`
		People   []accessPerson `json:"people"`
		Admins   int            `json:"admins"`
		Networks accessNetworks `json:"networks"`
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
			Disabled     bool     `json:"disabled"`
		} `json:"ci"`
		Networks accessNetworks `json:"networks"`
		LockedBy string         `json:"locked_by"`
		Sentence string         `json:"sentence"`
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
	s := StartServer(t, ServerOpts{})
	repo := RandName("acc")
	s.CreateRepo(t, repo, "maven", false, nil)
	reader, writer, gone, boss := RandName("reader"), RandName("writer"), RandName("gone"), RandName("boss")
	for _, u := range []string{reader, writer, gone} {
		s.CreateUser(t, u, u+"-password-1", false)
	}
	s.CreateUser(t, boss, boss+"-password-1", true)
	s.SetPermission(t, repo, reader, "read")
	s.SetPermission(t, repo, writer, "write")
	s.SetPermission(t, repo, gone, "write")
	s.SetPermission(t, repo, boss, "read") // an admin's grant changes nothing
	s.MustAPI(t, http.MethodPatch, "/api/users", map[string]any{"usernames": []string{gone}, "disabled": true}, s.Admin(), nil)

	t.Run("people and admins", func(t *testing.T) {
		a := getAccess(t, s, repo, "")
		if a.Repo != repo || a.Format != "maven" || a.Download.Public {
			t.Fatalf("access: %+v", a)
		}
		wantPeople := []accessPerson{{reader, "read", "permission"}, {writer, "write", "permission"}}
		slices.SortFunc(wantPeople, func(x, y accessPerson) int { return compareStrings(x.Username, y.Username) })
		if !slices.Equal(a.Download.People, wantPeople) {
			t.Fatalf("download people = %+v, want %+v (no disabled user, no admin)", a.Download.People, wantPeople)
		}
		if !slices.Equal(a.Publish.People, []accessPerson{{writer, "write", "permission"}}) {
			t.Fatalf("publish people = %+v, want only %s", a.Publish.People, writer)
		}
		// At least the bootstrap admin and boss; every admin here is a local account.
		if a.Download.Admins < 2 || a.Publish.Admins != a.Download.Admins {
			t.Fatalf("admins = %d / %d, want the same count, at least 2", a.Download.Admins, a.Publish.Admins)
		}
		if !a.Download.Networks.Any || !a.Publish.Networks.Any || len(a.Publish.CI) != 0 {
			t.Fatalf("networks or CI: %+v", a)
		}
		wantSentence(t, "download", a.Download.Sentence, "2 people.")
		wantSentence(t, "publish", a.Publish.Sentence, "1 person.")

		listed := getAccess(t, s, repo, "?admins=1")
		var admins []string
		for _, p := range listed.Download.People {
			if p.Via == "admin" {
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
		if a.Download.Networks.Any || len(a.Download.Networks.CIDRs) != 1 || a.Download.Networks.CIDRs[0].Note != "VPN" {
			t.Fatalf("download networks = %+v", a.Download.Networks)
		}
		if a.Publish.Networks.Any || len(a.Publish.Networks.Lists) != 1 || a.Publish.Networks.Lists[0].Title != "Office" {
			t.Fatalf("publish networks = %+v", a.Publish.Networks)
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
			Username string `json:"username"`
			IsAdmin  bool   `json:"is_admin"`
			Kind     string `json:"kind"`
		}
		s.MustAPI(t, http.MethodGet, "/api/users/"+writer, nil, s.Admin(), &u)
		if u.Username != writer || u.IsAdmin || u.Kind != "local" {
			t.Fatalf("GET /api/users/%s: %+v", writer, u)
		}
		st, b := s.API(t, http.MethodGet, "/api/users/"+RandName("nobody"), nil, s.Admin())
		wantAPIStatus(t, "GET an unknown user", st, b, http.StatusNotFound)
	})

	t.Run("cross-links", func(t *testing.T) {
		type userAccess struct {
			Username   string `json:"username"`
			IsAdmin    bool   `json:"is_admin"`
			CanPublish bool   `json:"can_publish"`
			Items      []struct {
				Repo  string `json:"repo"`
				Level string `json:"level"`
				Via   string `json:"via"`
			} `json:"items"`
			Next *string `json:"next"`
		}
		levelOf := func(u userAccess, name string) string {
			for _, it := range u.Items {
				if it.Repo == name {
					return it.Level + "/" + it.Via
				}
			}
			return ""
		}
		private := RandName("priv")
		s.CreateRepo(t, private, "raw", false, nil)

		var w userAccess
		s.MustAPI(t, http.MethodGet, "/api/users/"+writer+"/access?limit=500", nil, s.Admin(), &w)
		if !w.CanPublish || w.IsAdmin || levelOf(w, repo) != "write/permission" || levelOf(w, private) != "" {
			t.Fatalf("writer's access: %+v", w)
		}
		var r userAccess
		s.MustAPI(t, http.MethodGet, "/api/users/"+reader+"/access?limit=500", nil, s.Admin(), &r)
		if levelOf(r, repo) != "read/permission" {
			t.Fatalf("reader's access: %+v", r)
		}
		var b userAccess
		s.MustAPI(t, http.MethodGet, "/api/users/"+boss+"/access?limit=500", nil, s.Admin(), &b)
		if !b.IsAdmin || levelOf(b, private) != "write/admin" || levelOf(b, repo) != "write/admin" {
			t.Fatalf("admin's access: %+v", b)
		}
		var g userAccess
		s.MustAPI(t, http.MethodGet, "/api/users/"+gone+"/access", nil, s.Admin(), &g)
		if len(g.Items) != 0 || g.CanPublish {
			t.Fatalf("a disabled user's access: %+v", g)
		}

		var me userAccess
		s.MustAPI(t, http.MethodGet, "/api/me/access?limit=500", nil, Basic{writer, writer + "-password-1"}, &me)
		if me.Username != writer || levelOf(me, repo) != "write/permission" {
			t.Fatalf("/api/me/access: %+v", me)
		}
		// A public repo without a grant is "read/public".
		pub := RandName("pub")
		s.CreateRepo(t, pub, "raw", true, nil)
		s.MustAPI(t, http.MethodGet, "/api/me/access?limit=500", nil, Basic{reader, reader + "-password-1"}, &me)
		if levelOf(me, pub) != "read/public" {
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

	t.Run("admins only", func(t *testing.T) {
		for _, p := range []string{"/api/repos/" + repo + "/access", "/api/users/" + writer, "/api/users/" + writer + "/access"} {
			st, b := s.API(t, http.MethodGet, p, nil, Basic{writer, writer + "-password-1"})
			wantAPIStatus(t, fmt.Sprintf("GET %s as a non-admin", p), st, b, http.StatusUnauthorized, http.StatusForbidden)
		}
	})
}

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
