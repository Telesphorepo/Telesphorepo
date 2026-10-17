//go:build e2e || import || scale || clients

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"

	"github.com/Telesphorepo/Telesphorepo/internal/containerengine"
)

const defaultImage = "telesphorepo-e2e:latest"

var (
	// ServerImage is the server image under test (E2E_IMAGE overrides).
	ServerImage = defaultImage
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
	if os.Getenv("E2E_SKIP_BUILD") != "1" {
		if err := buildImage(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "e2e: image build failed:", err)
			return 1
		}
	}

	if err := pullFloating(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "e2e: floating images:", err)
		return 1
	}

	if err := generateSigningKey(); err != nil {
		fmt.Fprintln(os.Stderr, "e2e: signing key:", err)
		return 1
	}

	sweepLeftovers(ctx)

	nw, err := network.New(ctx, network.WithLabels(e2eLabels))
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e: network:", err)
		return 1
	}
	TestNetwork = nw
	defer func() { _ = nw.Remove(context.Background()) }()
	defer stopSharedSeaweed()

	return m.Run()
}

// e2eLabels marks every container and network of a run. Tests remove their own at the end; a run
// that is killed cannot, and rootless Podman has no Ryuk to do it, so the next run removes them.
var e2eLabels = map[string]string{"telesphorepo.e2e": "1"}

// leftoverVolume matches the volumes the harness names with RandName (NewDataVolume, the scale
// test). The bare "telesphorepo-data" volume of a person's own server never matches.
var leftoverVolume = regexp.MustCompile(`^telesphorepo-(data|scale)-[0-9a-f]{8}$`)

// sweepLeftovers removes the containers, networks and volumes that an earlier killed run left.
func sweepLeftovers(ctx context.Context) {
	list := func(args ...string) []string {
		out, err := exec.CommandContext(ctx, Engine.CLI, args...).Output()
		if err != nil {
			return nil
		}
		return strings.Fields(string(out))
	}
	remove := func(kind string, args []string, names []string) {
		if len(names) == 0 {
			return
		}
		if out, err := exec.CommandContext(ctx, Engine.CLI, append(args, names...)...).CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "e2e: remove leftover %s: %v\n%s", kind, err, out)
			return
		}
		fmt.Fprintf(os.Stderr, "e2e: removed %d leftover %s from an earlier run\n", len(names), kind)
	}
	remove("containers", []string{"rm", "-f"}, list("ps", "-aq", "--filter", "label=telesphorepo.e2e"))
	remove("networks", []string{"network", "rm", "-f"}, list("network", "ls", "-q", "--filter", "label=telesphorepo.e2e"))
	var vols []string
	for _, v := range list("volume", "ls", "--format", "{{.Name}}") {
		if leftoverVolume.MatchString(v) {
			vols = append(vols, v)
		}
	}
	remove("volumes", []string{"volume", "rm", "-f"}, vols)
}

// buildImage shells out to "<engine> build" (docker: BuildKit; podman: buildah). Both respect
// .dockerignore and RUN --mount=type=cache.
func buildImage(ctx context.Context) error {
	root, err := filepath.Abs("..")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, Engine.CLI, "build", "-t", ServerImage, "-f", filepath.Join(root, "Dockerfile"), root)
	cmd.Env = append(os.Environ(), "DOCKER_BUILDKIT=1")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		os.Stderr.Write(out.Bytes())
		return err
	}
	return nil
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
