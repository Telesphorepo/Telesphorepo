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
	// Via says where a publish snippet publishes from: ViaComputer (a client plugin with a
	// device), ViaCI or ViaHand (the browser, or a publish token with curl). "" for a use snippet.
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
	// ID names the tool, e.g. "gradle", "maven", "curl". A tool with Setup has an ID of lower-case
	// letters and digits only: the setup scripts name their functions after it.
	ID    string
	Label string
	// Publish returns how the tool publishes to repo from the CI system (ci.GitHubActions or
	// ci.Jenkins). baseURL is the server's public URL without a trailing slash.
	Publish func(repo *repos.Repo, baseURL, system string) ci.PublishSteps
	// CINote goes under the CI files of the tool, e.g. what the project needs first ("" for none).
	CINote string
	// Setup is the tool's part of the project setup scripts; nil: no project setup.
	Setup *SetupFragment
}

// SetupFragment is a build tool's part of the project setup scripts GET /setup.sh and
// GET /setup.ps1 (the publish module). The scripts run the fragment of the first tool whose
// detection finds a project in the current folder.
//
// Sh defines the sh functions detect_<id> (true when the folder holds a project of the tool),
// apply_<id> (stages the changes of the project files), publish_cmd_<id> (prints the command
// that publishes from a computer), ci_edit_<id> (a filter of a CI file, e.g. for the wrapper of
// the project) and next_<id> (prints extra next steps with step and more). PS1 defines the
// PowerShell functions Test-<Id>, Set-<Id>, Get-<Id>PublishCommand, Edit-<Id>Ci and
// Write-<Id>Next, with <Id> the ID with a capital first letter. Both use the frame's variables
// (REPO, URL, CI) and helpers (stage_file, stage_path, stage_insert_after, stage_append,
// block_start, block_end, plan, manual, note; PowerShell: Get-Work, Add-Lines, Find-Block,
// Test-Line, Find-OnlyLine, Add-Plan, Add-Manual, $Notes).
type SetupFragment struct {
	// Project names the files that mark a project of the tool, e.g. "settings.gradle.kts or
	// settings.gradle".
	Project string
	Sh      string
	PS1     string
}

// CISnippet returns the CI file that publishes to repo with tool from system (ci.GitHubActions or
// ci.Jenkins) when t fires.
func CISnippet(repo *repos.Repo, baseURL string, tool Tool, system string, t ci.Trigger) Snippet {
	steps := tool.Publish(repo, baseURL, system)
	s := Snippet{Tool: system, Purpose: SnippetPublish, Context: SnippetRepo, Via: ViaCI, Title: tool.Label, Note: tool.CINote}
	switch system {
	case ci.Jenkins:
		s.ID, s.File, s.Lang = "jenkins.pipeline."+tool.ID, "Jenkinsfile", "groovy"
		s.Code = ci.Jenkinsfile(repo.Name, steps, t)
		s.Note = joinNote(ci.JenkinsNote(baseURL), tool.CINote)
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

// TokenCurl starts a curl command that reads the publish token in $TOKEN from a curl config on
// stdin. printf is a shell builtin, so the token is never an argument of a process.
const TokenCurl = `printf 'user = "publish:%s"\n' "$TOKEN" | curl -fsS -K - `

// CurlTool is the tool of a format without a client plugin: curl publishes with a publish session
// that the CI job's identity token ($OIDC_TOKEN) mints, and closes the session at the end, also
// when the upload fails. upload is a TokenCurl command for repo. The token goes to curl on stdin,
// never in an argument, and the script turns shell tracing off before it holds the session
// token.
func CurlTool(upload func(repo *repos.Repo, baseURL string) string) Tool {
	return Tool{ID: "curl", Label: "curl", Publish: func(repo *repos.Repo, baseURL, _ string) ci.PublishSteps {
		closeCmd := TokenCurl + "-X DELETE " + baseURL + "/api/publish/sessions/$ID"
		return ci.PublishSteps{Token: true, Publish: "set +x\n" +
			"SESSION=$(printf '{\"token\":\"%s\",\"repo\":\"" + repo.Name + "\"}' \"$OIDC_TOKEN\" \\\n" +
			"  | curl -fsS -H 'Content-Type: application/json' --data @- " + baseURL + "/api/publish/sessions/oidc)\n" +
			"TOKEN=$(echo \"$SESSION\" | jq -r .token)\n" +
			"ID=$(echo \"$SESSION\" | jq -r .id)\n" +
			"close_session() { " + closeCmd + "; }\ntrap close_session EXIT\n" +
			upload(repo, baseURL) + "\n"}
	}}
}

// TokenPrompt reads a publish token into $TOKEN at a hidden prompt (bash and zsh), so the token
// stays out of the shell history.
const TokenPrompt = "printf 'Publish token: '; read -rs TOKEN; echo"

// ReadTokenPrompt reads a read token into $READ_TOKEN at a hidden prompt (bash and zsh).
const ReadTokenPrompt = "printf 'Read token: '; read -rs READ_TOKEN; echo"

// ReadTokenConfig prints a curl config with YOUR_USERNAME and the read token in $READ_TOKEN.
// Pipe it to "curl -K -", so the token is never an argument of a process.
const ReadTokenConfig = `printf 'user = "YOUR_USERNAME:%s"\n' "$READ_TOKEN"`

// HandSnippet returns the publish snippet for a person with a publish token: upload is a
// TokenCurl command.
func HandSnippet(upload string) Snippet {
	return Snippet{ID: "curl.publish", Tool: "curl", Purpose: SnippetPublish, Context: SnippetRepo, Via: ViaHand,
		Title: "Upload with a publish token", Lang: "sh", Code: TokenPrompt + "   # Settings › Tokens › New publish token\n" + upload + "\n",
		Note: "Or drop the files on the repository's Files view in the browser."}
}
