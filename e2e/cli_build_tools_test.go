//go:build clients

package e2e

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// newestMaven4 returns the newest Maven 4 version (a release candidate before 4.0.0) in the
// metadata of org.apache.maven:apache-maven on Maven Central.
func newestMaven4(t testing.TB) string {
	t.Helper()
	resp, err := http.Get("https://repo.maven.apache.org/maven2/org/apache/maven/apache-maven/maven-metadata.xml")
	if err != nil {
		t.Fatalf("the Maven versions: %v", err)
	}
	defer resp.Body.Close()
	var m struct {
		Versions []string `xml:"versioning>versions>version"`
	}
	if err := xml.NewDecoder(resp.Body).Decode(&m); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("the Maven versions: status %d: %v", resp.StatusCode, err)
	}
	// The file lists the versions from the oldest to the newest.
	for i := len(m.Versions) - 1; i >= 0; i-- {
		if v := m.Versions[i]; strings.HasPrefix(v, "4.") && !strings.Contains(v, "alpha") && !strings.Contains(v, "beta") {
			return v
		}
	}
	t.Fatal("Maven Central lists no Maven 4")
	return ""
}

// mavenUserSettings is the ~/.m2/settings.xml of a person whose company mirrors Maven Central:
// every request for Central goes to the mirror.
func mavenUserSettings(mirrorURL string) ContainerFile {
	return ContainerFile{Path: "/root/.m2/settings.xml", Content: fmt.Sprintf(`<settings>
  <mirrors>
    <mirror>
      <id>e2e-company-mirror</id>
      <mirrorOf>central</mirrorOf>
      <url>%s</url>
    </mirror>
  </mirrors>
</settings>
`, mirrorURL)}
}

// TestCLIMaven: setup and publish run mvn deploy for a project whose pom names another deployment
// target, with the newest Maven 3 and the newest Maven 4; the mirror of the person's settings.xml stays in force
// (Central comes through a proxy repo of the server); mvn resolves the published jars.
func TestCLIMaven(t *testing.T) {
	c := newCLI(t)
	ForEachStorage(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		repo, central := RandName("releases"), RandName("central")
		s.CreateRepo(t, repo, "maven", false, nil)
		s.MustAPI(t, http.MethodPost, "/api/repos", map[string]any{"name": central, "format": "maven", "anonymous_read": true,
			"kind": "proxy", "proxy": map[string]any{"remote_url": "https://repo.maven.apache.org/maven2/"}, "force": true}, s.Admin(), nil)
		dev := RandName("dev")
		s.CreateUser(t, dev, "dev-pass-1234")
		s.SetPerms(t, repo, dev, "repo.download", "repo.publish")
		devices := NewDataVolume(t)
		group := "com.example.cli" + randHex(4)
		gp := strings.ReplaceAll(group, ".", "/")
		settings := mavenUserSettings(s.InternalURL + "/maven/" + central)
		// The pom deploys elsewhere: publish needs no change of it.
		project := func(version string) []ContainerFile {
			return append(mavenLibProject(group, version, "http://nowhere.invalid/maven", "hello from "+group+" "+version), settings)
		}
		const publish = `set -e
cd /work/lib
telesphorepo setup "$E2E_LINK"
telesphorepo publish
`

		t.Run("maven 3", func(t *testing.T) {
			// A fresh local repository, so every plugin comes through the mirror.
			out := c.mustRun(t, mavenImage, "publish with Maven 3", publish, ClientOpts{
				Files:  project("1.0.0"),
				Env:    map[string]string{"E2E_LINK": s.NewSetupCode(t, dev, repo).Link, "MAVEN_ARGS": "-B -ntp -Dmaven.repo.local=/tmp/m2"},
				Mounts: map[string]string{devices: "/root/.telesphorepo"},
			})
			wantText(t, "publish with Maven 3", out, "Publishing to "+repo+" (maven)", " deploy -gs ", "-DaltDeploymentRepository=telesphorepo::"+s.InternalURL+"/maven/"+repo)
			requireAssets(t, s, repo, gp+"/", gp+"/hello/1.0.0/hello-1.0.0.jar", gp+"/hello/1.0.0/hello-1.0.0.pom")
			if len(assetPaths(t, s, central, "org/apache/maven/plugins/maven-deploy-plugin/")) == 0 {
				t.Fatalf("the mirror of settings.xml served no maven-deploy-plugin: the user's settings were not in force")
			}
		})

		t.Run("maven 4", func(t *testing.T) {
			maven4 := newestMaven4(t)
			t.Logf("Maven %s", maven4)
			c.mustRun(t, mavenImage, "publish with Maven 4", `set -e
curl -fsSL "https://repo.maven.apache.org/maven2/org/apache/maven/apache-maven/$MAVEN4/apache-maven-$MAVEN4-bin.tar.gz" | tar -xz -C /opt
export PATH="/opt/apache-maven-$MAVEN4/bin:$PATH"
mvn --version
`+publish, ClientOpts{
				Files:  project("2.0.0"),
				Env:    map[string]string{"E2E_LINK": s.NewSetupCode(t, dev, repo).Link, "MAVEN_ARGS": "-B -ntp", "MAVEN4": maven4},
				Mounts: map[string]string{devices: "/root/.telesphorepo", M2Volume: "/root/.m2/repository"},
			})
			requireAssets(t, s, repo, gp+"/", gp+"/hello/2.0.0/hello-2.0.0.jar", gp+"/hello/2.0.0/hello-2.0.0.pom")
		})

		t.Run("resolve", func(t *testing.T) {
			var script strings.Builder
			script.WriteString("set -e\ncd /work/consumer\n")
			for _, v := range []string{"1.0.0", "2.0.0"} {
				fmt.Fprintf(&script, "rm -rf /root/.m2/repository/%[1]s\nmvn -B -ntp -q -s /work/settings.xml -Ddep.version=%[2]s -Dmdep.outputFile=/tmp/cp.txt org.apache.maven.plugins:maven-dependency-plugin:3.8.1:build-classpath\njava -cp \"$(cat /tmp/cp.txt)\" %[3]s.Hello\n", gp, v, group)
			}
			code, out := RunClient(t, mavenImage, ClientOpts{
				Cmd: []string{script.String()},
				Files: []ContainerFile{{Path: "/work/settings.xml", Content: mavenSettingsXML(s.AdminUser, s.AdminPass)},
					mavenConsumerProject(group, s.InternalURL+"/maven/"+repo)},
				Mounts:  map[string]string{M2Volume: "/root/.m2/repository"},
				Timeout: 15 * time.Minute,
			})
			if code != 0 {
				t.Fatalf("resolve the published jars: exit %d\n%s", code, out)
			}
			wantText(t, "resolve the published jars", out, "hello from "+group+" 1.0.0", "hello from "+group+" 2.0.0")
		})
	})
}

