//go:build clients

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Zone clients: the Gradle plugin and the Maven extension are built from
// the clients repo (E2E_CLIENTS_DIR or a clone) and published to a test server, then real Gradle and Maven projects use them to
// enroll a device and publish, and to publish as GitHub Actions with a Keycloak-minted token.

const (
	clientsGradleImage = "gradle:9.8.1-jdk21"

	// Coordinates that the clients repo publishes. The Gradle build takes its version from -Pversion.
	clientsGroup         = "com.telesphorepo"
	gradleSettingsPlugin = "com.telesphorepo.publish"
	mavenExtensionID     = "telesphorepo-maven-extension"
	mavenPluginID        = "telesphorepo-maven-plugin"
	gradleClientVersion  = "0.0.0-e2e"
)

// approveCodeRe matches the line both clients print during enrollment:
// "Open <verification_url>?code=ABCD-EFGH and approve code ABCD-EFGH".
var approveCodeRe = regexp.MustCompile(`Open \S+.*approve code ([A-Z0-9]{4}-[A-Z0-9]{4})`)

// alreadyEnrolled is what telesphorepoLogin and the Maven login goal print when they keep the
// saved device.
const alreadyEnrolled = "already enrolled as device"

// noIDTokenEnv is a GitHub Actions job without "permissions: id-token: write": the runner sets
// no ACTIONS_ID_TOKEN_REQUEST_URL.
var noIDTokenEnv = map[string]string{"GITHUB_ACTIONS": "true", "CI": "true"}

func TestClients(t *testing.T) {
	s := StartServer(t, ServerOpts{Env: map[string]string{
		// The two bootstrap builds use sessions minted before they start; a cold cache can make
		// a build longer than the default 15m.
		"TR_PUBLISH_SESSION_TTL": "1h",
	}})
	pluginsRepo := RandName("clients")
	s.CreateRepo(t, pluginsRepo, "maven", true, nil)
	cl := buildClients(t, s, pluginsRepo)

	t.Run("gradle device", func(t *testing.T) {
		repo := RandName("dev-gradle")
		s.CreateRepo(t, repo, "maven", true, nil)
		testGradleDevice(t, s, cl, repo)
	})
	t.Run("maven device", func(t *testing.T) {
		repo := RandName("dev-maven")
		s.CreateRepo(t, repo, "maven", true, nil)
		testMavenDevice(t, s, cl, repo)
	})
	t.Run("github actions", func(t *testing.T) {
		repo := RandName("ci")
		s.CreateRepo(t, repo, "maven", true, nil)
		// Keycloak's ci-github client stands in for GitHub's token issuer. Its tokens carry the
		// server's base URL as audience, the default audience of a trust policy.
		kc := newKeycloak(t)
		kc.CIAudience = s.InternalURL
		kc.Start(t, s.URL+oidcCallbackPath)
		gh := startGitHubTokenServer(t)
		createCITrust(t, s, kc.Issuer, repo)
		t.Run("gradle", func(t *testing.T) { testGradleGitHub(t, s, cl, kc, gh, repo) })
		t.Run("maven", func(t *testing.T) { testMavenGitHub(t, s, cl, kc, gh, repo) })
	})
}

// ---------------------------------------------------------------- building the clients

// builtClients are the coordinates of the clients built from source and published to
// PluginsURL (an anonymous-read repo).
type builtClients struct {
	PluginsURL string
	ExtVersion string // version of the Maven extension and the Maven plugin
}

// publishInitScript adds the repository "e2e" to every project that publishes, so the clients'
// own build needs no change to publish to a test server.
const publishInitScript = `allprojects {
    plugins.withId('maven-publish') {
        publishing {
            repositories {
                maven {
                    name = 'e2e'
                    url = System.getenv('E2E_REPO_URL')
                    allowInsecureProtocol = true
                    credentials {
                        username = 'e2e'
                        password = System.getenv('E2E_PUBLISH_TOKEN')
                    }
                }
            }
        }
    }
}
`

