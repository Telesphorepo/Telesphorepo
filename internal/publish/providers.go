package publish

import (
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/ci"
)

// githubIssuer is the issuer of GitHub.com Actions tokens; an enterprise with a custom issuer
// has "/<enterprise>" after it.
const githubIssuer = "https://token.actions.githubusercontent.com"

var (
	ghRepositoryRE = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}/[A-Za-z0-9_.-]{1,100}$`)
	ghSHARE        = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)
	digitsRE       = regexp.MustCompile(`^[0-9]{1,20}$`)
	ghActorRE      = regexp.MustCompile(`^[A-Za-z0-9_.\-\[\]]{1,100}$`)
	ghWorkflowRE   = regexp.MustCompile(`^\.github/workflows/[A-Za-z0-9_.\-/]{1,200}$`)
)

func isGitHubIssuer(issuer string) bool {
	return issuer == githubIssuer || strings.HasPrefix(issuer, githubIssuer+"/")
}

// GitHubActions is the provider of GitHub Actions tokens: the tokens of GitHub.com, and the
// tokens of any other issuer that carries GitHub Actions claims (GitHub Enterprise Server or a
// compatible server). A token makes a build record when it has a well-formed repository, sha,
// run_id and workflow_ref. GitHub.com builds link to https://github.com, others to the issuer's
// origin.
func GitHubActions() ci.Provider {
	return ci.Provider{
		ID: "github-actions", Title: "GitHub Actions",
		Match: func(issuer string, claims map[string]any) bool {
			if isGitHubIssuer(issuer) {
				return true
			}
			_, repo := claims["repository"].(string)
			_, wf := claims["workflow_ref"].(string)
			return repo && wf
		},
		Build:    githubBuild,
		Describe: githubDescribe,
	}
}

// githubServer returns the web URL of the GitHub server behind a token issuer.
func githubServer(issuer string) string {
	if isGitHubIssuer(issuer) {
		return "https://github.com"
	}
	u, err := url.Parse(issuer)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

func githubBuild(issuer string, claims map[string]any) (*ci.BuildRecord, bool) {
	repo := ci.Claim(claims, "repository", 201)
	sha := strings.ToLower(ci.Claim(claims, "sha", 64))
	runID := ci.Claim(claims, "run_id", 20)
	attempt := ci.Claim(claims, "run_attempt", 20)
	workflowRef := ci.Claim(claims, "workflow_ref", 512)
	server := githubServer(issuer)
	if !ghRepositoryRE.MatchString(repo) || !ghSHARE.MatchString(sha) || !digitsRE.MatchString(runID) ||
		!strings.HasPrefix(workflowRef, repo+"/") || server == "" {
		return nil, false
	}
	if !digitsRE.MatchString(attempt) {
		attempt = ""
	}
	actor := ci.Claim(claims, "actor", 100)
	if !ghActorRE.MatchString(actor) {
		actor = ""
	}
	ref := ci.Claim(claims, "ref", 255)
	refName, refType := ci.SplitRef(ref)
	// workflow_ref is "<owner>/<name>/<path>@<ref>".
	path, _, _ := strings.Cut(strings.TrimPrefix(workflowRef, repo+"/"), "@")
	workflow := ci.Claim(claims, "workflow", 255)
	if workflow == "" {
		workflow = path
	}
	repoURL := server + "/" + repo
	runURL := repoURL + "/actions/runs/" + runID
	if attempt != "" {
		runURL += "/attempts/" + attempt
	}
	links := []ci.Link{
		{Rel: "repository", Label: repo, URL: repoURL},
		{Rel: "commit", Label: sha[:7], URL: repoURL + "/commit/" + sha},
		{Rel: "run", Label: "Run " + runID, URL: runURL},
	}
	if ghWorkflowRE.MatchString(path) && !strings.Contains(path, "..") {
		links = append(links, ci.Link{Rel: "workflow", Label: path, URL: repoURL + "/blob/" + sha + "/" + path})
	}
	key := server + "/" + repo + "/" + runID + "/" + attempt
	return &ci.BuildRecord{Key: key, Repository: repo, Revision: sha, Ref: ref, RefName: refName, RefType: refType,
		Run: runID, Workflow: workflow, Actor: actor, Links: links}, true
}

// githubDescribe reads the claims repository, ref, workflow_ref, job_workflow_ref, workflow and
// sub of a trust policy.
func githubDescribe(issuer string, claims map[string][]string) ci.Pipeline {
	var p ci.Pipeline
	for _, name := range sortedKeys(claims) {
		for _, v := range claims[name] {
			switch name {
			case "repository":
				p.Repositories = append(p.Repositories, v)
			case "ref":
				switch n, t := ci.SplitRef(v); t {
				case "branch":
					p.Branches = append(p.Branches, n)
				case "tag":
					p.Tags = append(p.Tags, n)
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

// Jenkins is the provider of the tokens of the Jenkins OpenID Connect provider plugin: the issuer
// is "<jenkins>/oidc" and sub is the URL of the job. The repository of a build is the job path
// (the part of sub after the Jenkins URL), its run the build_number claim. It links to the job,
// and to the build only when the token has a build number.
func Jenkins() ci.Provider {
	return ci.Provider{
		ID: "jenkins", Title: "Jenkins",
		Match: func(issuer string, claims map[string]any) bool {
			return strings.HasSuffix(issuer, "/oidc") && jenkinsBase(issuer) != ""
		},
		Build: func(issuer string, claims map[string]any) (*ci.BuildRecord, bool) {
			base := jenkinsBase(issuer)
			job := ci.Claim(claims, "sub", 1024)
			path, ok := jenkinsJob(base, job)
			if !ok {
				return nil, false
			}
			rec := &ci.BuildRecord{Repository: path, Links: []ci.Link{{Rel: "repository", Label: path, URL: job}}}
			n := ci.Claim(claims, "build_number", 20)
			if f, ok := claims["build_number"].(float64); ok && f >= 0 && f < 1e15 && f == float64(int64(f)) {
				n = strconv.FormatInt(int64(f), 10)
			}
			if digitsRE.MatchString(n) {
				rec.Run = n
				rec.Links = append(rec.Links, ci.Link{Rel: "run", Label: "Build #" + n, URL: job + n + "/"})
			}
			rec.Key = job + "#" + rec.Run
			return rec, true
		},
		Describe: func(issuer string, claims map[string][]string) ci.Pipeline {
			base := jenkinsBase(issuer)
			var p ci.Pipeline
			for _, sub := range claims["sub"] {
				if path, ok := jenkinsJob(base, strings.TrimSuffix(sub, "*")); ok && !strings.Contains(sub, "*") {
					p.Repositories = append(p.Repositories, path)
				} else {
					p.Subjects = append(p.Subjects, sub)
				}
			}
			return p
		},
	}
}

// jenkinsBase is the Jenkins URL of an issuer "<jenkins>/oidc", with a trailing "/"; "" when the
// issuer is not an http(s) URL.
func jenkinsBase(issuer string) string {
	base := strings.TrimSuffix(issuer, "oidc")
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return ""
	}
	return base
}

var jenkinsSegRE = regexp.MustCompile(`^[A-Za-z0-9_.~%+-]{1,200}$`)

// jenkinsJob returns the job path of a job URL "<base>job/a/job/b/" as "a/b", when the URL is
// well formed.
func jenkinsJob(base, job string) (string, bool) {
	rest, ok := strings.CutPrefix(job, base)
	if base == "" || !ok || !strings.HasSuffix(rest, "/") {
		return "", false
	}
	segs := strings.Split(strings.TrimSuffix(rest, "/"), "/")
	if len(segs) < 2 || len(segs)%2 != 0 {
		return "", false
	}
	var names []string
	for i := 0; i < len(segs); i += 2 {
		if segs[i] != "job" || !jenkinsSegRE.MatchString(segs[i+1]) {
			return "", false
		}
		names = append(names, segs[i+1])
	}
	return strings.Join(names, "/"), true
}
