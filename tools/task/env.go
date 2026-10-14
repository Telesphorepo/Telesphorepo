package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	modulePath = "github.com/Telesphorepo/Telesphorepo"
	webDir     = "web"
	stampFile  = "web/node_modules/.telesphorepo-stamp"
)

// enterRoot changes into the repository root (the directory whose go.mod declares this
// module), so every command works from any subdirectory.
func enterRoot() error {
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	for {
		if b, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil && moduleOf(string(b)) == modulePath {
			return os.Chdir(dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return errors.New("not inside the telesphorepo repository (no go.mod for module " + modulePath + " found)")
		}
		dir = parent
	}
}

func moduleOf(gomod string) string {
	for _, l := range strings.Split(gomod, "\n") {
		if f := strings.Fields(l); len(f) == 2 && f[0] == "module" {
			return strings.Trim(f[1], `"`)
		}
	}
	return ""
}

// goDirective returns the "go" version from go.mod (e.g. "1.26.0").
func goDirective() (string, error) {
	b, err := os.ReadFile("go.mod")
	if err != nil {
		return "", err
	}
	for _, l := range strings.Split(string(b), "\n") {
		if f := strings.Fields(l); len(f) == 2 && f[0] == "go" {
			return f[1], nil
		}
	}
	return "", errors.New("go.mod has no go directive")
}

// checkGo verifies the running toolchain satisfies go.mod. It returns the toolchain version.
func checkGo() (string, error) {
	have := strings.TrimPrefix(strings.Fields(runtime.Version())[0], "go")
	want, err := goDirective()
	if err != nil {
		return have, err
	}
	if strings.HasPrefix(have, "devel") {
		return have, nil
	}
	if versionLess(have, want) {
		return have, fmt.Errorf("Go %s is too old: go.mod needs Go %s or newer (https://go.dev/dl/)", have, want)
	}
	return have, nil
}

// versionLess compares dotted numeric versions ("1.26", "1.26.0", "1.27rc1").
func versionLess(a, b string) bool {
	pa, pb := versionParts(a), versionParts(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] < pb[i]
		}
	}
	return false
}

func versionParts(v string) [3]int {
	var out [3]int
	for i, p := range strings.SplitN(v, ".", 3) {
		end := 0
		for end < len(p) && p[end] >= '0' && p[end] <= '9' {
			end++
		}
		out[i], _ = strconv.Atoi(p[:end])
	}
	return out
}

// checkNode verifies Node.js (which runs Vite) meets the major version in .node-version.
// It returns the installed version.
func checkNode() (string, error) {
	b, err := os.ReadFile(".node-version")
	if err != nil {
		return "", errors.New(".node-version missing")
	}
	want := strings.TrimPrefix(strings.TrimSpace(string(b)), "v")
	p := lookPath("node")
	if p == "" {
		return "", fmt.Errorf("Node.js %s+ not found (Vite needs it): %s", want, nodeHint(want))
	}
	have := strings.TrimPrefix(firstLine(p, "--version"), "v")
	if versionLess(have, want) {
		return have, fmt.Errorf("Node.js %s is too old, need %s+: %s", have, want, nodeHint(want))
	}
	return have, nil
}

func nodeHint(major string) string {
	switch runtime.GOOS {
	case "darwin":
		return "brew install node@" + major + " (and put it first on PATH), or fnm/nvm/volta"
	case "windows":
		return "winget install OpenJS.NodeJS, or fnm/nvm-windows"
	}
	return "install Node " + major + " via your package manager or fnm/nvm"
}

// pkgManager is the JS package manager used for web/: bun (preferred) or npm.
type pkgManager struct {
	Name string // "bun" or "npm"
	Path string
}

func findPkgManager() (pkgManager, error) {
	for _, name := range []string{"bun", "npm"} {
		if p := lookPath(name); p != "" {
			return pkgManager{Name: name, Path: p}, nil
		}
	}
	return pkgManager{}, errors.New("neither bun nor npm found on PATH; install bun (preferred): https://bun.sh")
}

// lookPath finds a tool, also trying Windows launcher names (bun.exe, npm.cmd).
func lookPath(name string) string {
	names := []string{name}
	if runtime.GOOS == "windows" {
		names = append(names, name+".exe", name+".cmd")
	}
	for _, n := range names {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	return ""
}

// runArgs returns the argv to run a package.json script in web/ ("bun run x" / "npm run x").
// Extra args go to the script (npm needs "--" before them).
func (pm pkgManager) runArgs(script string, extra ...string) []string {
	args := []string{"run", script}
	if len(extra) > 0 {
		if pm.Name == "npm" {
			args = append(args, "--")
		}
		args = append(args, extra...)
	}
	return args
}

// prepare runs the checks every web-touching command needs and installs web deps when
// node_modules is missing or package.json/bun.lock changed since the last install.
func prepare(ctx context.Context) (pkgManager, error) {
	if _, err := checkGo(); err != nil {
		return pkgManager{}, err
	}
	if _, err := checkNode(); err != nil {
		return pkgManager{}, err
	}
	pm, err := findPkgManager()
	if err != nil {
		return pm, err
	}
	if !depsStale() {
		return pm, nil
	}
	var args []string
	if pm.Name == "bun" {
		args = []string{"install"}
	} else {
		note("bun is the preferred package manager (https://bun.sh); falling back to npm")
		// web/bun.lock is the committed lockfile; don't leave a stray package-lock.json behind.
		args = []string{"install", "--no-package-lock", "--no-audit", "--no-fund"}
	}
	step("installing web dependencies (%s %s)", pm.Name, strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, pm.Path, args...)
	cmd.Dir = webDir
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return pm, fmt.Errorf("%s install in web/ failed: %w", pm.Name, err)
	}
	if err := os.WriteFile(stampFile, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644); err != nil {
		return pm, err
	}
	return pm, nil
}

func depsStale() bool {
	if _, err := os.Stat(filepath.Join(webDir, "node_modules")); err != nil {
		return true
	}
	stamp, err := os.Stat(stampFile)
	if err != nil {
		return true
	}
	for _, f := range []string{"package.json", "bun.lock"} {
		if fi, err := os.Stat(filepath.Join(webDir, f)); err == nil && fi.ModTime().After(stamp.ModTime()) {
			return true
		}
	}
	return false
}

// gitVersion is "git describe --tags --always --dirty", or "dev" without git/history.
func gitVersion(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "git", "describe", "--tags", "--always", "--dirty").Output()
	if v := strings.TrimSpace(string(out)); err == nil && v != "" {
		return v
	}
	return "dev"
}

func exe(p string) string {
	if runtime.GOOS == "windows" {
		return p + ".exe"
	}
	return p
}

// firstLine of a command's output, for doctor.
func firstLine(name string, args ...string) string {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return ""
	}
	s := bufio.NewScanner(strings.NewReader(string(out)))
	if s.Scan() {
		return strings.TrimSpace(s.Text())
	}
	return ""
}

// ---- output helpers ----

var useColor = func() bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	fi, err := os.Stdout.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	return enableVT()
}()

func color(code, s string) string {
	if !useColor {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func red(s string) string    { return color("31", s) }
func green(s string) string  { return color("32", s) }
func yellow(s string) string { return color("33", s) }
func cyan(s string) string   { return color("36", s) }
func bold(s string) string   { return color("1", s) }
func dim(s string) string    { return color("2", s) }

func step(format string, a ...any) {
	out.line(cyan("› ") + fmt.Sprintf(format, a...))
}

func note(format string, a ...any) {
	out.line(yellow("! ") + fmt.Sprintf(format, a...))
}
