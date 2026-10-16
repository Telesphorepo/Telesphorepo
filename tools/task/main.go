// Command task is the cross-platform developer entry point:
// "go tool task dev | build | preview | test | gen | doctor". It needs only Go and
// bun (or npm); nothing here depends on bash or make, so it behaves the same on Windows,
// macOS and Linux.
//
//	go tool task dev            backend + Vite with live reload
//	go tool task build [image]  bin/telesphorepo-server with the UI embedded (+ container image)
//	go tool task preview        build, then run the production binary locally
//	go tool task test [zone] [args]  e2e zone: core (default), import, scale, clients or all; args go to "go test"
//	go tool task cli [version]  the six CLI archives that the server embeds (internal/clidist/dist)
//	go tool task release [version]  cross-compiled archives and SHA256SUMS in bin/release
//	go tool task gen           regenerate the sqlc data layer (every internal/**/sqlc.yaml)
//	go tool task doctor         show what was detected
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/Telesphorepo/Telesphorepo/changelog"
	"github.com/Telesphorepo/Telesphorepo/internal/clidist"
)

const usage = `Usage: go tool task <command>

  dev              run everything locally with live reload: API on :8080, UI on http://localhost:5173
  build            build bin/telesphorepo-server with the UI and the six CLI archives embedded
  build image      ...and the container image telesphorepo:local (Docker or Podman)
  preview          build, then run the production binary at http://localhost:8080
  test [args]      run the core e2e zone; args pass to go test (e.g. go tool task test -run TestMaven)
  test import      run the import zone: Nexus sources, sync and cut-over (e.g. go tool task test import -run TestNexus)
  test scale       run the scale zone: 100k and 1M seeded assets, p95 must not double
  test clients     run the clients zone: the CLI of cmd/telesphorepo with the real tools in containers
  test all         run the core, import, scale and clients zones one after the other; fails if any fails
  cli [version]    build the six CLI archives that the server serves at /cli/ into internal/clidist/dist
                   (the version defaults to git describe; skipped while cmd/telesphorepo does not exist).
                   A given release version is written into each "dev" entry of changes.toml first
  release [version]  build the UI once, then bin/release/<program>_<version>_<os>_<arch> archives
                   (.tar.gz, .zip on Windows) for linux, darwin and windows on amd64 and arm64,
                   plus SHA256SUMS; the version defaults to git describe (e.g. go tool task release 1.2.3).
                   A given version is written into each "dev" entry of changes.toml first
  gen              regenerate the sqlc query code for every internal/**/sqlc.yaml
  doctor          show detected tools (Go, bun/npm, Docker/Podman) and what is missing

Data for dev/preview lives in ./.telesphorepo-dev (delete it to start over).
`

// exitError carries a child's exit status through to os.Exit without extra noise.
type exitError struct{ code int }

func (e exitError) Error() string { return fmt.Sprintf("exit status %d", e.code) }

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:])
	stop()
	var ee exitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		os.Exit(ee.code)
	default:
		fmt.Fprintln(os.Stderr, red("error: ")+err.Error())
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		fmt.Print(usage)
		return nil
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	case "doctor":
		if err := noArgs(verb, rest); err != nil {
			return err
		}
		return doctor(ctx)
	case "test":
		if err := enterRoot(); err != nil {
			return err
		}
		return test(ctx, rest)
	}

	if err := enterRoot(); err != nil {
		return err
	}
	switch verb {
	case "dev":
		if err := noArgs(verb, rest); err != nil {
			return err
		}
		return dev(ctx)
	case "build":
		image := false
		switch {
		case len(rest) == 0:
		case len(rest) == 1 && rest[0] == "image":
			image = true
		default:
			return usageErr("build takes no arguments except \"image\" (go tool task build image)")
		}
		_, err := build(ctx, image)
		return err
	case "preview":
		if err := noArgs(verb, rest); err != nil {
			return err
		}
		return preview(ctx)
	case "release":
		if len(rest) > 1 {
			return usageErr("release takes at most one argument, the version (go tool task release 1.2.3)")
		}
		version := ""
		if len(rest) == 1 {
			version = rest[0]
		}
		return release(ctx, version)
	case "cli":
		if len(rest) > 1 {
			return usageErr("cli takes at most one argument, the version (go tool task cli 1.2.3)")
		}
		version := gitVersion(ctx)
		if len(rest) == 1 {
			version = rest[0]
			// A release version goes into the change log first, so the archives and a server
			// that a later step builds from the same tree (the Dockerfile) embed the stamped log.
			if _, err := changelog.ParseVersion(version); err == nil {
				if err := stampChanges(version); err != nil {
					return err
				}
			}
		}
		return cliArchives(ctx, version, clidist.Targets)
	case "gen":
		if err := noArgs(verb, rest); err != nil {
			return err
		}
		return gen(ctx)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", verb, usage)
		return exitError{2}
	}
}

func noArgs(verb string, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Sprintf("%s takes no arguments", verb))
	}
	return nil
}

func usageErr(msg string) error {
	fmt.Fprintf(os.Stderr, "%s\n\n%s", msg, usage)
	return exitError{2}
}

// childExit converts a finished child's error into an exitError carrying its status.
func childExit(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() > 0 {
		return exitError{ee.ExitCode()}
	}
	return err
}
