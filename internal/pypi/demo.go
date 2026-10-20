package pypi

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"math/rand/v2"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/blobs"
	"github.com/Telesphorepo/Telesphorepo/internal/demo"
	"github.com/Telesphorepo/Telesphorepo/internal/module"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// The PyPI part of the demo dataset: a public hosted repo of small projects, each version with an
// sdist and a pure-Python wheel built here, stored through ingest.

var demoRepos = []module.DemoRepo{{Name: "pypi", Description: "Python packages of the platform team", Public: true}}

var (
	demoPyPrefixes = []string{"acme", "northwind", "contoso", "globex", "initech", "bluepeak"}
	demoPyWords    = []string{"client", "toolkit", "config", "metrics", "auth", "cli", "schema", "events", "retry", "cache",
		"storage", "tracing", "testing", "jobs", "search", "billing", "geo", "ledger", "notify", "queue"}
	demoPyThings = []string{"HTTP client", "configuration loader", "metrics exporter", "command-line helpers", "schema validator",
		"event bus", "retry policies", "cache layer", "job scheduler", "test fixtures"}
)

// Demo fills a PyPI demo repo with about 40 projects of 1 to 6 versions.
func (h *Handler) Demo(ctx context.Context, run *demo.Run, repo *repos.Repo, rng *rand.Rand) error {
	type file struct {
		name string
		data []byte
		by   int64
		at   int64
	}
	var files []file
	seen := map[string]bool{}
	for i := 0; len(seen) < 40 && i < 400; i++ {
		proj := demo.Pick(rng, demoPyPrefixes) + "-" + demo.Pick(rng, demoPyWords)
		if seen[proj] {
			continue
		}
		seen[proj] = true
		desc := "A " + demo.Pick(rng, demoPyThings) + " for " + strings.SplitN(proj, "-", 2)[0] + " services."
		n := 1 + rng.IntN(6)
		times := demo.Times(rng, n)
		major, minor, patch := rng.IntN(3), rng.IntN(8), 0
		for vi := range n {
			if vi > 0 {
				if rng.IntN(3) == 0 {
					minor, patch = minor+1, 0
				} else {
					patch++
				}
			}
			v := fmt.Sprintf("%d.%d.%d", major, minor, patch)
			at := run.Anchor - times[vi]
			by := run.Uploader(rng, repo, len(seen))
			dist := strings.ReplaceAll(proj, "-", "_")
			files = append(files,
				file{dist + "-" + v + ".tar.gz", demoSdist(proj, dist, v, desc), by, at},
				file{dist + "-" + v + "-py3-none-any.whl", demoWheel(proj, dist, v, desc), by, at})
		}
	}
	run.Expect(len(files))
	for _, f := range files {
		if _, _, err := h.ingest(ctx, repo, f.name, blobs.Bytes(f.data), demo.Origin(f.by, 0, f.at), wantMeta{}); err != nil {
			return fmt.Errorf("upload %s: %w", f.name, err)
		}
		run.Stored(1)
	}
	return nil
}

// demoMetadata is the core metadata of a demo project.
func demoMetadata(proj, v, desc string) string {
	return "Metadata-Version: 2.3\nName: " + proj + "\nVersion: " + v + "\nSummary: " + desc +
		"\nRequires-Python: >=3.9\nLicense: Apache-2.0\n\n" + desc + "\n"
}

// demoWheel is a pure-Python wheel of the demo project with its RECORD.
func demoWheel(proj, dist, v, desc string) []byte {
	info := dist + "-" + v + ".dist-info/"
	files := [][2]string{
		{dist + "/__init__.py", "\"\"\"" + desc + "\"\"\"\n\n__version__ = \"" + v + "\"\n"},
		{info + "METADATA", demoMetadata(proj, v, desc)},
		{info + "WHEEL", "Wheel-Version: 1.0\nGenerator: telesphorepo-demo\nRoot-Is-Purelib: true\nTag: py3-none-any\n\n"},
	}
	var record strings.Builder
	for _, f := range files {
		sum := sha256.Sum256([]byte(f[1]))
		fmt.Fprintf(&record, "%s,sha256=%s,%d\n", f[0], base64.RawURLEncoding.EncodeToString(sum[:]), len(f[1]))
	}
	record.WriteString(info + "RECORD,,\n")
	files = append(files, [2]string{info + "RECORD", record.String()})
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: f[0], Method: zip.Deflate, Modified: demo.ZipTime})
		if err != nil {
			panic(err) // writing to a bytes.Buffer does not fail
		}
		w.Write([]byte(f[1]))
	}
	if err := zw.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// demoSdist is the sdist of the demo project: PKG-INFO, pyproject.toml and the module.
func demoSdist(proj, dist, v, desc string) []byte {
	top := dist + "-" + v + "/"
	files := [][2]string{
		{top + "PKG-INFO", demoMetadata(proj, v, desc)},
		{top + "pyproject.toml", "[build-system]\nrequires = [\"hatchling\"]\nbuild-backend = \"hatchling.build\"\n\n[project]\nname = \"" +
			proj + "\"\nversion = \"" + v + "\"\ndescription = \"" + desc + "\"\nrequires-python = \">=3.9\"\n"},
		{top + "src/" + dist + "/__init__.py", "__version__ = \"" + v + "\"\n"},
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range files {
		tw.WriteHeader(&tar.Header{Name: f[0], Mode: 0o644, Size: int64(len(f[1])), ModTime: demo.ZipTime, Typeflag: tar.TypeReg})
		tw.Write([]byte(f[1]))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}
