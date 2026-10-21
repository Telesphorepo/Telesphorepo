package main

import (
	"archive/tar"
	"archive/zip"
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

	"github.com/Telesphorepo/Telesphorepo/internal/edgedist"
)

// edgeDir holds the edge Worker (TypeScript, bun, wrangler).
const edgeDir = "edge"

// edgeDist is the folder that the server embeds the edge Worker folder from (internal/edgedist).
const edgeDist = "internal/edgedist/dist"

// edgeVersionDefine is the global that the bundle of the Worker defines to the server version
// (wrangler --define), so a Worker of another version passes every request to the server.
const edgeVersionDefine = "TELESPHOREPO_VERSION"

// edgeDevScripts are the scripts of edge/package.json that need the sources; the Worker folder
// of a download or a release has the bundle only, so its package.json leaves them out.
var edgeDevScripts = []string{"build", "typecheck"}

// jsString is s as a JavaScript string literal, the define value of the version; the server's
// wrangler.jsonc (internal/edge) defines the same value.
func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// edgeBundle is the edge Worker folder of one build: the bundle and the files next to it, in a
// temporary folder.
type edgeBundle struct {
	files map[string]string // name in the folder -> source
	tmp   string
}

func (b *edgeBundle) close() { os.RemoveAll(b.tmp) }

// bundleEdge bundles the edge Worker (wrangler deploy --dry-run --outdir) with the version
// defined and gathers the Worker folder: the bundle (dist/), package.json without the scripts
// that need the sources, bun.lock, the provision script and the README of edge/. It returns nil
// when edge/ does not exist, and fails without bun.
func bundleEdge(ctx context.Context, version string) (*edgeBundle, error) {
	if st, err := os.Stat(filepath.Join(edgeDir, "package.json")); err != nil || st.IsDir() {
		note("skipping the edge Worker: %s/package.json does not exist", edgeDir)
		return nil, nil
	}
	bun := lookPath("bun")
	if bun == "" {
		return nil, errors.New("the edge Worker needs bun on PATH: https://bun.sh")
	}
	step("installing edge dependencies (bun install --frozen-lockfile)")
	if err := runStep(ctx, edgeDir, nil, bun, "install", "--frozen-lockfile"); err != nil {
		return nil, fmt.Errorf("bun install in %s/ failed: %w", edgeDir, err)
	}
	tmp, err := os.MkdirTemp("", "telesphorepo-edge-")
	if err != nil {
		return nil, err
	}
	b := &edgeBundle{files: map[string]string{}, tmp: tmp}
	out := filepath.Join(tmp, "dist")
	step("bundling the edge Worker %s (wrangler deploy --dry-run)", version)
	// The script goes first: --define takes several values, so it would take a script after it.
	if err := runStep(ctx, edgeDir, []string{"WRANGLER_SEND_METRICS=false"}, bun, "x", "wrangler", "deploy", "src/index.ts",
		"--dry-run", "--outdir", out, "--name", "telesphorepo-edge", "--compatibility-date", "2026-10-01",
		"--define", edgeVersionDefine+":"+jsString(version)); err != nil {
		b.close()
		return nil, fmt.Errorf("bundling the edge Worker failed: %w", err)
	}
	err = filepath.WalkDir(out, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(out, p)
		if err != nil {
			return err
		}
		b.files[path.Join("dist", filepath.ToSlash(rel))] = p
		return nil
	})
	if err == nil && b.files["dist/index.js"] == "" {
		err = errors.New("the bundle has no dist/index.js")
	}
	if err == nil {
		b.files["package.json"], err = bundlePackageJSON(tmp)
	}
	if err != nil {
		b.close()
		return nil, err
	}
	for _, f := range []string{"bun.lock", "README.md", "scripts/provision.ts"} {
		src := filepath.Join(edgeDir, filepath.FromSlash(f))
		if _, err := os.Stat(src); err == nil {
			b.files[f] = src
		} else if f != "bun.lock" {
			b.close()
			return nil, fmt.Errorf("%s/%s is missing", edgeDir, f)
		}
	}
	return b, nil
}

