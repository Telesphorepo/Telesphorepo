// Package access is the policy engine of repo access (engine.go) and the access report built from
// the same parts: who can download from a repo and who can publish to it, what blocks publishing
// (the repo kind, an operation on the repo), and what each guard limits (e.g. the networks of the
// network module), each said in one plain sentence.
//
// The holders of the All repositories permission can download from and publish to every repo.
// They are counted (admins), not listed, unless the caller asks for them; the sentences name
// only the people and pipelines that were given access, and say "only admins" when there are
// none.
package access

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/ci"
	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// Ways a person has access.
const (
	ViaPermission = "permission" // a grant on the repo
	ViaAllRepos   = "all_repos"  // the All repositories permission: every repo
	ViaPublic     = "public"     // anonymous read
)

// Person is someone with access to a repo: their repository permissions, in display order.
type Person struct {
	Username string   `json:"username"`
	Perms    []string `json:"perms"`
	Via      string   `json:"via"`
}

// Pipeline is a CI rule (publish trust policy) that lists the repo, with what its claims allow
// in the words of its CI provider (ci.Registry.Describe). Disabled rules are listed; the sentence
// leaves them out.
type Pipeline struct {
	ID       int64  `json:"id"`
	Rule     string `json:"rule"` // the rule's name
	Issuer   string `json:"issuer"`
	Disabled bool   `json:"disabled"`
	ci.Pipeline
}

// Pipelines returns the CI rules that list a repo (the publish module).
type Pipelines func(ctx context.Context, repoID int64) ([]Pipeline, error)

// SetPipelines sets the source of the CI rules of the report. Without one, a report lists none.
func (e *Engine) SetPipelines(fn Pipelines) { e.pipelines = fn }

// Blocked says why nobody can publish to a repo now: code CodeReadOnlyKind (a proxy or a group)
// or CodeOperation (an operation on the repo, e.g. a Nexus mirror).
type Blocked struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// DownloadNote returns one sentence about the downloads of repo for the access report, or "" for
// none (e.g. the edge module: "Downloads come from the edge.").
type DownloadNote func(ctx context.Context, repo *repos.Repo) (string, error)

// AddDownloadNote adds a source of the Notes of Download, in module start order. It runs before
// the server serves.
func (e *Engine) AddDownloadNote(fn DownloadNote) { e.notes = append(e.notes, fn) }

// Download is who can download from a repo. People are the users with a grant of download;
// Admins counts the enabled holders of the All repositories permission. Guards are the reports
// of the guards, by guard name. Notes are the sentences of the DownloadNote sources.
type Download struct {
	Public   bool                   `json:"public"`
	People   []Person               `json:"people"`
	Admins   int                    `json:"admins"`
	Guards   map[string]GuardReport `json:"guards"`
	Sentence string                 `json:"sentence"`
	Notes    []string               `json:"notes"`
}

// Publish is who can publish to a repo. People are the users with a grant of publish (they
// publish from a device or the browser); Admins counts the enabled holders of the All
// repositories permission; CI are the rules that list the repo. Blocked says why nobody can
// publish now (null when the people and pipelines above can); a proxy or a group lists no
// publisher.
type Publish struct {
	People   []Person               `json:"people"`
	Admins   int                    `json:"admins"`
	CI       []Pipeline             `json:"ci"`
	Guards   map[string]GuardReport `json:"guards"`
	Blocked  *Blocked               `json:"blocked"`
	Sentence string                 `json:"sentence"`
}

// Repo is the effective access of one repo. People lists each person with a grant once, with
// their whole permission set.
type Repo struct {
	Repo     string   `json:"repo"`
	Format   string   `json:"format"`
	Kind     string   `json:"kind"`
	People   []Person `json:"people"`
	Download Download `json:"download"`
	Publish  Publish  `json:"publish"`
}

