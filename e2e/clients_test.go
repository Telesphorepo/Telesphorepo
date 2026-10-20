//go:build e2e || import || scale || clients

package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	osexec "os/exec"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Client images and helpers that several zones share: Maven projects, Debian packages and apt,
// curl for raw files, Docker in Docker, Helm, ORAS and the Caddy reverse proxy.

// The tools float (see images_test.go). orasImage is the newest ORAS release, which the run looks
// up before it pulls: ORAS has no floating tag.
var (
	mavenImage  = floatingImage("docker.io/library/maven:3")
	gradleImage = floatingImage("docker.io/library/gradle:latest")
	debImage    = floatingImage("docker.io/library/debian:stable")
	curlImage   = floatingImage("docker.io/curlimages/curl:latest")
	helmImage   = floatingImage("docker.io/alpine/helm:latest")
	orasImage   string
)

// caddyImage is the reverse proxy of the tests: a redirector, a proxy with a body limit, a
// proxy in front of two servers and a proxy with no upstream. It is infrastructure of the tests,
// so it stays pinned.
const caddyImage = "caddy:2.10.2-alpine"

// registryHost is the registry host of s as Docker names it: its network alias and port.
func registryHost(s *Server) string { return s.Alias + ":8080" }

// startCaddy starts Caddy on :8080 with the Caddyfile conf and the network aliases aliases.
func startCaddy(t *testing.T, conf, what string, aliases ...string) {
	t.Helper()
	c, err := runContainer(context.Background(), caddyImage,
		testcontainers.WithLabels(e2eLabels),
		testcontainers.WithFiles(testcontainers.ContainerFile{Reader: strings.NewReader(conf), ContainerFilePath: "/etc/caddy/Caddyfile", FileMode: 0o644}),
		testcontainers.WithExposedPorts("8080/tcp"),
		network.WithNetwork(aliases, TestNetwork),
		testcontainers.WithWaitStrategy(wait.ForListeningPort("8080/tcp").WithStartupTimeout(time.Minute)),
	)
	registerCleanup(t, c, what)
	if err != nil {
		t.Fatalf("start %s: %v", what, err)
	}
}

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

