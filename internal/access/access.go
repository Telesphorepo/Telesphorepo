// Package access answers the two questions every repository has (docs/design/
// information-architecture.md): who can download from it, and who can publish to it, each with
// the networks it may come from and, for publishing, what blocks it. It reads the repo's grants,
// the enabled admins, the CI rules (publish trust policies) that list the repo, and the repo's IP
// rules, and says the effective answer in one plain sentence each.
//
// The holders of the All repositories permission can download from and publish to every repo.
// They are counted (admins), not listed, unless the caller asks for them; the sentences name
// only the people and pipelines that were given access, and say "only admins" when there are
// none.
package access

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/netrules"
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

// Pipeline is a CI rule (publish trust policy) that lists the repo, with what its claims allow,
// read from GitHub Actions claims (repository, ref, workflow_ref, job_workflow_ref, workflow) and
// the subject. Disabled rules are listed; the sentence leaves them out.
type Pipeline struct {
	ID           int64    `json:"id"`
	Rule         string   `json:"rule"`   // the rule's name
	System       string   `json:"system"` // "github-actions" or "other"
	Issuer       string   `json:"issuer"`
	Repositories []string `json:"repositories,omitempty"` // e.g. "acme/app"
	Branches     []string `json:"branches,omitempty"`     // e.g. "main", "release/*"
	Tags         []string `json:"tags,omitempty"`         // e.g. "v*"
	Workflows    []string `json:"workflows,omitempty"`    // e.g. ".github/workflows/release.yml"
	Subjects     []string `json:"subjects,omitempty"`     // the sub claim, e.g. a Jenkins job URL
	Disabled     bool     `json:"disabled"`
}

// Networks is where requests may come from: Any, or only the listed IP lists and address
// ranges.
type Networks struct {
	Any   bool      `json:"any"`
	Lists []NetList `json:"lists"`
	CIDRs []NetCIDR `json:"cidrs"`
}

// NetList is an IP list a repo rule names.
type NetList struct {
	Name  string `json:"name"`
	Title string `json:"title"`
}

// NetCIDR is an address range a repo rule names.
type NetCIDR struct {
	CIDR string `json:"cidr"`
	Note string `json:"note"`
}

// Download is who can download from a repo. People are the users with a grant of download;
// Admins counts the enabled holders of the All repositories permission.
type Download struct {
	Public   bool     `json:"public"`
	People   []Person `json:"people"`
	Admins   int      `json:"admins"`
	Networks Networks `json:"networks"`
	Sentence string   `json:"sentence"`
}

// Publish is who can publish to a repo. People are the users with a grant of publish (they
// publish from a device or the browser); Admins counts the enabled holders of the All
// repositories permission; CI are the rules that list the repo. LockedBy says why nobody can publish now ("" when anyone above can).
type Publish struct {
	People   []Person   `json:"people"`
	Admins   int        `json:"admins"`
	CI       []Pipeline `json:"ci"`
	Networks Networks   `json:"networks"`
	LockedBy string     `json:"locked_by"`
	Sentence string     `json:"sentence"`
}

// Repo is the effective access of one repo. People lists each person with a grant once, with
// their whole permission set.
type Repo struct {
	Repo     string   `json:"repo"`
	Format   string   `json:"format"`
	People   []Person `json:"people"`
	Download Download `json:"download"`
	Publish  Publish  `json:"publish"`
}

// Service computes effective access.
type Service struct {
	q     *dbq.Queries
	repos *repos.Service
	auth  *auth.Service
	rules *netrules.Service
}

// New returns the access service.
func New(d *db.DB, rs *repos.Service, a *auth.Service, nr *netrules.Service) *Service {
	return &Service{q: dbq.New(d.R), repos: rs, auth: a, rules: nr}
}

// admins are the enabled holders of the All repositories permission, in username order.
type admins struct {
	all []string
}

func (s *Service) admins(ctx context.Context) (admins, error) {
	rows, err := s.q.ListPermHolders(ctx, "repos")
	if err != nil {
		return admins{}, err
	}
	slices.SortFunc(rows, func(a, b dbq.ListPermHoldersRow) int { return strings.Compare(a.Username, b.Username) })
	var a admins
	for _, r := range rows {
		a.all = append(a.all, r.Username)

	}
	return a, nil
}

