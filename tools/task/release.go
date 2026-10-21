package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"

	"github.com/Telesphorepo/Telesphorepo/changelog"
	"github.com/Telesphorepo/Telesphorepo/internal/clidist"
)

// releaseTargets are the platforms that "go tool task release" builds: those of the CLI archives.
var releaseTargets = clidist.Targets

// releaseFiles are packed next to the binary in every archive.
var releaseFiles = []string{"LICENSE", "README.md"}

// releasePrograms are the programs of a release: the CLI and the server. The CLI is also the
// Docker credential helper under a second name, which the install scripts make. A release packs
// each program whose package exists.
var releasePrograms = []struct{ name, pkg string }{
	{"telesphorepo", cliPkg},
	{"telesphorepo-server", "./cmd/telesphorepo-server"},
}

// release builds the UI once, the CLI archives and the edge Worker folder that the server
// embeds, cross-compiles each release program for every release target, packs each binary with
// LICENSE and README.md, packs the edge Worker folder (telesphorepo-edge_<version>.tar.gz), and
// writes SHA256SUMS. Output goes to bin/release. A version given as the argument must be a
// release version, and changes.toml must have no "dev" entry (go tool task tag stamps them).
func release(ctx context.Context, version string) error {
	pm, err := prepare(ctx)
	if err != nil {
		return err
	}
	if version == "" {
		version = gitVersion(ctx)
	} else if err := checkStamped(version); err != nil {
		return err
	}
	step("building web UI (%s run build)", pm.Name)
	if err := runStep(ctx, webDir, nil, append([]string{pm.Path}, pm.runArgs("build")...)...); err != nil {
		return fmt.Errorf("web build failed: %w", err)
	}
	if err := cliArchives(ctx, version, clidist.Targets); err != nil {
		return err
	}
	if err := clearEdgeDist(); err != nil {
		return err
	}
	edge, err := bundleEdge(ctx, version)
	if err != nil {
		return err
	}
	if edge != nil {
		defer edge.close()
		if err := writeEdgeDist(edge, version); err != nil {
			return err
		}
	}

	dir := filepath.Join("bin", "release")
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	sums := map[string]string{}
	for _, p := range releasePrograms {
		if st, err := os.Stat(filepath.FromSlash(p.pkg)); err != nil || !st.IsDir() {
			note("skipping %s: %s does not exist", p.name, p.pkg)
			continue
		}
		files := releaseFiles
		if p.pkg == cliPkg {
			files = nil
			if _, err := os.Stat(cliLicense); err == nil {
				files = []string{cliLicense}
			}
		}
		for _, t := range releaseTargets {
			archive, err := releaseArchive(ctx, dir, version, p.name, p.pkg, t.OS, t.Arch, files)
			if err != nil {
				return err
			}
			sum, err := fileSHA256(filepath.Join(dir, archive))
			if err != nil {
				return err
			}
			sums[archive] = sum
		}
	}
	if edge != nil {
		archive, err := edgeArchive(edge, dir, version)
		if err != nil {
			return err
		}
		if sums[archive], err = fileSHA256(filepath.Join(dir, archive)); err != nil {
			return err
		}
	}
	if err := writeSums(filepath.Join(dir, "SHA256SUMS"), sums); err != nil {
		return err
	}
	out.line(green("✓ ") + fmt.Sprintf("released %d archives and SHA256SUMS to %s (version %s)", len(sums), bold(filepath.ToSlash(dir)), version))
	return nil
}

// changesFile is the change log at the repo root.
const changesFile = "changes.toml"

// stampChanges writes version into each "dev" entry of the change log and returns the number of
// entries it changed.
func stampChanges(version string) (int, error) {
	data, err := os.ReadFile(changesFile)
	if err != nil {
		return 0, err
	}
	out, n, err := changelog.Stamp(data, version)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", changesFile, err)
	}
	if n == 0 {
		return 0, nil
	}
	if err := os.WriteFile(changesFile, out, 0o644); err != nil {
		return 0, err
	}
	step("%s: wrote version %s into %d dev entries", changesFile, version, n)
	return n, nil
}

// checkStamped refuses a build for the release version when the change log still has a "dev"
// entry: the tagged commit must hold the stamped log, so every later build embeds the same one.
func checkStamped(version string) error {
	data, err := os.ReadFile(changesFile)
	if err != nil {
		return err
	}
	_, n, err := changelog.Stamp(data, version)
	if err != nil {
		return fmt.Errorf("%s: %w", changesFile, err)
	}
	if n > 0 {
		return fmt.Errorf("%s has %d dev entries; make the release with go tool task tag %s", changesFile, n, version)
	}
	return nil
}

// releaseArchive cross-compiles the program of package pkg for goos/goarch and packs it with
// files into <name>_<version>_<goos>_<goarch>.tar.gz (.zip on Windows) in dir. It returns the
// archive name.
func releaseArchive(ctx context.Context, dir, version, name, pkg, goos, goarch string, files []string) (string, error) {
	base := fmt.Sprintf("%s_%s_%s_%s", name, version, goos, goarch)
	if goos == "windows" {
		name += ".exe"
	}
	bin := filepath.Join(dir, base+filepath.Ext(name))
	step("building %s for %s/%s", pkg, goos, goarch)
	env := []string{"CGO_ENABLED=0", "GOOS=" + goos, "GOARCH=" + goarch}
	if err := runStep(ctx, ".", env, "go", "build", "-trimpath",
		"-ldflags", "-s -w -X main.version="+version, "-o", bin, pkg); err != nil {
		return "", fmt.Errorf("go build %s %s/%s failed: %w", pkg, goos, goarch, err)
	}
	var archive string
	var err error
	if goos == "windows" {
		archive = base + ".zip"
		err = packZipFiles(filepath.Join(dir, archive), bin, name, files)
	} else {
		archive = base + ".tar.gz"
		err = packTarGzFiles(filepath.Join(dir, archive), bin, name, files)
	}
	if rmErr := os.Remove(bin); err == nil {
		err = rmErr
	}
	if err != nil {
		return "", fmt.Errorf("pack %s: %w", archive, err)
	}
	return archive, nil
}

// packTarGzFiles writes a .tar.gz with the binary (mode 0755) and files, each under its base
// name.
func packTarGzFiles(archive, bin, binName string, files []string) (err error) {
	f, err := os.Create(archive)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	if err := tarAdd(tw, bin, binName, 0o755); err != nil {
		return err
	}
	for _, rf := range files {
		if err := tarAdd(tw, rf, filepath.Base(rf), 0o644); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

func tarAdd(tw *tar.Writer, src, name string, mode int64) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: path.Clean(name), Mode: mode, Size: st.Size(), ModTime: st.ModTime()}); err != nil {
		return err
	}
	_, err = io.Copy(tw, in)
	return err
}

// packZipFiles writes a .zip with the binary and files, each under its base name.
func packZipFiles(archive, bin, binName string, files []string) (err error) {
	f, err := os.Create(archive)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}()
	zw := zip.NewWriter(f)
	if err := zipAdd(zw, bin, binName); err != nil {
		return err
	}
	for _, rf := range files {
		if err := zipAdd(zw, rf, filepath.Base(rf)); err != nil {
			return err
		}
	}
	return zw.Close()
}

func zipAdd(zw *zip.Writer, src, name string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	hdr, err := zip.FileInfoHeader(st)
	if err != nil {
		return err
	}
	hdr.Name = name
	hdr.Method = zip.Deflate
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, in)
	return err
}

func fileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
