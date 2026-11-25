//go:build clients

package e2e

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

var gradleImage = floatingImage("docker.io/library/gradle:latest")

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
// target, with the newest Maven 3; the mirror of the person's settings.xml stays in force
// (Central comes through a proxy repo of the server). The session TTL of 5 s and an mvn that
// sleeps 6 s before it starts make the publish outlive its first session term, so the CLI must
// renew the session (cmd/telesphorepo/renew.go) for the deploy to succeed.
func TestCLIMaven(t *testing.T) {
	t.Parallel()
	c := newCLI(t)
	OnLocalDisk(t, func(t *testing.T, opts ServerOpts) {
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
			setSetting(t, s, "publish.session_ttl", "5s")
			t.Cleanup(func() { setSetting(t, s, "publish.session_ttl", nil) })
			// A fresh local repository, so every plugin comes through the mirror.
			out := c.mustRun(t, mavenImage, "publish with Maven 3", `set -e
export REAL_MVN="$(command -v mvn)"
mkdir -p /tmp/slow-maven
cat > /tmp/slow-maven/mvn <<'EOF'
#!/bin/sh
sleep 6
exec "$REAL_MVN" "$@"
EOF
chmod +x /tmp/slow-maven/mvn
export PATH="/tmp/slow-maven:$PATH"
`+publish, ClientOpts{
				Files:  project("1.0.0"),
				Env:    map[string]string{"E2E_LINK": s.NewSetupCode(t, dev, repo).Link, "MAVEN_ARGS": "-B -ntp -Dmaven.repo.local=/tmp/m2"},
				Mounts: map[string]string{devices: "/root/.telesphorepo"},
			})
			wantText(t, "publish with Maven 3", out, "mvn deploy")
			requireAssets(t, s, repo, gp+"/", gp+"/hello/1.0.0/hello-1.0.0.jar", gp+"/hello/1.0.0/hello-1.0.0.pom")
			if log := publishLog(t, s, repo); len(log) == 0 || log[0].State != "closed" {
				t.Fatalf("publish must close its session after Maven exits: %+v", log)
			}
			if len(assetPaths(t, s, central, "org/apache/maven/plugins/maven-deploy-plugin/")) == 0 {
				t.Fatalf("the mirror of settings.xml served no maven-deploy-plugin: the user's settings were not in force")
			}
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

// gradleProperties keeps the builds of the containers free of a daemon and of progress output.
const gradleProperties = "org.gradle.daemon=false\norg.gradle.console=plain\n"

// gradlePubFiles is a build at /work/pub: the library lib (Kotlin DSL) with its own second
// publishing repository at otherURL, and the Gradle plugin greet (Groovy DSL), both of group.
// Neither names the server.
func gradlePubFiles(group, otherURL string) []ContainerFile {
	groupPath := strings.ReplaceAll(group, ".", "/")
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
`, group, otherURL)
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
`, group)
	libSrc := fmt.Sprintf("package %s;\n\npublic class Lib {\n    public static String message() {\n        return \"lib\";\n    }\n}\n", group)
	greetSrc := fmt.Sprintf(`package %s;

import org.gradle.api.Plugin;
import org.gradle.api.Project;

public class GreetPlugin implements Plugin<Project> {
    @Override
    public void apply(Project project) {
        project.getTasks().register("hello", task -> task.doLast(t -> System.out.println("hello")));
    }
}
`, group)
	return []ContainerFile{
		{Path: "/work/pub/settings.gradle.kts", Content: "rootProject.name = \"pub\"\ninclude(\"lib\", \"greet\")\n"},
		{Path: "/work/pub/gradle.properties", Content: gradleProperties},
		{Path: "/work/pub/lib/build.gradle.kts", Content: lib},
		{Path: "/work/pub/lib/src/main/java/" + groupPath + "/Lib.java", Content: libSrc},
		{Path: "/work/pub/greet/build.gradle", Content: greet},
		{Path: "/work/pub/greet/src/main/java/" + groupPath + "/GreetPlugin.java", Content: greetSrc},
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
// host gets no credentials; a build with no maven-publish gets the message of the CLI.
func TestCLIGradle(t *testing.T) {
	t.Parallel()
	c := newCLI(t)
	other := RandName("otherhost")
	startCaddy(t, otherHostConf, "Caddy of another host", other)
	OnLocalDisk(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		repo := RandName("plugins")
		s.CreateRepo(t, repo, "maven", true, nil)
		dev := RandName("dev")
		s.CreateUser(t, dev, "dev-pass-1234")
		s.SetPerms(t, repo, dev, "repo.download", "repo.publish")
		devices := NewDataVolume(t)
		group := "com.example.g" + randHex(4)

		out := c.mustRun(t, gradleImage, "gradle publish", `set -e
cd /work/pub
telesphorepo setup "$E2E_LINK"
telesphorepo publish
`, gradleOpts(gradlePubFiles(group, "http://"+other+":8080/m2"), devices, map[string]string{"E2E_LINK": s.NewSetupCode(t, dev, repo).Link}))
		wantText(t, "gradle publish", out, "gradle publish", ":lib:publishLibPublicationToOtherRepository")
		gp := strings.ReplaceAll(group, ".", "/")
		id := group + ".greet"
		marker := id + ".gradle.plugin"
		requireAssets(t, s, repo, gp+"/", gp+"/lib/1.0/lib-1.0.jar", gp+"/greet/1.0/greet-1.0.jar")
		requireAssets(t, s, repo, strings.ReplaceAll(id, ".", "/")+"/", strings.ReplaceAll(id, ".", "/")+"/"+marker+"/1.0/"+marker+"-1.0.pom")

		plain := []ContainerFile{
			{Path: "/work/plain/settings.gradle", Content: "rootProject.name = 'plain'\n"},
			{Path: "/work/plain/gradle.properties", Content: gradleProperties},
			{Path: "/work/plain/build.gradle", Content: "plugins {\n    id 'java'\n}\n"},
		}
		code, out := c.run(t, gradleImage, "gradle publish without maven-publish", `set -e
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
