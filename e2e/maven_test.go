//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

const mavenImage = "maven:3.9.16-eclipse-temurin-21"

// mavenSettingsXML returns a settings.xml with one server (id "telesphorepo") and the
// maven-default-http-blocker mirror neutralised so plain-http repos on the test network work.
func mavenSettingsXML(user, pass string) string {
	return fmt.Sprintf(`<settings>
  <servers>
    <server><id>telesphorepo</id><username>%s</username><password>%s</password></server>
  </servers>
  <mirrors>
    <mirror>
      <id>maven-default-http-blocker</id>
      <mirrorOf>external:dummy:*</mirrorOf>
      <url>http://0.0.0.0/</url>
      <blocked>false</blocked>
    </mirror>
  </mirrors>
</settings>
`, user, pass)
}

const mavenPluginVersions = `<pluginManagement><plugins>
      <plugin><groupId>org.apache.maven.plugins</groupId><artifactId>maven-resources-plugin</artifactId><version>3.3.1</version></plugin>
      <plugin><groupId>org.apache.maven.plugins</groupId><artifactId>maven-compiler-plugin</artifactId><version>3.13.0</version></plugin>
      <plugin><groupId>org.apache.maven.plugins</groupId><artifactId>maven-surefire-plugin</artifactId><version>3.5.2</version></plugin>
      <plugin><groupId>org.apache.maven.plugins</groupId><artifactId>maven-jar-plugin</artifactId><version>3.4.2</version></plugin>
      <plugin><groupId>org.apache.maven.plugins</groupId><artifactId>maven-install-plugin</artifactId><version>3.1.3</version></plugin>
      <plugin><groupId>org.apache.maven.plugins</groupId><artifactId>maven-deploy-plugin</artifactId><version>3.1.3</version></plugin>
    </plugins></pluginManagement>`

// mavenLibProject returns the files of a tiny jar project at /work/lib whose Hello main
// class prints message.
func mavenLibProject(group, version, repoURL, message string) []ContainerFile {
	pom := fmt.Sprintf(`<project xmlns="http://maven.apache.org/POM/4.0.0">
  <modelVersion>4.0.0</modelVersion>
  <groupId>%[1]s</groupId>
  <artifactId>hello</artifactId>
  <version>%[2]s</version>
  <packaging>jar</packaging>
  <properties>
    <maven.compiler.release>21</maven.compiler.release>
    <project.build.sourceEncoding>UTF-8</project.build.sourceEncoding>
  </properties>
  <distributionManagement>
    <repository><id>telesphorepo</id><url>%[3]s</url></repository>
    <snapshotRepository><id>telesphorepo</id><url>%[3]s</url></snapshotRepository>
  </distributionManagement>
  <build>
    %[4]s
  </build>
</project>
`, group, version, repoURL, mavenPluginVersions)
	src := fmt.Sprintf(`package %s;

public class Hello {
    public static void main(String[] args) {
        System.out.println(%q);
    }
}
`, group, message)
	return []ContainerFile{
		{Path: "/work/lib/pom.xml", Content: pom},
		{Path: "/work/lib/src/main/java/" + strings.ReplaceAll(group, ".", "/") + "/Hello.java", Content: src},
	}
}

// mavenConsumerProject returns a pom-packaged project at /work/consumer that depends on
// group:hello:${dep.version} and resolves it from repoURL.
func mavenConsumerProject(group, repoURL string) ContainerFile {
	return ContainerFile{Path: "/work/consumer/pom.xml", Content: fmt.Sprintf(`<project xmlns="http://maven.apache.org/POM/4.0.0">
  <modelVersion>4.0.0</modelVersion>
  <groupId>%[1]s.consumer</groupId>
  <artifactId>consumer</artifactId>
  <version>1.0.0</version>
  <packaging>pom</packaging>
  <repositories>
    <repository>
      <id>telesphorepo</id>
      <url>%[2]s</url>
      <releases><enabled>true</enabled></releases>
      <snapshots><enabled>true</enabled><updatePolicy>always</updatePolicy></snapshots>
    </repository>
  </repositories>
  <dependencies>
    <dependency><groupId>%[1]s</groupId><artifactId>hello</artifactId><version>${dep.version}</version></dependency>
  </dependencies>
</project>
`, group, repoURL)}
}

// mavenAssetPaths lists asset paths of repo under prefix via the JSON API (follows keyset pages).
func mavenAssetPaths(t *testing.T, s *Server, repo, prefix string, auth Auth) []string {
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
		s.MustAPI(t, http.MethodGet, q, nil, auth, &page)
		for _, it := range page.Items {
			paths = append(paths, strings.TrimPrefix(it.Path, "/"))
		}
		if page.Next == nil || *page.Next == "" {
			return paths
		}
		after = *page.Next
	}
}

func mavenHead(t *testing.T, s *Server, path string, auth Auth) *http.Response {
	t.Helper()
	resp, _ := s.Do(t, s.NewRequest(t, http.MethodHead, path, nil), auth)
	return resp
}