// Repo returns the effective access of repo. With listAdmins the holders of the All
// repositories permission appear in the people of Download and Publish too (via "all_repos").
func (s *Service) Repo(ctx context.Context, repo *repos.Repo, listAdmins bool) (*Repo, error) {
	adm, err := s.admins(ctx)
	if err != nil {
		return nil, err
	}
	return s.repo(ctx, repo, adm, s.listTitles(), listAdmins)
}

// listTitles maps each IP list name to its title.
func (s *Service) listTitles() map[string]string {
	m := map[string]string{}
	for _, l := range s.rules.Lists() {
		m[l.Name] = l.Title
	}
	return m
}

func (s *Service) repo(ctx context.Context, repo *repos.Repo, adm admins, titles map[string]string, listAdmins bool) (*Repo, error) {
	grants, err := s.q.ListRepoGrants(ctx, repo.ID)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(grants, func(a, b dbq.ListRepoGrantsRow) int { return strings.Compare(a.Username, b.Username) })
	out := &Repo{Repo: repo.Name, Format: repo.Format, People: []Person{}}
	d, p := &out.Download, &out.Publish
	d.Public, d.People, p.People, p.CI = repo.AnonymousRead, []Person{}, []Person{}, []Pipeline{}
	d.Admins, p.Admins = len(adm.all), len(adm.all)
	// The rows come one per permission; group them by person, in username order.
	type grant struct{ set auth.RepoPerm }
	byUser := map[string]*grant{}
	var names []string
	for _, g := range grants {
		if g.Disabled || g.AllRepos {
			continue // a disabled user has no access; a holder of All repositories has it anyway
		}
		x := byUser[g.Username]
		if x == nil {
			x = &grant{}
			byUser[g.Username] = x
			names = append(names, g.Username)
		}
		set, _ := auth.ParseRepoPerms([]string{g.Perm})
		x.set |= set
	}
	slices.Sort(names)
	for _, u := range names {
		x := byUser[u]
		person := Person{u, x.set.Names(), ViaPermission}
		out.People = append(out.People, person)
		if x.set.Has(auth.RepoDownload) {
			d.People = append(d.People, person)
		}
		if x.set.Has(auth.RepoPublish) {
			p.People = append(p.People, person)
		}
	}
	if listAdmins {
		all := auth.AllRepoPerms.Names()
		for _, u := range adm.all {
			d.People = append(d.People, Person{u, all, ViaAllRepos})
		}
		for _, u := range adm.all {
			p.People = append(p.People, Person{u, all, ViaAllRepos})
		}
	}

	trusts, err := s.q.ListRepoTrusts(ctx, repo.ID)
	if err != nil {
		return nil, err
	}
	for _, t := range trusts {
		p.CI = append(p.CI, pipelineOf(t))
	}
	slices.SortFunc(p.CI, func(a, b Pipeline) int { return strings.Compare(a.Rule, b.Rule) })

	rules := s.rules.RepoRules(repo.ID)
	d.Networks, p.Networks = networks(rules.Read, titles), networks(rules.Publish, titles)
	p.LockedBy = repo.LockedBy
	d.Sentence, p.Sentence = downloadSentence(d), publishSentence(p)
	return out, nil
}

func networks(rules []netrules.Rule, titles map[string]string) Networks {
	n := Networks{Any: len(rules) == 0, Lists: []NetList{}, CIDRs: []NetCIDR{}}
	for _, r := range rules {
		switch r.Kind {
		case netrules.RuleList:
			n.Lists = append(n.Lists, NetList{r.Value, cmp.Or(titles[r.Value], r.Value)})
		case netrules.RuleCIDR:
			n.CIDRs = append(n.CIDRs, NetCIDR{r.Value, r.Note})
		}
	}
	return n
}

const githubIssuer = "https://token.actions.githubusercontent.com"

