//go:build e2e

package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

const gradleImage = "gradle:9.8.1-jdk21"

// gradleRepoBlock is a Kotlin DSL maven { } repository pointing at the server with credentials.
func gradleRepoBlock(repoURL, user, pass string) string {
	return fmt.Sprintf(`maven {
            name = "telesphorepo"
            url = uri(%q)
            isAllowInsecureProtocol = true
            credentials {
                username = %q
                password = %q
            }
        }`, repoURL, user, pass)
}

// gradlePluginProject is project A: a java-gradle-plugin whose "hello" task prints marker.
func gradlePluginProject(group, pluginID, repoURL, user, pass, marker string) []ContainerFile {
	build := fmt.Sprintf(`plugins {
    `+"`java-gradle-plugin`"+`
    `+"`maven-publish`"+`
}

group = %[1]q
version = "1.0"

gradlePlugin {
    plugins {
        create("greet") {
            id = %[2]q
            implementationClass = "%[1]s.GreetPlugin"
        }
    }
}

publishing {
    repositories {
        %[3]s
    }
}
`, group, pluginID, gradleRepoBlock(repoURL, user, pass))
	src := fmt.Sprintf(`package %s;

import org.gradle.api.Plugin;
import org.gradle.api.Project;

public class GreetPlugin implements Plugin<Project> {
    @Override
    public void apply(Project project) {
        project.getTasks().register("hello", task -> task.doLast(t -> System.out.println(%q)));
    }
}
`, group, marker)
	return []ContainerFile{
		{Path: "/work/plugin/settings.gradle.kts", Content: `rootProject.name = "greet"` + "\n"},
		{Path: "/work/plugin/build.gradle.kts", Content: build},
		{Path: "/work/plugin/src/main/java/" + strings.ReplaceAll(group, ".", "/") + "/GreetPlugin.java", Content: src},
	}
}

// gradleConsumerProject is project B: applies the plugin through plugins {} resolved via pluginManagement.
func gradleConsumerProject(pluginID, repoURL, user, pass string) []ContainerFile {
	settings := fmt.Sprintf(`pluginManagement {
    repositories {
        %s
        gradlePluginPortal()
    }
}
rootProject.name = "consumer"
`, gradleRepoBlock(repoURL, user, pass))
	build := fmt.Sprintf("plugins {\n    id(%q) version \"1.0\"\n}\n", pluginID)
	return []ContainerFile{
		{Path: "/work/consumer/settings.gradle.kts", Content: settings},
		{Path: "/work/consumer/build.gradle.kts", Content: build},
	}
}

func TestGradlePlugin(t *testing.T) {
	ForEachStorage(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		repo := RandName("gradle")
		s.CreateRepo(t, repo, "maven", false, nil)
		writer, writerPass := RandName("writer"), randHex(12)
		s.CreateUser(t, writer, writerPass, false)
		s.SetPermission(t, repo, writer, "write")
		token := s.CreateToken(t, writer, writerPass, "gradle-e2e")

		group := "com.example.e2e" + randHex(4)
		groupPath := strings.ReplaceAll(group, ".", "/")
		pluginID := group + ".greet"
		marker := "GREET-MARKER-" + randHex(6)
		repoURL := s.InternalURL + "/maven/" + repo
		run := func(dir string, files []ContainerFile, task string) (int, string) {
			t.Helper()
			return RunClient(t, gradleImage, ClientOpts{
				Cmd:     []string{"cd " + dir + " && gradle --no-daemon --console=plain --stacktrace " + task},
				Files:   files,
				Env:     map[string]string{"GRADLE_USER_HOME": "/home/gradle/.gradle"},
				Mounts:  map[string]string{GradleVolume: "/home/gradle/.gradle"},
				User:    "root",
				Timeout: 15 * time.Minute,
			})
		}

		if code, out := run("/work/plugin", gradlePluginProject(group, pluginID, repoURL, writer, token, marker), "publish"); code != 0 {
			t.Fatalf("gradle publish: exit %d\n%s", code, out)
		}

		markerDir := groupPath + "/greet/" + pluginID + ".gradle.plugin/1.0/"
		wantPom := markerDir + pluginID + ".gradle.plugin-1.0.pom"
		wantJar := groupPath + "/greet/1.0/greet-1.0.jar"
		got := map[string]bool{}
		for _, p := range mavenAssetPaths(t, s, repo, groupPath+"/greet/", s.Admin()) {
			got[p] = true
		}
		for _, want := range []string{wantPom, wantJar} {
			if !got[want] {
				t.Fatalf("asset %s not published; have %v", want, got)
			}
		}

		code, out := run("/work/consumer", gradleConsumerProject(pluginID, repoURL, writer, token), "hello")
		if code != 0 {
			t.Fatalf("gradle hello: exit %d\n%s", code, out)
		}
		if !strings.Contains(out, marker) {
			t.Fatalf("gradle hello output missing marker %q:\n%s", marker, out)
		}
	})
}