// otherHostConf is a Maven repository of another host: it accepts uploads without credentials,
// answers 404 to reads, and refuses each request that carries credentials.
const otherHostConf = `{
	admin off
	auto_https off
}
:8080 {
	@auth header Authorization *
	respond @auth "credentials arrived" 403
	@read method GET HEAD
	respond @read 404
	respond 200
}
`

// gradlePub is a build at /work/pub: the library lib (Kotlin DSL) with its own second publishing
// repository at OtherURL, and the Gradle plugin greet (Groovy DSL). Neither names the server.
type gradlePub struct {
	RepoURL     string
	OtherURL    string
	Group       string
	LibMarker   string
	GreetMarker string
}

func newGradlePub(s *Server, repo, otherURL string) gradlePub {
	return gradlePub{
		RepoURL:     s.InternalURL + "/maven/" + repo,
		OtherURL:    otherURL,
		Group:       "com.example.g" + randHex(4),
		LibMarker:   "LIB-MARKER-" + randHex(6),
		GreetMarker: "GREET-MARKER-" + randHex(6),
	}
}

// gradleProperties keeps the builds of the containers free of a daemon and of progress output.
const gradleProperties = "org.gradle.daemon=false\norg.gradle.console=plain\n"

func (g gradlePub) files() []ContainerFile {
	groupPath := strings.ReplaceAll(g.Group, ".", "/")
	lib := fmt.Sprintf(`plugins {
    `+"`java-library`"+`
    `+"`maven-publish`"+`
}
group = %[1]q
version = "1.0"
publishing {
    publications {
        create<MavenPublication>("lib") {
            from(components["java"])
        }
    }
    repositories {
        maven {
            name = "other"
            url = uri(%[2]q)
            isAllowInsecureProtocol = true
        }
    }
}
`, g.Group, g.OtherURL)
	greet := fmt.Sprintf(`plugins {
    id 'java-gradle-plugin'
    id 'maven-publish'
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
	libSrc := fmt.Sprintf("package %s;\n\npublic class Lib {\n    public static String message() {\n        return %q;\n    }\n}\n", g.Group, g.LibMarker)
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
		{Path: "/work/pub/settings.gradle.kts", Content: "rootProject.name = \"pub\"\ninclude(\"lib\", \"greet\")\n"},
		{Path: "/work/pub/gradle.properties", Content: gradleProperties},
		{Path: "/work/pub/lib/build.gradle.kts", Content: lib},
		{Path: "/work/pub/lib/src/main/java/" + groupPath + "/Lib.java", Content: libSrc},
		{Path: "/work/pub/greet/build.gradle", Content: greet},
		{Path: "/work/pub/greet/src/main/java/" + groupPath + "/GreetPlugin.java", Content: greetSrc},
	}
}

// useFiles is a plain build at /work/use that applies the published plugin and loads the
// published library from the repo.
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
		{Path: "/work/use/gradle.properties", Content: gradleProperties},
		{Path: "/work/use/build.gradle", Content: build},
	}
}

// gradleOpts runs as root with the shared Gradle cache, the device folder on devices and env.
func gradleOpts(files []ContainerFile, devices string, env map[string]string) ClientOpts {
	allEnv := map[string]string{"GRADLE_USER_HOME": "/home/gradle/.gradle"}
	for k, v := range env {
		allEnv[k] = v
	}
	mounts := map[string]string{GradleVolume: "/home/gradle/.gradle"}
	if devices != "" {
		mounts[devices] = "/root/.telesphorepo"
	}
	return ClientOpts{Files: files, Env: allEnv, Mounts: mounts, User: "root", Timeout: 20 * time.Minute}
}

// TestCLIGradle: setup and publish run gradle publish with the init script of the CLI for a
// library (Kotlin DSL) and a Gradle plugin (Groovy DSL); the build's own repository of another
// host gets no credentials; a build with no maven-publish gets the message of the CLI; a clean
// build applies the plugin and loads the library.
func TestCLIGradle(t *testing.T) {
	c := newCLI(t)
	other := RandName("otherhost")
	startCaddy(t, otherHostConf, "Caddy of another host", other)
	ForEachStorage(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		repo := RandName("plugins")
		s.CreateRepo(t, repo, "maven", true, nil)
		dev := RandName("dev")
		s.CreateUser(t, dev, "dev-pass-1234")
		s.SetPerms(t, repo, dev, "repo.download", "repo.publish")
		devices := NewDataVolume(t)
		g := newGradlePub(s, repo, "http://"+other+":8080/m2")

		out := c.mustRun(t, gradleImage, "gradle publish", `set -e
cd /work/pub
telesphorepo setup "$E2E_LINK"
telesphorepo publish
`, gradleOpts(g.files(), devices, map[string]string{"E2E_LINK": s.NewSetupCode(t, dev, repo).Link}))
		wantText(t, "gradle publish", out, "Publishing to "+repo+" (maven)", " publish --init-script ", ":lib:publishLibPublicationToOtherRepository")
		gp := strings.ReplaceAll(g.Group, ".", "/")
		id := g.Group + ".greet"
		marker := id + ".gradle.plugin"
		requireAssets(t, s, repo, gp+"/", gp+"/lib/1.0/lib-1.0.jar", gp+"/greet/1.0/greet-1.0.jar")
		requireAssets(t, s, repo, strings.ReplaceAll(id, ".", "/")+"/", strings.ReplaceAll(id, ".", "/")+"/"+marker+"/1.0/"+marker+"-1.0.pom")

		code, out := RunClient(t, gradleImage, ClientOpts{Cmd: []string{"cd /work/use && gradle hello libMessage"},
			Files: g.useFiles(), Env: map[string]string{"GRADLE_USER_HOME": "/home/gradle/.gradle"},
			Mounts: map[string]string{GradleVolume: "/home/gradle/.gradle"}, User: "root", Timeout: 20 * time.Minute})
		if code != 0 {
			t.Fatalf("apply the published plugin and load the library: exit %d\n%s", code, out)
		}
		wantText(t, "apply the published plugin and load the library", out, g.GreetMarker, g.LibMarker)

		plain := []ContainerFile{
			{Path: "/work/plain/settings.gradle", Content: "rootProject.name = 'plain'\n"},
			{Path: "/work/plain/gradle.properties", Content: gradleProperties},
			{Path: "/work/plain/build.gradle", Content: "plugins {\n    id 'java'\n}\n"},
		}
		code, out = c.run(t, gradleImage, "gradle publish without maven-publish", `set -e
cd /work/plain
telesphorepo setup "$E2E_LINK"
telesphorepo publish
`, gradleOpts(plain, devices, map[string]string{"E2E_LINK": s.NewSetupCode(t, dev, repo).Link}))
		if code == 0 {
			t.Fatalf("gradle publish without maven-publish: exit 0, want a failure:\n%s", out)
		}
		wantText(t, "gradle publish without maven-publish", out, "No project of this build applies maven-publish")
	})
}
