// Package containerengine detects a usable Docker or Podman engine on macOS, Linux and
// Windows (spec §16). Used by tools/stack and the e2e harness; not linked into the server.
package containerengine

import "context"

type Engine struct {
	Name string // "docker" or "podman"
	CLI  string // path of the CLI binary to exec (docker or podman)
	// Host is the DOCKER_HOST value API clients should use ("" = client default).
	Host string
}

// Detect picks an engine: TR_ENGINE override, existing DOCKER_HOST, a responding Docker
// daemon, then Podman (podman info / podman machine inspect). The error lists what was tried.
func Detect(ctx context.Context) (Engine, error) { panic("TODO") }

// ApplyEnv sets DOCKER_HOST (when Host != "") and, for Podman,
// TESTCONTAINERS_RYUK_CONTAINER_PRIVILEGED=true in the current process environment.
func (e Engine) ApplyEnv() { panic("TODO") }

// Compose returns the argv prefix for compose: ["docker","compose"] or ["podman","compose"]
// (falling back to "podman-compose" / "docker-compose" binaries when the plugin is missing).
func (e Engine) Compose(ctx context.Context) ([]string, error) { panic("TODO") }
