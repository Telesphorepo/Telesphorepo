package format

import "strings"

// Snippet helpers that formats share: publishing with plain curl from CI and by hand.

// CurlCISnippets returns the publish snippets for CI without a client plugin: a GitHub Actions
// step and a Jenkins stage that exchange the job's identity token for a publish session of repo,
// run upload (a command that reads the session token from $TOKEN) and close the session.
func CurlCISnippets(baseURL, repo, upload string) []Snippet {
	exchange := func(oidc string) string {
		return "SESSION=$(curl -fsS -X POST " + baseURL + "/api/publish/sessions/oidc \\\n" +
			"  -H 'Content-Type: application/json' \\\n" +
			"  -d \"{\\\"token\\\":\\\"" + oidc + "\\\",\\\"repo\\\":\\\"" + repo + "\\\"}\")\n" +
			"TOKEN=$(echo \"$SESSION\" | jq -r .token)\n" +
			"ID=$(echo \"$SESSION\" | jq -r .id)\n"
	}
	closeCmd := "curl -fsS -X DELETE -u publish:$TOKEN " + baseURL + "/api/publish/sessions/$ID\n"
	script := func(oidc string) string { return exchange(oidc) + upload + "\n" + closeCmd }
	return []Snippet{
		{Client: "github-actions", Purpose: SnippetPublish, Context: SnippetRepo, Via: ViaCI,
			Title: ".github/workflows/publish.yml", Lang: "yaml",
			Code: "permissions:\n  id-token: write\n  contents: read\n\njobs:\n  publish:\n    runs-on: ubuntu-latest\n    steps:\n" +
				"      - uses: actions/checkout@v4\n      - name: Publish to " + repo + "\n        run: |\n" +
				"          OIDC=$(curl -fsS -H \"Authorization: Bearer $ACTIONS_ID_TOKEN_REQUEST_TOKEN\" \\\n" +
				"            \"$ACTIONS_ID_TOKEN_REQUEST_URL&audience=" + baseURL + "\" | jq -r .value)\n" +
				indent(script("$OIDC"), "          ")},
		{Client: "jenkins", Purpose: SnippetPublish, Context: SnippetRepo, Via: ViaCI,
			Title: "Jenkinsfile", Lang: "groovy",
			Code: "stage('Publish to " + repo + "') {\n  steps {\n" +
				"    withCredentials([string(credentialsId: 'telesphorepo-id-token', variable: 'OIDC_TOKEN')]) {\n" +
				"      sh '''\n" + indent(script("$OIDC_TOKEN"), "        ") + "      '''\n    }\n  }\n}\n",
			Note: "Bind an \"OpenID Connect id token\" credential with audience " + baseURL + " as telesphorepo-id-token."},
	}
}

// HandSnippet returns the publish snippet for a person with a publish token: upload is the curl
// command, with the token in $TOKEN.
func HandSnippet(upload string) Snippet {
	return Snippet{Client: "curl", Purpose: SnippetPublish, Context: SnippetRepo, Via: ViaHand,
		Title: "Upload with a publish token", Lang: "sh", Code: "TOKEN=trs_...   # Settings › Tokens › New publish token\n" + upload + "\n",
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