// warmMaven fills M2Volume with the plugins of mavenPluginVersions and maven-dependency-plugin
// 3.8.1 before the tests run: it builds, deploys and resolves a tiny project against a local file
// repo. Maven retries no download whose body Maven Central cuts off, so a reset fails the build;
// warmMaven runs the build up to three times, and the parallel tests then find every plugin in
// the volume.
func warmMaven(ctx context.Context) error {
	const repo = "file:///tmp/deployed"
	var script strings.Builder
	script.WriteString("set -e\n")
	for _, f := range append(mavenLibProject("e2e.warm", "1.0.0", repo, "warm"), mavenConsumerProject("e2e.warm", repo)) {
		fmt.Fprintf(&script, "mkdir -p %s && echo %s | base64 -d > %s\n", path.Dir(f.Path), base64.StdEncoding.EncodeToString([]byte(f.Content)), f.Path)
	}
	script.WriteString("cd /work/lib && mvn -B -ntp -q deploy\n")
	script.WriteString("cd /work/consumer && mvn -B -ntp -q -Ddep.version=1.0.0 -Dmdep.outputFile=/tmp/cp.txt org.apache.maven.plugins:maven-dependency-plugin:3.8.1:build-classpath\n")
	var out []byte
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		out, err = osexec.CommandContext(ctx, Engine.CLI, "run", "--rm", "--label", "telesphorepo.e2e=1",
			"-v", M2Volume+":/root/.m2/repository", "-e", "MAVEN_OPTS="+mavenFileLocks,
			mavenImage, "sh", "-c", script.String()).CombinedOutput()
		cancel()
		if err == nil {
			return nil
		}
		fmt.Fprintf(os.Stderr, "e2e: maven warm-up attempt %d failed: %v\n", attempt, err)
	}
	return fmt.Errorf("%w\n%s", err, out)
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
apt-get install -y --no-install-recommends ${E2E_PKGS:-e2e-hello e2e-native}
echo "RESULT: $(e2e-hello)"
if command -v e2e-native >/dev/null; then echo "NATIVE: $(e2e-native)"; fi
`

// debAptInstall runs apt-get update/install of e2e-hello and e2e-native in a fresh container and
// returns its output.
func debAptInstall(t *testing.T, s *Server, repo, user, pass, key string) string {
	t.Helper()
	return debAptInstallOf(t, s, repo, user, pass, key, "e2e-hello e2e-native")
}

// debAptInstallOf is debAptInstall of the packages pkgs (separated by spaces).
func debAptInstallOf(t *testing.T, s *Server, repo, user, pass, key, pkgs string) string {
	t.Helper()
	sources := fmt.Sprintf("Types: deb\nURIs: %s/debian/%s\nSuites: trixie\nComponents: main\nSigned-By: /etc/apt/keyrings/tr.asc\n", s.InternalURL, repo)
	// apt only applies auth.conf entries to plain http when the protocol is spelled out.
	auth := fmt.Sprintf("machine http://%s:8080/debian/%s login %s password %s\n", s.Alias, repo, user, pass)
	code, out := RunClient(t, debImage, ClientOpts{
		Cmd: []string{"sh", "/apt.sh"},
		Env: map[string]string{"E2E_PKGS": pkgs},
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
	// "doesn't support architecture": a Release whose Architectures lacks the client's, as Nexus
	// writes for a repo of only "all" packages.
	for _, bad := range []string{"NO_PUBKEY", "not signed", "insecure", "Err:", "doesn't support architecture"} {
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

// ---------------------------------------------------------------- Docker in Docker

// dindImage is the Docker engine that the registry tests drive (privileged, on TestNetwork).
var dindImage = floatingImage("docker.io/library/docker:dind")

// Dind is a running Docker engine with its CLI, on TestNetwork.
type Dind struct {
	c     testcontainers.Container
	Alias string
}

// StartDind starts a Docker engine that reaches the registries insecure ("host:port") over plain
// HTTP, and waits until "docker info" answers.
func StartDind(t testing.TB, insecure ...string) *Dind {
	t.Helper()
	var args []string
	for _, h := range insecure {
		args = append(args, "--insecure-registry="+h)
	}
	return StartDindArgs(t, args...)
}

// StartDindArgs starts a Docker engine with the dockerd flags args and waits until "docker info"
// answers.
func StartDindArgs(t testing.TB, args ...string) *Dind {
	t.Helper()
	alias := RandName("dind")
	c, err := testcontainers.Run(context.Background(), dindImage,
		testcontainers.WithLabels(e2eLabels),
		testcontainers.WithEnv(map[string]string{"DOCKER_TLS_CERTDIR": ""}),
		testcontainers.WithCmd(args...),
		testcontainers.WithHostConfigModifier(func(hc *container.HostConfig) { hc.Privileged = true }),
		network.WithNetwork([]string{alias}, TestNetwork),
		testcontainers.WithWaitStrategyAndDeadline(3*time.Minute, wait.ForExec([]string{"docker", "info"}).WithStartupTimeout(3*time.Minute)),
	)
	registerCleanup(t, c, "dind "+alias)
	if err != nil {
		t.Fatalf("start docker in docker: %v", err)
	}
	return &Dind{c: c, Alias: alias}
}

// Exec runs script with sh in the Docker engine's container and returns the exit code and the
// combined output.
func (d *Dind) Exec(t testing.TB, script string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	code, rd, err := d.c.Exec(ctx, []string{"sh", "-c", script}, tcexec.Multiplexed())
	if err != nil {
		t.Fatalf("docker in docker exec: %v", err)
	}
	out, _ := io.ReadAll(rd)
	return code, string(out)
}

// MustExec is Exec that fails the test on a non-zero exit.
func (d *Dind) MustExec(t testing.TB, what, script string) string {
	t.Helper()
	code, out := d.Exec(t, script)
	if code != 0 {
		t.Fatalf("%s: exit %d:\n%s", what, code, out)
	}
	return out
}

// CopyFile copies the local file src into the Docker engine's container at dst with mode.
func (d *Dind) CopyFile(t testing.TB, src, dst string, mode int64) {
	t.Helper()
	if err := d.c.CopyFileToContainer(context.Background(), src, dst, mode); err != nil {
		t.Fatalf("copy %s into docker in docker: %v", dst, err)
	}
}

// DockerScratchImage returns shell commands that write a build context in the new folder dir:
// one file f<i> of sizes[i] random bytes per layer, and a Dockerfile FROM scratch that copies each
// file in its own layer. No image comes from a registry.
func DockerScratchImage(dir string, sizes ...int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "mkdir -p %[1]s && cd %[1]s\nprintf 'FROM scratch\\n' > Dockerfile\n", dir)
	for i, n := range sizes {
		fmt.Fprintf(&b, "head -c %d /dev/urandom > f%d\nprintf 'COPY f%d /f%d\\n' >> Dockerfile\n", n, i, i, i)
	}
	b.WriteString("cd /\n")
	return b.String()
}
