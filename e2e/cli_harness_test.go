//go:build clients

package e2e

import (
	"context"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/printer"
	"go/token"
	"go/types"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
)

// Zone clients: the Telesphorepo CLI of this repo, built on the host for Linux, runs in client
// containers with the real tools (git, Docker, Helm, ORAS, Maven, Gradle, apt, the install
// scripts) against a test server. After each run the harness lists the files of the container
// that hold a session token (trs_), a setup code (trsu_) or the CI token of the test, and fails
// on a match. A test passes setup links and CI tokens in the env, never in a file.

// cliPath is where the tests put the CLI in a container. The prelude of each script makes
// docker-credential-telesphorepo next to it.
const cliPath = "/usr/local/bin/telesphorepo"

const (
	scanBegin = "=== e2e secret scan ==="
	scanEnd   = "=== e2e secret scan end ==="
)

// secretScan prints each file of the root file system of the container, and of the folders in
// $E2E_SCAN, that holds a session token, a setup code or $E2E_SECRET. find -xdev stays on one
// file system, so /proc, /sys, /dev and the volumes of build caches stay out. A token starts a
// word: C++ symbols in compilers (cc1plus, rustdoc) hold "trs_" inside longer names such as "attrs_".
const secretScan = `echo '` + scanBegin + `'
for d in / $E2E_SCAN; do
	find "$d" -xdev -type f -size -65536k -print0 2>/dev/null | xargs -0 -r grep -lE '(^|[^A-Za-z0-9_])trsu?_[A-Za-z0-9_-]{40,}' 2>/dev/null
	if [ -n "${E2E_SECRET:-}" ]; then
		find "$d" -xdev -type f -size -65536k -print0 2>/dev/null | xargs -0 -r grep -lF -e "$E2E_SECRET" 2>/dev/null
	fi
done
echo '` + scanEnd + `'
`

// cliScript wraps script: the prelude, the script in a subshell, then the secret scan. The exit
// code is the script's.
func cliScript(script string) string {
	return "#!/bin/sh\nln -sf telesphorepo /usr/local/bin/docker-credential-telesphorepo\n(\n" + script + "\n)\nrc=$?\n" + secretScan + "exit $rc\n"
}

// scanned returns the output of a run of cliScript without the secret scan, and fails the test
// when the scan found a file.
func scanned(t testing.TB, what, out string) string {
	t.Helper()
	before, rest, ok := strings.Cut(out, scanBegin)
	if !ok {
		t.Fatalf("%s: the secret scan did not run:\n%s", what, out)
	}
	hits, _, ok := strings.Cut(rest, scanEnd)
	if !ok {
		t.Fatalf("%s: the secret scan did not end:\n%s", what, out)
	}
	var files []string
	for _, l := range strings.Split(hits, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			files = append(files, l)
		}
	}
	if len(files) > 0 {
		t.Fatalf("%s: these files hold a session token, a setup code or the CI token:\n%s\n--- output ---\n%s", what, strings.Join(files, "\n"), before)
	}
	return before
}

// cli is a CLI binary for the client containers.
type cli struct{ bin string }

// newCLI builds the CLI of this repo (a dev build).
func newCLI(t testing.TB) *cli { return &cli{bin: BuildCLI(t, "", "", nil)} }

// opts returns opts that run script with the CLI at cliPath, as root with HOME=/root (opts can
// set others). The secret scan also covers each volume of opts.Mounts other than the build
// caches.
func (c *cli) opts(script string, opts ClientOpts) ClientOpts {
	opts.Files = append(append([]ContainerFile{}, opts.Files...),
		ContainerFile{HostPath: c.bin, Path: cliPath, Mode: 0o755},
		ContainerFile{Path: "/e2e/run.sh", Content: cliScript(script)})
	opts.Cmd = []string{"sh /e2e/run.sh"}
	env := map[string]string{"HOME": "/root"}
	for k, v := range opts.Env {
		env[k] = v
	}
	var scan []string
	for vol, target := range opts.Mounts {
		if vol != M2Volume && vol != GradleVolume {
			scan = append(scan, target)
		}
	}
	env["E2E_SCAN"] = strings.Join(scan, " ")
	opts.Env = env
	if opts.User == "" {
		opts.User = "0"
	}
	if opts.Timeout == 0 {
		opts.Timeout = 15 * time.Minute
	}
	return opts
}

// run runs script with the CLI in image and returns the exit code and the output without the
// secret scan.
func (c *cli) run(t testing.TB, image, what, script string, opts ClientOpts) (int, string) {
	t.Helper()
	code, out := RunClient(t, image, c.opts(script, opts))
	return code, scanned(t, what, out)
}

// mustRun is run that fails the test on an exit code other than 0.
func (c *cli) mustRun(t testing.TB, image, what, script string, opts ClientOpts) string {
	t.Helper()
	code, out := c.run(t, image, what, script, opts)
	if code != 0 {
		t.Fatalf("%s: exit %d:\n%s", what, code, out)
	}
	return out
}

