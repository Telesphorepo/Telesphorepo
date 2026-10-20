package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// The detection table of "publish": a marker in the project folder picks the ecosystem, a
// wrapper or a lockfile picks the tool. A new format adds a row. A row without markers takes
// FILE arguments and the CLI uploads them itself.
//
// The token goes only into the env of the child process, as TELESPHOREPO_TOKEN and the tool's own
// variables. A config file that the CLI writes holds a ${env} reference, never the token. It goes
// in the private temp folder of the job, which the CLI removes at exit.

// row is one way to publish to a repo of a format.
type row struct {
	format  string   // the format of the repo on the server
	kind    string   // the marker kind; kind = "<kind>" in the project file picks it
	title   string   // the name for the person
	markers []string // the files that mark a project of the kind; none: the row takes FILE arguments
	ciTool  string   // the tool of GET /api/repos/{name}/ci; "": the server has no CI file for the row
	// ciToolOf picks the CI tool by the project folder (e.g. by lockfile); nil: ciTool.
	ciToolOf func(dir string) string
	run      func(*job) error
	preview  func(dir string) string // the command that "publish" runs, for "status"
}

var rows = []row{
	{format: "maven", kind: "maven", title: "Maven", markers: []string{"pom.xml"}, ciTool: "maven",
		run: runMaven, preview: func(dir string) string { return toolPreview(dir, "mvnw", "mvn") + " deploy" }},
	{format: "maven", kind: "gradle", title: "Gradle",
		markers: []string{"settings.gradle.kts", "settings.gradle", "build.gradle.kts", "build.gradle"}, ciTool: "gradle",
		run: runGradle, preview: func(dir string) string { return toolPreview(dir, "gradlew", "gradle") + " publish" }},
	{format: "docker", kind: "helm", title: "Helm chart", markers: []string{"Chart.yaml"},
		run: runHelm, preview: func(string) string { return "helm package, then helm push" }},
	{format: "raw", kind: "files", title: "Files", ciTool: "telesphorepo", run: uploadRaw},
	{format: "debian", kind: "packages", title: "Debian packages", ciTool: "telesphorepo", run: uploadDebian},
	{format: "cargo", kind: "cargo", title: "Cargo", markers: []string{"Cargo.toml"}, ciTool: "cargo",
		run: runCargo, preview: func(string) string { return "cargo publish --registry telesphorepo" }},
	{format: "npm", kind: "npm", title: "npm package", markers: []string{"package.json"}, ciTool: "npm", ciToolOf: npmTool,
		run: runNPM, preview: npmPreview},
}

// helperFormat is a format of the helper category: its tool publishes with its own command and
// asks the credential helper of the CLI for the token.
type helperFormat struct {
	tool    string                         // the tool, e.g. "Docker"
	command func(host, repo string) string // the native command, e.g. docker push
}

var helperFormats = map[string]helperFormat{
	"docker": {tool: "Docker", command: func(host, repo string) string { return "docker push " + host + "/" + repo + "/app:1.0" }},
}

// rowsOf returns the rows of format.
func rowsOf(format string) []row {
	var out []row
	for _, r := range rows {
		if r.format == format {
			out = append(out, r)
		}
	}
	return out
}

// takesFiles reports whether the row takes FILE arguments.
func (r row) takesFiles() bool { return len(r.markers) == 0 }

// marker returns the first marker of r in dir, or "".
func (r row) marker(dir string) string {
	for _, m := range r.markers {
		if st, err := os.Stat(filepath.Join(dir, m)); err == nil && !st.IsDir() {
			return m
		}
	}
	return ""
}

// markersOf lists the markers of the rows of format, e.g. "pom.xml, settings.gradle.kts, ...".
func markersOf(format string) string {
	var ms []string
	for _, r := range rowsOf(format) {
		ms = append(ms, r.markers...)
	}
	return strings.Join(ms, ", ")
}

// pickRow returns the row of format for the project folder dir. kind ("" for any) picks one of
// several marker kinds; with several and no kind, the CLI asks on a terminal and stops without
// one. ok is false when no row has a marker in dir.
func pickRow(format, kind, dir string) (row, bool, error) {
	var found []row
	for _, r := range rowsOf(format) {
		if r.takesFiles() {
			return r, true, nil
		}
		if (kind == "" || kind == r.kind) && r.marker(dir) != "" {
			found = append(found, r)
		}
	}
	switch {
	case len(found) == 0 && kind != "":
		return row{}, false, fmt.Errorf("%s names kind = %q, but %s has no marker of that kind", projectFileName, kind, dir)
	case len(found) == 0:
		return row{}, false, nil
	case len(found) == 1:
		return found[0], true, nil
	}
	options := make([]string, len(found))
	kinds := make([]string, len(found))
	for i, r := range found {
		options[i] = fmt.Sprintf("%s (%s)", r.title, r.marker(dir))
		kinds[i] = fmt.Sprintf("kind = %q", r.kind)
	}
	if !interactive() {
		return row{}, false, fmt.Errorf("%s has projects of several kinds: %s. Add one line to %s: %s",
			dir, strings.Join(options, ", "), projectFileName, strings.Join(kinds, " or "))
	}
	return found[choose("This folder has projects of several kinds. Which one?", options, 0)], true, nil
}

