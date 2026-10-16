package ci

import (
	"maps"
	"slices"
	"strconv"
	"strings"
)

// The CI files that publish to a repository: one GitHub Actions workflow builder and one
// Jenkinsfile builder for every build tool. A build tool gives its PublishSteps; the builders give
// the frame, the identity token and the trigger.

// CI system IDs of the CI files. They are the IDs of the CI providers that check the tokens of
// those systems.
const (
	GitHubActions = "github-actions"
	Jenkins       = "jenkins"
)

// Systems are the CI systems that the builders write files for, in display order.
var Systems = []string{GitHubActions, Jenkins}

// JenkinsCredential is the ID of the Jenkins "OpenID Connect id token" credential that every
// Jenkinsfile binds to the variable OIDC_TOKEN.
const JenkinsCredential = "telesphorepo-id-token"

// JenkinsNote tells how to make the credential that a Jenkinsfile binds.
func JenkinsNote(baseURL string) string {
	return "Install the OpenID Connect Provider plugin in Jenkins. Then add an \"OpenID Connect id token\" credential with the ID " +
		JenkinsCredential + " and the audience " + baseURL + "."
}

// WorkflowStep is a GitHub Actions step that sets up a toolchain, e.g. actions/setup-java with its
// inputs in order.
type WorkflowStep struct {
	Uses string
	With [][2]string
}

// PublishSteps is how a build tool publishes from one CI system.
type PublishSteps struct {
	// Toolchain are the GitHub Actions steps after the checkout.
	Toolchain []WorkflowStep
	// Build builds and tests before the publish ("" for none). Jenkins runs it in a stage of its
	// own; GitHub Actions in a step of its own.
	Build string
	// Publish are the shell commands that publish.
	Publish string
	// Token makes the job's identity token available to Publish in $OIDC_TOKEN. Jenkins always
	// binds it in the publish stage; GitHub Actions requests it only with Token.
	Token bool
	// Env is the environment of the publish commands.
	Env map[string]string
}

// Trigger is when a CI file publishes. The zero Trigger is the default: a GitHub Actions push to
// main, and every run of the Jenkins job.
type Trigger struct {
	Branches []string // branch names or globs
	Tags     []string // tag names or globs
}

func (t Trigger) zero() bool { return len(t.Branches) == 0 && len(t.Tags) == 0 }

