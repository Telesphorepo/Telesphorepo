package nuget

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/blobs"
	"github.com/Telesphorepo/Telesphorepo/internal/demo"
	"github.com/Telesphorepo/Telesphorepo/internal/module"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// demoRepos are the NuGet repos of the demo dataset.
var demoRepos = []module.DemoRepo{{Name: "nuget", Description: ".NET libraries of the platform team", Public: true}}

var (
	demoNames = []string{"Logging", "Configuration", "Http", "Caching", "Messaging", "Storage", "Telemetry",
		"Security", "Scheduling", "Validation", "Serialization", "Search", "Billing", "Notifications", "Identity",
		"Payments", "Reporting", "Workflow", "Geo", "Imaging"}
	demoKinds   = []string{"", ".Abstractions", ".Testing", ".AspNetCore"}
	demoAuthors = []string{"Platform Team", "Mira Okafor", "Jonas Lind", "Ana Souza"}
	demoTFMs    = []string{"net8.0", "net9.0", "netstandard2.0"}
)

// demoPackage is one .nupkg of the demo repo.
type demoPackage struct {
	name string
	data []byte
	by   int64
	at   int64
}

// Demo fills a NuGet demo repo: packages of the platform team, each with 1 to 6 versions (some
// prereleases), pushed through the push of the module with their demo times.
func (h *Handler) Demo(ctx context.Context, run *demo.Run, repo *repos.Repo, rng *rand.Rand) error {
	var pkgs []demoPackage
	for i, ni := range rng.Perm(len(demoNames)) {
		base := "Contoso." + demoNames[ni]
		kinds := []string{""}
		for _, k := range demoKinds[1:] {
			if rng.IntN(100) < 35 {
				kinds = append(kinds, k)
			}
		}
		n := 1 + int(5*math.Pow(rng.Float64(), 1.5))
		offsets := demo.Times(rng, n)
		major, minor, patch := 1+rng.IntN(3), rng.IntN(8), 0
		author := demo.Pick(rng, demoAuthors)
		tfm := demo.Pick(rng, demoTFMs)
		for vi := range n {
			if vi > 0 {
				if rng.IntN(3) == 0 {
					minor, patch = minor+1, 0
				} else {
					patch++
				}
			}
			v := fmt.Sprintf("%d.%d.%d", major, minor, patch)
			if vi == n-1 && rng.IntN(4) == 0 {
				v = fmt.Sprintf("%d.%d.0-preview.%d", major, minor+1, 1+rng.IntN(3))
			}
			by, at := run.Uploader(rng, repo, i), run.Anchor-offsets[vi]
			for _, k := range kinds {
				id := base + k
				var deps []string
				if k != "" {
					deps = append(deps, base)
				}
				pkgs = append(pkgs, demoPackage{name: id + "." + v + ".nupkg", data: demoNupkg(id, v, author, tfm, deps), by: by, at: at})
			}
		}
	}
	run.Expect(len(pkgs))
	for _, p := range pkgs {
		if _, _, err := h.ingest(ctx, repo, blobs.Bytes(p.data), demo.Origin(p.by, 0, p.at)); err != nil {
			return fmt.Errorf("push %s: %w", p.name, err)
		}
		run.Stored(1)
	}
	return nil
}

// demoNupkg builds a small .nupkg: the .nuspec and an assembly placeholder for one framework.
func demoNupkg(id, v, author, tfm string, deps []string) []byte {
	var dg strings.Builder
	if len(deps) > 0 {
		dg.WriteString("    <dependencies>\n      <group targetFramework=\"" + tfm + "\">\n")
		for _, d := range deps {
			dg.WriteString("        <dependency id=\"" + d + "\" version=\"" + v + "\" exclude=\"Build,Analyzers\" />\n")
		}
		dg.WriteString("      </group>\n    </dependencies>\n")
	}
	nuspec := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://schemas.microsoft.com/packaging/2013/05/nuspec.xsd">
  <metadata>
    <id>%[1]s</id>
    <version>%[2]s</version>
    <authors>%[3]s</authors>
    <description>%[1]s: shared code of the platform team.</description>
    <license type="expression">MIT</license>
    <projectUrl>https://example.org/%[4]s</projectUrl>
    <tags>contoso platform</tags>
%[5]s  </metadata>
</package>
`, id, v, author, strings.ToLower(id), dg.String())
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range []struct{ name, body string }{
		{id + ".nuspec", nuspec},
		{"lib/" + tfm + "/" + id + ".dll", "demo assembly of " + id + " " + v + "\n"},
	} {
		w, _ := zw.CreateHeader(&zip.FileHeader{Name: f.name, Method: zip.Deflate, Modified: demo.ZipTime})
		w.Write([]byte(f.body))
	}
	zw.Close()
	return buf.Bytes()
}
