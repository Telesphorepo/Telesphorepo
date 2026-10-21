//go:build e2e || clients

package e2e

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/clidist"
)

// Programs of this repo that the tests build on the host for Linux on the CPU of the test, to copy
// into containers: the CLI, and server binaries of a given version, change log and CLI archives.

// cliArchiveName is the name of the CLI archive of the test containers' OS and CPU on a server.
var cliArchiveName = clidist.Archive("linux", runtime.GOARCH)

// repoRoot is the root folder of this repo (the tests run in e2e/).
func repoRoot(t testing.TB) string {
	t.Helper()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// goBuild builds the package pkg of this repo (e.g. "./cmd/telesphorepo") for Linux on the CPU of
// the test, without cgo, into a new file of the test's temp folder, and returns its path. version
// stamps main.version ("" keeps "dev"). overlay maps a file of the repo, relative to the root, to
// its new content; a nil content deletes the file. The build reads the overlay as go build
// -overlay does, so the tree on disk stays as it is.
func goBuild(t testing.TB, pkg, version string, overlay map[string][]byte) string {
	t.Helper()
	root := repoRoot(t)
	tmp := t.TempDir()
	out := filepath.Join(tmp, "out", filepath.Base(pkg))
	args := []string{"build", "-trimpath"}
	ldflags := "-s -w"
	if version != "" {
		ldflags += " -X main.version=" + version
	}
	args = append(args, "-ldflags="+ldflags)
	if len(overlay) > 0 {
		replace := map[string]string{}
		i := 0
		for rel, content := range overlay {
			dst := filepath.Join(root, filepath.FromSlash(rel))
			if content == nil {
				replace[dst] = ""
				continue
			}
			i++
			src := filepath.Join(tmp, fmt.Sprintf("overlay-%d%s", i, filepath.Ext(rel)))
			if err := os.WriteFile(src, content, 0o644); err != nil {
				t.Fatal(err)
			}
			replace[dst] = src
		}
		b, err := json.Marshal(map[string]any{"Replace": replace})
		if err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(tmp, "overlay.json")
		if err := os.WriteFile(file, b, 0o644); err != nil {
			t.Fatal(err)
		}
		args = append(args, "-overlay", file)
	}
	args = append(args, "-o", out, pkg)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	cmd := osexec.CommandContext(ctx, "go", args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go %v: %v\n%s", args, err, b)
	}
	return out
}

// BuildCLI builds the Telesphorepo CLI (cmd/telesphorepo) at version ("" for a dev build) with
// the change log changes ("" keeps changes.toml) and the extra Go files of package main in extra
// (name -> source).
func BuildCLI(t testing.TB, version, changes string, extra map[string]string) string {
	t.Helper()
	overlay := map[string][]byte{}
	if changes != "" {
		overlay["changes.toml"] = []byte(changes)
	}
	for name, src := range extra {
		overlay["cmd/telesphorepo/"+name] = []byte(src)
	}
	return goBuild(t, "./cmd/telesphorepo", version, overlay)
}

// BuildServer builds the server (cmd/telesphorepo-server) at version with the change log changes
// ("" keeps changes.toml). The build embeds the CLI archive of the test containers' platform for
// the CLI binary cli and its SHA256SUMS, or no CLI archives at all when cli is "". The CLI
// archives that a local build left in internal/clidist/dist never reach it. Start it with
// ServerBinary.
func BuildServer(t testing.TB, version, changes, cli string) string {
	t.Helper()
	return BuildServerWith(t, version, changes, cli, nil)
}

// BuildServerWith is BuildServer with the source files of the repo in files (path from the root
// -> content) in place of those on disk: the code of another version of the server.
func BuildServerWith(t testing.TB, version, changes, cli string, files map[string][]byte) string {
	t.Helper()
	overlay := map[string][]byte{}
	for p, b := range files {
		overlay[p] = b
	}
	if changes != "" {
		overlay["changes.toml"] = []byte(changes)
	}
	dist := filepath.Join(repoRoot(t), "internal", "clidist", "dist")
	entries, err := os.ReadDir(dist)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !e.IsDir() && e.Name() != ".keep" {
			overlay["internal/clidist/dist/"+e.Name()] = nil
		}
	}
	if cli != "" {
		archive := CLIArchive(t, cli)
		sum := sha256.Sum256(archive)
		overlay["internal/clidist/dist/"+cliArchiveName] = archive
		overlay["internal/clidist/dist/"+clidist.SumsFile] = []byte(hex.EncodeToString(sum[:]) + "  " + cliArchiveName + "\n")
	}
	return goBuild(t, "./cmd/telesphorepo-server", version, overlay)
}

// ServerBinary is the server file of ServerOpts.Files that replaces the server of the image with
// the binary bin.
func ServerBinary(bin string) ContainerFile {
	return ContainerFile{HostPath: bin, Path: "/telesphorepo-server", Mode: 0o755}
}

// CLIArchive returns a CLI archive as the release task packs it: a .tar.gz with the binary bin as
// telesphorepo.
func CLIArchive(t testing.TB, bin string) []byte {
	t.Helper()
	b, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "telesphorepo", Mode: 0o755, Size: int64(len(b)), ModTime: time.Now(), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