// admins are the enabled holders of a permission that gives all repositories, in username order.
func (e *Engine) admins(ctx context.Context) ([]string, error) {
	var out []string
	for _, id := range perm.Current().AllReposIDs() {
		rows, err := e.q.ListPermHolders(ctx, string(id))
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			out = append(out, r.Username)
		}
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// Report returns the effective access of repo. With listAdmins the holders of the All
// repositories permission appear in the people of Download and Publish too (via "all_repos").
func (e *Engine) Report(ctx context.Context, repo *repos.Repo, listAdmins bool) (*Repo, error) {
	adm, err := e.admins(ctx)
	if err != nil {
		return nil, err
	}
	grants, err := e.q.ListRepoGrants(ctx, repo.ID)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(grants, func(a, b dbq.ListRepoGrantsRow) int { return strings.Compare(a.Username, b.Username) })
	out := &Repo{Repo: repo.Name, Format: repo.Format, Kind: repo.Kind, People: []Person{}}
	d, p := &out.Download, &out.Publish
	d.Public, d.People, p.People, p.CI = repo.AnonymousRead, []Person{}, []Person{}, []Pipeline{}
	d.Admins, p.Admins = len(adm), len(adm)
	switch {
	case readOnly(repo, Write):
		p.Blocked = &Blocked{Code: CodeReadOnlyKind, Message: ReadOnly(repo)}
	case repo.Op != nil:
		p.Blocked = &Blocked{Code: CodeOperation, Message: e.ops.Message(repo.Op)}
	}
	// The rows come one per permission; group them by person, in username order.
	g := perm.Current()
	byUser := map[string]perm.Set{}
	var names []string
	for _, gr := range grants {
		if gr.Disabled || g.AllRepos(perm.Joined(gr.ServerPerms)) {
			continue // a disabled user has no access; a holder of All repositories has it anyway
		}
		if _, ok := byUser[gr.Username]; !ok {
			names = append(names, gr.Username)
		}
		byUser[gr.Username] = byUser[gr.Username].Union(perm.SetOf(perm.ID(gr.Perm)).Of(perm.ScopeRepo))
	}
	writable := p.Blocked == nil || p.Blocked.Code != CodeReadOnlyKind
	for _, u := range names {
		set := byUser[u]
		if set.Empty() {
			continue
		}
		person := Person{u, set.Strings(), ViaPermission}
		out.People = append(out.People, person)
		if set.Has(perm.RepoDownload) {
			d.People = append(d.People, person)
		}
		if writable && set.Has(perm.RepoPublish) {
			p.People = append(p.People, person)
		}
	}
	if listAdmins {
		all := g.AllRepo().Strings()
		for _, u := range adm {
			d.People = append(d.People, Person{u, all, ViaAllRepos})
			if writable {
				p.People = append(p.People, Person{u, all, ViaAllRepos})
			}
		}
	}
	if writable && e.pipelines != nil {
		ps, err := e.pipelines(ctx, repo.ID)
		if err != nil {
			return nil, err
		}
		p.CI = append(p.CI, ps...)
		slices.SortFunc(p.CI, func(a, b Pipeline) int { return strings.Compare(a.Rule, b.Rule) })
	}
	d.Guards, p.Guards = map[string]GuardReport{}, map[string]GuardReport{}
	for _, gd := range e.guards {
		d.Guards[gd.Name()] = gd.Describe(repo, Read)
		p.Guards[gd.Name()] = gd.Describe(repo, Write)
	}
	d.Sentence, p.Sentence = downloadSentence(d), publishSentence(p)
	d.Notes = []string{}
	for _, fn := range e.notes {
		n, err := fn(ctx, repo)
		if err != nil {
			return nil, err
		}
		if n != "" {
			d.Notes = append(d.Notes, n)
		}
	}
	return out, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// --- sentences ---

// count is "1 person" / "3 people".
func count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// and joins items as "a", "a and b", "a, b and c".
func and(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

// limits is ", <summary>" for each guard that restricts, in guard name order, or "".
func limits(guards map[string]GuardReport) string {
	out := ""
	for _, name := range sortedKeys(guards) {
		if g := guards[name]; g.Restricted && g.Summary != "" {
			out += ", " + g.Summary
		}
	}
	return out
}

// restricted reports whether any guard restricts.
func restricted(guards map[string]GuardReport) bool {
	for _, g := range guards {
		if g.Restricted {
			return true
		}
	}
	return false
}

// downloadSentence: "anyone on the internet.", "anyone, only from GitHub Actions.",
// "3 people.", "only admins, only from 10.0.0.0/8."
func downloadSentence(d *Download) string {
	var who string
	people := 0
	for _, p := range d.People {
		if p.Via == ViaPermission {
			people++
		}
	}
	switch {
	case d.Public && !restricted(d.Guards):
		return "anyone on the internet."
	case d.Public:
		who = "anyone"
	case people > 0:
		who = count(people, "person", "people")
	default:
		who = "only admins"
	}
	return who + limits(d.Guards) + "."
}

// publishSentence: "3 people and 1 CI pipeline (acme/app, branch main), only from GitHub
// Actions.", "only admins.", "nobody. Uploads and deletes start after the switch-over from Nexus
// server acme." A blocked message is full sentences, so it follows "nobody." as it is.
func publishSentence(p *Publish) string {
	if p.Blocked != nil {
		return "nobody. " + p.Blocked.Message
	}
	var parts []string
	people := 0
	for _, x := range p.People {
		if x.Via == ViaPermission {
			people++
		}
	}
	if people > 0 {
		parts = append(parts, count(people, "person", "people"))
	}
	var enabled []Pipeline
	for _, c := range p.CI {
		if !c.Disabled {
			enabled = append(enabled, c)
		}
	}
	switch len(enabled) {
	case 0:
	case 1:
		parts = append(parts, "1 CI pipeline ("+describe(enabled[0])+")")
	default:
		var names []string
		for i, c := range enabled {
			if i == 3 {
				names = append(names, fmt.Sprintf("%d more", len(enabled)-3))
				break
			}
			names = append(names, cmp.Or(first(c.Repositories), c.Rule))
		}
		parts = append(parts, count(len(enabled), "CI pipeline", "CI pipelines")+" ("+strings.Join(names, ", ")+")")
	}
	who := and(parts)
	if who == "" {
		who = "only admins"
	}
	return who + limits(p.Guards) + "."
}

// describe is "acme/app, branch main", "acme/app, tags v*", or the rule name.
func describe(c Pipeline) string {
	var parts []string
	if len(c.Repositories) > 0 {
		parts = append(parts, and(c.Repositories))
	} else {
		parts = append(parts, c.Rule)
	}
	if len(c.Branches) > 0 {
		parts = append(parts, plural(len(c.Branches), "branch", "branches")+" "+and(c.Branches))
	}
	if len(c.Tags) > 0 {
		parts = append(parts, plural(len(c.Tags), "tag", "tags")+" "+and(c.Tags))
	}
	return strings.Join(parts, ", ")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func first(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

// --- a user's access ---

// UserRepo is one repo a user can use: Perms their repository permissions there, Via how
// (ViaAllRepos, ViaPermission or ViaPublic). A user's grant counts over public read; on a public
// repo the grant gets download too.
type UserRepo struct {
	Repo   string   `json:"repo"`
	Format string   `json:"format"`
	Kind   string   `json:"kind"`
	Public bool     `json:"public"`
	Perms  []string `json:"perms"`
	Via    string   `json:"via"`
}

// User is a user's access across repos. CanPublish is false for a disabled account: it holds no
// publish session, whatever its permissions. Permissions are the user's server permissions.
type User struct {
	Username    string     `json:"username"`
	Permissions []string   `json:"permissions"`
	Disabled    bool       `json:"disabled"`
	Kind        string     `json:"kind"`
	CanPublish  bool       `json:"can_publish"`
	Repos       []UserRepo `json:"repos"`
}

// User returns a keyset page (by repo name, after the name after) of the repos u can use. A
// disabled user can use none. On a proxy or a group a user holds no publish. next is "" at the
// end.
func (e *Engine) User(ctx context.Context, u *auth.User, after string, limit int) (*User, string, error) {
	out := &User{Username: u.Username, Permissions: u.Permissions, Disabled: u.Disabled, Kind: u.Kind,
		CanPublish: !u.Disabled, Repos: []UserRepo{}}
	if u.Disabled {
		return out, "", nil
	}
	g := perm.Current()
	allRepos := g.AllRepos(u.Perms)
	sets := map[int64]perm.Set{}
	if !allRepos {
		var err error
		if sets, err = e.auth.UserRepoPerms(ctx, u.ID); err != nil {
			return nil, "", err
		}
	}
	// A holder of All repositories pages through every repo. Anyone else pages through the repos
	// they hold a grant on and the public ones, so a repo they cannot use is never read. A repo of
	// a grant that holds no known permission is skipped, so the page reads on until it is full.
	for {
		var page []*repos.Repo
		var next string
		var err error
		if allRepos {
			page, next, err = e.repos.Page(ctx, after, limit)
		} else {
			page, next, err = e.repos.PageVisibleTo(ctx, u.ID, after, limit)
		}
		if err != nil {
			return nil, "", err
		}
		for i, r := range page {
			ur := UserRepo{Repo: r.Name, Format: r.Format, Kind: r.Kind, Public: r.AnonymousRead}
			var set perm.Set
			switch {
			case allRepos:
				set, ur.Via = g.AllRepo(), ViaAllRepos
			case !sets[r.ID].Empty():
				set, ur.Via = sets[r.ID], ViaPermission
				if r.AnonymousRead {
					set = set.Union(perm.SetOf(perm.RepoDownload))
				}
			case r.AnonymousRead:
				set, ur.Via = perm.SetOf(perm.RepoDownload), ViaPublic
			default:
				continue
			}
			if readOnly(r, Write) {
				set = set.Minus(g.Dependents(perm.RepoPublish))
			}
			ur.Perms = set.Strings()
			out.Repos = append(out.Repos, ur)
			if len(out.Repos) == limit {
				if i == len(page)-1 && next == "" {
					return out, "", nil
				}
				return out, r.Name, nil
			}
		}
		if next == "" {
			return out, "", nil
		}
		after = next
	}
}
