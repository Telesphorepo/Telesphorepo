// Package containerengine detects a usable Docker or Podman engine on macOS, Linux and
// Windows (spec §16). Used by tools/stack and the e2e harness; not linked into the server.
package containerengine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type Engine struct {
	Name string // "docker" or "podman"
	CLI  string // path of the CLI binary to exec (docker or podman)
	// Host is the DOCKER_HOST value API clients should use ("" = client default).
	Host string
}

// probeTimeout bounds every CLI probe so a hung daemon/VM never stalls detection.
const probeTimeout = 5 * time.Second

// Detect picks an engine: TR_ENGINE override, existing DOCKER_HOST, a responding Docker
// daemon, then Podman (podman info / podman machine inspect). The error lists what was tried.
func Detect(ctx context.Context) (Engine, error) {
	switch forced := strings.ToLower(strings.TrimSpace(os.Getenv("TR_ENGINE"))); forced {
	case "":
	case "docker":
		cli, err := exec.LookPath("docker")
		if err != nil {
			return Engine{}, fmt.Errorf("TR_ENGINE=docker but the docker CLI is not on PATH")
		}
		return Engine{Name: "docker", CLI: cli, Host: os.Getenv("DOCKER_HOST")}, nil
	case "podman":
		if host := os.Getenv("DOCKER_HOST"); host != "" {
			cli, err := exec.LookPath("podman")
			if err != nil {
				return Engine{}, fmt.Errorf("TR_ENGINE=podman but the podman CLI is not on PATH")
			}
			return Engine{Name: "podman", CLI: cli, Host: host}, nil
		}
		e, err := detectPodman(ctx)
		if err != nil {
			return Engine{}, fmt.Errorf("TR_ENGINE=podman: %w", err)
		}
		return e, nil
	default:
		return Engine{}, fmt.Errorf("TR_ENGINE=%q: must be docker or podman", forced)
	}

	if host := os.Getenv("DOCKER_HOST"); host != "" {
		name := "docker"
		if strings.Contains(strings.ToLower(host), "podman") {
			name = "podman"
		}
		cli, err := exec.LookPath(name)
		if err != nil {
			// The API endpoint is what matters to clients; fall back to whichever CLI exists.
			other := map[string]string{"docker": "podman", "podman": "docker"}[name]
			if cli, err = exec.LookPath(other); err != nil {
				return Engine{}, fmt.Errorf("DOCKER_HOST=%s is set but neither docker nor podman is on PATH", host)
			}
			name = other
		}
		return Engine{Name: name, CLI: cli, Host: host}, nil
	}

	var tried []string
	if cli, err := exec.LookPath("docker"); err != nil {
		tried = append(tried, "docker: CLI not found on PATH")
	} else {
		out, err := run(ctx, cli, "version", "--format", "{{.Server.Version}}")
		switch {
		case err == nil && strings.Contains(strings.ToLower(out), "podman"):
			// podman-docker shim ("Emulate Docker CLI using podman"): treat it as podman.
			tried = append(tried, "docker: CLI is the podman-docker shim")
		case err == nil && strings.TrimSpace(out) != "":
			return Engine{Name: "docker", CLI: cli}, nil
		default:
			tried = append(tried, "docker: daemon not responding ("+firstLine(out, err)+")")
		}
	}

	e, err := detectPodman(ctx)
	if err == nil {
		return e, nil
	}
	tried = append(tried, "podman: "+err.Error())
	return Engine{}, fmt.Errorf("no container engine found; tried:\n  - %s\ninstall/start Docker or Podman, or set TR_ENGINE / DOCKER_HOST",
		strings.Join(tried, "\n  - "))
}

