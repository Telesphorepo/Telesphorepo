package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/clidist"
)

const (
	// cliPkg is the package of the Telesphorepo CLI.
	cliPkg = "./cmd/telesphorepo"
	// cliDist is the folder that the server embeds the CLI archives from (internal/clidist).
	cliDist = "internal/clidist/dist"
)

// cliLicense is the license that goes next to the CLI binary in its archives, when it exists.
var cliLicense = filepath.Join("cmd", "telesphorepo", "LICENSE")

// hostTarget is the platform of this computer.
func hostTarget() []clidist.Target {
	return []clidist.Target{{OS: runtime.GOOS, Arch: runtime.GOARCH}}
}

// cliArchives builds the CLI for each target with version and writes its archives and
// SHA256SUMS into cliDist, which the server embeds. It first removes the archives of an earlier
// build. Without cmd/telesphorepo it writes none and says so: that server answers 404 on /cli/.
func cliArchives(ctx context.Context, version string, targets []clidist.Target) error {
	if err := clearCLIDist(); err != nil {
		return err
	}
	if st, err := os.Stat(filepath.FromSlash(cliPkg)); err != nil || !st.IsDir() {
		note("skipping the CLI archives: %s does not exist, so the server serves no CLI downloads", cliPkg)
		return nil
	}
	var extra []string
	if _, err := os.Stat(cliLicense); err == nil {
		extra = append(extra, cliLicense)
	}
	sums := map[string]string{}
	for _, t := range targets {
		name := clidist.Archive(t.OS, t.Arch)
		bin := filepath.Join(cliDist, "telesphorepo_"+t.OS+"_"+t.Arch)
		binName := "telesphorepo"
		if t.OS == "windows" {
			bin, binName = bin+".exe", binName+".exe"
		}
		step("building %s for %s/%s", cliPkg, t.OS, t.Arch)
		env := []string{"CGO_ENABLED=0", "GOOS=" + t.OS, "GOARCH=" + t.Arch}
		if err := runStep(ctx, ".", env, "go", "build", "-trimpath",
			"-ldflags", "-s -w -X main.version="+version, "-o", bin, cliPkg); err != nil {
			return fmt.Errorf("go build %s %s/%s failed: %w", cliPkg, t.OS, t.Arch, err)
		}
		archive := filepath.Join(cliDist, name)
		var err error
		if t.OS == "windows" {
			err = packZipFiles(archive, bin, binName, extra)
		} else {
			err = packTarGzFiles(archive, bin, binName, extra)
		}
		if rmErr := os.Remove(bin); err == nil {
			err = rmErr
		}
		if err != nil {
			return fmt.Errorf("pack %s: %w", name, err)
		}
		if sums[name], err = fileSHA256(archive); err != nil {
			return err
		}
	}
	if err := writeSums(filepath.Join(cliDist, clidist.SumsFile), sums); err != nil {
		return err
	}
	out.line(green("✓ ") + fmt.Sprintf("built %d CLI archives into %s", len(sums), bold(cliDist)))
	return nil
}

// clearCLIDist removes every file of cliDist except .keep.
func clearCLIDist() error {
	entries, err := os.ReadDir(cliDist)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() == ".keep" {
			continue
		}
		if err := os.RemoveAll(filepath.Join(cliDist, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// writeSums writes the sums of the archives by name in the format of sha256sum, sorted by name.
func writeSums(file string, sums map[string]string) error {
	names := make([]string, 0, len(sums))
	for n := range sums {
		names = append(names, n)
	}
	sort.Strings(names)
	var sb strings.Builder
	for _, n := range names {
		fmt.Fprintf(&sb, "%s  %s\n", sums[n], n)
	}
	return os.WriteFile(file, []byte(sb.String()), 0o644)
}
