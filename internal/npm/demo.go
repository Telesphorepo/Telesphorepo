package npm

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"

	"github.com/Telesphorepo/Telesphorepo/internal/blobs"
	"github.com/Telesphorepo/Telesphorepo/internal/demo"
	"github.com/Telesphorepo/Telesphorepo/internal/module"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// demoRepos are the npm repos of the demo dataset.
var demoRepos = []module.DemoRepo{{Name: "npm", Description: "JavaScript packages of the team", Public: true}}

var (
	npmWords = []string{"aurora", "basalt", "cadence", "dynamo", "ember", "falcon", "garnet", "harbor", "indigo",
		"juniper", "kestrel", "lantern", "marlin", "nectar", "onyx", "pebble", "quill", "raven", "sable", "tundra",
		"umber", "vortex", "willow", "xenon", "yarrow", "zinnia", "acorn", "bramble", "cobalt", "dapple"}
	npmKinds = []string{"client", "utils", "config", "logger", "cli", "ui", "hooks", "schema"}
)

// Demo fills an npm demo repo: about 40 packages, a third of them under the scope @acme, each with
// 1 to 6 versions (some prereleases), as real tarballs through the format's upload step.
func (h *Handler) Demo(ctx context.Context, run *demo.Run, repo *repos.Repo, rng *rand.Rand) error {
	type pkg struct {
		data []byte
		by   int64
		at   int64
	}
	var all []pkg
	for i, wi := range rng.Perm(len(npmWords))[:20] {
		for _, ki := range rng.Perm(len(npmKinds))[:2] {
			kind := npmKinds[ki]
			name := npmWords[wi] + "-" + kind
			if i%3 == 0 {
				name = "@acme/" + name
			}
			n := 1 + int(5*math.Pow(rng.Float64(), 1.5))
			offsets := demo.Times(rng, n)
			major, minor, patch := rng.IntN(3), rng.IntN(8), rng.IntN(4)
			for vi := range n {
				if vi > 0 {
					switch x := rng.IntN(10); {
					case x < 6:
						patch++
					case x < 9:
						minor, patch = minor+1, 0
					default:
						major, minor, patch = major+1, 0, 0
					}
				}
				v := fmt.Sprintf("%d.%d.%d", major, minor, patch)
				if vi == n-1 && rng.IntN(5) == 0 {
					v += "-beta.1"
				}
				all = append(all, pkg{data: demoTarball(name, v, "a "+kind+" for "+npmWords[wi]), by: run.Uploader(rng, repo, i), at: run.Anchor - offsets[vi]})
			}
		}
	}
	run.Expect(len(all))
	for _, p := range all {
		if _, err := h.ingest(ctx, repo, blobs.Bytes(p.data), nil, demo.Origin(p.by, 0, p.at)); err != nil {
			return fmt.Errorf("npm demo package: %w", err)
		}
		run.Stored(1)
	}
	return nil
}

// demoTarball returns the tarball of a tiny package: package.json, index.js and README.md under
// package/.
func demoTarball(name, version, desc string) []byte {
	pj, _ := json.MarshalIndent(map[string]any{"name": name, "version": version, "description": desc, "main": "index.js",
		"license": "MIT", "keywords": []string{"demo"}}, "", "  ")
	files := []struct{ name, body string }{
		{"package/package.json", string(pj) + "\n"},
		{"package/index.js", fmt.Sprintf("module.exports = { name: %q, version: %q };\n", name, version)},
		{"package/README.md", "# " + name + "\n\n" + desc + ".\n"},
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range files {
		tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o644, Size: int64(len(f.body)), ModTime: demo.ZipTime, Typeflag: tar.TypeReg})
		tw.Write([]byte(f.body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}