// bundlePackageJSON writes edge/package.json without edgeDevScripts into dir and returns its
// path. The dependencies stay as they are, so bun.lock still matches it.
func bundlePackageJSON(dir string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(edgeDir, "package.json"))
	if err != nil {
		return "", err
	}
	var pkg map[string]json.RawMessage
	if err := json.Unmarshal(raw, &pkg); err != nil {
		return "", fmt.Errorf("%s/package.json: %w", edgeDir, err)
	}
	var scripts map[string]string
	if err := json.Unmarshal(pkg["scripts"], &scripts); err != nil {
		return "", fmt.Errorf("%s/package.json scripts: %w", edgeDir, err)
	}
	for _, s := range edgeDevScripts {
		delete(scripts, s)
	}
	for _, s := range []string{"provision", "deploy"} {
		if scripts[s] == "" {
			return "", fmt.Errorf("%s/package.json has no %s script", edgeDir, s)
		}
	}
	if pkg["scripts"], err = json.Marshal(scripts); err != nil {
		return "", err
	}
	body, err := json.MarshalIndent(pkg, "", "  ")
	if err != nil {
		return "", err
	}
	p := filepath.Join(dir, "package.json")
	return p, os.WriteFile(p, append(body, '\n'), 0o644)
}

// clearEdgeDist removes every file of edgeDist except .keep.
func clearEdgeDist() error {
	entries, err := os.ReadDir(edgeDist)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() == ".keep" {
			continue
		}
		if err := os.RemoveAll(filepath.Join(edgeDist, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// writeEdgeDist packs the Worker folder of b into edgedist.ZipFile in edgeDist, each file under
// edgedist.Dir, and writes the version into edgedist.VersionFile.
func writeEdgeDist(b *edgeBundle, version string) error {
	files := map[string]string{}
	for n, src := range b.files {
		files[edgedist.Dir+n] = src
	}
	if err := packZipTree(filepath.Join(edgeDist, edgedist.ZipFile), files); err != nil {
		return fmt.Errorf("pack %s: %w", edgedist.ZipFile, err)
	}
	if err := os.WriteFile(filepath.Join(edgeDist, edgedist.VersionFile), []byte(version+"\n"), 0o644); err != nil {
		return err
	}
	out.line(green("✓ ") + "built the edge Worker download into " + bold(edgeDist))
	return nil
}

// edgeWorker writes the edge Worker folder of version into edgeDist, which the server embeds and
// serves at GET /api/edge/worker.zip. It first removes the folder of an earlier build. Without
// bun it writes none and says so, unless needBun: that server answers the download with 404.
func edgeWorker(ctx context.Context, version string, needBun bool) error {
	if err := clearEdgeDist(); err != nil {
		return err
	}
	if !needBun && lookPath("bun") == "" {
		note("skipping the edge Worker download: bun is not on PATH, so the server has no edge Worker to hand out")
		return nil
	}
	b, err := bundleEdge(ctx, version)
	if err != nil || b == nil {
		return err
	}
	defer b.close()
	return writeEdgeDist(b, version)
}

// edgeArchive packs the Worker folder of b into telesphorepo-edge_<version>.tar.gz in dir and
// returns the archive name.
func edgeArchive(b *edgeBundle, dir, version string) (string, error) {
	archive := fmt.Sprintf("telesphorepo-edge_%s.tar.gz", version)
	if err := packTarGzTree(filepath.Join(dir, archive), b.files); err != nil {
		return "", fmt.Errorf("pack %s: %w", archive, err)
	}
	return archive, nil
}

// sortedNames returns the names of files in order, and fails on a name that leaves the folder.
func sortedNames(files map[string]string) ([]string, error) {
	names := make([]string, 0, len(files))
	for n := range files {
		if strings.Contains(n, "..") {
			return nil, fmt.Errorf("bad archive name %q", n)
		}
		names = append(names, n)
	}
	slices.Sort(names)
	return names, nil
}

// packTarGzTree writes a .tar.gz with files (archive name -> source), each with mode 0644.
func packTarGzTree(archive string, files map[string]string) (err error) {
	names, err := sortedNames(files)
	if err != nil {
		return err
	}
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
	for _, n := range names {
		if err := tarAdd(tw, files[n], n, 0o644); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

// packZipTree writes a .zip with files (archive name -> source).
func packZipTree(archive string, files map[string]string) (err error) {
	names, err := sortedNames(files)
	if err != nil {
		return err
	}
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
	for _, n := range names {
		if err := zipAdd(zw, files[n], n); err != nil {
			return err
		}
	}
	return zw.Close()
}
