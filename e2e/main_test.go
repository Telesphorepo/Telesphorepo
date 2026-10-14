//go:build e2e || import || scale || clients

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	// SigningPrivateKey / SigningPublicKey are the armored OpenPGP key pair generated for this run.
	SigningPrivateKey string
	SigningPublicKey  string
	// SigningPublicKeyFile is the host path of the armored public key.
	SigningPublicKeyFile string
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

	keyDir, err := os.MkdirTemp("", "telesphorepo-e2e-keys-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e:", err)
		return 1
	}
	defer os.RemoveAll(keyDir)
	if err := generateSigningKey(keyDir); err != nil {
		fmt.Fprintln(os.Stderr, "e2e: signing key:", err)
		return 1
	}

	nw, err := network.New(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e: network:", err)
		return 1
	}
	TestNetwork = nw
	defer func() { _ = nw.Remove(context.Background()) }()
	defer stopSharedSeaweed()

	return m.Run()
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
func generateSigningKey(dir string) error {
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
	SigningPublicKeyFile = filepath.Join(dir, "debian.asc")
	if err := os.WriteFile(filepath.Join(dir, "signing.asc"), priv.Bytes(), 0o600); err != nil {
		return err
	}
	return os.WriteFile(SigningPublicKeyFile, pub.Bytes(), 0o644)
}
