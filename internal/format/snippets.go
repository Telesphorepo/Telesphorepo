package format

import "strings"

// Snippet helpers that formats share: publishing from CI and with plain curl by hand.

// JenkinsCredential is the ID of the Jenkins "OpenID Connect id token" credential that every
// Jenkinsfile snippet binds to the variable OIDC_TOKEN.
const JenkinsCredential = "telesphorepo-id-token"

// JenkinsNote tells how to make the credential that a Jenkinsfile snippet binds.
func JenkinsNote(baseURL string) string {
	return "Install the OpenID Connect Provider plugin in Jenkins. Then add an \"OpenID Connect id token\" credential with the ID " +
		JenkinsCredential + " and the audience " + baseURL + "."
}

// GitHubTrigger is the trigger of the workflow snippets: a push to main. The CI pipeline page
// puts the branches and tags of the pipeline in its place.
const GitHubTrigger = "on:\n  push:\n    branches: [\"main\"]\n\n"

// JenkinsPipeline returns a declarative Jenkinsfile. When build is not empty, it runs in a stage
// of its own. publish runs in the last stage, with the identity token in OIDC_TOKEN. Jenkins
// makes the token when withCredentials starts, and the server takes a token for 10 minutes
// only, so only the publish stage binds it. A Groovy string in single quotes reads a backslash
// as an escape, so each backslash of a script is doubled.
func JenkinsPipeline(repo, build, publish string) string {
	sh := func(script, prefix string) string {
		script = strings.ReplaceAll(strings.TrimRight(script, "\n"), `\`, `\\`)
		if !strings.ContainsAny(script, "\n'") {
			return prefix + "sh '" + script + "'\n"
		}
		return prefix + "sh '''\n" + indent(script, prefix+"  ") + prefix + "'''\n"
	}
	var b strings.Builder
	b.WriteString("pipeline {\n  agent any\n  stages {\n")
	if build != "" {
		b.WriteString("    stage('Build') {\n      steps {\n" + sh(build, "        ") + "      }\n    }\n")
	}
	b.WriteString("    stage('Publish to " + repo + "') {\n      steps {\n" +
		"        // The token is good for 10 minutes, so only this stage gets it.\n" +
		"        withCredentials([string(credentialsId: '" + JenkinsCredential + "', variable: 'OIDC_TOKEN')]) {\n" +
		sh(publish, "          ") + "        }\n      }\n    }\n  }\n}\n")
	return b.String()
}

// TokenCurl starts a curl command that reads the publish token in $TOKEN from a curl config on
// stdin. printf is a shell builtin, so the token is never an argument of a process.
const TokenCurl = `printf 'user = "publish:%s"\n' "$TOKEN" | curl -fsS -K - `

// CurlCISnippets returns the publish snippets for CI without a client plugin: a GitHub Actions
// workflow and a Jenkinsfile that exchange the job's identity token for a publish session of
// repo, run upload (a TokenCurl command) and close the session. Every token goes to curl on
// stdin, never in an argument. The workflow masks each token in the job log as soon as it has
// it. Jenkins runs sh with tracing on, so the Jenkins script turns tracing off before it gets
// the session token, and closes the session in an exit trap, also when the upload fails.
func CurlCISnippets(baseURL, repo, upload string) []Snippet {
	exchange := func(oidc, mask string) string {
		return "SESSION=$(printf '{\"token\":\"%s\",\"repo\":\"" + repo + "\"}' \"" + oidc + "\" \\\n" +
			"  | curl -fsS -H 'Content-Type: application/json' --data @- " + baseURL + "/api/publish/sessions/oidc)\n" +
			"TOKEN=$(echo \"$SESSION\" | jq -r .token)\n" + mask +
			"ID=$(echo \"$SESSION\" | jq -r .id)\n"
	}
	closeCmd := TokenCurl + "-X DELETE " + baseURL + "/api/publish/sessions/$ID"
	github := exchange("$OIDC", "echo \"::add-mask::$TOKEN\"\n") + upload + "\n" + closeCmd + "\n"
	jenkins := "set +x\n" + exchange("$OIDC_TOKEN", "") +
		"close_session() { " + closeCmd + "; }\ntrap close_session EXIT\n" + upload + "\n"
	return []Snippet{
		{Client: "github-actions", Purpose: SnippetPublish, Context: SnippetRepo, Via: ViaCI,
			Title: ".github/workflows/publish.yml", Lang: "yaml",
			Code: GitHubTrigger + "permissions:\n  id-token: write\n  contents: read\n\njobs:\n  publish:\n    runs-on: ubuntu-latest\n    steps:\n" +
				"      - uses: actions/checkout@v4\n      - name: Publish to " + repo + "\n        run: |\n" +
				"          OIDC=$(printf 'header = \"Authorization: Bearer %s\"\\n' \"$ACTIONS_ID_TOKEN_REQUEST_TOKEN\" \\\n" +
				"            | curl -fsS -K - \"$ACTIONS_ID_TOKEN_REQUEST_URL&audience=" + baseURL + "\" | jq -r .value)\n" +
				"          echo \"::add-mask::$OIDC\"\n" +
				indent(github, "          ")},
		{Client: "jenkins", Purpose: SnippetPublish, Context: SnippetRepo, Via: ViaCI,
			Title: "Jenkinsfile", Lang: "groovy",
			Code: JenkinsPipeline(repo, "", jenkins),
			Note: JenkinsNote(baseURL)},
	}
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
	return Snippet{Client: "curl", Purpose: SnippetPublish, Context: SnippetRepo, Via: ViaHand,
		Title: "Upload with a publish token", Lang: "sh", Code: TokenPrompt + "   # Settings › Tokens › New publish token\n" + upload + "\n",
		Note: "Or drop the files on the repository's Files view in the browser."}
}

func indent(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = prefix + l
		}
	}
	return strings.Join(lines, "\n") + "\n"
}