// job is one run of "publish": the project, the session and the private temp folder.
type job struct {
	ctx   context.Context
	c     *client
	p     *project
	s     *session
	tmp   string
	files []string
}

// env is the environment of a child process of the job: the session token as
// TELESPHOREPO_TOKEN, and extra.
func (j *job) env(extra ...string) []string {
	return childEnv(append([]string{envToken + "=" + j.s.Token}, extra...)...)
}

// exitError is a child process that ended with a non-zero exit code. The CLI passes the code
// through. An exitError without a name ends the CLI with the code and no message.
type exitError struct {
	name string
	code int
}

func (e *exitError) Error() string { return fmt.Sprintf("%s exited with code %d", e.name, e.code) }

// findTool returns the command of a tool for the project folder dir: the wrapper of the project
// when it has one (wrapper on Linux and macOS, wrapper.cmd or .bat on Windows), else name on the
// PATH. A wrapper without the execute bit runs through sh.
func findTool(dir, wrapper, name string) ([]string, error) {
	if runtime.GOOS == "windows" {
		for _, ext := range []string{".cmd", ".bat"} {
			if p := filepath.Join(dir, wrapper+ext); fileExists(p) {
				return []string{p}, nil
			}
		}
	} else if p := filepath.Join(dir, wrapper); fileExists(p) {
		if st, err := os.Stat(p); err == nil && st.Mode().Perm()&0o111 == 0 {
			return []string{"sh", p}, nil
		}
		return []string{p}, nil
	}
	p, err := exec.LookPath(name)
	if err != nil {
		return nil, fmt.Errorf("this project has no %s and %s is not on the PATH. Install %s, or add the wrapper to the project", wrapper, name, name)
	}
	return []string{p}, nil
}

// toolPreview names the tool that findTool picks, for "status".
func toolPreview(dir, wrapper, name string) string {
	cmd, err := findTool(dir, wrapper, name)
	if err != nil {
		return name + " (not found)"
	}
	return strings.Join(cmd, " ")
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// runTool prints the command, then runs it in the project folder with env and the terminal of
// the CLI. A command that ends with a non-zero exit code is an *exitError. Ctrl-C reaches the
// child from the terminal; when the CLI itself is stopped, it stops the child too.
func (j *job) runTool(argv []string, env []string) error {
	fmt.Fprintln(os.Stderr, "$ "+strings.Join(argv, " "))
	cmd := exec.CommandContext(j.ctx, argv[0], argv[1:]...)
	cmd.Dir = j.p.dir
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if runtime.GOOS != "windows" {
		cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	}
	cmd.WaitDelay = 15 * time.Second
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ee):
		code := ee.ExitCode()
		if code <= 0 {
			code = 1
		}
		return &exitError{name: filepath.Base(argv[0]), code: code}
	}
	return fmt.Errorf("cannot run %s: %w", argv[0], err)
}

// writeTemp writes a config file of the job into its temp folder and returns its path.
func (j *job) writeTemp(name, text string) (string, error) {
	f := filepath.Join(j.tmp, name)
	if err := os.WriteFile(f, []byte(text), 0o600); err != nil {
		return "", err
	}
	return f, nil
}

// mavenURL is the URL of the Maven repo of the job.
func (j *job) mavenURL() string { return j.c.id.base() + "/maven/" + j.p.Repo }

// mavenSettings is the settings file that "mvn -gs" reads: one server entry whose password
// refers to the env. -gs replaces only the settings of the Maven installation, so the user's
// ~/.m2/settings.xml stays in force.
const mavenSettings = `<?xml version="1.0" encoding="UTF-8"?>
<settings xmlns="http://maven.apache.org/SETTINGS/1.0.0">
  <servers>
    <server>
      <id>telesphorepo</id>
      <username>telesphorepo</username>
      <password>${env.TELESPHOREPO_TOKEN}</password>
    </server>
  </servers>
</settings>
`

