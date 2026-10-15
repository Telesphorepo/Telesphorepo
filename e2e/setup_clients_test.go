//go:build clients

package e2e

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"
)

// The project setup scripts (GET /setup.sh, /setup.ps1) against fresh projects: setup.sh in the
// Gradle and Maven images, then a publish with the files it wrote; setup.ps1 in a PowerShell
// image, checked by its files.

const powershellImage = "mcr.microsoft.com/powershell:7.5-ubuntu-24.04"

// versionLineRe is the line of setup.sh that names the clients version.
var versionLineRe = regexp.MustCompile(`(?m)^VERSION='[^']*'$`)

// setupWithVersion returns setup.sh with the clients version of this run: the clients zone
// builds the clients at its own versions, not at clientsVersion.
func setupWithVersion(t *testing.T, script, version string) string {
	t.Helper()
	if !versionLineRe.MatchString(script) {
		t.Fatal("setup.sh has no VERSION line")
	}
	return versionLineRe.ReplaceAllString(script, "VERSION='"+version+"'")
}

// setupTwice runs setup.sh with args twice in dir and fails unless the second run changes no
// file. The files of the first run are then in the returned container.
func setupTwice(dir, args string) string {
	return strings.Join([]string{
		"set -e",
		"cd " + dir,
		"cat /e2e/setup.sh | sh -s -- " + args,
		"find . -type f -not -path './.gradle/*' | sort | xargs sha256sum > /tmp/first",
		"cat /e2e/setup.sh | sh -s -- " + args,
		"find . -type f -not -path './.gradle/*' | sort | xargs sha256sum > /tmp/second",
		"cmp /tmp/first /tmp/second",
		"echo SETUP-IDEMPOTENT",
	}, "\n")
}

func requireSetupOutput(t *testing.T, code int, out string) {
	t.Helper()
	if code != 0 || !strings.Contains(out, "SETUP-IDEMPOTENT") || !strings.Contains(out, "No file changed") {
		t.Fatalf("setup.sh twice: exit %d, want 0, an unchanged second run and \"No file changed\"\n%s", code, out)
	}
}

func requireContains(t *testing.T, name, body string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Fatalf("%s has no %q:\n%s", name, w, body)
		}
	}
}

// testSetupGradle sets up a Kotlin build that already has a pluginManagement block and a
// plugins block, with --ci github, then publishes from the files that setup.sh wrote.
func testSetupGradle(t *testing.T, s *Server, cl *builtClients, repo string) {
	group := "com.example.setup" + randHex(4)
	marker := "SETUP-MARKER-" + randHex(6)
	settings := fmt.Sprintf(`pluginManagement {
    repositories {
        maven {
            url = uri("%s")
            isAllowInsecureProtocol = true
        }
        gradlePluginPortal()
    }
}

plugins {
    id("org.gradle.toolchains.foojay-resolver-convention") version "1.0.0"
}

rootProject.name = "setup-kts"
`, cl.PluginsURL)
	project := []ContainerFile{
		{Path: "/work/p/build.gradle.kts", Content: "plugins {\n    `java-library`\n}\n\ngroup = \"" + group + "\"\nversion = \"1.0\"\n"},
		{Path: "/work/p/src/main/java/" + strings.ReplaceAll(group, ".", "/") + "/Lib.java",
			Content: "package " + group + ";\n\npublic class Lib {\n    public static String message() {\n        return \"" + marker + "\";\n    }\n}\n"},
	}
	script := setupWithVersion(t, fetchSetup(t, s, "setup.sh"), gradleClientVersion)
	files := append([]ContainerFile{{Path: "/work/p/settings.gradle.kts", Content: settings}, {Path: "/e2e/setup.sh", Content: script}}, project...)

	opts := gradleOpts(setupTwice("/work/p", repo+" --ci github --yes"), files, "", nil)
	r := RunClientResult(t, clientsGradleImage, opts)
	requireSetupOutput(t, r.ExitCode, r.Output)
	requireContains(t, "setup.sh output", r.Output, "Settings › CI pipelines", "allow tags v*", "click Approve")

	got := string(r.ReadFile(t, "/work/p/settings.gradle.kts"))
	// The id line goes first into the existing plugins block, after pluginManagement.
	want := regexp.MustCompile(`(?s)^pluginManagement \{.*?\n\}\n\nplugins \{\n    id\("com\.telesphorepo\.publish"\) version "` +
		regexp.QuoteMeta(gradleClientVersion) + `"\n    id\("org\.gradle\.toolchains\.foojay-resolver-convention"\) version "1\.0\.0"\n\}\n`)
	if !want.MatchString(got) {
		t.Fatalf("settings.gradle.kts after setup: the plugin is not in the plugins block after pluginManagement:\n%s", got)
	}
	requireContains(t, "settings.gradle.kts", got,
		"telesphorepo {\n    url = \""+s.InternalURL+"\"\n    repository = \""+repo+"\"\n}\n")
	if strings.Count(got, "plugins {") != 1 {
		t.Fatalf("settings.gradle.kts has more than one plugins block:\n%s", got)
	}
	workflow := string(r.ReadFile(t, "/work/p/.github/workflows/publish.yml"))
	requireContains(t, "publish.yml", workflow, "tags: [\"v*\"]", "id-token: write", "actions/checkout@v7", "actions/setup-java@v6", "run: gradle publish")

	// Publish with the settings file that setup.sh wrote: the first publish enrolls the computer.
	pubFiles := append([]ContainerFile{{Path: "/work/p/settings.gradle.kts", Content: got}}, project...)
	deviceVol := NewDataVolume(t)
	pub := startClient(t, clientsGradleImage, gradleOpts("cd /work/p && "+gradleCmd+"publish", pubFiles, deviceVol, nil))
	userCode := pub.waitOutput(t, approveCodeRe, 15*time.Minute)[1]
	approveDevice(t, s, s.AdminUser, userCode)
	if code, out := pub.wait(t, 10*time.Minute); code != 0 {
		t.Fatalf("publish after setup.sh: exit %d after approval\n%s", code, out)
	}
	gp := strings.ReplaceAll(group, ".", "/")
	requireAssets(t, s, repo, gp+"/", gp+"/setup-kts/1.0/setup-kts-1.0.jar", gp+"/setup-kts/1.0/setup-kts-1.0.pom")
}

