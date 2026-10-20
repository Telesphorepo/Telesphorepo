package ci

import (
	"maps"
	"slices"
	"strconv"
	"strings"
)

// The CI files that publish to a repository: one GitHub Actions workflow builder, one Jenkinsfile
// builder and one Jenkins freestyle builder for every build tool. A build tool gives its
// PublishSteps; the builders give the frame, the install step of the Telesphorepo CLI, the
// identity token and the trigger. The CLI and its credential helpers read the identity token of
// the job themselves.

// CI system IDs of the CI files. They are the IDs of the CI providers that check the tokens of
// those systems.
const (
	GitHubActions = "github-actions"
	Jenkins       = "jenkins"
)

// Systems are the CI systems that the builders write files for, in display order.
var Systems = []string{GitHubActions, Jenkins}

// JenkinsFreestyleSystem asks GET /api/repos/{name}/ci for the shell build step of a Jenkins
// freestyle job (JenkinsFreestyle). Its CI provider is Jenkins.
const JenkinsFreestyleSystem = "jenkins-freestyle"

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
	// BuildPS is the PowerShell command of Build for a Jenkins agent on Windows. "": Build, which
	// then must also run in PowerShell.
	BuildPS string
	// BuildAdded says that Build belongs to the publish (it names what Publish pushes, such as an
	// image tag), so the change to an existing file adds it. Otherwise the change shows Build as a
	// step the project already has.
	BuildAdded bool
	// Publish are the shell commands that publish after the install step, e.g. "telesphorepo
	// publish".
	Publish string
	// PublishPS are the PowerShell commands of Publish for a Jenkins agent on Windows. "": Publish,
	// which then must also run in PowerShell.
	PublishPS string
	// Env is the environment of the publish commands.
	Env map[string]string
}

// InstallSh is the shell step that installs the Telesphorepo CLI of the server at baseURL.
func InstallSh(baseURL string) string { return "curl -fsSL " + baseURL + "/install.sh | sh" }

// InstallPS is the PowerShell step that installs the Telesphorepo CLI of the server at baseURL.
func InstallPS(baseURL string) string {
	return "& ([scriptblock]::Create((irm " + baseURL + "/install.ps1)))"
}

// localBin puts the folder of install.sh on the PATH of the same shell.
const localBin = `export PATH="$HOME/.local/bin:$PATH"`

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
// The job may ask for an identity token (id-token: write): the CLI asks for one with the audience
// baseURL. The install step adds the CLI to the PATH of the later steps.
func GitHubWorkflow(repo, baseURL string, s PublishSteps, t Trigger) string {
	var b strings.Builder
	b.WriteString(ghTrigger(t) + "\n" + ghPermissions + "\njobs:\n  publish:\n    runs-on: ubuntu-latest\n    steps:\n")
	b.WriteString(ghSetup(s))
	if s.Build != "" {
		b.WriteString(runStep("", s.Build, nil))
	}
	b.WriteString(ghPublish(repo, baseURL, s))
	return b.String()
}

// GitHubWorkflowDiff returns what an existing workflow needs to publish to repo with s, as the
// lines of a diff (see Added and Context): the permissions and the publish steps are added; the
// trigger of t, a job with the checkout, the toolchain and the build are context.
func GitHubWorkflowDiff(repo, baseURL string, s PublishSteps, t Trigger) string {
	var b strings.Builder
	b.WriteString(Context(ghTrigger(t) + "\n"))
	b.WriteString(Added(ghPermissions))
	b.WriteString(Context("\njobs:\n  build:\n    runs-on: ubuntu-latest\n    steps:\n" + ghSetup(s)))
	switch {
	case s.Build == "":
		b.WriteString(Context("      # your build steps\n"))
	case s.BuildAdded:
		b.WriteString(Added(runStep("", s.Build, nil)))
	default:
		b.WriteString(Context(runStep("", s.Build, nil)))
	}
	b.WriteString(Added(ghPublish(repo, baseURL, s)))
	return b.String()
}

// ghPermissions lets the job ask for an identity token.
const ghPermissions = "permissions:\n  id-token: write\n  contents: read\n"

// ghTrigger is the on: block of t; the zero Trigger is a push to main.
func ghTrigger(t Trigger) string {
	if t.zero() {
		t.Branches = []string{"main"}
	}
	s := "on:\n  push:\n"
	if len(t.Branches) > 0 {
		s += "    branches: " + yamlList(t.Branches) + "\n"
	}
	if len(t.Tags) > 0 {
		s += "    tags: " + yamlList(t.Tags) + "\n"
	}
	return s
}

// ghSetup are the checkout and the toolchain steps.
func ghSetup(s PublishSteps) string {
	var b strings.Builder
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
	return b.String()
}

// ghPublish are the install step of the CLI and the publish step.
func ghPublish(repo, baseURL string, s PublishSteps) string {
	name := ""
	if strings.Contains(strings.TrimRight(s.Publish, "\n"), "\n") {
		name = "Publish to " + repo
	}
	return runStep("", InstallSh(baseURL), nil) + runStep(name, s.Publish, s.Env)
}