// ValidPattern reports whether s may be a branch or tag pattern of a Trigger: letters, digits and
// . _ / * - only, 1 to 100 bytes. Such a pattern needs no quoting in YAML or Groovy.
func ValidPattern(s string) bool {
	if s == "" || len(s) > 100 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._/*-", c)) {
			return false
		}
	}
	return true
}

// GitHubWorkflow returns the GitHub Actions workflow that publishes to repo with s when t fires.
// The job asks for an identity token with the audience baseURL; a step that needs it gets it in
// $OIDC_TOKEN, masked in the job log.
func GitHubWorkflow(repo, baseURL string, s PublishSteps, t Trigger) string {
	var b strings.Builder
	b.WriteString("on:\n  push:\n")
	if t.zero() {
		t.Branches = []string{"main"}
	}
	if len(t.Branches) > 0 {
		b.WriteString("    branches: " + yamlList(t.Branches) + "\n")
	}
	if len(t.Tags) > 0 {
		b.WriteString("    tags: " + yamlList(t.Tags) + "\n")
	}
	b.WriteString("\npermissions:\n  id-token: write\n  contents: read\n\njobs:\n  publish:\n    runs-on: ubuntu-latest\n    steps:\n")
	b.WriteString("      - uses: actions/checkout@v7\n")
	for _, st := range s.Toolchain {
		b.WriteString("      - uses: " + st.Uses + "\n")
		if len(st.With) > 0 {
			b.WriteString("        with:\n")
			for _, kv := range st.With {
				b.WriteString("          " + kv[0] + ": " + kv[1] + "\n")
			}
		}
	}
	if s.Build != "" {
		b.WriteString(runStep("", s.Build, nil))
	}
	publish := s.Publish
	if s.Token {
		publish = "OIDC_TOKEN=$(printf 'header = \"Authorization: Bearer %s\"\\n' \"$ACTIONS_ID_TOKEN_REQUEST_TOKEN\" \\\n" +
			"  | curl -fsS -K - \"$ACTIONS_ID_TOKEN_REQUEST_URL&audience=" + baseURL + "\" | jq -r .value)\n" +
			"echo \"::add-mask::$OIDC_TOKEN\"\n" + publish
	}
	name := ""
	if strings.Contains(strings.TrimRight(publish, "\n"), "\n") {
		name = "Publish to " + repo
	}
	b.WriteString(runStep(name, publish, s.Env))
	return b.String()
}

// runStep is a GitHub Actions run step: one line, or a block with a name.
func runStep(name, script string, env map[string]string) string {
	script = strings.TrimRight(script, "\n")
	var b strings.Builder
	if name == "" && !strings.Contains(script, "\n") {
		b.WriteString("      - run: " + script + "\n")
	} else {
		if name == "" {
			name = "Run"
		}
		b.WriteString("      - name: " + name + "\n        run: |\n" + Indent(script, "          "))
	}
	if len(env) > 0 {
		b.WriteString("        env:\n")
		for _, k := range sortedKeys(env) {
			b.WriteString("          " + k + ": " + strconv.Quote(env[k]) + "\n")
		}
	}
	return b.String()
}

func yamlList(xs []string) string {
	q := make([]string, len(xs))
	for i, x := range xs {
		q[i] = strconv.Quote(x)
	}
	return "[" + strings.Join(q, ", ") + "]"
}

// Jenkinsfile returns a declarative Jenkinsfile that publishes to repo with s. Build runs in a
// stage of its own. The publish stage binds the identity token to OIDC_TOKEN: Jenkins makes the
// token when withCredentials starts, and the server takes a token for 10 minutes only. A Trigger
// that is not the zero Trigger limits the publish stage to its branches and tags (a multibranch
// job). A Groovy string in single quotes reads a backslash as an escape, so each backslash of a
// script is doubled.
func Jenkinsfile(repo string, s PublishSteps, t Trigger) string {
	sh := func(script, prefix string) string {
		script = strings.ReplaceAll(strings.TrimRight(script, "\n"), `\`, `\\`)
		if !strings.ContainsAny(script, "\n'") {
			return prefix + "sh '" + script + "'\n"
		}
		return prefix + "sh '''\n" + Indent(script, prefix+"  ") + prefix + "'''\n"
	}
	var b strings.Builder
	b.WriteString("pipeline {\n  agent any\n  stages {\n")
	if s.Build != "" {
		b.WriteString("    stage('Build') {\n      steps {\n" + sh(s.Build, "        ") + "      }\n    }\n")
	}
	b.WriteString("    stage('Publish to " + repo + "') {\n")
	if !t.zero() {
		b.WriteString("      when {\n        anyOf {\n")
		for _, br := range t.Branches {
			b.WriteString("          branch '" + br + "'\n")
		}
		for _, tg := range t.Tags {
			b.WriteString("          tag '" + tg + "'\n")
		}
		b.WriteString("        }\n      }\n")
	}
	b.WriteString("      steps {\n")
	inner := "        "
	if len(s.Env) > 0 {
		var env []string
		for _, k := range sortedKeys(s.Env) {
			env = append(env, "'"+k+"="+strings.ReplaceAll(s.Env[k], "'", `\'`)+"'")
		}
		b.WriteString(inner + "withEnv([" + strings.Join(env, ", ") + "]) {\n")
		inner += "  "
	}
	b.WriteString(inner + "// The token is good for 10 minutes, so only this stage gets it.\n" +
		inner + "withCredentials([string(credentialsId: '" + JenkinsCredential + "', variable: 'OIDC_TOKEN')]) {\n" +
		sh(s.Publish, inner+"  ") + inner + "}\n")
	if len(s.Env) > 0 {
		b.WriteString("        }\n")
	}
	b.WriteString("      }\n    }\n  }\n}\n")
	return b.String()
}

// Indent puts prefix before each line of s that is not empty and ends s with one line break.
func Indent(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = prefix + l
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

func sortedKeys(m map[string]string) []string { return slices.Sorted(maps.Keys(m)) }