// testSetupMaven sets up a fresh Maven project (no .mvn, no distributionManagement) with --ci none, then deploys
// with the files that setup.sh wrote.
func testSetupMaven(t *testing.T, s *Server, cl *builtClients, repo string) {
	m := newMavenPub(s, cl, repo, "msetup")
	lib := mavenLibProject(m.Group, "1.0.0", m.RepoURL, m.Message)
	pom := lib[0].Content
	start, end := strings.Index(pom, "  <distributionManagement>"), strings.Index(pom, "</distributionManagement>\n")
	if start < 0 || end < 0 {
		t.Fatal("mavenLibProject has no distributionManagement to remove")
	}
	lib[0].Content = pom[:start] + pom[end+len("</distributionManagement>\n"):]

	script := setupWithVersion(t, fetchSetup(t, s, "setup.sh"), cl.ExtVersion)
	files := append([]ContainerFile{{Path: "/e2e/setup.sh", Content: script}}, lib...)
	r := RunClientResult(t, mavenImage, ClientOpts{Cmd: []string{setupTwice("/work/lib", repo+" --ci none --yes")}, Files: files, Timeout: 5 * time.Minute})
	requireSetupOutput(t, r.ExitCode, r.Output)
	requireContains(t, "setup.sh output", r.Output, "Run mvn deploy and click Approve")

	gotPom := string(r.ReadFile(t, "/work/lib/pom.xml"))
	dm := "  <distributionManagement>\n    <repository>\n      <id>" + repo + "</id>\n      <url>" + m.RepoURL + "</url>\n    </repository>\n" +
		"    <snapshotRepository>\n      <id>" + repo + "</id>\n      <url>" + m.RepoURL + "</url>\n    </snapshotRepository>\n  </distributionManagement>\n</project>\n"
	requireContains(t, "pom.xml", gotPom, dm)
	ext := string(r.ReadFile(t, "/work/lib/.mvn/extensions.xml"))
	requireContains(t, ".mvn/extensions.xml", ext, "<artifactId>"+mavenExtensionID+"</artifactId>", "<version>"+cl.ExtVersion+"</version>")
	config := string(r.ReadFile(t, "/work/lib/.mvn/maven.config"))
	if config != "-Dtelesphorepo.url="+s.InternalURL+"\n" {
		t.Fatalf(".mvn/maven.config = %q", config)
	}

	// Deploy with the files that setup.sh wrote: the first deploy enrolls the computer.
	deployFiles := append([]ContainerFile{
		{Path: "/work/lib/pom.xml", Content: gotPom},
		{Path: "/work/lib/.mvn/extensions.xml", Content: ext},
		{Path: "/work/lib/.mvn/maven.config", Content: config},
		m.settings(),
	}, lib[1:]...)
	deviceVol := NewDataVolume(t)
	dep := startClient(t, mavenImage, mavenOpts("cd /work/lib && "+mvnCmd+"deploy", deployFiles, deviceVol, nil))
	userCode := dep.waitOutput(t, approveCodeRe, 15*time.Minute)[1]
	approveDevice(t, s, s.AdminUser, userCode)
	if code, out := dep.wait(t, 10*time.Minute); code != 0 {
		t.Fatalf("mvn deploy after setup.sh: exit %d after approval\n%s", code, out)
	}
	m.resolve(t, s, repo)
}

