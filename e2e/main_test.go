//go:build e2e || import || clients

package e2e

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
	"github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"

	"github.com/Telesphorepo/Telesphorepo/internal/containerengine"
	"github.com/Telesphorepo/Telesphorepo/internal/e2erun"
)

var (
	// ServerImage is the server image under test: the one "go tool task test" built for the run,
	// or E2E_IMAGE.
	ServerImage = e2erun.Image
	// TestNetwork is the shared docker network every container of the run joins.
	TestNetwork *testcontainers.DockerNetwork
	// SigningPrivateKey / SigningPublicKey are an armored OpenPGP key pair generated for this run
	// (the signing key import test uses it).
	SigningPrivateKey string
	SigningPublicKey  string
	// RunID is unique per test-binary run (used in S3 prefixes, group ids, ...).
	RunID string
	// Engine is the detected container engine (docker or podman); its CLI is used for
	// "build" and "exec -i" shell-outs.
	Engine containerengine.Engine
)

func TestMain(m *testing.M) {
	os.Exit(runMain(m))
}

func runMain(m *testing.M) int {
	// "go test -list" (go tool task test --shard) only names the tests: no engine, image or pulls.
	flag.Parse()
	if f := flag.Lookup("test.list"); f != nil && f.Value.String() != "" {
		return m.Run()
	}
	// Only "go tool task test" starts a run: it builds the image, removes what a killed run left,
	// holds the cooldown between runs and runs the zones at the same time.
	run, err := e2erun.CheckTicket(os.Getenv(e2erun.TicketEnv))
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e:", err)
		return 1
	}
	ctx := context.Background()
	RunID = randHex(6)

	// Engine detection must precede every testcontainers call: it sets DOCKER_HOST (and the
	// Ryuk switch-off for Podman) in this process's environment.
	eng, err := containerengine.Detect(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e: no container engine:", err)
		return 1
	}
	eng.ApplyEnv()
	Engine = eng
	fmt.Fprintf(os.Stderr, "e2e: container engine %s (cli %s, host %q)\n", eng.Name, eng.CLI, eng.Host)

	if img := os.Getenv("E2E_IMAGE"); img != "" {
		ServerImage = img
	}
	if err := pullFloating(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "e2e: floating images:", err)
		return 1
	}
	if err := warmMaven(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "e2e: maven warm-up:", err)
		return 1
	}

	if err := generateSigningKey(); err != nil {
		fmt.Fprintln(os.Stderr, "e2e: signing key:", err)
		return 1
	}

	nw, err := network.New(ctx, network.WithLabels(e2eLabels))
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e: network:", err)
		return 1
	}
	TestNetwork = nw
	defer func() { _ = nw.Remove(context.Background()) }()
	defer stopSharedSeaweed()

	// The budget of the tests, which "go tool task test" scaled to this computer: go test's own
	// timeout, whose clock starts in m.Run, after the setup above. It replaces any -timeout of the
	// command line. Past it the binary panics and names the tests that still run.
	if err := flag.Set("test.timeout", run.Budget.String()); err != nil {
		fmt.Fprintln(os.Stderr, "e2e: set the budget:", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "e2e: the tests of this zone have %s in all (%s on CI's runner, e2erun.ReferenceBudget): past it the run fails. Make tests leaner; never raise the budget\n",
		run.Budget, e2erun.ReferenceBudget)
	start := time.Now()
	code := m.Run()
	fmt.Fprintf(os.Stderr, "e2e: the tests took %s of their %s\n", time.Since(start).Round(time.Second), run.Budget)
	return code
}

// e2eLabels marks every container and network of a run. Tests remove their own at the end; a run
// that is killed cannot, and rootless Podman has no Ryuk to do it, so "go tool task test" removes
// them before the next run.
var e2eLabels = map[string]string{"telesphorepo.e2e": "1"}

// e2eContainer is the option every container of a run starts with: e2eLabels, and a log kept in a
// file (json-file; Podman's k8s-file). Podman's default log driver, journald, drops a unit's lines
// past 10,000 in 30 s, and all containers of a rootless run share one unit, so a full zone lost
// the log lines that tests wait for and read.
var e2eContainer testcontainers.CustomizeRequestOption = func(req *testcontainers.GenericContainerRequest) error {
	if err := testcontainers.WithLabels(e2eLabels)(req); err != nil {
		return err
	}
	return testcontainers.WithHostConfigModifier(func(hc *container.HostConfig) {
		hc.LogConfig = container.LogConfig{Type: "json-file"}
	})(req)
}

// buildArgs returns the arguments of "<engine> build" with args. With Docker it adds --load: a
// builder other than Docker's own (CI's persistent BuildKit) keeps its result to itself unless
// asked to load it into the engine that the tests start containers in. Podman builds into its own
// store.
func buildArgs(args ...string) []string {
	out := []string{"build"}
	if Engine.Name == "docker" {
		out = append(out, "--load")
	}
	return append(out, args...)
}

// generateSigningKey creates a v4 Ed25519 (legacy EdDSA, algo 22, understood by apt's gpgv) key.
func generateSigningKey() error {
	cfg := &packet.Config{Algorithm: packet.PubKeyAlgoEdDSA, Curve: packet.Curve25519}
	ent, err := openpgp.NewEntity("Telesphorepo E2E", "", "e2e@telesphorepo.test", cfg)
	if err != nil {
		return err
	}
	var priv, pub bytes.Buffer
	w, err := armor.Encode(&priv, openpgp.PrivateKeyType, nil)
	if err != nil {
		return err
	}
	if err := ent.SerializePrivate(w, cfg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	w, err = armor.Encode(&pub, openpgp.PublicKeyType, nil)
	if err != nil {
		return err
	}
	if err := ent.Serialize(w); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	SigningPrivateKey, SigningPublicKey = priv.String(), pub.String()
	return nil
}