// start starts script with the CLI in image and returns at once. Read the output of the end with
// scanned.
func (c *cli) start(t testing.TB, image, script string, opts ClientOpts) *clientRun {
	t.Helper()
	return startClient(t, image, c.opts(script, opts))
}

// section returns the output between the line "=== name ===" and the next line that starts
// with "=== ".
func section(out, name string) string {
	_, rest, ok := strings.Cut(out, "=== "+name+" ===")
	if !ok {
		return ""
	}
	if i := strings.Index(rest, "\n=== "); i >= 0 {
		rest = rest[:i+1]
	}
	return rest
}

// wantText fails unless text holds each of subs.
func wantText(t testing.TB, what, text string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(text, s) {
			t.Fatalf("%s: no %q in:\n%s", what, s, text)
		}
	}
}

// wantNoText fails when text holds one of subs.
func wantNoText(t testing.TB, what, text string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if strings.Contains(text, s) {
			t.Fatalf("%s: %q in:\n%s", what, s, text)
		}
	}
}

// cliConst returns the integer constant name of cmd/telesphorepo/file, read from the source. Its
// value is a constant expression of literals, such as 64 << 20.
func cliConst(t testing.TB, file, name string) int64 {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join(repoRoot(t), "cmd", "telesphorepo", file), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.CONST {
			continue
		}
		for _, spec := range g.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, id := range vs.Names {
				if id.Name != name || i >= len(vs.Values) {
					continue
				}
				var src strings.Builder
				if err := printer.Fprint(&src, fset, vs.Values[i]); err != nil {
					t.Fatal(err)
				}
				tv, err := types.Eval(fset, nil, token.NoPos, src.String())
				if err != nil {
					t.Fatalf("the constant %s of %s: %v", name, file, err)
				}
				if tv.Value == nil || tv.Value.Kind() != constant.Int {
					t.Fatalf("the constant %s of %s is %s, not an integer", name, file, src.String())
				}
				v, exact := constant.Int64Val(tv.Value)
				if !exact {
					t.Fatalf("the constant %s of %s is %s, more than an int64", name, file, src.String())
				}
				return v
			}
		}
	}
	t.Fatalf("cmd/telesphorepo/%s has no constant %s", file, name)
	return 0
}

// projectFile is a telesphorepo.toml in dir for repo on s, as setup writes it.
func projectFile(s *Server, dir, repo string) ContainerFile {
	return ContainerFile{Path: dir + "/telesphorepo.toml", Content: "server = \"" + s.InternalURL + "\"\nrepo = \"" + repo + "\"\n"}
}

// ---------------------------------------------------------------- long-running clients

// approveCodeRe matches the line of the device flow of the CLI: "Open <link> and approve code
// ABCD-EFGH".
var approveCodeRe = regexp.MustCompile(`Open \S+ and approve code ([A-Z]{4}-[A-Z]{4})`)

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
		testcontainers.WithLabels(e2eLabels),
		testcontainers.WithEntrypoint("sh", "-c"),
		// One stream: the container log does not keep the order of stdout and stderr.
		testcontainers.WithCmd("exec 2>&1; "+strings.Join(opts.Cmd, " ")),
		testcontainers.WithEnv(clientEnv(opts)),
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

// approveDevice approves a user code of the device flow the way a developer does in the browser:
// sign in as user, then approve with a fresh TOTP code.
func approveDevice(t testing.TB, s *Server, user, userCode string) {
	t.Helper()
	sess := s.LoginTOTP(t, user)
	if st, b := s.ApproveEnrollment(t, sess, user, userCode); st/100 != 2 {
		t.Fatalf("approve code %s as %s: status %d: %s", userCode, user, st, b)
	}
}

// ---------------------------------------------------------------- repo contents

// assetPaths lists the paths of every asset under prefix in repo (keyset pages).
func assetPaths(t testing.TB, s *Server, repo, prefix string) []string {
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
	have := assetPaths(t, s, repo, prefix)
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

// publishLogItem is one row of GET /api/publish/log.
type publishLogItem struct {
	Repo     string `json:"repo"`
	Via      string `json:"via"`
	Subject  string `json:"subject"`
	DeviceID *int64 `json:"device_id"`
}

// publishLog returns the publish sessions of repo, newest first.
func publishLog(t testing.TB, s *Server, repo string) []publishLogItem {
	t.Helper()
	var log struct {
		Items []publishLogItem `json:"items"`
	}
	s.MustAPI(t, http.MethodGet, "/api/publish/log", nil, s.Admin(), &log)
	var out []publishLogItem
	for _, it := range log.Items {
		if it.Repo == repo {
			out = append(out, it)
		}
	}
	return out
}