// TestSetupPowerShell runs setup.ps1, fetched with irm as the docs show it, in a Gradle Kotlin
// project with --Ci jenkins-pipeline and a Maven project with -Ci github, twice each, and checks
// the files. It does not publish.
func TestSetupPowerShell(t *testing.T) {
	s := StartServer(t, ServerOpts{})
	repo := RandName("ps")
	version := clientsVersionOf(t)
	run := func(ci string) string {
		return "pwsh -NoProfile -Command '& ([scriptblock]::Create((irm " + s.InternalURL + "/setup.ps1))) " + repo + " -Ci " + ci + " -Yes'"
	}
	twice := func(dir, ci string) string {
		return strings.Join([]string{
			"cd " + dir,
			run(ci),
			"find . -type f | sort | xargs sha256sum > /tmp/first",
			run(ci),
			"find . -type f | sort | xargs sha256sum > /tmp/second",
			"cmp /tmp/first /tmp/second",
		}, " && ")
	}
	files := []ContainerFile{
		{Path: "/work/g/settings.gradle.kts", Content: "rootProject.name = \"g\"\n"},
		{Path: "/work/g/gradlew", Content: "#!/bin/sh\n", Mode: 0o755},
		{Path: "/work/m/pom.xml", Content: "<project>\n    <modelVersion>4.0.0</modelVersion>\n    <groupId>com.example</groupId>\n    <artifactId>m</artifactId>\n    <version>1.0.0</version>\n</project>\n"},
	}
	r := RunClientResult(t, powershellImage, ClientOpts{
		Cmd:     []string{twice("/work/g", "jenkins-pipeline") + " && " + twice("/work/m", "github") + " && echo SETUP-IDEMPOTENT"},
		Files:   files,
		Timeout: 5 * time.Minute,
	})
	requireSetupOutput(t, r.ExitCode, r.Output)
	requireContains(t, "setup.ps1 output", r.Output, "Settings › CI pipelines", "telesphorepo-id-token")

	settings := string(r.ReadFile(t, "/work/g/settings.gradle.kts"))
	if want := "plugins {\n    id(\"com.telesphorepo.publish\") version \"" + version + "\"\n}\n\nrootProject.name = \"g\"\n\n" +
		"telesphorepo {\n    url = \"" + s.InternalURL + "\"\n    repository = \"" + repo + "\"\n    oidcTokenEnv = \"OIDC_TOKEN\"\n}\n"; settings != want {
		t.Fatalf("settings.gradle.kts after setup.ps1:\n%s\nwant:\n%s", settings, want)
	}
	jenkinsfile := string(r.ReadFile(t, "/work/g/Jenkinsfile"))
	requireContains(t, "Jenkinsfile", jenkinsfile, "stage('Publish to "+repo+"')", "sh './gradlew build'", "sh './gradlew publish'",
		"credentialsId: 'telesphorepo-id-token', variable: 'OIDC_TOKEN'")

	pom := string(r.ReadFile(t, "/work/m/pom.xml"))
	requireContains(t, "pom.xml", pom, "    <distributionManagement>\n        <repository>\n            <id>"+repo+"</id>\n            <url>"+s.InternalURL+"/maven/"+repo+"</url>")
	requireContains(t, ".mvn/extensions.xml", string(r.ReadFile(t, "/work/m/.mvn/extensions.xml")), "<version>"+version+"</version>")
	requireContains(t, ".mvn/maven.config", string(r.ReadFile(t, "/work/m/.mvn/maven.config")), "-Dtelesphorepo.url="+s.InternalURL+"\n")
	requireContains(t, "publish.yml", string(r.ReadFile(t, "/work/m/.github/workflows/publish.yml")), "tags: [\"v*\"]", "run: mvn -B deploy")
}