func TestMaven(t *testing.T) {
	ForEachStorage(t, func(t *testing.T, opts ServerOpts) {
		s := StartServer(t, opts)
		repo := RandName("maven")
		s.CreateRepo(t, repo, "maven", false, nil)
		writer, writerPass := RandName("writer"), randHex(12)
		s.CreateUser(t, writer, writerPass, false)
		s.SetPermission(t, repo, writer, "write")
		token := s.CreateToken(t, writer, writerPass, "maven-e2e")

		group := "com.example.e2e" + randHex(4)
		groupPath := strings.ReplaceAll(group, ".", "/")
		repoURL := s.InternalURL + "/maven/" + repo
		deploySettings := ContainerFile{Path: "/work/settings.xml", Content: mavenSettingsXML(writer, writerPass)}
		mounts := map[string]string{M2Volume: "/root/.m2/repository"}

		deploy := func(version, message string) (int, string) {
			t.Helper()
			files := append(mavenLibProject(group, version, repoURL, message), deploySettings)
			return RunClient(t, mavenImage, ClientOpts{
				Cmd:     []string{"cd /work/lib && mvn -B -ntp -s /work/settings.xml deploy"},
				Files:   files,
				Mounts:  mounts,
				Timeout: 15 * time.Minute,
			})
		}

		releaseMsg := "hello from " + group + " release 1.0.0"
		if code, out := deploy("1.0.0", releaseMsg); code != 0 {
			t.Fatalf("deploy 1.0.0: exit %d\n%s", code, out)
		}
		snapMsg := func(n int) string { return fmt.Sprintf("hello from %s snapshot build %d", group, n) }
		for i := 1; i <= 2; i++ {
			if code, out := deploy("1.1.0-SNAPSHOT", snapMsg(i)); code != 0 {
				t.Fatalf("deploy 1.1.0-SNAPSHOT #%d: exit %d\n%s", i, code, out)
			}
		}

		jarPath := "/maven/" + repo + "/" + groupPath + "/hello/1.0.0/hello-1.0.0.jar"
		before := mavenHead(t, s, jarPath, Basic{writer, writerPass})
		if before.StatusCode != http.StatusOK {
			t.Fatalf("HEAD %s: status %d", jarPath, before.StatusCode)
		}
		if n, err := strconv.ParseInt(before.Header.Get("Content-Length"), 10, 64); err != nil || n <= 0 {
			t.Fatalf("HEAD %s: bad Content-Length %q", jarPath, before.Header.Get("Content-Length"))
		}

		// Release redeploy with different content must be rejected.
		code, out := deploy("1.0.0", "modified "+releaseMsg)
		if code == 0 {
			t.Fatalf("redeploy of release 1.0.0 with modified content succeeded:\n%s", out)
		}
		if !strings.Contains(out, "409") {
			t.Fatalf("redeploy of release 1.0.0 failed without a 409:\n%s", out)
		}
		after := mavenHead(t, s, jarPath, Basic{writer, writerPass})
		if after.StatusCode != http.StatusOK || after.Header.Get("Content-Length") != before.Header.Get("Content-Length") {
			t.Fatalf("release jar changed after rejected redeploy: status %d, length %q -> %q",
				after.StatusCode, before.Header.Get("Content-Length"), after.Header.Get("Content-Length"))
		}
		if et := before.Header.Get("ETag"); et != "" && after.Header.Get("ETag") != et {
			t.Fatalf("release jar ETag changed after rejected redeploy: %q -> %q", et, after.Header.Get("ETag"))
		}

		// Two snapshot deploys produce two timestamped jars.
		var snapJars int
		for _, p := range mavenAssetPaths(t, s, repo, groupPath+"/hello/1.1.0-SNAPSHOT/", s.Admin()) {
			if strings.HasSuffix(p, ".jar") {
				snapJars++
			}
		}
		if snapJars < 2 {
			t.Fatalf("expected >= 2 snapshot jars, got %d", snapJars)
		}

		missing := s.NewRequest(t, http.MethodGet, "/maven/"+repo+"/"+groupPath+"/hello/9.9.9/hello-9.9.9.jar", nil)
		if resp, _ := s.Do(t, missing, Basic{writer, writerPass}); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("GET missing artifact: status %d, want 404", resp.StatusCode)
		}

		// Fresh consumer container: resolve from the server (token auth) and run both versions.
		script := strings.Join([]string{
			"set -e",
			"rm -rf /root/.m2/repository/" + groupPath,
			"cd /work/consumer",
			"for v in 1.0.0 1.1.0-SNAPSHOT; do",
			"mvn -B -ntp -q -U -s /work/settings.xml -Ddep.version=$v -Dmdep.outputFile=/tmp/cp-$v.txt org.apache.maven.plugins:maven-dependency-plugin:3.8.1:build-classpath;",
			`java -cp "$(cat /tmp/cp-$v.txt)" ` + group + ".Hello;",
			"done",
		}, "\n")
		code, out = RunClient(t, mavenImage, ClientOpts{
			Cmd: []string{script},
			Files: []ContainerFile{
				{Path: "/work/settings.xml", Content: mavenSettingsXML(writer, token)},
				mavenConsumerProject(group, repoURL),
			},
			Mounts:  mounts,
			Timeout: 15 * time.Minute,
		})
		if code != 0 {
			t.Fatalf("consumer: exit %d\n%s", code, out)
		}
		for _, want := range []string{releaseMsg, snapMsg(2)} {
			if !strings.Contains(out, want) {
				t.Fatalf("consumer output missing %q:\n%s", want, out)
			}
		}
		if strings.Contains(out, "modified "+releaseMsg) || strings.Contains(out, snapMsg(1)) {
			t.Fatalf("consumer resolved stale or rejected content:\n%s", out)
		}
	})
}
