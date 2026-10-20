package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// edgeDir holds the edge Worker (TypeScript, bun, wrangler).
const edgeDir = "edge"

// edgeVersionDefine is the global that the release build of the Worker defines to the server
// version (wrangler --define), so a Worker of another version passes every request to the server.
const edgeVersionDefine = "TELESPHOREPO_VERSION"

// jsString is s as a JavaScript string literal, the define value of the version; the server's
// wrangler.jsonc (internal/edge) defines the same value.
func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// edgeArchive bundles the edge Worker (wrangler deploy --dry-run --outdir) with the version
// defined, and packs the bundle (dist/), package.json, the provision script and the README of
// edge/ into telesphorepo-edge_<version>.tar.gz in dir. It returns the archive name, or "" when
// edge/ does not exist.
func edgeArchive(ctx context.Context, dir, version string) (string, error) {
	if st, err := os.Stat(filepath.Join(edgeDir, "package.json")); err != nil || st.IsDir() {
		note("skipping the edge Worker: %s/package.json does not exist", edgeDir)
		return "", nil
	}
	bun := lookPath("bun")
	if bun == "" {
		return "", errors.New("the edge Worker needs bun on PATH: https://bun.sh")
	}
	step("installing edge dependencies (bun install --frozen-lockfile)")
	if err := runStep(ctx, edgeDir, nil, bun, "install", "--frozen-lockfile"); err != nil {
		return "", fmt.Errorf("bun install in %s/ failed: %w", edgeDir, err)
	}
	tmp, err := os.MkdirTemp("", "telesphorepo-edge-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	out := filepath.Join(tmp, "dist")
	step("bundling the edge Worker (wrangler deploy --dry-run)")
	if err := runStep(ctx, edgeDir, nil, bun, "x", "wrangler", "deploy", "--dry-run", "--outdir", out,
		"--name", "telesphorepo-edge", "--compatibility-date", "2026-10-01",
		"--define", edgeVersionDefine+":"+jsString(version), "src/index.ts"); err != nil {
		return "", fmt.Errorf("bundling the edge Worker failed: %w", err)
	}
	files := map[string]string{} // archive name -> source
	err = filepath.WalkDir(out, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(out, p)
		if err != nil {
			return err
		}
		files[path.Join("dist", filepath.ToSlash(rel))] = p
		return nil
	})
	if err != nil {
		return "", err
	}
	for _, f := range []string{"package.json", "bun.lock", "README.md", "scripts/provision.ts"} {
		src := filepath.Join(edgeDir, filepath.FromSlash(f))
		if _, err := os.Stat(src); err == nil {
			files[f] = src
		} else if f != "bun.lock" {
			return "", fmt.Errorf("%s/%s is missing", edgeDir, f)
		}
	}
	archive := fmt.Sprintf("telesphorepo-edge_%s.tar.gz", version)
	if err := packTarGzTree(filepath.Join(dir, archive), files); err != nil {
		return "", fmt.Errorf("pack %s: %w", archive, err)
	}
	return archive, nil
}

// packTarGzTree writes a .tar.gz with files (archive name -> source), each with mode 0644.
func packTarGzTree(archive string, files map[string]string) (err error) {
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
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		if strings.Contains(n, "..") {
			return fmt.Errorf("bad archive name %q", n)
		}
		if err := tarAdd(tw, files[n], n, 0o644); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}
