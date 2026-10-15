//go:build e2e || import || scale || clients

package e2e

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
)

// Client images and helpers that several zones share: Maven projects, Debian packages and apt,
// curl for raw files, and the project setup scripts.

const (
	mavenImage = "maven:3.9.16-eclipse-temurin-21"
	debImage   = "debian:trixie-20261005"
	curlImage  = "curlimages/curl:8.16.0"
)

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

// debBuildScript builds the test packages with dpkg-deb into /out.
const debBuildScript = `#!/bin/sh
set -e
mkdir -p /out
mk() { # package version arch message compression outname
	d=/build/$6
	mkdir -p "$d/DEBIAN" "$d/usr/bin"
	printf 'Package: %s\nVersion: %s\nArchitecture: %s\nMaintainer: E2E <e2e@telesphorepo.test>\nDescription: telesphorepo e2e test package\n' "$1" "$2" "$3" > "$d/DEBIAN/control"
	printf '#!/bin/sh\necho "%s"\n' "$4" > "$d/usr/bin/$1"
	chmod 0755 "$d/usr/bin/$1"
	dpkg-deb --root-owner-group -Z"$5" --build "$d" "/out/$6.deb"
}
arch=$(dpkg --print-architecture)
printf '%s' "$arch" > /out/arch
mk e2e-hello 1.0 all "hello 1.0" xz hello-1.0
mk e2e-hello 2.0 all "hello 2.0" xz hello-2.0
mk e2e-hello 1.0 all "hello 1.0 conflicting" gzip hello-1.0-conflict
mk e2e-native 1.0 "$arch" "native $arch" gzip native
ls -l /out
`

type debPackages struct {
	arch                                   string
	hello1, hello2, hello1Conflict, native []byte
}

func buildDebPackages(t *testing.T) *debPackages {
	t.Helper()
	r := RunClientResult(t, debImage, ClientOpts{
		Cmd:   []string{"sh", "/build.sh"},
		Files: []ContainerFile{{Content: debBuildScript, Path: "/build.sh", Mode: 0o755}},
	})
	if r.ExitCode != 0 {
		t.Fatalf("building .debs failed (exit %d):\n%s", r.ExitCode, r.Output)
	}
	return &debPackages{
		arch:           strings.TrimSpace(string(r.ReadFile(t, "/out/arch"))),
		hello1:         r.ReadFile(t, "/out/hello-1.0.deb"),
		hello2:         r.ReadFile(t, "/out/hello-2.0.deb"),
		hello1Conflict: r.ReadFile(t, "/out/hello-1.0-conflict.deb"),
		native:         r.ReadFile(t, "/out/native.deb"),
	}
}

const debAptScript = `#!/bin/sh
set -e
rm -f /etc/apt/sources.list.d/debian.sources /etc/apt/sources.list
if ! apt-get update >/tmp/update.log 2>&1; then cat /tmp/update.log; exit 1; fi
cat /tmp/update.log
echo '=== end of apt-get update ==='
apt-get install -y --no-install-recommends e2e-hello e2e-native
echo "RESULT: $(e2e-hello)"
echo "NATIVE: $(e2e-native)"
`

// debAptInstall runs apt-get update/install in a fresh container and returns its output.
func debAptInstall(t *testing.T, s *Server, repo, user, pass, key string) string {
	t.Helper()
	sources := fmt.Sprintf("Types: deb\nURIs: %s/debian/%s\nSuites: trixie\nComponents: main\nSigned-By: /etc/apt/keyrings/tr.asc\n", s.InternalURL, repo)
	// apt only applies auth.conf entries to plain http when the protocol is spelled out.
	auth := fmt.Sprintf("machine http://%s:8080/debian/%s login %s password %s\n", s.Alias, repo, user, pass)
	code, out := RunClient(t, debImage, ClientOpts{
		Cmd: []string{"sh", "/apt.sh"},
		Files: []ContainerFile{
			{Content: debAptScript, Path: "/apt.sh", Mode: 0o755},
			{Content: key, Path: "/etc/apt/keyrings/tr.asc"},
			{Content: sources, Path: "/etc/apt/sources.list.d/tr.sources"},
			{Content: auth, Path: "/etc/apt/auth.conf.d/tr.conf", Mode: 0o644},
		},
	})
	if code != 0 {
		t.Fatalf("apt client exited %d:\n%s", code, out)
	}
	update, _, ok := strings.Cut(out, "=== end of apt-get update ===")
	if !ok {
		t.Fatalf("apt output missing update marker:\n%s", out)
	}
	for _, bad := range []string{"NO_PUBKEY", "not signed", "insecure", "Err:"} {
		if strings.Contains(update, bad) {
			t.Fatalf("apt-get update output contains %q:\n%s", bad, update)
		}
	}
	return out
}

func debUpload(t *testing.T, s *Server, repo string, deb []byte, auth Auth) (int, []byte) {
	t.Helper()
	req := s.NewRequest(t, http.MethodPut, "/debian/"+repo+"/upload/trixie/main", bytes.NewReader(deb))
	req.Header.Set("Content-Type", "application/vnd.debian.binary-package")
	resp, b := s.Do(t, req, auth)
	return resp.StatusCode, b
}

func rawPut(t *testing.T, s *Server, path string, body []byte, auth Auth) (*http.Response, []byte) {
	t.Helper()
	return s.Do(t, s.NewRequest(t, http.MethodPut, path, bytes.NewReader(body)), auth)
}

func rawRandom(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		t.Fatal(err)
	}
	return b
}

// clientsVersionOf reads the clientsVersion constant from internal/maven/usage.go.
func clientsVersionOf(t testing.TB) string {
	t.Helper()
	src, err := os.ReadFile("../internal/maven/usage.go")
	if err != nil {
		t.Fatalf("read usage.go: %v", err)
	}
	m := regexp.MustCompile(`const clientsVersion = "([^"]+)"`).FindSubmatch(src)
	if m == nil {
		t.Fatal("no clientsVersion constant in internal/maven/usage.go")
	}
	return string(m[1])
}

// fetchSetup gets GET /setup.sh or /setup.ps1 without credentials.
func fetchSetup(t testing.TB, s *Server, name string) string {
	t.Helper()
	resp, body := s.Do(t, s.NewRequest(t, http.MethodGet, "/"+name, nil), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /%s: status %d: %s", name, resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("GET /%s: Content-Type %q, want text/plain", name, ct)
	}
	return string(body)
}
