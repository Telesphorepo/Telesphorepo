//go:build clients

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	osexec "os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/ci"
	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

// jenkinsImage is the Jenkins of testdata/jenkins: the newest Jenkins LTS with the newest OpenID
// Connect Provider, Pipeline, Credentials Binding and Git plugins, Maven and the Docker CLI. A
// run builds it once, with the newest base images, and the plugins again each day.
const jenkinsImage = "telesphorepo-e2e-jenkins"

var buildJenkinsImage = sync.OnceValue(func() error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	dir, err := filepath.Abs(filepath.Join("testdata", "jenkins"))
	if err != nil {
		return err
	}
	cmd := osexec.CommandContext(ctx, Engine.CLI, buildArgs("--pull", "--build-arg", "E2E_DAY="+time.Now().Format(time.DateOnly), "-t", jenkinsImage, dir)...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		os.Stderr.Write(out.Bytes())
		return fmt.Errorf("build %s: %w", jenkinsImage, err)
	}
	return nil
})

// jenkinsCI is a running Jenkins on TestNetwork. Its URL is http://<alias>:8080/, so the issuer
// of its identity tokens is that URL + "oidc" and a job URL is <URL>job/<name>/.
type jenkinsCI struct {
	c   *testcontainers.DockerContainer
	URL string
}

// startJenkins starts Jenkins with the credential of the Jenkinsfiles of the server
// (ci.JenkinsCredential) for the audience audience, and env in the env of its builds.
func startJenkins(t testing.TB, audience string, env map[string]string) *jenkinsCI {
	t.Helper()
	if err := buildJenkinsImage(); err != nil {
		t.Fatal(err)
	}
	alias := RandName("jenkins")
	url := "http://" + alias + ":8080/"
	all := map[string]string{"E2E_JENKINS_URL": url, "E2E_AUDIENCE": audience, "E2E_CREDENTIAL": ci.JenkinsCredential, "MAVEN_OPTS": mavenFileLocks}
	for k, v := range env {
		all[k] = v
	}
	c, err := testcontainers.Run(context.Background(), jenkinsImage,
		e2eContainer,
		testcontainers.WithEnv(all),
		testcontainers.WithMounts(testcontainers.VolumeMount(M2Volume, "/root/.m2/repository")),
		network.WithNetwork([]string{alias}, TestNetwork),
		testcontainers.WithWaitStrategyAndDeadline(5*time.Minute, wait.ForLog("Jenkins is fully up and running").WithStartupTimeout(5*time.Minute)),
	)
	registerCleanup(t, c, "jenkins "+alias)
	if err != nil {
		t.Fatalf("start jenkins: %v", err)
	}
	j := &jenkinsCI{c: c, URL: url}
	j.mustExec(t, "the Jenkins CLI", "curl -fsS -o /opt/jenkins-cli.jar http://localhost:8080/jnlpJars/jenkins-cli.jar")
	t.Logf("Jenkins %s", j.mustExec(t, "the Jenkins version", j.cli()+" version"))
	t.Logf("plugins:\n%s", j.mustExec(t, "the plugins", j.cli()+" list-plugins | grep -E '^(oidc-provider|workflow-job|credentials-binding|git) '"))
	return j
}

func (j *jenkinsCI) exec(t testing.TB, script string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	code, rd, err := j.c.Exec(ctx, []string{"sh", "-c", script}, tcexec.Multiplexed())
	if err != nil {
		t.Fatalf("jenkins exec: %v", err)
	}
	out, _ := io.ReadAll(rd)
	return code, string(out)
}

func (j *jenkinsCI) mustExec(t testing.TB, what, script string) string {
	t.Helper()
	code, out := j.exec(t, script)
	if code != 0 {
		t.Fatalf("%s: exit %d:\n%s", what, code, out)
	}
	return out
}

// cli is the Jenkins CLI as the Jenkins container runs it. It names the Jenkins URL, because
// Jenkins refuses a WebSocket of another origin.
func (j *jenkinsCI) cli() string { return "java -jar /opt/jenkins-cli.jar -s " + j.URL + " -webSocket" }