// pipelineOf reads what a rule's claims allow.
func pipelineOf(t dbq.ListRepoTrustsRow) Pipeline {
	p := Pipeline{ID: t.ID, Rule: t.Name, System: "other", Issuer: t.Issuer, Disabled: t.Disabled != 0}
	if t.Issuer == githubIssuer || strings.HasPrefix(t.Issuer, githubIssuer+"/") {
		p.System = "github-actions"
	}
	var claims map[string][]string
	if json.Unmarshal([]byte(t.Claims), &claims) != nil {
		return p
	}
	for _, name := range sortedKeys(claims) {
		for _, v := range claims[name] {
			switch name {
			case "repository":
				p.Repositories = append(p.Repositories, v)
			case "ref":
				if b, ok := strings.CutPrefix(v, "refs/heads/"); ok {
					p.Branches = append(p.Branches, b)
				} else if tag, ok := strings.CutPrefix(v, "refs/tags/"); ok {
					p.Tags = append(p.Tags, tag)
				}
			case "workflow_ref", "job_workflow_ref":
				p.Workflows = append(p.Workflows, workflowPath(v))
			case "workflow":
				p.Workflows = append(p.Workflows, v)
			case "sub":
				p.Subjects = append(p.Subjects, v)
			}
		}
	}
	p.Workflows = slices.Compact(slices.Sorted(slices.Values(p.Workflows)))
	return p
}

// workflowPath is the file of a workflow_ref "owner/name/.github/workflows/x.yml@ref".
func workflowPath(ref string) string {
	ref, _, _ = strings.Cut(ref, "@")
	if i := strings.Index(ref, ".github/workflows/"); i >= 0 {
		return ref[i:]
	}
	return ref
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

// from is ", only from <lists and ranges>", or "" for any network.
func from(n Networks) string {
	if n.Any {
		return ""
	}
	var parts []string
	for _, l := range n.Lists {
		parts = append(parts, l.Title)
	}
	switch len(n.CIDRs) {
	case 0:
	case 1:
		parts = append(parts, n.CIDRs[0].CIDR)
	default:
		parts = append(parts, count(len(n.CIDRs), "address range", "address ranges"))
	}
	return ", only from " + and(parts)
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
	case d.Public && d.Networks.Any:
		return "anyone on the internet."
	case d.Public:
		who = "anyone"
	case people > 0:
		who = count(people, "person", "people")
	default:
		who = "only admins"
	}
	return who + from(d.Networks) + "."
}

// publishSentence: "3 people and 1 CI pipeline (acme/app, branch main), only from GitHub
// Actions.", "only admins.", "nobody: mirrors Nexus source acme; cut over first."
func publishSentence(p *Publish) string {
	if p.LockedBy != "" {
		return "nobody: " + strings.TrimSuffix(p.LockedBy, ".") + "."
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
	return who + from(p.Networks) + "."
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
// disabled user can use none. next is "" at the end.
func (s *Service) User(ctx context.Context, u *auth.User, after string, limit int) (*User, string, error) {
	out := &User{Username: u.Username, Permissions: u.Permissions, Disabled: u.Disabled, Kind: u.Kind,
		CanPublish: !u.Disabled, Repos: []UserRepo{}}
	if u.Disabled {
		return out, "", nil
	}
	allRepos := u.Perms.Has(auth.PermRepos)
	sets := map[int64]auth.RepoPerm{}
	if !allRepos {
		var err error
		if sets, err = s.auth.UserRepoPerms(ctx, u.ID); err != nil {
			return nil, "", err
		}
	}
	// Walk the repos by name until the page is full; a page of repos the user cannot use is
	// skipped, so the walk is bounded by the number of repos.
	for {
		page, next, err := s.repos.Page(ctx, after, limit)
		if err != nil {
			return nil, "", err
		}
		for i, r := range page {
			ur := UserRepo{Repo: r.Name, Format: r.Format, Public: r.AnonymousRead}
			switch {
			case allRepos:
				ur.Perms, ur.Via = auth.AllRepoPerms.Names(), ViaAllRepos
			case sets[r.ID] != 0:
				set := sets[r.ID]
				if r.AnonymousRead {
					set |= auth.RepoDownload
				}
				ur.Perms, ur.Via = set.Names(), ViaPermission
			case r.AnonymousRead:
				ur.Perms, ur.Via = auth.RepoDownload.Names(), ViaPublic
			default:
				continue
			}
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
