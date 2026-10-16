package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/Telesphorepo/Telesphorepo/internal/containerengine"
)

const imageTag = "telesphorepo:local"

// build builds the UI, then bin/telesphorepo with it embedded; with image it also builds
// the container image with the detected engine. It returns the binary path.
func build(ctx context.Context, image bool) (string, error) {
	pm, err := prepare(ctx)
	if err != nil {
		return "", err
	}
	step("building web UI (%s run build)", pm.Name)
	if err := runStep(ctx, webDir, nil, append([]string{pm.Path}, pm.runArgs("build")...)...); err != nil {
		return "", fmt.Errorf("web build failed: %w", err)
	}
	version := gitVersion(ctx)
	bin := exe(filepath.Join("bin", "telesphorepo"))
	step("building %s (version %s)", filepath.ToSlash(bin), version)
	if err := runStep(ctx, ".", []string{"CGO_ENABLED=0"}, "go", "build", "-trimpath",
		"-ldflags", "-s -w -X main.version="+version, "-o", bin, "./cmd/telesphorepo"); err != nil {
		return "", fmt.Errorf("go build failed: %w", err)
	}
	out.line(green("✓ ") + "built " + bold(filepath.ToSlash(bin)))
	if !image {
		return bin, nil
	}

	eng, err := containerengine.Detect(ctx)
	if err != nil {
		return bin, err
	}
	eng.ApplyEnv()
	step("building image %s with %s", imageTag, eng.Name)
	if err := runStep(ctx, ".", nil, eng.CLI, "build", "-t", imageTag, "--build-arg", "VERSION="+version, "."); err != nil {
		return bin, fmt.Errorf("image build failed: %w", err)
	}
	out.line(green("✓ ") + "built image " + bold(imageTag) + dim("  (run: "+filepath.Base(eng.CLI)+" run -p 8080:8080 -v telesphorepo-data:/data "+imageTag+")"))
	return bin, nil
}

// preview builds, then runs the production binary in dev mode (UI embedded) on :8080.
func preview(ctx context.Context) error {
	bin, err := build(ctx, false)
	if err != nil {
		return err
	}
	b := newBanner("http://localhost:8080", "")
	stop := make(chan struct{})
	defer close(stop)
	api, err := startChild("api", "35", ".", []string{"TELESPHOREPO_RUNNER=1"}, b.filter, "./"+filepath.ToSlash(bin), "dev")
	if err != nil {
		return err
	}
	go b.watch(stop)
	select {
	case <-ctx.Done():
		out.line("")
		step("stopping")
		api.stop()
		return nil
	case <-api.done:
		return childExit(api.err)
	}
}

// testZones maps each e2e zone to its go test arguments before the package.
var testZones = map[string][]string{
	"core":    {"-tags", "e2e", "-timeout", "30m"},
	"import":  {"-tags", "import", "-timeout", "30m"},
	"scale":   {"-tags", "scale", "-run", "TestScale", "-timeout", "60m"},
	"clients": {"-tags", "clients", "-timeout", "40m"},
}

// test runs e2e zones with the detected container engine. An optional first argument picks
// the zone ("import", "scale", "clients" or "all"; the default is core); the remaining args
// pass to go test. "all" runs core, import, scale and clients one after the other and fails if
// any fails.
func test(ctx context.Context, args []string) error {
	if _, err := checkGo(); err != nil {
		return err
	}
	zones := []string{"core"}
	if len(args) > 0 {
		switch args[0] {
		case "import", "scale", "clients":
			zones, args = []string{args[0]}, args[1:]
		case "all":
			zones, args = []string{"core", "import", "scale", "clients"}, args[1:]
		}
	}
	eng, err := containerengine.Detect(ctx)
	if err != nil {
		return err
	}
	eng.ApplyEnv()
	step("container engine: %s %s", eng.Name, dim(eng.Host))

	var failed []string
	var last error
	for _, zone := range zones {
		argv := append([]string{"go", "test"}, testZones[zone]...)
		argv = append(argv, "./e2e/...", "-count=1", "-v")
		argv = append(argv, args...)
		step("zone %s: %s", zone, strings.Join(argv, " "))
		err := runStep(ctx, ".", nil, argv...)
		if ctx.Err() != nil {
			return exitError{130}
		}
		if err != nil {
			failed, last = append(failed, zone), childExit(err)
		}
	}
	if len(zones) > 1 && len(failed) > 0 {
		out.line(red("✗ ") + "failed zones: " + strings.Join(failed, ", "))
	}
	return last
}

// doctor prints what was detected and fails when a required tool is missing.
func doctor(ctx context.Context) error {
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	ok, warn, bad := green("ok"), yellow("--"), red("MISSING")
	failed := false
	row := func(status, name, detail string) { fmt.Fprintf(tw, "  %s\t%s\t%s\n", status, name, detail) }

	rootErr := enterRoot()
	if rootErr != nil {
		row(bad, "repo", rootErr.Error())
		failed = true
	}

	have, err := checkGo()
	switch {
	case rootErr != nil:
		row(ok, "go", have)
	case err != nil:
		row(bad, "go", err.Error())
		failed = true
	default:
		want, _ := goDirective()
		row(ok, "go", have+dim("  (go.mod needs "+want+")"))
	}

	if rootErr == nil {
		if v, err := checkNode(); err != nil {
			row(bad, "node", err.Error())
			failed = true
		} else {
			row(ok, "node", v)
		}
	}

	bunPath, npmPath := lookPath("bun"), lookPath("npm")
	switch {
	case bunPath != "":
		row(ok, "bun", firstLine(bunPath, "--version")+dim("  "+bunPath))
	case npmPath != "":
		row(warn, "bun", "not found; using npm (bun preferred: https://bun.sh)")
	default:
		row(bad, "bun", "neither bun nor npm found; install bun: https://bun.sh")
		failed = true
	}
	if npmPath != "" {
		row(ok, "npm", firstLine(npmPath, "--version")+dim("  (fallback)"))
	}
	if rootErr == nil {
		if depsStale() {
			row(warn, "web deps", "not installed or out of date; dev/build install them automatically")
		} else {
			row(ok, "web deps", "installed")
		}
	}

	if g := lookPath("git"); g != "" {
		row(ok, "git", firstLine(g, "--version"))
	} else {
		row(warn, "git", "not found; builds are stamped version \"dev\"")
	}

	eng, err := containerengine.Detect(ctx)
	if err != nil {
		row(warn, "engine", "no Docker/Podman (only needed for test and build image)")
		tw.Flush()
		for _, l := range strings.Split(err.Error(), "\n") {
			fmt.Println("        " + dim(l))
		}
	} else {
		detail := eng.Name + dim("  "+eng.CLI)
		if eng.Host != "" {
			detail += dim("  DOCKER_HOST=" + eng.Host)
		}
		row(ok, "engine", detail)
	}
	tw.Flush()

	if failed {
		fmt.Println("\n" + red("Something required is missing; see above."))
		return exitError{1}
	}
	fmt.Println("\n" + green("Ready.") + " Run " + bold("go tool task dev") + " to start.")
	return nil
}