// gitRepo makes the git repository /srv/<name> with files (path -> content) in one commit on
// main, and returns the commit.
func (j *jenkinsCI) gitRepo(t testing.TB, name string, files map[string]string) string {
	t.Helper()
	var b strings.Builder
	fmt.Fprintf(&b, "set -e\nmkdir -p /srv/%[1]s\ncd /srv/%[1]s\ngit init -q -b main\n", name)
	for p, content := range files {
		fmt.Fprintf(&b, "mkdir -p \"$(dirname '%[1]s')\"\ncat > '%[1]s' <<'E2E_EOF'\n%[2]s\nE2E_EOF\n", p, strings.TrimRight(content, "\n"))
	}
	b.WriteString("git add -A\ngit -c user.name=E2E -c user.email=e2e@telesphorepo.test commit -q -m 'E2E'\ngit rev-parse HEAD\n")
	out := j.mustExec(t, "git repository "+name, b.String())
	return strings.TrimSpace(out[strings.LastIndex(strings.TrimSpace(out), "\n")+1:])
}

// pipelineJob creates the Pipeline job name that reads the Jenkinsfile of the main branch of the
// git repository /srv/<name>.
func (j *jenkinsCI) pipelineJob(t testing.TB, name string) {
	t.Helper()
	config := fmt.Sprintf(`<?xml version='1.1' encoding='UTF-8'?>
<flow-definition plugin="workflow-job">
  <definition class="org.jenkinsci.plugins.workflow.cps.CpsScmFlowDefinition" plugin="workflow-cps">
    <scm class="hudson.plugins.git.GitSCM" plugin="git">
      <configVersion>2</configVersion>
      <userRemoteConfigs><hudson.plugins.git.UserRemoteConfig><url>file:///srv/%[1]s</url></hudson.plugins.git.UserRemoteConfig></userRemoteConfigs>
      <branches><hudson.plugins.git.BranchSpec><name>*/main</name></hudson.plugins.git.BranchSpec></branches>
    </scm>
    <scriptPath>Jenkinsfile</scriptPath>
    <lightweight>false</lightweight>
  </definition>
</flow-definition>`, name)
	j.mustExec(t, "create the job "+name, fmt.Sprintf("%s create-job %s <<'E2E_EOF'\n%s\nE2E_EOF\n", j.cli(), name, config))
}

var startedRE = regexp.MustCompile(`Started \S+ #(\d+)`)

// run runs job name, waits for the end and returns whether the build succeeded, its build
// number and its console log.
func (j *jenkinsCI) run(t testing.TB, name string) (bool, string, string) {
	t.Helper()
	code, out := j.exec(t, j.cli()+" build "+name+" -s -v 2>&1")
	m := startedRE.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("Jenkins build of %s: exit %d, no build number in:\n%s", name, code, out)
	}
	return code == 0, m[1], out
}

// build is run for a build that must succeed.
func (j *jenkinsCI) build(t testing.TB, name string) (string, string) {
	t.Helper()
	ok, n, out := j.run(t, name)
	if !ok {
		t.Fatalf("Jenkins build %s #%s failed:\n%s", name, n, out)
	}
	return n, out
}

// helperJenkinsfile is a Pipeline that installs the CLI of server and asks its Docker credential
// helper, with the identity token of the build, for a registry host of another server.
func helperJenkinsfile(server string) string {
	return `pipeline {
  agent any
  stages {
    stage('Ask the helper') {
      steps {
        withCredentials([string(credentialsId: '` + ci.JenkinsCredential + `', variable: 'OIDC_TOKEN')]) {
          sh '''
            curl -fsSL ` + server + `/install.sh | sh
            export PATH="$HOME/.local/bin:$PATH"
            if echo other.example:5000 | TELESPHOREPO_SERVER=` + server + ` docker-credential-telesphorepo get; then exit 1; fi
          '''
        }
      }
    }
  }
}
`
}