// buildClients builds gradle/ and maven/ of the clients repo in parallel containers and publishes
// both to repo with publish sessions from the harness. Both builds compile the shared sources of
// core/, so each container gets core/ at /src/core next to its own folder.
func buildClients(t *testing.T, s *Server, repo string) *builtClients {
	t.Helper()
	repoURL := s.InternalURL + "/maven/" + repo
	clientsDir := clientsSourceDir(t)
	gradleSrc := hostDirFiles(t, filepath.Join(clientsDir, "gradle"), "/src/gradle")
	mavenSrc := hostDirFiles(t, filepath.Join(clientsDir, "maven"), "/src/maven")
	coreSrc := hostDirFiles(t, filepath.Join(clientsDir, "core"), "/src/core")
	// Mint both sessions before the builds start: each one needs a fresh second-factor step.
	gradleToken := PublishToken(t, s, s.AdminUser, repo)
	mavenToken := PublishToken(t, s, s.AdminUser, repo)

	t.Run("build", func(t *testing.T) {
		t.Run("gradle plugin", func(t *testing.T) {
			t.Parallel()
			code, out := RunClient(t, clientsGradleImage, ClientOpts{
				Cmd:   []string{"cd /src/gradle && gradle --no-daemon --console=plain --stacktrace -I /e2e/publish.init.gradle -Pversion=" + gradleClientVersion + " publishAllPublicationsToE2eRepository"},
				Files: append(append(gradleSrc, coreSrc...), ContainerFile{Path: "/e2e/publish.init.gradle", Content: publishInitScript}),
				Env: map[string]string{
					"GRADLE_USER_HOME":  "/home/gradle/.gradle",
					"E2E_REPO_URL":      repoURL,
					"E2E_PUBLISH_TOKEN": gradleToken,
				},
				Mounts:  map[string]string{GradleVolume: "/home/gradle/.gradle"},
				User:    "root",
				Timeout: 20 * time.Minute,
			})
			if code != 0 {
				t.Fatalf("build and publish gradle/: exit %d\n%s", code, out)
			}
		})
		t.Run("maven extension", func(t *testing.T) {
			t.Parallel()
			code, out := RunClient(t, mavenImage, ClientOpts{
				Cmd: []string{"cd /src/maven && mvn -B -ntp -s /e2e/settings.xml -DskipTests -DaltDeploymentRepository=telesphorepo::" + repoURL + " deploy"},
				Files: append(append(mavenSrc, coreSrc...), ContainerFile{
					Path: "/e2e/settings.xml", Content: mavenSettingsXML("e2e", mavenToken),
				}),
				Mounts:  map[string]string{M2Volume: "/root/.m2/repository"},
				Timeout: 20 * time.Minute,
			})
			if code != 0 {
				t.Fatalf("build and deploy maven/: exit %d\n%s", code, out)
			}
		})
	})
	if t.Failed() {
		t.FailNow()
	}

	// The Maven clients carry the version of maven/pom.xml; read it from what arrived.
	groupPath := strings.ReplaceAll(clientsGroup, ".", "/")
	paths := clientsAssetPaths(t, s, repo, groupPath+"/")
	extRe := regexp.MustCompile(`^` + regexp.QuoteMeta(groupPath+"/"+mavenExtensionID) + `/([^/]+)/[^/]+\.pom$`)
	cl := &builtClients{PluginsURL: repoURL}
	for _, p := range paths {
		if m := extRe.FindStringSubmatch(p); m != nil {
			cl.ExtVersion = m[1]
		}
	}
	if cl.ExtVersion == "" {
		t.Fatalf("no %s pom published; assets under %s:\n%s", mavenExtensionID, groupPath, strings.Join(paths, "\n"))
	}
	// The plugin marker has the plugin id as groupId and "<id>.gradle.plugin" as artifactId.
	marker := gradleSettingsPlugin + ".gradle.plugin"
	requireAssets(t, s, repo, groupPath+"/",
		strings.ReplaceAll(gradleSettingsPlugin, ".", "/")+"/"+marker+"/"+gradleClientVersion+"/"+marker+"-"+gradleClientVersion+".pom",
		groupPath+"/"+mavenPluginID+"/"+cl.ExtVersion+"/"+mavenPluginID+"-"+cl.ExtVersion+".jar")
	t.Logf("clients: gradle plugin %s, maven extension and plugin %s", gradleClientVersion, cl.ExtVersion)
	return cl
}

// clientsRepoURL is the repository that holds the client sources.
const clientsRepoURL = "https://github.com/Telesphorepo/clients"