// runMaven runs mvn deploy with the temp settings file and the repo as the deployment
// repository.
func runMaven(j *job) error {
	tool, err := findTool(j.p.dir, "mvnw", "mvn")
	if err != nil {
		return err
	}
	settings, err := j.writeTemp("settings.xml", mavenSettings)
	if err != nil {
		return err
	}
	argv := append(tool, "deploy", "-gs", settings, "-DaltDeploymentRepository=telesphorepo::"+j.mavenURL())
	return j.runTool(argv, j.env())
}

// gradleInit is the init script of "gradle publish": each project that applies maven-publish gets
// the repository telesphorepo, with the URL and the credentials from the env. It touches no other
// repository. A build in which no project applies maven-publish stops with the plugin line.
const gradleInit = `import org.gradle.api.publish.PublishingExtension

val telesphorepoUrl: String = System.getenv("TELESPHOREPO_MAVEN_URL")
    ?: throw GradleException("TELESPHOREPO_MAVEN_URL is not set; run the build with telesphorepo publish")
var telesphorepoPublishing = false

allprojects {
    plugins.withId("maven-publish") {
        telesphorepoPublishing = true
        extensions.getByType(PublishingExtension::class.java).repositories.maven {
            name = "telesphorepo"
            setUrl(telesphorepoUrl)
            // A server on plain HTTP (the project file names http://) serves its Maven repos on it.
            isAllowInsecureProtocol = telesphorepoUrl.startsWith("http://")
            credentials {
                username = "telesphorepo"
                password = System.getenv("TELESPHOREPO_TOKEN")
            }
        }
    }
}

gradle.projectsEvaluated {
    if (!telesphorepoPublishing) {
        throw GradleException("No project of this build applies maven-publish, so it has nothing to publish. Add the plugin to the build file: plugins { ` + "`maven-publish`" + ` } (Kotlin) or plugins { id 'maven-publish' } (Groovy)")
    }
}
`

// runGradle runs gradle publish with the init script.
func runGradle(j *job) error {
	tool, err := findTool(j.p.dir, "gradlew", "gradle")
	if err != nil {
		return err
	}
	script, err := j.writeTemp("telesphorepo.init.gradle.kts", gradleInit)
	if err != nil {
		return err
	}
	argv := append(tool, "publish", "--init-script", script)
	return j.runTool(argv, j.env("TELESPHOREPO_MAVEN_URL="+j.mavenURL()))
}

// runHelm packages the chart and pushes it with helm push. The temp registry config names the
// credential helper of the CLI for the Docker hosts of the server, and the helper answers the
// session token of the env.
func runHelm(j *job) error {
	helm, err := exec.LookPath("helm")
	if err != nil {
		return errors.New("helm is not on the PATH. Install Helm 3.8 or later")
	}
	hosts, err := j.c.registryHosts(j.ctx)
	if err != nil {
		return err
	}
	out := filepath.Join(j.tmp, "chart")
	if err := os.Mkdir(out, 0o700); err != nil {
		return err
	}
	pathEnv, err := helperOnPath(j.tmp)
	if err != nil {
		return err
	}
	env := j.env(envDockerHosts+"="+strings.Join(hosts, ","), pathEnv)
	if err := j.runTool([]string{helm, "package", j.p.dir, "-d", out}, env); err != nil {
		return err
	}
	charts, _ := filepath.Glob(filepath.Join(out, "*.tgz"))
	if len(charts) != 1 {
		return fmt.Errorf("helm package wrote %d charts into %s, not one", len(charts), out)
	}
	cfg := `{"credHelpers": {`
	for i, h := range hosts {
		if i > 0 {
			cfg += ", "
		}
		cfg += fmt.Sprintf("%q: %q", h, helperName)
	}
	regConfig, err := j.writeTemp("config.json", cfg+"}}\n")
	if err != nil {
		return err
	}
	// Helm 4 and ORAS push to the upload host when the server has one.
	host := hosts[len(hosts)-1]
	argv := []string{helm, "push", charts[0], "oci://" + host + "/" + j.p.Repo, "--registry-config", regConfig}
	if j.c.id.scheme == "http" {
		// A server on plain HTTP serves its registry on plain HTTP too.
		argv = append(argv, "--plain-http")
	}
	return j.runTool(argv, env)
}

// helperOnPath returns the PATH entry of the child env with a folder that holds
// docker-credential-telesphorepo first: the folder of the CLI when the install made the name
// there, else a link to the CLI in the temp folder tmp.
func helperOnPath(tmp string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	name := dockerHelperName
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	dir := filepath.Dir(exe)
	if !fileExists(filepath.Join(dir, name)) {
		dir = filepath.Join(tmp, "bin")
		if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return "", err
		}
		if err := linkOrCopy(exe, filepath.Join(dir, name)); err != nil {
			return "", err
		}
	}
	return "PATH=" + dir + string(os.PathListSeparator) + os.Getenv("PATH"), nil
}
