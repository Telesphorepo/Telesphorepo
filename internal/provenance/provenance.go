// Package provenance is the build provenance of files published from GitHub Actions: the
// repository, commit, ref, run, workflow and actor that a publish session's verified CI token
// carried. A session minted from such a token records them (publish_sessions.ci_*), and every
// file that session writes points at it (assets.publish_session_id). Device, manual and other CI
// sessions record nothing: a local commit can be unpushed or the tree dirty, and a link to a
// commit that does not exist is worse than none.
package provenance

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
)

// githubIssuer is the issuer of GitHub.com Actions tokens; an enterprise with a custom issuer
// has "/<enterprise>" after it.
const githubIssuer = "https://token.actions.githubusercontent.com"

// Record is the stored provenance of one publish session.
type Record struct {
	Server      string // web URL of the GitHub server, e.g. https://github.com
	Repository  string // "owner/name"
	SHA         string
	Ref         string // e.g. refs/heads/main, refs/tags/v1.2.0
	RunID       string
	RunAttempt  string
	Workflow    string // the workflow's name
	WorkflowRef string // "owner/name/.github/workflows/release.yml@refs/heads/main"
	Actor       string
}

// Provenance is the provenance of a file as the API shows it, with links to the commit and the
// workflow run.
type Provenance struct {
	Repository    string `json:"repository"`     // "owner/name"
	RepositoryURL string `json:"repository_url"` // https://github.com/owner/name
	SHA           string `json:"sha"`
	CommitURL     string `json:"commit_url"`
	Ref           string `json:"ref"`      // refs/heads/main
	RefName       string `json:"ref_name"` // main, v1.2.0
	RefType       string `json:"ref_type"` // "branch", "tag" or "" (another ref, e.g. a pull request)
	RunID         string `json:"run_id"`
	RunAttempt    string `json:"run_attempt"` // "" when the token did not carry it
	RunURL        string `json:"run_url"`
	Workflow      string `json:"workflow"`      // the workflow's name, else its file
	WorkflowPath  string `json:"workflow_path"` // .github/workflows/release.yml
	WorkflowRef   string `json:"workflow_ref"`
	Actor         string `json:"actor"` // "" when the token did not carry it
}

var (
	repositoryRE = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}/[A-Za-z0-9_.-]{1,100}$`)
	shaRE        = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)
	digitsRE     = regexp.MustCompile(`^[0-9]{1,20}$`)
	actorRE      = regexp.MustCompile(`^[A-Za-z0-9_.\-\[\]]{1,100}$`)
)

// FromClaims returns the provenance of a verified CI token from issuer, or nil when the token
// does not carry GitHub Actions claims: repository, sha, run_id and workflow_ref, well formed.
// GitHub.com tokens link to https://github.com; any other issuer that sends these claims (GitHub
// Enterprise Server, or a server compatible with GitHub Actions) links to the issuer's origin.
func FromClaims(issuer string, claims map[string]any) *Record {
	str := func(name string, max int) string {
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
	r := &Record{
		Repository:  str("repository", 201),
		SHA:         strings.ToLower(str("sha", 64)),
		Ref:         str("ref", 255),
		RunID:       str("run_id", 20),
		RunAttempt:  str("run_attempt", 20),
		Workflow:    str("workflow", 255),
		WorkflowRef: str("workflow_ref", 512),
		Actor:       str("actor", 100),
	}
	if !repositoryRE.MatchString(r.Repository) || !shaRE.MatchString(r.SHA) || !digitsRE.MatchString(r.RunID) ||
		!strings.HasPrefix(r.WorkflowRef, r.Repository+"/") {
		return nil
	}
	if !digitsRE.MatchString(r.RunAttempt) {
		r.RunAttempt = ""
	}
	if !actorRE.MatchString(r.Actor) {
		r.Actor = ""
	}
	r.Server = serverOf(issuer)
	if r.Server == "" {
		return nil
	}
	return r
}

// serverOf returns the web URL of the GitHub server behind a token issuer.
func serverOf(issuer string) string {
	if issuer == githubIssuer || strings.HasPrefix(issuer, githubIssuer+"/") {
		return "https://github.com"
	}
	u, err := url.Parse(issuer)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// View returns the provenance with its links.
func (r *Record) View() *Provenance {
	repoURL := r.Server + "/" + r.Repository
	p := &Provenance{
		Repository: r.Repository, RepositoryURL: repoURL, SHA: r.SHA, CommitURL: repoURL + "/commit/" + r.SHA,
		Ref: r.Ref, RunID: r.RunID, RunAttempt: r.RunAttempt, RunURL: repoURL + "/actions/runs/" + r.RunID,
		Workflow: r.Workflow, WorkflowRef: r.WorkflowRef, Actor: r.Actor,
	}
	if r.RunAttempt != "" {
		p.RunURL += "/attempts/" + r.RunAttempt
	}
	switch {
	case strings.HasPrefix(r.Ref, "refs/heads/"):
		p.RefName, p.RefType = strings.TrimPrefix(r.Ref, "refs/heads/"), "branch"
	case strings.HasPrefix(r.Ref, "refs/tags/"):
		p.RefName, p.RefType = strings.TrimPrefix(r.Ref, "refs/tags/"), "tag"
	default:
		p.RefName = r.Ref
	}
	// workflow_ref is "<owner>/<name>/<path>@<ref>".
	path, _, _ := strings.Cut(strings.TrimPrefix(r.WorkflowRef, r.Repository+"/"), "@")
	p.WorkflowPath = path
	if p.Workflow == "" {
		p.Workflow = path
	}
	return p
}

// Loader reads the provenance of publish sessions and keeps what it read, so a page of files
// that one build wrote costs one lookup. It is for one request; it is not safe for concurrent
// use.
type Loader struct {
	q    *dbq.Queries
	seen map[int64]*Provenance
}

// NewLoader returns a Loader that reads through q.
func NewLoader(q *dbq.Queries) *Loader { return &Loader{q: q, seen: map[int64]*Provenance{}} }

// Get returns the provenance of session id (0: none), or nil when it has none.
func (l *Loader) Get(ctx context.Context, id int64) (*Provenance, error) {
	if id == 0 {
		return nil, nil
	}
	if p, ok := l.seen[id]; ok {
		return p, nil
	}
	row, err := l.q.GetSessionProvenance(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		l.seen[id] = nil
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var p *Provenance
	if row.CiRepository != "" {
		p = (&Record{
			Server: row.CiServer, Repository: row.CiRepository, SHA: row.CiSha, Ref: row.CiRef, RunID: row.CiRunID,
			RunAttempt: row.CiRunAttempt, Workflow: row.CiWorkflow, WorkflowRef: row.CiWorkflowRef, Actor: row.CiActor,
		}).View()
	}
	l.seen[id] = p
	return p, nil
}