// fileBuild is the build record of a file as GET /api/repos/{name}/file shows it.
type fileBuild struct {
	Provider   string `json:"provider"`
	Repository string `json:"repository"`
	Revision   string `json:"revision"`
	Run        string `json:"run"`
}

func buildOf(t testing.TB, s *Server, repo, p string) *fileBuild {
	t.Helper()
	var f struct {
		Build *fileBuild `json:"build"`
	}
	s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/file?path="+p, nil, s.AdminBasic(), &f)
	return f.Build
}

// ciFile is the CI file that the server gives for repo, system and tool (what telesphorepo setup
// writes).
func ciFile(t testing.TB, s *Server, repo, system, tool string) string {
	t.Helper()
	var f struct {
		Code string `json:"code"`
	}
	s.MustAPI(t, http.MethodGet, "/api/repos/"+repo+"/ci?system="+system+"&tool="+tool, nil, s.AdminBasic(), &f)
	if f.Code == "" {
		t.Fatalf("GET /api/repos/%s/ci?system=%s&tool=%s: no file", repo, system, tool)
	}
	return f.Code
}

// ciPOM is the Maven project of the CI tests (com.acme.ci:app:1.0.0). In the validate phase the
// antrun plugin prints OIDC_TOKEN as Maven sees it in its env: unset, Maven keeps the text
// ${env.OIDC_TOKEN}. The build tool of telesphorepo publish never gets the identity token.
const ciPOM = `<project xmlns="http://maven.apache.org/POM/4.0.0">
  <modelVersion>4.0.0</modelVersion>
  <groupId>com.acme.ci</groupId>
  <artifactId>app</artifactId>
  <version>1.0.0</version>
  <packaging>jar</packaging>
  <properties>
    <maven.compiler.release>21</maven.compiler.release>
    <project.build.sourceEncoding>UTF-8</project.build.sourceEncoding>
  </properties>
  <build>
    ` + mavenPluginVersions + `
    <plugins>
      <plugin>
        <groupId>org.apache.maven.plugins</groupId>
        <artifactId>maven-antrun-plugin</artifactId>
        <version>3.1.0</version>
        <executions>
          <execution>
            <id>e2e-env</id>
            <phase>validate</phase>
            <goals><goal>run</goal></goals>
            <configuration><target><echo message="e2e: OIDC_TOKEN=${env.OIDC_TOKEN}"/></target></configuration>
          </execution>
        </executions>
      </plugin>
    </plugins>
  </build>
</project>
`

// ciJar is the jar of ciPOM in a Maven repository.
const ciJar = "com/acme/ci/app/1.0.0/app-1.0.0.jar"

// oidcUnset is the line of ciPOM when Maven has no OIDC_TOKEN.
const oidcUnset = "e2e: OIDC_TOKEN=${env.OIDC_TOKEN}"

