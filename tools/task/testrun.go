package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"

	"github.com/Telesphorepo/Telesphorepo/internal/containerengine"
	"github.com/Telesphorepo/Telesphorepo/internal/e2erun"
)

// An e2e run goes through a gate: one run at a time on the computer, e2erun.Cooldown between any
// two runs on it, and code that no run on it has tested yet. The ledger is one per computer, not
// per checkout, so a run from another worktree or a subagent counts too. The gate hands the run a
// ticket that the test binaries check (e2erun.CheckTicket), so "go test" alone cannot skip it.

// ledger is what the gate keeps of the runs on this computer.
type ledger struct {
	LastRun time.Time `json:"last_run"` // the end of the last run; its start while it runs
	Refused time.Time `json:"refused"`  // the last start refused by the cooldown
	Trees   []string  `json:"trees"`    // treeFingerprint of the latest runs, newest last
}

// ledgerTrees is how many tested trees the ledger remembers.
const ledgerTrees = 50

type gate struct {
	ticket     string
	runFile    string
	ledgerFile string
	ledger     ledger
}

const cooldownRefusal = `the e2e tests ran on this computer less than %s ago. Running them this often means the work is not batched.
Do not wait for the cooldown: no sleep, no retry loop, no polling. Every refused start, this one too, starts the %s again.
Go on with the next part of the work without running tests. Run the tests once, when a batch of changes is done and committed,
and name every zone that batch needs in that one run (e.g. go tool task test core clients).
The cooldown is one for the whole computer: a subagent or another worktree does not get around it. The main agent runs the tests.`

const unchangedRefusal = `this code has already been tested on this computer (the same commit and the same uncommitted changes):
running the same code again tests nothing new. Change the code first, commit it, then run the tests once.
A flaky test is a bug to find and fix, not a reason to run the tests again.`

// openGate passes a run through the gate, or says why not. now skips the cooldown and the check
// for changes after the person at the terminal confirms it.
func openGate(ctx context.Context, now bool) (*gate, error) {
	dir, err := e2erun.Dir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	root, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	g := &gate{ledgerFile: filepath.Join(dir, "ledger.json")}
	if b, err := os.ReadFile(g.ledgerFile); err == nil {
		_ = json.Unmarshal(b, &g.ledger)
	}
	tree := treeFingerprint(ctx)

	if now {
		if err := confirmNow(); err != nil {
			return nil, err
		}
	} else {
		last := g.ledger.LastRun
		if g.ledger.Refused.After(last) {
			last = g.ledger.Refused
		}
		if !g.ledger.LastRun.IsZero() && time.Since(last) < e2erun.Cooldown {
			g.ledger.Refused = time.Now()
			g.save()
			return nil, fmt.Errorf(cooldownRefusal, e2erun.Cooldown, e2erun.Cooldown)
		}
		if tree != "" && slices.Contains(g.ledger.Trees, tree) {
			return nil, errors.New(unchangedRefusal)
		}
	}

	if err := g.lock(dir, root); err != nil {
		return nil, err
	}
	// The start counts as the last run until the end replaces it, so a killed run still has its
	// cooldown.
	g.ledger.LastRun, g.ledger.Refused = time.Now(), time.Time{}
	if tree != "" {
		g.ledger.Trees = append(g.ledger.Trees, tree)
		if n := len(g.ledger.Trees); n > ledgerTrees {
			g.ledger.Trees = g.ledger.Trees[n-ledgerTrees:]
		}
	}
	g.save()
	return g, nil
}

// lock takes the one run of the computer: e2erun.RunningFile, created exclusively. A file of a
// process that is gone is a killed run's, and is taken over.
func (g *gate) lock(dir, root string) error {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	g.ticket = hex.EncodeToString(b)
	f, err := e2erun.RunningFile()
	if err != nil {
		return err
	}
	g.runFile = f
	run := e2erun.Running{PID: os.Getpid(), Root: root, Ticket: g.ticket, Started: time.Now()}
	for range 2 {
		h, err := os.OpenFile(f, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			h.Close()
			return g.writeRunning(run)
		}
		if !errors.Is(err, fs.ErrExist) {
			return err
		}
		if r, err := e2erun.ReadRunning(); err == nil && r.PID != os.Getpid() && processAlive(r.PID) {
			return fmt.Errorf("an e2e run is already going on this computer (pid %d in %s, since %s). One run at a time: "+
				"go on with your work and run the tests once that run has ended", r.PID, r.Root, r.Started.Format(time.TimeOnly))
		}
		_ = os.Remove(f)
	}
	return fmt.Errorf("take %s: another run took it at the same time", f)
}

