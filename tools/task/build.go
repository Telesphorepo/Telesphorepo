package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/clidist"
	"github.com/Telesphorepo/Telesphorepo/internal/containerengine"
	"github.com/Telesphorepo/Telesphorepo/internal/e2erun"
)

const imageTag = "telesphorepo:local"

// build builds the UI, the six CLI archives and the edge Worker folder, then
// bin/telesphorepo-server with them embedded; with image it also builds the container image with
// the detected engine (which builds its own CLI archives and edge Worker folder). It returns the
// binary path.
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
	if err := cliArchives(ctx, version, clidist.Targets); err != nil {
		return "", err
	}
	if err := edgeWorker(ctx, version, false); err != nil {
		return "", err
	}
	bin := exe(filepath.Join("bin", "telesphorepo-server"))
	step("building %s (version %s)", filepath.ToSlash(bin), version)
	if err := runStep(ctx, ".", []string{"CGO_ENABLED=0"}, "go", "build", "-trimpath",
		"-ldflags", "-s -w -X main.version="+version, "-o", bin, "./cmd/telesphorepo-server"); err != nil {
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

// testZones maps each e2e zone to its go test arguments before the package. The -timeout bounds
// the whole binary, its setup (pulls, the Maven warm-up) too; the budget of the tests themselves
// is e2erun's, which TestMain sets.
var testZones = map[string][]string{
	"core":    {"-tags", "e2e", "-timeout", "30m"},
	"import":  {"-tags", "import", "-timeout", "30m"},
	"scale":   {"-tags", "scale", "-run", "TestScale", "-timeout", "90m"},
	"clients": {"-tags", "clients", "-timeout", "30m"},
}

// test runs e2e zones with the detected container engine. The leading arguments name the zones
// ("core", "import", "clients", "all" for the three; core when none), which run one after
// another; "--now" skips the cooldown after the person at the terminal confirms it; then
// "--shard i/n" runs only every n-th top-level test of one zone from the i-th on (CI runs a zone
// on n machines at once); the remaining args pass to go test. "scale" runs the scale zone alone,
// with no engine and none of the rules of an e2e run.
//
// A run goes through the gate (openGate): one run at a time, e2erun.Cooldown between the runs of
// a checkout, a change since the last run. Then it removes what a killed run left, builds the
// server image once, and runs each zone after calibrating its budget to this computer.
func test(ctx context.Context, args []string) error {
	if _, err := checkGo(); err != nil {
		return err
	}
	if len(args) > 0 && args[0] == "scale" {
		return testScale(ctx, args[1:])
	}
	var zones []string
	now, final := false, false
names:
	for len(args) > 0 {
		switch args[0] {
		case "core", "import", "clients":
			zones = append(zones, args[0])
		case "all":
			zones = append(zones, "core", "import", "clients")
		case "--now":
			now = true
		case "--final":
			final = true
		case "scale":
			return usageErr("the scale zone runs alone: go tool task test scale")
		default:
			break names
		}
		args = args[1:]
	}
	if len(zones) == 0 {
		zones = []string{"core"}
	}
	// Each zone once, core first: most changes need it, so its result comes soonest.
	zones = slices.DeleteFunc([]string{"core", "clients", "import"}, func(z string) bool { return !slices.Contains(zones, z) })
	shard, shards := 0, 0
	if len(args) > 1 && args[0] == "--shard" {
		if _, err := fmt.Sscanf(args[1], "%d/%d", &shard, &shards); err != nil || shards < 1 || shard < 1 || shard > shards {
			return usageErr(fmt.Sprintf("--shard takes i/n with 1 <= i <= n, not %q", args[1]))
		}
		if len(zones) > 1 || slices.Contains(args[2:], "-run") {
			return usageErr("--shard takes one zone and no -run")
		}
		args = args[2:]
	}

	if final && (shards > 0 || slices.Contains(args, "-run") || slices.ContainsFunc(args, func(a string) bool { return strings.HasPrefix(a, "-run=") })) {
		return usageErr("--final runs whole zones: it takes no -run and no --shard")
	}
	g, err := openGate(ctx, now, final, zones)
	if err != nil {
		return err
	}
	passed := false
	defer func() { g.close(passed) }()

	eng, err := containerengine.Detect(ctx)
	if err != nil {
		return err
	}
	eng.ApplyEnv()
	step("container engine: %s %s", eng.Name, dim(eng.Host))
	sweepLeftovers(ctx, eng)
	image := os.Getenv("E2E_IMAGE")
	if image == "" {
		image = e2erun.Image
		if err := buildE2EImage(ctx, eng); err != nil {
			return err
		}
	}
	// The zones run one after another: on one computer, zones side by side compete for its CPU and
	// memory and finish later than in turn. Each zone gets the budget of its own calibration, at
	// most what is left of the run's e2erun.MaxRun.
	env := []string{e2erun.TicketEnv + "=" + g.ticket, "E2E_IMAGE=" + image}
	start := time.Now()
	var failed []string
	var last error
	for _, zone := range zones {
		left := (e2erun.MaxRun - time.Since(start)).Round(time.Second)
		if left < time.Minute {
			out.line(red("✗ ") + fmt.Sprintf("zone %s: not started, the run has used its %s", zone, e2erun.MaxRun))
			failed, last = append(failed, zone), exitError{1}
			continue
		}
		batches := 1
		if shards == 0 {
			batches = ciRunners[zone]
		}
		cal, err := calibrate(ctx, eng, image, batches)
		if err != nil {
			return fmt.Errorf("calibrate: %w", err)
		}
		budget := min(e2erun.Budget(cal), left)
		if err := g.setBudget(cal, budget); err != nil {
			return err
		}
		step("zone %s: calibration %s for %d CI runners' work (%s for one on CI): its tests have %s, %s left in the run",
			zone, cal.Round(100*time.Millisecond), batches, e2erun.ReferenceCalibration, bold(budget.String()), left)

		argv := append([]string{"go", "test"}, testZones[zone]...)
		argv = append(argv, "./e2e/...", "-count=1", "-v")
		if shards > 0 {
			run, err := shardRun(ctx, zone, shard, shards)
			if err != nil {
				return err
			}
			argv = append(argv, "-run", run)
		}
		argv = append(argv, args...)
		step("zone %s: %s", zone, strings.Join(argv, " "))
		err = runStep(ctx, ".", env, argv...)
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
	passed = len(failed) == 0
	return last
}

// testScale runs the scale zone (TestScale) and prints the verdict of the run, which the test
// writes to the file of E2E_SCALE_VERDICT. FAIL fails as the test does; NOISY exits 3 and
// INCONCLUSIVE exits 4, so neither reads as a pass nor as a regression.
func testScale(ctx context.Context, args []string) error {
	if len(args) > 1 && args[0] == "--shard" {
		return usageErr("the scale zone has one test: it takes no --shard")
	}
	f, err := os.CreateTemp("", "telesphorepo-scale-verdict-")
	if err != nil {
		return err
	}
	verdictFile := f.Name()
	f.Close()
	defer os.Remove(verdictFile)

	argv := append([]string{"go", "test"}, testZones["scale"]...)
	argv = append(argv, "./e2e/...", "-count=1", "-v")
	argv = append(argv, args...)
	step("zone scale: %s", strings.Join(argv, " "))
	runErr := runStep(ctx, ".", []string{"E2E_SCALE_VERDICT=" + verdictFile}, argv...)
	if ctx.Err() != nil {
		return exitError{130}
	}
	b, _ := os.ReadFile(verdictFile)
	verdict, detail, _ := strings.Cut(strings.TrimSpace(string(b)), "\n")
	switch verdict {
	case "PASS":
		out.line(green("✓ ") + "scale: " + bold("PASS") + dim("  "+detail))
	case "FAIL":
		out.line(red("✗ ") + "scale: " + bold("FAIL") + "  " + detail)
	case "NOISY":
		out.line(yellow("! ") + "scale: " + bold("NOISY") + "  " + detail)
		if runErr == nil {
			return exitError{3}
		}
	case "INCONCLUSIVE":
		out.line(yellow("? ") + "scale: " + bold("INCONCLUSIVE") + "  " + detail)
		if runErr == nil {
			return exitError{4}
		}
	default:
		out.line(red("✗ ") + "scale: no verdict (the test stopped before it measured)")
	}
	if runErr != nil {
		return childExit(runErr)
	}
	return nil
}

// shardRun returns the -run pattern of shard i of n of a zone: its top-level tests by name
// (go test -list), every n-th from the i-th. A zone whose own args hold -run (scale) lists only
// those tests.
func shardRun(ctx context.Context, zone string, i, n int) (string, error) {
	argv := append([]string{"test"}, testZones[zone]...)
	if !slices.Contains(argv, "-run") {
		argv = append(argv, "-list", ".")
	} else {
		argv = append(argv, "-list", argv[slices.Index(argv, "-run")+1])
	}
	cmd := exec.CommandContext(ctx, "go", append(argv, "./e2e/")...)
	cmd.Stderr = os.Stderr
	b, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("list the tests of zone %s: %w", zone, err)
	}
	var names []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); strings.HasPrefix(l, "Test") {
			names = append(names, l)
		}
	}
	slices.Sort(names)
	var mine []string
	for k := i - 1; k < len(names); k += n {
		mine = append(mine, names[k])
	}
	if len(mine) == 0 {
		return "", fmt.Errorf("zone %s has %d tests: shard %d/%d has none", zone, len(names), i, n)
	}
	step("shard %d/%d: %d of %d tests", i, n, len(mine), len(names))
	return "^(" + strings.Join(mine, "|") + ")$", nil
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