// TestCLIJenkins: a real Jenkins with the OpenID Connect Provider plugin runs, from git
// repositories, the Jenkinsfiles that the server writes. The Maven job builds with mvn, installs
// the CLI with the install script of the server and runs telesphorepo publish; Maven never sees
// OIDC_TOKEN. The Docker job builds an image on a Docker engine and pushes it through the
// credential helper of the CLI. The server verifies the tokens of the plugin with the keys that
// Jenkins serves (discovery), and the build records name the job, the build number and the commit
// of the checkout (GIT_COMMIT). A project file that names another server gets no identity token,
// and the helper gives none for a registry host of another server. No file of Jenkins holds a
// session token.
func TestCLIJenkins(t *testing.T) {
	t.Parallel()
	alias := RandName("repo")
	server := "http://" + alias + ":8080"
	d := StartDind(t, alias+":8080")
	j := startJenkins(t, server, map[string]string{"DOCKER_HOST": "tcp://" + d.Alias + ":2375"})
	const maven, docker = "releases", "images"
	OnLocalDisk(t, func(t *testing.T, opts ServerOpts) {
		opts.Alias = alias
		s := StartServer(t, opts)
		s.CreateRepo(t, maven, "maven", false, nil)
		s.CreateRepo(t, docker, "docker", false, nil)
		s.MustAPI(t, http.MethodPost, "/api/publish/trusts", map[string]any{
			"name": "jenkins", "issuer": j.URL + "oidc", "audience": "", "jwks": "",
			"claims": map[string][]string{"sub": {j.URL + "job/maven-app/", j.URL + "job/docker-app/"}},
			"repos":  []string{maven, docker},
		}, s.Admin(), nil)

		mavenFile, dockerFile := ciFile(t, s, maven, "jenkins", "maven"), ciFile(t, s, docker, "jenkins", "docker")
		mavenCommit := j.gitRepo(t, "maven-app", map[string]string{
			"Jenkinsfile": mavenFile, "pom.xml": ciPOM,
			"telesphorepo.toml": "server = \"" + server + "\"\nrepo = \"" + maven + "\"\n",
		})
		dockerCommit := j.gitRepo(t, "docker-app", map[string]string{
			"Jenkinsfile": dockerFile, "Dockerfile": "FROM scratch\nCOPY app.txt /app.txt\n", "app.txt": "built by Jenkins\n",
		})
		j.gitRepo(t, "other-app", map[string]string{
			"Jenkinsfile": mavenFile, "pom.xml": ciPOM,
			"telesphorepo.toml": "server = \"http://other.invalid:8080\"\nrepo = \"" + maven + "\"\n",
		})
		j.gitRepo(t, "helper-check", map[string]string{"Jenkinsfile": helperJenkinsfile(server)})
		for _, job := range []string{"maven-app", "docker-app", "other-app", "helper-check"} {
			j.pipelineJob(t, job)
		}

		n, out := j.build(t, "maven-app")
		wantText(t, "the Maven job", out, "BUILD SUCCESS", "Finished: SUCCESS")
		// The build stage and the deploy of telesphorepo publish each print the line.
		if c := strings.Count(out, oidcUnset); c < 2 {
			t.Fatalf("the Maven job: %d lines %q, want one per Maven run:\n%s", c, oidcUnset, out)
		}
		requireAssets(t, s, maven, "com/acme/ci/", ciJar)
		if b := buildOf(t, s, maven, ciJar); b == nil || b.Provider != "jenkins" || b.Repository != "maven-app" || b.Run != n || b.Revision != mavenCommit {
			t.Fatalf("build of %s: %+v, want Jenkins maven-app #%s at %s", ciJar, b, n, mavenCommit)
		}

		n, out = j.build(t, "docker-app")
		wantText(t, "the Docker job", out, "docker push", "Finished: SUCCESS")
		// The image is the one that the Jenkinsfile of the server pushes.
		m := regexp.MustCompile(`docker push "[^"]*/` + regexp.QuoteMeta(docker) + `/([^":]+):`).FindStringSubmatch(dockerFile)
		if m == nil {
			t.Fatalf("the Jenkinsfile of %s pushes no image of the repo:\n%s", docker, dockerFile)
		}
		tag := m[1] + "/_tags/" + dockerCommit
		if b := buildOf(t, s, docker, tag); b == nil || b.Provider != "jenkins" || b.Repository != "docker-app" || b.Run != n || b.Revision != dockerCommit {
			t.Fatalf("build of the tag %s: %+v, want Jenkins docker-app #%s at %s", tag, b, n, dockerCommit)
		}

		ok, _, out := j.run(t, "other-app")
		if ok {
			t.Fatalf("the job whose project file names another server succeeded:\n%s", out)
		}
		wantText(t, "the job whose project file names another server", out,
			"the identity token in OIDC_TOKEN is for "+server+", not for http://other.invalid:8080")

		_, out = j.build(t, "helper-check")
		wantText(t, "the helper for a host of another server", out, server, "credentials not found in native keychain")

		scanned(t, "Jenkins", j.mustExec(t, "secret scan", secretScan))
	})
}