func (g *gate) writeRunning(r e2erun.Running) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(g.runFile, b, 0o600)
}

// setBudget records the calibration and the budget of the zone that runs next, which its test
// binary reads.
func (g *gate) setBudget(calibration, budget time.Duration) error {
	r, err := e2erun.ReadRunning()
	if err != nil {
		return err
	}
	r.Calibration, r.Budget = calibration, budget
	return g.writeRunning(r)
}

// close ends the run: its end starts the cooldown, and the computer is free for the next run.
func (g *gate) close() {
	g.ledger.LastRun = time.Now()
	g.save()
	if r, err := e2erun.ReadRunning(); err == nil && r.Ticket == g.ticket {
		_ = os.Remove(g.runFile)
	}
}

func (g *gate) save() {
	if b, err := json.MarshalIndent(g.ledger, "", "  "); err == nil {
		_ = os.WriteFile(g.ledgerFile, b, 0o600)
	}
}

// treeFingerprint is a hash of the checkout's commit, its uncommitted changes and its untracked
// files; "" outside a git checkout.
func treeFingerprint(ctx context.Context) string {
	git := func(args ...string) ([]byte, error) { return exec.CommandContext(ctx, "git", args...).Output() }
	h := sha256.New()
	for _, args := range [][]string{{"rev-parse", "HEAD"}, {"diff", "HEAD", "--binary"}} {
		b, err := git(args...)
		if err != nil {
			return ""
		}
		h.Write(b)
	}
	b, err := git("ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return ""
	}
	for _, name := range strings.Split(string(b), "\x00") {
		if name == "" {
			continue
		}
		h.Write([]byte(name))
		if c, err := os.ReadFile(name); err == nil {
			s := sha256.Sum256(c)
			h.Write(s[:])
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// confirmNow asks the person at the terminal to skip the cooldown and the check for changes.
// Without a terminal (an agent, a script) it refuses.
func confirmNow() error {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return errors.New("--now asks the person at the terminal, and there is none. The cooldown and the check for changes " +
			"are not skipped for an agent or a script: batch the work, and run the tests once it is done")
	}
	fmt.Print("Skip the cooldown and the check for changes for this run? Type now to confirm: ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	if strings.TrimSpace(line) != "now" {
		return errors.New("not confirmed: the run did not start")
	}
	return nil
}

// sweepLeftovers removes the containers, networks and volumes that an earlier killed run left.
// It runs while the gate is held, so no other run's containers exist.
func sweepLeftovers(ctx context.Context, eng containerengine.Engine) {
	list := func(args ...string) []string {
		out, err := exec.CommandContext(ctx, eng.CLI, args...).Output()
		if err != nil {
			return nil
		}
		return strings.Fields(string(out))
	}
	remove := func(kind string, args []string, names []string) {
		if len(names) == 0 {
			return
		}
		if out, err := exec.CommandContext(ctx, eng.CLI, append(args, names...)...).CombinedOutput(); err != nil {
			note("remove leftover %s: %v\n%s", kind, err, out)
			return
		}
		step("removed %d leftover %s of an earlier run", len(names), kind)
	}
	remove("containers", []string{"rm", "-f"}, list("ps", "-aq", "--filter", "label=telesphorepo.e2e"))
	remove("networks", []string{"network", "rm", "-f"}, list("network", "ls", "-q", "--filter", "label=telesphorepo.e2e"))
	var vols []string
	for _, v := range list("volume", "ls", "--format", "{{.Name}}") {
		if e2erun.LeftoverVolume.MatchString(v) {
			vols = append(vols, v)
		}
	}
	remove("volumes", []string{"volume", "rm", "-f"}, vols)
}

// buildE2EImage builds the server image of the run (docker: BuildKit; podman: buildah). With
// Docker it adds --load: a builder other than Docker's own (CI's persistent BuildKit) keeps its
// result to itself unless asked to load it into the engine the tests start containers in.
func buildE2EImage(ctx context.Context, eng containerengine.Engine) error {
	args := []string{eng.CLI, "build"}
	if eng.Name == "docker" {
		args = append(args, "--load")
	}
	args = append(args, "-q", "-t", e2erun.Image, "-f", "Dockerfile", ".")
	step("building the server image %s", e2erun.Image)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Env = append(os.Environ(), "DOCKER_BUILDKIT=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		os.Stderr.Write(out)
		return fmt.Errorf("build the server image: %w", err)
	}
	return nil
}

// The calibration batch: calibrationRounds rounds of calibrationServers servers of the run's
// image, started at once, each until it answers /healthz and a sign-in, then removed. That is the
// work every e2e test starts with, on the engine the tests use. Then calibrationAmd64Runs runs at
// once of an amd64 image, which an engine of another architecture emulates, as it does the
// RHEL-family clients of the tests.
//
// A zone does on one computer the work that CI spreads over ciRunners machines, so its
// calibration runs that many batches at once: the budget then compares this computer with that
// many CI runners, each running one batch (e2erun.ReferenceCalibration).
const (
	calibrationServers   = 6
	calibrationRounds    = 2
	calibrationAmd64Runs = 4
	calibrationAmd64     = "docker.io/rockylinux/rockylinux:9"
)

// ciRunners is the number of CI machines that run each zone (the core zone's shards in
// .github/workflows/ci.yml); a run of one shard is one machine's work.
var ciRunners = map[string]int{"core": 4, "import": 1, "clients": 1}

// calibrate times batches calibration batches at once on this computer.
func calibrate(ctx context.Context, eng containerengine.Engine, image string, batches int) (time.Duration, error) {
	if out, err := exec.CommandContext(ctx, eng.CLI, "pull", "-q", "--platform", "linux/amd64", calibrationAmd64).CombinedOutput(); err != nil {
		return 0, fmt.Errorf("pull %s: %w\n%s", calibrationAmd64, err, out)
	}
	start := time.Now()
	err := together(batches, func() error { return calibrationBatch(ctx, eng, image) })
	return time.Since(start), err
}

// calibrationBatch runs one calibration batch.
func calibrationBatch(ctx context.Context, eng containerengine.Engine, image string) error {
	for range calibrationRounds {
		if err := together(calibrationServers, func() error { return calibrationServer(ctx, eng, image) }); err != nil {
			return err
		}
	}
	return together(calibrationAmd64Runs, func() error {
		out, err := exec.CommandContext(ctx, eng.CLI, "run", "--rm", "--label", "telesphorepo.e2e=1", "--platform", "linux/amd64",
			calibrationAmd64, "sh", "-c", "rpm -qa >/dev/null && rpm -Va --nofiles --nodigest rpm bash >/dev/null; true").CombinedOutput()
		if err != nil {
			return fmt.Errorf("run %s: %w\n%s", calibrationAmd64, err, out)
		}
		return nil
	})
}

// together runs n copies of fn at once and returns the first error.
func together(n int, fn func() error) error {
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() { errs[i] = fn() })
	}
	wg.Wait()
	return errors.Join(errs...)
}