// clientsSourceDir returns the directory with the client sources (core/, gradle/ and maven/). It is
// E2E_CLIENTS_DIR when set. Otherwise it shallow-clones clientsRepoURL at E2E_CLIENTS_REF
// (default main) into a temp dir, which the test removes at the end.
func clientsSourceDir(t *testing.T) string {
	t.Helper()
	if dir := os.Getenv("E2E_CLIENTS_DIR"); dir != "" {
		return dir
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("the clients zone needs git to clone %s (or set E2E_CLIENTS_DIR to a local checkout): %v", clientsRepoURL, err)
	}
	ref := os.Getenv("E2E_CLIENTS_REF")
	if ref == "" {
		ref = "main"
	}
	dir, err := os.MkdirTemp("", "telesphorepo-clients-")
	if err != nil {
		t.Fatalf("make temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	out, err := exec.Command("git", "clone", "--depth", "1", "--branch", ref, clientsRepoURL, dir).CombinedOutput()
	if err != nil {
		t.Fatalf("git clone %s (ref %s): %v\n%s", clientsRepoURL, ref, err, out)
	}
	return dir
}

// hostDirFiles returns every file under the host directory dir as container files under
// target, leaving out build output and IDE state. gradlew and mvnw stay executable.
func hostDirFiles(t testing.TB, dir, target string) []ContainerFile {
	t.Helper()
	skip := map[string]bool{"build": true, "target": true, ".gradle": true, ".idea": true, ".kotlin": true, "out": true}
	var files []ContainerFile
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != dir && skip[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		f := ContainerFile{Path: path.Join(target, filepath.ToSlash(rel)), Content: string(b)}
		if d.Name() == "gradlew" || d.Name() == "mvnw" {
			f.Mode = 0o755
		}
		files = append(files, f)
		return nil
	})
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	if len(files) == 0 {
		t.Fatalf("%s has no files", dir)
	}
	return files
}

// clientsAssetPaths lists the paths of every asset under prefix in repo (keyset pages).
func clientsAssetPaths(t testing.TB, s *Server, repo, prefix string) []string {
	t.Helper()
	var paths []string
	after := ""
	for {
		var page struct {
			Items []struct {
				Path string `json:"path"`
			} `json:"items"`
			Next *string `json:"next"`
		}
		q := "/api/repos/" + repo + "/assets?limit=200&prefix=" + url.QueryEscape(prefix)
		if after != "" {
			q += "&after=" + url.QueryEscape(after)
		}
		s.MustAPI(t, http.MethodGet, q, nil, s.Admin(), &page)
		for _, it := range page.Items {
			paths = append(paths, strings.TrimPrefix(it.Path, "/"))
		}
		if page.Next == nil || *page.Next == "" {
			return paths
		}
		after = *page.Next
	}
}

// requireAssets fails unless every path in want is an asset of repo.
func requireAssets(t testing.TB, s *Server, repo, prefix string, want ...string) {
	t.Helper()
	have := clientsAssetPaths(t, s, repo, prefix)
	set := map[string]bool{}
	for _, p := range have {
		set[p] = true
	}
	for _, w := range want {
		if !set[w] {
			t.Fatalf("asset %s not published to %s; have:\n%s", w, repo, strings.Join(have, "\n"))
		}
	}
}

// ---------------------------------------------------------------- Gradle

// gradlePub describes the Gradle build that applies the settings plugin and publishes a
// library (subproject lib) and a Gradle plugin (subproject greet) to Repo at RepoURL.
type gradlePub struct {
	Cl          *builtClients
	ServerURL   string
	Repo        string
	RepoURL     string
	Group       string
	LibMarker   string
	GreetMarker string
}

// files returns the build at /work/pub. The build files have no publishing block: with
// telesphorepo.repository, the settings plugin adds maven-publish, the repository with its
// credentials and the publication of lib; java-gradle-plugin makes the publications of greet.
func (g gradlePub) files() []ContainerFile {
	groupPath := strings.ReplaceAll(g.Group, ".", "/")
	settings := fmt.Sprintf(`pluginManagement {
    repositories {
        maven {
            url = '%s'
            allowInsecureProtocol = true
        }
        gradlePluginPortal()
    }
}
plugins {
    id '%s' version '%s'
}
telesphorepo {
    url = '%s'
    repository = '%s'
}
rootProject.name = 'pub'
include 'lib', 'greet'
`, g.Cl.PluginsURL, gradleSettingsPlugin, gradleClientVersion, g.ServerURL, g.Repo)
	lib := fmt.Sprintf(`plugins {
    id 'java-library'
}
group = '%s'
version = '1.0'
`, g.Group)
	greet := fmt.Sprintf(`plugins {
    id 'java-gradle-plugin'
}
group = '%[1]s'
version = '1.0'
gradlePlugin {
    plugins {
        greet {
            id = '%[1]s.greet'
            implementationClass = '%[1]s.GreetPlugin'
        }
    }
}
`, g.Group)
	libSrc := fmt.Sprintf(`package %s;

public class Lib {
    public static String message() {
        return %q;
    }
}
`, g.Group, g.LibMarker)
	greetSrc := fmt.Sprintf(`package %s;

import org.gradle.api.Plugin;
import org.gradle.api.Project;

public class GreetPlugin implements Plugin<Project> {
    @Override
    public void apply(Project project) {
        project.getTasks().register("hello", task -> task.doLast(t -> System.out.println(%q)));
    }
}
`, g.Group, g.GreetMarker)
	return []ContainerFile{
		{Path: "/work/pub/settings.gradle", Content: settings},
		{Path: "/work/pub/lib/build.gradle", Content: lib},
		{Path: "/work/pub/lib/src/main/java/" + groupPath + "/Lib.java", Content: libSrc},
		{Path: "/work/pub/greet/build.gradle", Content: greet},
		{Path: "/work/pub/greet/src/main/java/" + groupPath + "/GreetPlugin.java", Content: greetSrc},
	}
}

// useFiles returns a plain build at /work/use that applies the published plugin and loads the
// published library; it does not apply the settings plugin.
func (g gradlePub) useFiles() []ContainerFile {
	settings := fmt.Sprintf(`pluginManagement {
    repositories {
        maven {
            url = '%s'
            allowInsecureProtocol = true
        }
        gradlePluginPortal()
    }
}
rootProject.name = 'use'
`, g.RepoURL)
	build := fmt.Sprintf(`plugins {
    id '%[1]s.greet' version '1.0'
}
repositories {
    maven {
        url = '%[2]s'
        allowInsecureProtocol = true
    }
}
def e2e = configurations.create('e2e')
dependencies {
    add('e2e', '%[1]s:lib:1.0')
}
tasks.register('libMessage') {
    def files = e2e
    doLast {
        def loader = new URLClassLoader(files.collect { it.toURI().toURL() } as URL[])
        println loader.loadClass('%[1]s.Lib').getMethod('message').invoke(null)
    }
}
`, g.Group, g.RepoURL)
	return []ContainerFile{
		{Path: "/work/use/settings.gradle", Content: settings},
		{Path: "/work/use/build.gradle", Content: build},
	}
}

// wantAssets are the paths a successful publish of the build leaves in the repo. The plugin
// marker has the plugin id as groupId and "<id>.gradle.plugin" as artifactId.
func (g gradlePub) wantAssets() (prefix string, paths []string) {
	gp := strings.ReplaceAll(g.Group, ".", "/")
	id := g.Group + ".greet"
	marker := id + ".gradle.plugin"
	return gp + "/", []string{
		gp + "/lib/1.0/lib-1.0.jar",
		gp + "/greet/1.0/greet-1.0.jar",
		strings.ReplaceAll(id, ".", "/") + "/" + marker + "/1.0/" + marker + "-1.0.pom",
	}
}

func newGradlePub(s *Server, cl *builtClients, repo, label string) gradlePub {
	return gradlePub{
		Cl:          cl,
		ServerURL:   s.InternalURL,
		Repo:        repo,
		RepoURL:     s.InternalURL + "/maven/" + repo,
		Group:       "com.example." + label + randHex(4),
		LibMarker:   "LIB-MARKER-" + randHex(6),
		GreetMarker: "GREET-MARKER-" + randHex(6),
	}
}

// gradleOpts runs cmd in the Gradle image as root with the shared Gradle cache and, when
// deviceVol is set, the device key directory ~/.telesphorepo on that volume.
func gradleOpts(cmd string, files []ContainerFile, deviceVol string, env map[string]string) ClientOpts {
	allEnv := map[string]string{"GRADLE_USER_HOME": "/home/gradle/.gradle"}
	for k, v := range env {
		allEnv[k] = v
	}
	mounts := map[string]string{GradleVolume: "/home/gradle/.gradle"}
	if deviceVol != "" {
		mounts[deviceVol] = "/root/.telesphorepo"
	}
	return ClientOpts{
		Cmd:     []string{cmd},
		Files:   files,
		Env:     allEnv,
		Mounts:  mounts,
		User:    "root",
		Timeout: 15 * time.Minute,
	}
}

const gradleCmd = "gradle --no-daemon --console=plain --stacktrace "

// resolveGradle checks the published plugin and library from a clean build.
func resolveGradle(t *testing.T, g gradlePub) {
	t.Helper()
	code, out := RunClient(t, clientsGradleImage, gradleOpts("cd /work/use && "+gradleCmd+"hello libMessage", g.useFiles(), "", nil))
	if code != 0 {
		t.Fatalf("resolve published plugin and library: exit %d\n%s", code, out)
	}
	for _, want := range []string{g.GreetMarker, g.LibMarker} {
		if !strings.Contains(out, want) {
			t.Fatalf("resolver output missing %q:\n%s", want, out)
		}
	}
}

func testGradleDevice(t *testing.T, s *Server, cl *builtClients, repo string) {
	g := newGradlePub(s, cl, repo, "gdev")
	deviceVol := NewDataVolume(t)
	files := g.files()

	// Without a device and without a CI token, the publish enrolls this computer inline: it
	// prints the approval link, waits for the approval and then publishes.
	pub := startClient(t, clientsGradleImage, gradleOpts("cd /work/pub && "+gradleCmd+"publish", files, deviceVol, nil))
	userCode := pub.waitOutput(t, approveCodeRe, 15*time.Minute)[1]
	approveDevice(t, s, s.AdminUser, userCode)
	if code, out := pub.wait(t, 10*time.Minute); code != 0 {
		t.Fatalf("publish with inline enrollment: exit %d after approval\n%s", code, out)
	}
	prefix, want := g.wantAssets()
	requireAssets(t, s, repo, prefix, want...)
	resolveGradle(t, g)

	// telesphorepoLogin keeps the device that the server accepts.
	code, out := RunClient(t, clientsGradleImage, gradleOpts("cd /work/pub && "+gradleCmd+"telesphorepoLogin", files, deviceVol, nil))
	if code != 0 || !strings.Contains(out, alreadyEnrolled) {
		t.Fatalf("telesphorepoLogin with an enrolled device: exit %d, want 0 and %q\n%s", code, alreadyEnrolled, out)
	}
}

func testGradleGitHub(t *testing.T, s *Server, cl *builtClients, kc *Keycloak, gh *gitHubTokenServer, repo string) {
	g := newGradlePub(s, cl, repo, "gci")

	// A job without "id-token: write" fails at once and names the missing permission.
	code, out := RunClient(t, clientsGradleImage, gradleOpts("cd /work/pub && "+gradleCmd+"publish", g.files(), "", noIDTokenEnv))
	if code == 0 || !strings.Contains(out, "id-token: write") {
		t.Fatalf("publish as GitHub Actions without an ID token: exit %d, want a failure that names id-token: write\n%s", code, out)
	}

	gh.Serve(t, kc.CIToken(t))
	before := gh.Requests(t)
	code, out = RunClient(t, clientsGradleImage, gradleOpts("cd /work/pub && "+gradleCmd+"publish", g.files(), "", gh.Env()))
	if code != 0 {
		t.Fatalf("publish as GitHub Actions: exit %d\n%s", code, out)
	}
	if gh.Requests(t) <= before {
		t.Fatal("publish as GitHub Actions never fetched the ID token from ACTIONS_ID_TOKEN_REQUEST_URL")
	}
	prefix, want := g.wantAssets()
	requireAssets(t, s, repo, prefix, want...)
	resolveGradle(t, g)
}

// ---------------------------------------------------------------- Maven

// mavenPub is a jar project at /work/lib (mavenLibProject) that loads the extension through
// .mvn/extensions.xml, names the server in .mvn/maven.config and deploys to RepoURL.
type mavenPub struct {
	Cl        *builtClients
	ServerURL string
	RepoURL   string
	Group     string
	Message   string
	// SettingsServer, when set, is the id of a <server> entry with a username and a password in
	// settings.xml. The extension must never send these credentials to a repository of the server.
	SettingsServer string
}

func newMavenPub(s *Server, cl *builtClients, repo, label string) mavenPub {
	group := "com.example." + label + randHex(4)
	return mavenPub{Cl: cl, ServerURL: s.InternalURL, RepoURL: s.InternalURL + "/maven/" + repo, Group: group, Message: "hello from " + group}
}

// settings has no plugin group, and no server entry unless SettingsServer is set. An active
// profile adds the clients repo, so Maven resolves the core extension and the plugin from it.
func (m mavenPub) settings() ContainerFile {
	servers := ""
	if m.SettingsServer != "" {
		servers = fmt.Sprintf(`
  <servers>
    <server>
      <id>%s</id>
      <username>e2e</username>
      <password>e2e-settings-password</password>
    </server>
  </servers>`, m.SettingsServer)
	}
	return ContainerFile{Path: "/work/settings.xml", Content: fmt.Sprintf(`<settings>%[2]s
  <mirrors>
    <mirror>
      <id>maven-default-http-blocker</id>
      <mirrorOf>external:dummy:*</mirrorOf>
      <url>http://0.0.0.0/</url>
      <blocked>false</blocked>
    </mirror>
  </mirrors>
  <profiles>
    <profile>
      <id>telesphorepo-clients</id>
      <repositories>
        <repository>
          <id>telesphorepo-clients</id>
          <url>%[1]s</url>
          <releases><enabled>true</enabled></releases>
          <snapshots><enabled>true</enabled></snapshots>
        </repository>
      </repositories>
      <pluginRepositories>
        <pluginRepository>
          <id>telesphorepo-clients</id>
          <url>%[1]s</url>
          <releases><enabled>true</enabled></releases>
          <snapshots><enabled>true</enabled></snapshots>
        </pluginRepository>
      </pluginRepositories>
    </profile>
  </profiles>
  <activeProfiles>
    <activeProfile>telesphorepo-clients</activeProfile>
  </activeProfiles>
</settings>
`, m.Cl.PluginsURL, servers)}
}

func (m mavenPub) files() []ContainerFile {
	ext := ContainerFile{Path: "/work/lib/.mvn/extensions.xml", Content: fmt.Sprintf(`<extensions xmlns="http://maven.apache.org/EXTENSIONS/1.1.0">
  <extension>
    <groupId>%s</groupId>
    <artifactId>%s</artifactId>
    <version>%s</version>
  </extension>
</extensions>
`, clientsGroup, mavenExtensionID, m.Cl.ExtVersion)}
	config := ContainerFile{Path: "/work/lib/.mvn/maven.config", Content: "-Dtelesphorepo.url=" + m.ServerURL + "\n"}
	return append(mavenLibProject(m.Group, "1.0.0", m.RepoURL, m.Message), ext, config, m.settings())
}

// mavenOpts runs script in the Maven image. Each script first drops the clients from the shared
// local repository, so they always come from this run's server.
func mavenOpts(script string, files []ContainerFile, deviceVol string, env map[string]string) ClientOpts {
	mounts := map[string]string{M2Volume: "/root/.m2/repository"}
	if deviceVol != "" {
		mounts[deviceVol] = "/root/.telesphorepo"
	}
	return ClientOpts{
		Cmd:     []string{"rm -rf /root/.m2/repository/" + strings.ReplaceAll(clientsGroup, ".", "/") + " && " + script},
		Files:   files,
		Env:     env,
		Mounts:  mounts,
		Timeout: 15 * time.Minute,
	}
}

const mvnCmd = "mvn -B -ntp -s /work/settings.xml "

// deployAndResolve deploys the project, checks the jar on the server and runs it from a clean
// consumer.
func (m mavenPub) deployAndResolve(t *testing.T, s *Server, repo, deviceVol string, env map[string]string) {
	t.Helper()
	code, out := RunClient(t, mavenImage, mavenOpts("cd /work/lib && "+mvnCmd+"deploy", m.files(), deviceVol, env))
	if code != 0 {
		t.Fatalf("mvn deploy: exit %d\n%s", code, out)
	}
	m.resolve(t, s, repo)
}

// resolve checks the deployed jar on the server and runs it from a clean consumer.
func (m mavenPub) resolve(t *testing.T, s *Server, repo string) {
	t.Helper()
	gp := strings.ReplaceAll(m.Group, ".", "/")
	requireAssets(t, s, repo, gp+"/", gp+"/hello/1.0.0/hello-1.0.0.jar", gp+"/hello/1.0.0/hello-1.0.0.pom")

	script := strings.Join([]string{
		"set -e",
		"rm -rf /root/.m2/repository/" + gp,
		"cd /work/consumer",
		mvnCmd + "-q -Ddep.version=1.0.0 -Dmdep.outputFile=/tmp/cp.txt org.apache.maven.plugins:maven-dependency-plugin:3.8.1:build-classpath",
		`java -cp "$(cat /tmp/cp.txt)" ` + m.Group + ".Hello",
	}, "\n")
	code, out := RunClient(t, mavenImage, mavenOpts(script, []ContainerFile{m.settings(), mavenConsumerProject(m.Group, m.RepoURL)}, "", nil))
	if code != 0 {
		t.Fatalf("resolve deployed jar: exit %d\n%s", code, out)
	}
	if !strings.Contains(out, m.Message) {
		t.Fatalf("consumer output missing %q:\n%s", m.Message, out)
	}
}

func testMavenDevice(t *testing.T, s *Server, cl *builtClients, repo string) {
	m := newMavenPub(s, cl, repo, "mdev")
	deviceVol := NewDataVolume(t)

	// Without a device and without a CI token, the deploy enrolls this computer inline: it
	// prints the approval link, waits for the approval and then deploys.
	dep := startClient(t, mavenImage, mavenOpts("cd /work/lib && "+mvnCmd+"deploy", m.files(), deviceVol, nil))
	userCode := dep.waitOutput(t, approveCodeRe, 15*time.Minute)[1]
	approveDevice(t, s, s.AdminUser, userCode)
	if code, out := dep.wait(t, 10*time.Minute); code != 0 {
		t.Fatalf("mvn deploy with inline enrollment: exit %d after approval\n%s", code, out)
	}
	m.resolve(t, s, repo)

	// The login goal, with its full coordinates and no plugin group, keeps the device.
	goal := clientsGroup + ":" + mavenPluginID + ":" + m.Cl.ExtVersion + ":login"
	code, out := RunClient(t, mavenImage, mavenOpts("cd /work/lib && "+mvnCmd+goal, m.files(), deviceVol, nil))
	if code != 0 || !strings.Contains(out, alreadyEnrolled) {
		t.Fatalf("%s with an enrolled device: exit %d, want 0 and %q\n%s", goal, code, alreadyEnrolled, out)
	}

	// A failed session mint stops the deploy: a deploy to a repository that does not exist says
	// so, does not blame the device, and uploads nothing. settings.xml has credentials for that
	// repository id; the extension never falls back to them.
	missing := RandName("missing")
	alt := "-DaltDeploymentRepository=" + missing + "::" + m.ServerURL + "/maven/" + missing + " "
	withServer := m
	withServer.SettingsServer = missing
	code, out = RunClient(t, mavenImage, mavenOpts("cd /work/lib && "+mvnCmd+alt+"deploy", withServer.files(), deviceVol, nil))
	if code == 0 || !strings.Contains(out, "cannot start a publish session for repository "+missing) ||
		!strings.Contains(out, "does not exist") || strings.Contains(out, "refused device") {
		t.Fatalf("deploy to the missing repository %s: exit %d, want a failed session mint that says it does not exist\n%s", missing, code, out)
	}
	if strings.Contains(out, "Uploading to "+missing) {
		t.Fatalf("deploy to the missing repository %s uploaded after the failed session mint:\n%s", missing, out)
	}

	// A repository URL with another scheme than telesphorepo.url belongs to another server: the
	// extension sends no credential, although a device is enrolled, and the server refuses the
	// anonymous upload.
	other := m
	other.ServerURL = "https" + strings.TrimPrefix(m.ServerURL, "http")
	code, out = RunClient(t, mavenImage, mavenOpts("cd /work/lib && "+mvnCmd+"deploy", other.files(), deviceVol, nil))
	if code == 0 || !strings.Contains(out, "401") || strings.Contains(out, "Telesphorepo:") || approveCodeRe.MatchString(out) {
		t.Fatalf("deploy with telesphorepo.url %s to %s: exit %d, want an anonymous upload that gets 401 and no Telesphorepo output\n%s", other.ServerURL, m.RepoURL, code, out)
	}
}

func testMavenGitHub(t *testing.T, s *Server, cl *builtClients, kc *Keycloak, gh *gitHubTokenServer, repo string) {
	m := newMavenPub(s, cl, repo, "mci")

	// A job without "id-token: write" stops before the build and names the missing permission.
	code, out := RunClient(t, mavenImage, mavenOpts("cd /work/lib && "+mvnCmd+"deploy", m.files(), "", noIDTokenEnv))
	if code == 0 || !strings.Contains(out, "id-token: write") {
		t.Fatalf("deploy as GitHub Actions without an ID token: exit %d, want a failure that names id-token: write\n%s", code, out)
	}

	gh.Serve(t, kc.CIToken(t))
	before := gh.Requests(t)
	m.deployAndResolve(t, s, repo, "", gh.Env())
	if gh.Requests(t) <= before {
		t.Fatal("deploy as GitHub Actions never fetched the ID token from ACTIONS_ID_TOKEN_REQUEST_URL")
	}
}

// ---------------------------------------------------------------- long-running clients

// clientRun is a client container that keeps running while the test reads its output.
type clientRun struct {
	c     *testcontainers.DockerContainer
	image string
}

// startClient starts image like RunClient but returns at once; opts.Timeout is not used.
func startClient(t testing.TB, image string, opts ClientOpts) *clientRun {
	t.Helper()
	files := make([]testcontainers.ContainerFile, len(opts.Files))
	for i, f := range opts.Files {
		files[i] = f.tc()
	}
	var mounts []testcontainers.ContainerMount
	for vol, target := range opts.Mounts {
		mounts = append(mounts, testcontainers.VolumeMount(vol, testcontainers.ContainerMountTarget(target)))
	}
	c, err := testcontainers.Run(context.Background(), image,
		testcontainers.WithEntrypoint("sh", "-c"),
		testcontainers.WithCmd(strings.Join(opts.Cmd, " ")),
		testcontainers.WithEnv(opts.Env),
		testcontainers.WithFiles(files...),
		testcontainers.WithMounts(mounts...),
		network.WithNetwork(nil, TestNetwork),
		testcontainers.WithConfigModifier(func(c *container.Config) {
			if opts.WorkingDir != "" {
				c.WorkingDir = opts.WorkingDir
			}
			if opts.User != "" {
				c.User = opts.User
			}
		}),
	)
	registerCleanup(t, c, "client "+image)
	if err != nil {
		t.Fatalf("start client %s: %v", image, err)
	}
	return &clientRun{c: c, image: image}
}

// waitOutput polls the output until re matches and returns the submatches. It fails when the
// container exits first or timeout passes.
func (r *clientRun) waitOutput(t testing.TB, re *regexp.Regexp, timeout time.Duration) []string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		out := containerLogs(r.c)
		if m := re.FindStringSubmatch(out); m != nil {
			return m
		}
		st, err := r.c.State(context.Background())
		if err == nil && !st.Running {
			out = containerLogs(r.c)
			if m := re.FindStringSubmatch(out); m != nil {
				return m
			}
			t.Fatalf("client %s exited %d before printing %q:\n%s", r.image, st.ExitCode, re, out)
		}
		if time.Now().After(deadline) {
			t.Fatalf("client %s printed no %q within %s:\n%s", r.image, re, timeout, out)
		}
		time.Sleep(time.Second)
	}
}

