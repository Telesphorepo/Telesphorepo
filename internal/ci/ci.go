// Package ci is the CI provider registry and the build records. A CI provider (GitHub Actions,
// Jenkins) reads the verified claims of a CI identity token: Build turns them into a build record
// in a form that no CI system shapes, and Describe says in words what a trust policy's allowed
// claims let publish. Modules register providers (module.Extender.CIProvider); the publish module
// registers github-actions and jenkins. One provider interprets the claims of its tokens for the
// build records and for the access report.
package ci

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Link is a link of a build record. Rel is "repository", "commit", "run" or "workflow".
type Link struct {
	Rel   string `json:"rel"`
	Label string `json:"label"`
	URL   string `json:"url"`
}

// BuildRecord is what a provider reads from the claims of a CI token. Key is unique per run of
// the provider (e.g. "<server>/<repository>/<run>/<attempt>"); a second mint of the same run
// updates the same record. Links are made only by the provider, from values it checked.
type BuildRecord struct {
	Key        string
	Repository string // e.g. "owner/name", a Jenkins job path
	Revision   string // the commit
	Ref        string // e.g. refs/heads/main
	RefName    string // e.g. main
	RefType    string // "branch", "tag" or ""
	Run        string // the run id or the build number
	Workflow   string
	Actor      string
	Links      []Link
}

// Pipeline is what a trust policy's allowed claims let publish, in words of the provider.
type Pipeline struct {
	System       string   `json:"system"` // provider ID, or "other"
	Repositories []string `json:"repositories,omitempty"`
	Branches     []string `json:"branches,omitempty"`
	Tags         []string `json:"tags,omitempty"`
	Workflows    []string `json:"workflows,omitempty"`
	Subjects     []string `json:"subjects,omitempty"`
}

// Provider is one CI system.
type Provider struct {
	ID    string // ^[a-z][a-z0-9-]{0,31}$, e.g. "github-actions"
	Title string // e.g. "GitHub Actions"
	// Match reports whether a token (or, with nil claims, a trust policy) of issuer belongs to
	// this provider.
	Match func(issuer string, claims map[string]any) bool
	// Build returns the build record of a verified token; false gives no record.
	Build func(issuer string, claims map[string]any) (*BuildRecord, bool)
	// Describe reads a trust policy's allowed claims (claim name -> patterns).
	Describe func(issuer string, claims map[string][]string) Pipeline
}

var idRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// Registry holds the providers in registration order. It is fixed after startup.
type Registry struct {
	providers []Provider
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{} }

// Add registers p. It fails on a bad or repeated ID or a provider without Match, Build or
// Describe.
func (r *Registry) Add(p Provider) error {
	if !idRE.MatchString(p.ID) || p.ID == "other" {
		return fmt.Errorf("ci: bad provider ID %q", p.ID)
	}
	if p.Match == nil || p.Build == nil || p.Describe == nil {
		return fmt.Errorf("ci: provider %s needs Match, Build and Describe", p.ID)
	}
	if _, ok := r.Get(p.ID); ok {
		return fmt.Errorf("ci: provider %s registered twice", p.ID)
	}
	r.providers = append(r.providers, p)
	return nil
}

// Get returns provider id.
func (r *Registry) Get(id string) (Provider, bool) {
	for _, p := range r.providers {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

// Title returns the title of provider id, or id when no provider has it.
func (r *Registry) Title(id string) string {
	if p, ok := r.Get(id); ok {
		return p.Title
	}
	return id
}

// match returns the first provider that matches.
func (r *Registry) match(issuer string, claims map[string]any) (Provider, bool) {
	for _, p := range r.providers {
		if p.Match(issuer, claims) {
			return p, true
		}
	}
	return Provider{}, false
}

// Build returns the provider ID and the build record of a verified token. A token that no
// provider matches, or whose provider gives no record, has none (ok false).
func (r *Registry) Build(issuer string, claims map[string]any) (string, *BuildRecord, bool) {
	p, ok := r.match(issuer, claims)
	if !ok {
		return "", nil, false
	}
	rec, ok := p.Build(issuer, claims)
	if !ok || rec == nil || rec.Key == "" {
		return "", nil, false
	}
	return p.ID, rec, true
}

// Describe returns what a trust policy of issuer allows. A policy that no provider matches is
// System "other" with its subjects.
func (r *Registry) Describe(issuer string, claims map[string][]string) Pipeline {
	if p, ok := r.match(issuer, nil); ok {
		out := p.Describe(issuer, claims)
		out.System = p.ID
		return out
	}
	return Pipeline{System: "other", Subjects: claims["sub"]}
}

// Claim returns the string claim name when it is valid UTF-8 without control characters and at
// most max bytes long, else "".
func Claim(claims map[string]any, name string, max int) string {
	s, _ := claims[name].(string)
	if !utf8.ValidString(s) || len(s) > max {
		return ""
	}
	for _, c := range s {
		if c < 0x20 || c == 0x7f {
			return ""
		}
	}
	return s
}

// SplitRef returns the name and type of a git ref: ("main", "branch") for refs/heads/main,
// ("v1", "tag") for refs/tags/v1, (ref, "") for another ref.
func SplitRef(ref string) (name, typ string) {
	switch {
	case strings.HasPrefix(ref, "refs/heads/"):
		return strings.TrimPrefix(ref, "refs/heads/"), "branch"
	case strings.HasPrefix(ref, "refs/tags/"):
		return strings.TrimPrefix(ref, "refs/tags/"), "tag"
	}
	return ref, ""
}