// calibrationServer starts a server of image, waits for /healthz, signs in once, and removes it.
func calibrationServer(ctx context.Context, eng containerengine.Engine, image string) error {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	pass := hex.EncodeToString(b)
	out, err := exec.CommandContext(ctx, eng.CLI, "run", "-d", "--label", "telesphorepo.e2e=1", "-p", "127.0.0.1::8080",
		"-e", "TR_COOKIE_SECURE=false", "-e", "TR_BASE_URL=http://localhost:8080",
		"-e", "TR_BOOTSTRAP_ADMIN_USER=admin", "-e", "TR_BOOTSTRAP_ADMIN_PASSWORD="+pass, image).Output()
	if err != nil {
		return fmt.Errorf("start a calibration server: %w", err)
	}
	id := strings.TrimSpace(string(out))
	defer exec.Command(eng.CLI, "rm", "-f", id).Run() //nolint:errcheck
	out, err = exec.CommandContext(ctx, eng.CLI, "port", id, "8080/tcp").Output()
	if err != nil {
		return fmt.Errorf("the port of a calibration server: %w", err)
	}
	addr := strings.TrimSpace(strings.Split(string(out), "\n")[0])
	base := "http://" + addr
	client := &http.Client{Timeout: 10 * time.Second}
	deadline := time.Now().Add(90 * time.Second)
	for {
		if resp, err := client.Get(base + "/healthz"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return fmt.Errorf("a calibration server did not answer /healthz in 90 s")
		}
		time.Sleep(50 * time.Millisecond)
	}
	resp, err := client.Post(base+"/api/auth/login", "application/json",
		strings.NewReader(`{"username":"admin","password":"`+pass+`"}`))
	if err != nil {
		return fmt.Errorf("sign in to a calibration server: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("sign in to a calibration server: status %d", resp.StatusCode)
	}
	return nil
}