// wait waits for the container to exit and returns its exit code and output.
func (r *clientRun) wait(t testing.TB, timeout time.Duration) (int, string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		st, err := r.c.State(context.Background())
		if err != nil {
			t.Fatalf("client %s state: %v", r.image, err)
		}
		if !st.Running {
			return st.ExitCode, containerLogs(r.c)
		}
		if time.Now().After(deadline) {
			t.Fatalf("client %s still running after %s:\n%s", r.image, timeout, containerLogs(r.c))
		}
		time.Sleep(time.Second)
	}
}

// approveDevice approves a client's enrollment code the way a developer does in the browser:
// sign in as user with password and TOTP, then approve with a fresh TOTP code.
func approveDevice(t testing.TB, s *Server, user, userCode string) {
	t.Helper()
	sess := s.LoginTOTP(t, user)
	if st, b := s.ApproveEnrollment(t, sess, user, userCode); st/100 != 2 {
		t.Fatalf("approve code %s as %s: status %d: %s", userCode, user, st, b)
	}
}

// ---------------------------------------------------------------- CI identity (GitHub Actions)

// createCITrust adds an enabled trust policy for the Keycloak issuer on repo, shaped like a
// GitHub Actions release policy. The audience is left empty, so it is the server's base URL.
func createCITrust(t testing.TB, s *Server, issuer, repo string) {
	t.Helper()
	s.MustAPI(t, http.MethodPost, "/api/publish/trusts", map[string]any{
		"name":     RandName("github"),
		"issuer":   issuer,
		"audience": "",
		"claims": map[string][]string{
			"repository":   {kcCIRepository},
			"ref":          {"refs/tags/v*"},
			"workflow_ref": {"acme/app/.github/workflows/release.yml@*"},
		},
		"jwks":  "",
		"repos": []string{repo},
	}, s.Admin(), nil)
}

