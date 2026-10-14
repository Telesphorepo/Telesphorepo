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
	"sort"
	"strings"
)

// releaseTargets are the platforms that "go tool task release" builds.
var releaseTargets = []struct{ goos, goarch string }{
	{"linux", "amd64"}, {"linux", "arm64"},
	{"darwin", "amd64"}, {"darwin", "arm64"},
	{"windows", "amd64"}, {"windows", "arm64"},
}

// releaseFiles are packed next to the binary in every archive.
var releaseFiles = []string{"LICENSE", "README.md"}

// release builds the UI once, cross-compiles the binary for every release target, packs each
// binary with LICENSE and README.md, and writes SHA256SUMS. Output goes to bin/release.
func release(ctx context.Context, version string) error {
	pm, err := prepare(ctx)
	if err != nil {
		return err
	}
	if version == "" {
		version = gitVersion(ctx)
	}
	step("building web UI (%s run build)", pm.Name)
	if err := runStep(ctx, webDir, nil, append([]string{pm.Path}, pm.runArgs("build")...)...); err != nil {
		return fmt.Errorf("web build failed: %w", err)
	}

	dir := filepath.Join("bin", "release")
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	sums := map[string]string{}
	for _, t := range releaseTargets {
		base := fmt.Sprintf("telesphorepo_%s_%s_%s", version, t.goos, t.goarch)
		name := "telesphorepo"
		if t.goos == "windows" {
			name += ".exe"
		}
		bin := filepath.Join(dir, base+filepath.Ext(name))
		step("building %s/%s", t.goos, t.goarch)
		env := []string{"CGO_ENABLED=0", "GOOS=" + t.goos, "GOARCH=" + t.goarch}
		if err := runStep(ctx, ".", env, "go", "build", "-trimpath",
			"-ldflags", "-s -w -X main.version="+version, "-o", bin, "./cmd/telesphorepo"); err != nil {
			return fmt.Errorf("go build %s/%s failed: %w", t.goos, t.goarch, err)
		}
		var archive string
		if t.goos == "windows" {
			archive = base + ".zip"
			err = packZip(filepath.Join(dir, archive), bin, name)
		} else {
			archive = base + ".tar.gz"
			err = packTarGz(filepath.Join(dir, archive), bin, name)
		}
		if rmErr := os.Remove(bin); err == nil {
			err = rmErr
		}
		if err != nil {
			return fmt.Errorf("pack %s: %w", archive, err)
		}
		sum, err := fileSHA256(filepath.Join(dir, archive))
		if err != nil {
			return err
		}
		sums[archive] = sum
	}

	names := make([]string, 0, len(sums))
	for n := range sums {
		names = append(names, n)
	}
	sort.Strings(names)
	var sb strings.Builder
	for _, n := range names {
		fmt.Fprintf(&sb, "%s  %s\n", sums[n], n)
	}
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(sb.String()), 0o644); err != nil {
		return err
	}
	out.line(green("✓ ") + fmt.Sprintf("released %d archives and SHA256SUMS to %s (version %s)", len(names), bold(filepath.ToSlash(dir)), version))
	return nil
}

// packTarGz writes a .tar.gz with the binary (mode 0755) and the release files.
func packTarGz(archive, bin, binName string) (err error) {
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
	for _, rf := range releaseFiles {
		if err := tarAdd(tw, rf, rf, 0o644); err != nil {
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

// packZip writes a .zip with the binary and the release files.
func packZip(archive, bin, binName string) (err error) {
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
	for _, rf := range releaseFiles {
		if err := zipAdd(zw, rf, rf); err != nil {
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