func detectPodman(ctx context.Context) (Engine, error) {
	cli, err := exec.LookPath("podman")
	if err != nil {
		return Engine{}, errors.New("CLI not found on PATH")
	}
	e := Engine{Name: "podman", CLI: cli}
	switch runtime.GOOS {
	case "darwin", "windows":
		field := "{{.ConnectionInfo.PodmanSocket.Path}}"
		if runtime.GOOS == "windows" {
			field = "{{.ConnectionInfo.PodmanPipe.Path}}"
		}
		out, err := run(ctx, cli, "machine", "inspect", "--format", "{{.State}}|"+field)
		if err != nil {
			return Engine{}, fmt.Errorf("no podman machine (%s); run: podman machine init && podman machine start", firstLine(out, err))
		}
		// With several machines inspect prints one line each; the first is the default one.
		state, path, _ := strings.Cut(strings.TrimSpace(strings.SplitN(out, "\n", 2)[0]), "|")
		state, path = strings.TrimSpace(state), strings.TrimSpace(path)
		if !strings.EqualFold(state, "running") {
			return Engine{}, fmt.Errorf("podman machine is %s; run: podman machine start", orUnknown(state))
		}
		if path == "" || path == "<no value>" {
			return Engine{}, errors.New("podman machine inspect reported no API socket; run: podman machine stop && podman machine start")
		}
		if runtime.GOOS == "windows" {
			e.Host = npipeURL(path)
		} else {
			e.Host = "unix://" + path
		}
		return e, nil
	default: // linux and other unixes
		var candidates []string
		if out, err := run(ctx, cli, "info", "--format", "{{.Host.RemoteSocket.Path}}"); err == nil {
			if p := strings.TrimPrefix(strings.TrimSpace(out), "unix://"); p != "" && p != "<no value>" {
				candidates = append(candidates, p)
			}
		} else {
			return Engine{}, fmt.Errorf("podman info failed (%s)", firstLine(out, err))
		}
		if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
			candidates = append(candidates, filepath.Join(xdg, "podman", "podman.sock"))
		}
		candidates = append(candidates, "/run/podman/podman.sock")
		for _, p := range candidates {
			if fi, err := os.Stat(p); err == nil && fi.Mode()&os.ModeSocket != 0 {
				e.Host = "unix://" + p
				return e, nil
			}
		}
		return Engine{}, fmt.Errorf("podman API socket not active (looked at %s); run: systemctl --user enable --now podman.socket",
			strings.Join(candidates, ", "))
	}
}

// npipeURL converts \\.\pipe\name (or //./pipe/name) to npipe:////./pipe/name.
func npipeURL(p string) string {
	if strings.HasPrefix(p, "npipe://") {
		return p
	}
	p = strings.ReplaceAll(p, `\`, "/")
	p = strings.TrimPrefix(p, "//./pipe/")
	p = strings.TrimPrefix(p, "/")
	return "npipe:////./pipe/" + p
}

// ApplyEnv sets DOCKER_HOST (when Host != "") and, for Podman,
// TESTCONTAINERS_RYUK_CONTAINER_PRIVILEGED=true in the current process environment.
func (e Engine) ApplyEnv() {
	if e.Host != "" {
		_ = os.Setenv("DOCKER_HOST", e.Host)
	}
	if e.Name == "podman" {
		_ = os.Setenv("TESTCONTAINERS_RYUK_CONTAINER_PRIVILEGED", "true")
	}
}

// Compose returns the argv prefix for compose: ["docker","compose"] or ["podman","compose"]
// (falling back to "podman-compose" / "docker-compose" binaries when the plugin is missing).
func (e Engine) Compose(ctx context.Context) ([]string, error) {
	cli := e.CLI
	if cli == "" {
		cli = e.Name
	}
	if _, err := run(ctx, cli, "compose", "version"); err == nil {
		return []string{cli, "compose"}, nil
	}
	fallbacks := []string{"docker-compose", "podman-compose"}
	if e.Name == "podman" {
		fallbacks = []string{"podman-compose", "docker-compose"}
	}
	for _, name := range fallbacks {
		if p, err := exec.LookPath(name); err == nil {
			return []string{p}, nil
		}
	}
	if e.Name == "podman" {
		return nil, errors.New("no compose provider found; install podman-compose or docker-compose (podman compose needs one of them)")
	}
	return nil, errors.New("no compose found; install the Docker Compose plugin (docker compose) or docker-compose")
}

// run executes name args... without a shell, bounded by probeTimeout; returns combined output.
func run(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	cmd.Stdin = nil
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		err = fmt.Errorf("timed out after %s", probeTimeout)
	}
	return buf.String(), err
}

func firstLine(out string, err error) string {
	if s := strings.TrimSpace(out); s != "" {
		return strings.TrimSpace(strings.SplitN(s, "\n", 2)[0])
	}
	if err != nil {
		return err.Error()
	}
	return "no output"
}

func orUnknown(s string) string {
	if s == "" {
		return "not running"
	}
	return strings.ToLower(s)
}