// gitHubTokenServer plays the GitHub Actions ID-token endpoint: a JDK jwebserver serving
// /srv/token.json as {"value": "<token>"}. The query string that clients send
// (api-version, audience) is ignored, as a static file server does.
type gitHubTokenServer struct {
	c   *testcontainers.DockerContainer
	url string
}

func startGitHubTokenServer(t testing.TB) *gitHubTokenServer {
	t.Helper()
	alias := RandName("gh-oidc")
	c, err := testcontainers.Run(context.Background(), mavenImage,
		testcontainers.WithEntrypoint("jwebserver"),
		testcontainers.WithCmd("-b", "0.0.0.0", "-p", "8000", "-d", "/srv"),
		testcontainers.WithExposedPorts("8000/tcp"),
		testcontainers.WithFiles(testcontainers.ContainerFile{Reader: strings.NewReader(`{"value":""}`), ContainerFilePath: "/srv/token.json", FileMode: 0o644}),
		network.WithNetwork([]string{alias}, TestNetwork),
		testcontainers.WithWaitStrategyAndDeadline(2*time.Minute, wait.ForHTTP("/token.json").WithPort("8000/tcp").WithStartupTimeout(2*time.Minute)),
	)
	registerCleanup(t, c, "github token server "+alias)
	if err != nil {
		t.Fatalf("start github token server: %v", err)
	}
	return &gitHubTokenServer{c: c, url: "http://" + alias + ":8000/token.json?api-version=2.0"}
}

// Serve replaces the token the endpoint returns.
func (g *gitHubTokenServer) Serve(t testing.TB, token string) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"count": 1, "value": token})
	if err := g.c.CopyToContainer(context.Background(), b, "/srv/token.json", 0o644); err != nil {
		t.Fatalf("serve github token: %v", err)
	}
}

// Requests counts the token requests the endpoint has logged so far.
func (g *gitHubTokenServer) Requests(t testing.TB) int {
	t.Helper()
	return strings.Count(containerLogs(g.c), "GET /token.json")
}

// Env is the environment of a GitHub Actions job with "permissions: id-token: write".
func (g *gitHubTokenServer) Env() map[string]string {
	return map[string]string{
		"GITHUB_ACTIONS":                 "true",
		"ACTIONS_ID_TOKEN_REQUEST_URL":   g.url,
		"ACTIONS_ID_TOKEN_REQUEST_TOKEN": "e2e-request-token",
	}
}
