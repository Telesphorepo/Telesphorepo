package format

import (
	"github.com/Telesphorepo/Telesphorepo/internal/ci"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// Snippet is one copy-paste step. The UI shows one tab per Tool and the snippets of a tool in
// order.
type Snippet struct {
	// ID is stable and unique among the snippets of one answer, e.g. "gradle-kotlin.settings".
	ID string `json:"id"`
	// Tool is the tool the snippet is for, e.g. "gradle-kotlin", "maven", "apt", "curl",
	// "github-actions" or "jenkins". The answer names its label.
	Tool string `json:"tool"`
	// Purpose is SnippetUse (add the repo or artifact to a build) or SnippetPublish.
	Purpose string `json:"purpose"`
	// Context is SnippetRepo (repo setup) or SnippetArtifact (one artifact).
	Context string `json:"context"`
	// Via says where a publish snippet publishes from: ViaComputer (the Telesphorepo CLI with a
	// device), ViaCI or ViaHand (a publish token). "" for a use snippet.
	Via string `json:"via,omitempty"`
	// File is the file the snippet belongs in, e.g. ".github/workflows/publish.yml"; "" for a
	// command.
	File  string `json:"file,omitempty"`
	Title string `json:"title"` // what the block is, e.g. "~/.m2/settings.xml" or "Publish"
	Lang  string `json:"lang"`  // highlighting hint: "xml", "kotlin", "groovy", "sh", "yaml", ...
	Code  string `json:"code"`
	Note  string `json:"note,omitempty"` // one line under the code, e.g. a trade-off
}

// Snippet purposes, contexts and publish sources.
const (
	SnippetUse      = "use"
	SnippetPublish  = "publish"
	SnippetRepo     = "repo"
	SnippetArtifact = "artifact"

	ViaComputer = "computer"
	ViaCI       = "ci"
	ViaHand     = "hand"
)

// Tool is a build tool that publishes to repos of a format (Spec.Tools).
type Tool struct {
	// ID names the tool, e.g. "gradle", "maven", "telesphorepo".
	ID    string
	Label string
	// Publish returns how the tool publishes to repo from the CI system (ci.GitHubActions or
	// ci.Jenkins). baseURL is the server's public URL without a trailing slash.
	Publish func(repo *repos.Repo, baseURL, system string) ci.PublishSteps
	// CINote goes under the CI files of the tool, e.g. what the project needs first ("" for none).
	CINote string
}

// CLIPublish is the publish command of the Telesphorepo CLI.
const CLIPublish = "telesphorepo publish"

// CLITool is the tool of a format without a project, such as raw or Debian: the Telesphorepo CLI
// uploads the files of args (e.g. "build/app.zip") with "telesphorepo publish".
func CLITool(args string) Tool {
	return Tool{ID: "telesphorepo", Label: "Telesphorepo CLI", Publish: func(*repos.Repo, string, string) ci.PublishSteps {
		return ci.PublishSteps{Publish: CLIPublish + " " + args}
	}}
}

// CLISnippet returns the publish snippet of the Telesphorepo CLI on a computer for tool: cmd is
// the command, note says what the project needs.
func CLISnippet(tool, cmd, note string) Snippet {
	return Snippet{ID: tool + ".publish", Tool: tool, Purpose: SnippetPublish, Context: SnippetRepo, Via: ViaComputer,
		Title: "Publish", Lang: "sh", Code: cmd + "\n",
		Note: joinNote("Set up the project once with Set up a project on this tab.", note)}
}

// CISnippet returns the CI file that publishes to repo with tool from system (ci.GitHubActions,
// ci.Jenkins or ci.JenkinsFreestyleSystem) when t fires. A freestyle job has no trigger of its
// own.
func CISnippet(repo *repos.Repo, baseURL string, tool Tool, system string, t ci.Trigger) Snippet {
	sys := system
	if system == ci.JenkinsFreestyleSystem {
		sys = ci.Jenkins
	}
	steps := tool.Publish(repo, baseURL, sys)
	s := Snippet{Tool: sys, Purpose: SnippetPublish, Context: SnippetRepo, Via: ViaCI, Title: tool.Label, Note: tool.CINote}
	switch system {
	case ci.Jenkins:
		s.ID, s.File, s.Lang = "jenkins.pipeline."+tool.ID, "Jenkinsfile", "groovy"
		s.Code = ci.Jenkinsfile(repo.Name, baseURL, steps, t)
		s.Note = joinNote(ci.JenkinsNote(baseURL), tool.CINote)
	case ci.JenkinsFreestyleSystem:
		s.ID, s.Lang = "jenkins.freestyle."+tool.ID, "sh"
		s.Code = ci.JenkinsFreestyle(baseURL, steps)
		s.Note = joinNote(ci.JenkinsNote(baseURL)+" In the job, bind it to the variable OIDC_TOKEN (Build Environment › Use secret text(s) or file(s)) and add this shell build step.", tool.CINote)
	default:
		s.ID, s.File, s.Lang = "github-actions.workflow."+tool.ID, ".github/workflows/publish.yml", "yaml"
		s.Code = ci.GitHubWorkflow(repo.Name, baseURL, steps, t)
	}
	return s
}

func joinNote(a, b string) string {
	if a == "" || b == "" {
		return a + b
	}
	return a + " " + b
}

// TokenPrompt reads a publish token into $TOKEN at a hidden prompt (bash and zsh), so the token
// stays out of the shell history.
const TokenPrompt = "printf 'Publish token: '; read -rs TOKEN; echo"

// ReadTokenPrompt reads a read token into $READ_TOKEN at a hidden prompt (bash and zsh).
const ReadTokenPrompt = "printf 'Read token: '; read -rs READ_TOKEN; echo"

// ReadTokenConfig prints a curl config with YOUR_USERNAME and the read token in $READ_TOKEN.
// Pipe it to "curl -K -", so the token is never an argument of a process.
const ReadTokenConfig = `printf 'user = "YOUR_USERNAME:%s"\n' "$READ_TOKEN"`