// Added marks each line of block as added in a diff: "+" and the line.
func Added(block string) string { return markLines("+", block) }

// Context marks each line of block as context in a diff: " " and the line.
func Context(block string) string { return markLines(" ", block) }

func markLines(mark, block string) string {
	var b strings.Builder
	for _, l := range strings.Split(strings.TrimSuffix(block, "\n"), "\n") {
		b.WriteString(mark + l + "\n")
	}
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

// Jenkinsfile returns a declarative Jenkinsfile that publishes to repo of the server at baseURL
// with s. Build runs in a stage of its own: with sh on a Unix agent, with PowerShell on a Windows
// agent. The publish stage binds the identity token to
// OIDC_TOKEN: Jenkins makes the token when withCredentials starts, and the server takes a token for
// 10 minutes only. Inside it, the stage installs the CLI and publishes: with sh on a Unix agent,
// with PowerShell on a Windows agent. A Trigger that is not the zero Trigger limits the publish
// stage to its branches and tags (a multibranch job). A Groovy string in single quotes reads a
// backslash as an escape, so each backslash of a script is doubled.
func Jenkinsfile(repo, baseURL string, s PublishSteps, t Trigger) string {
	var b strings.Builder
	b.WriteString(jenkinsHead)
	if s.Build != "" {
		b.WriteString(jenkinsBuild(s))
	}
	b.WriteString(jenkinsPublish(repo, baseURL, s, t))
	b.WriteString(jenkinsTail)
	return b.String()
}

// JenkinsfileDiff returns what an existing declarative Jenkinsfile needs to publish to repo with
// s, as the lines of a diff (see Added and Context): the publish stage of Jenkinsfile is added
// after a Build stage of context.
func JenkinsfileDiff(repo, baseURL string, s PublishSteps, t Trigger) string {
	var b strings.Builder
	b.WriteString(Context(jenkinsHead))
	switch {
	case s.Build == "":
		b.WriteString(Context("    stage('Build') {\n      steps {\n        // your build steps\n      }\n    }\n"))
	case s.BuildAdded:
		b.WriteString(Added(jenkinsBuild(s)))
	default:
		b.WriteString(Context("    stage('Build') {\n      steps {\n" + groovyStep("sh", s.Build, "        ") + "      }\n    }\n"))
	}
	b.WriteString(Added(jenkinsPublish(repo, baseURL, s, t)))
	b.WriteString(Context(jenkinsTail))
	return b.String()
}

const (
	jenkinsHead = "pipeline {\n  agent any\n  stages {\n"
	jenkinsTail = "  }\n}\n"
)

// groovyStep is a sh or powershell step with script, each line after prefix.
func groovyStep(kind, script, prefix string) string {
	script = strings.ReplaceAll(strings.TrimRight(script, "\n"), `\`, `\\`)
	if !strings.ContainsAny(script, "\n'") {
		return prefix + kind + " '" + script + "'\n"
	}
	return prefix + kind + " '''\n" + Indent(script, prefix+"  ") + prefix + "'''\n"
}

// jenkinsBuild is the Build stage: Build with sh on a Unix agent, BuildPS with PowerShell on a
// Windows agent.
func jenkinsBuild(s PublishSteps) string {
	buildPS := s.BuildPS
	if buildPS == "" {
		buildPS = s.Build
	}
	return "    stage('Build') {\n      steps {\n        script {\n          if (isUnix()) {\n" +
		groovyStep("sh", s.Build, "            ") + "          } else {\n" +
		groovyStep("powershell", buildPS, "            ") + "          }\n        }\n      }\n    }\n"
}

// jenkinsPublish is the publish stage.
func jenkinsPublish(repo, baseURL string, s PublishSteps, t Trigger) string {
	ps := s.PublishPS
	if ps == "" {
		ps = s.Publish
	}
	var b strings.Builder
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
		inner + "  script {\n" +
		inner + "    if (isUnix()) {\n" +
		groovyStep("sh", InstallSh(baseURL)+"\n"+localBin+"\n"+s.Publish, inner+"      ") +
		inner + "    } else {\n" +
		groovyStep("powershell", InstallPS(baseURL)+"\n"+ps, inner+"      ") +
		inner + "    }\n" +
		inner + "  }\n" +
		inner + "}\n")
	if len(s.Env) > 0 {
		b.WriteString("        }\n")
	}
	b.WriteString("      }\n    }\n")
	return b.String()
}

// JenkinsFreestyle returns the shell build step of a Jenkins freestyle job on a Unix agent that
// publishes with s to the server at baseURL: the build, the install step of the CLI and the
// publish. The job binds the identity token to OIDC_TOKEN in its build environment.
func JenkinsFreestyle(baseURL string, s PublishSteps) string {
	var b strings.Builder
	if s.Build != "" {
		b.WriteString(strings.TrimRight(s.Build, "\n") + "\n")
	}
	b.WriteString(InstallSh(baseURL) + "\n" + localBin + "\n")
	for _, k := range sortedKeys(s.Env) {
		b.WriteString("export " + k + "=" + strconv.Quote(s.Env[k]) + "\n")
	}
	b.WriteString(strings.TrimRight(s.Publish, "\n") + "\n")
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
