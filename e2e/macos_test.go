//go:build macos

package e2e

// The macOS zone (tag macos): the release job of release.yml on macos-latest runs it. It needs no
// container engine. It starts the darwin server binary of "go tool task build" (E2E_SERVER_BIN),
// installs the CLI with install.sh, and runs setup and publish for a Maven and a raw project. It
// needs curl, Java and Maven on the PATH.

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// TestMacOS: on macOS, install.sh installs the CLI of the server, setup enrolls the computer
// and writes telesphorepo.toml, and publish runs mvn deploy for a Maven project and uploads a
// file to a raw repo.
func TestMacOS(t *testing.T) {
	t.Parallel()
	bin := os.Getenv("E2E_SERVER_BIN")
	if bin == "" {
		t.Fatal("set E2E_SERVER_BIN to the server binary of go tool task build")
	}
	s := startMacServer(t, bin)
	admin := &totpUser{name: "admin", pass: "admin-pass-macos-1234"}
	adminJar := s.signIn(t, admin)

	mvn, raw := "releases", "files"
	for _, r := range [][2]string{{mvn, "maven"}, {raw, "raw"}} {
		s.mustAPI(t, adminJar, "", http.MethodPost, "/api/repos", map[string]any{"name": r[0], "format": r[1], "anonymous_read": false}, nil)
	}
	dev := &totpUser{name: "dev", pass: "dev-pass-macos-1234"}
	s.mustAPI(t, adminJar, "", http.MethodPost, "/api/users", map[string]any{"username": dev.name, "password": dev.pass, "permissions": []string{}}, nil)
	s.mustAPI(t, adminJar, "", http.MethodPut, "/api/grants", map[string]any{"users": []string{dev.name}, "repos": []string{mvn, raw},
		"perms": []string{"repo.download", "repo.publish"}}, nil)
	devJar := s.signIn(t, dev)

	home := t.TempDir()
	app := filepath.Join(t.TempDir(), "app")
	files := filepath.Join(t.TempDir(), "files")
	for _, d := range []string{filepath.Join(app, "src", "main", "java", "com", "example"), files} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(app, "pom.xml"), `<project xmlns="http://maven.apache.org/POM/4.0.0">
  <modelVersion>4.0.0</modelVersion>
  <groupId>com.example</groupId>
  <artifactId>macos-demo</artifactId>
  <version>1.0</version>
  <properties>
    <maven.compiler.release>17</maven.compiler.release>
    <project.build.sourceEncoding>UTF-8</project.build.sourceEncoding>
  </properties>
</project>
`)
	writeFile(t, filepath.Join(app, "src", "main", "java", "com", "example", "Hello.java"),
		"package com.example;\n\npublic class Hello {\n  public static String hello() { return \"hello\"; }\n}\n")
	writeFile(t, filepath.Join(files, "hello.txt"), "hello from macOS\n")

	install := func(link string) string {
		return "curl -fsSL " + s.base + "/install.sh | sh -s -- setup '" + link + "'"
	}

	run(t, home, app, install(s.setupLink(t, devJar, dev, mvn)))
	wantProjectRepo(t, app, mvn)
	run(t, home, app, "telesphorepo publish")
	s.wantFile(t, admin, "/maven/"+mvn+"/com/example/macos-demo/1.0/macos-demo-1.0.jar", nil)

	run(t, home, files, install(s.setupLink(t, devJar, dev, raw)))
	wantProjectRepo(t, files, raw)
	run(t, home, files, "telesphorepo publish hello.txt")
	s.wantFile(t, admin, "/raw/"+raw+"/hello.txt", []byte("hello from macOS\n"))

	for _, name := range []string{"telesphorepo", "docker-credential-telesphorepo"} {
		if _, err := os.Stat(filepath.Join(home, ".local", "bin", name)); err != nil {
			t.Fatalf("install.sh did not make %s: %v", name, err)
		}
	}
}

// startMacServer starts the server binary with a new data folder and the bootstrap admin.
func startMacServer(t *testing.T, bin string) *nativeServer {
	t.Helper()
	return startNativeServer(t, bin, t.TempDir(), []string{"TR_BOOTSTRAP_ADMIN_USER=admin", "TR_BOOTSTRAP_ADMIN_PASSWORD=admin-pass-macos-1234"}, nil)
}

// setupLink makes a setup code of u for repo, as the Publish tab does, and returns its link.
func (s *nativeServer) setupLink(t *testing.T, jar http.CookieJar, u *totpUser, repo string) string {
	t.Helper()
	var c struct {
		Link string `json:"link"`
	}
	s.mustAPI(t, jar, "totp "+u.freshCode(t), http.MethodPost, "/api/publish/setup-codes", map[string]string{"repo": repo}, &c)
	if !strings.HasPrefix(c.Link, s.base+"/") {
		t.Fatalf("POST /api/publish/setup-codes for %s: link %q", repo, c.Link)
	}
	return c.Link
}

// wantFile fails the test unless u reads path with status 200, and the content want when it is
// not nil.
func (s *nativeServer) wantFile(t *testing.T, u *totpUser, path string, want []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, s.base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(u.name, u.pass)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || (want != nil && !bytes.Equal(b, want)) {
		t.Fatalf("GET %s: status %d, %d bytes", path, resp.StatusCode, len(b))
	}
}

// run runs script with sh in dir, as a person runs it on a Mac: HOME is home, ~/.local/bin comes
// first on the PATH, and no CI variable is set. It fails the test on a non-zero exit and returns
// the output.
func run(t *testing.T, home, dir, script string) string {
	t.Helper()
	cmd := exec.Command("sh", "-c", script)
	cmd.Dir = dir
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch {
		case k == "CI", k == "HOME", k == "PATH", strings.HasPrefix(k, "GITHUB_"), strings.HasPrefix(k, "ACTIONS_"), strings.HasPrefix(k, "JENKINS_"):
			continue
		}
		cmd.Env = append(cmd.Env, kv)
	}
	cmd.Env = append(cmd.Env, "HOME="+home, "PATH="+filepath.Join(home, ".local", "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	t.Logf("$ %s\n%s", script, out)
	if err != nil {
		t.Fatalf("%s: %v", script, err)
	}
	return string(out)
}

func writeFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// wantProjectRepo fails the test unless the telesphorepo.toml that setup wrote in dir names repo.
func wantProjectRepo(t *testing.T, dir, repo string) {
	t.Helper()
	name := filepath.Join(dir, "telesphorepo.toml")
	var p struct {
		Repo string `toml:"repo"`
	}
	if _, err := toml.DecodeFile(name, &p); err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	if p.Repo != repo {
		t.Fatalf("%s names repo %q, want %q", name, p.Repo, repo)
	}
}
