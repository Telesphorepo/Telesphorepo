package debian

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"math"
	"math/rand/v2"
	"path"
	"slices"
	"strings"
	"sync"

	"github.com/Telesphorepo/Telesphorepo/internal/blobs"
	"github.com/Telesphorepo/Telesphorepo/internal/demo"
	"github.com/Telesphorepo/Telesphorepo/internal/module"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

const (
	demoDebPackages = 300
	demoDebDist     = "stable"
	demoDebComp     = "main"
	demoDebWorkers  = 4 // concurrent .deb uploads
)

var (
	debWords = []string{
		"aurora", "basalt", "cadence", "dynamo", "ember", "falcon", "garnet", "harbor", "indigo",
		"juniper", "kestrel", "lantern", "marlin", "nectar", "onyx", "pebble", "quill", "raven",
		"sable", "tundra", "umber", "vortex", "willow", "xenon", "yarrow", "zinnia", "acorn",
		"bramble", "cobalt", "dapple", "estuary", "fjord", "glimmer", "hazel", "islet", "jasper",
		"kelpie", "lichen", "meadow", "nimbus", "orchid", "prism", "quartz", "ripple", "sorrel",
		"thistle", "upland", "verdant", "wren", "alder", "birch", "cedar", "dune", "ermine",
		"fennel", "gorse", "heron", "ivy", "jackdaw", "kiln", "larch", "moss", "nettle", "osprey",
		"plover", "quince", "rowan", "sedge", "tansy", "umbra", "vetch", "wicket", "yew", "anvil",
		"bellows", "chisel", "dowel", "easel", "ferrule", "gimlet", "hasp", "ingot", "keel",
		"lathe", "mallet", "nib", "oakum", "pulley", "quoin", "rivet", "sprocket", "tackle",
		"vise", "winch", "yoke", "abacus", "beacon", "cipher", "delta", "ether", "flux", "glyph",
		"helix", "joule", "kelvin", "lumen", "matrix", "nexus", "optic", "pulse", "quark", "radix",
		"sigma", "tensor", "vector",
	}
	debAdjectives = []string{"fast", "small", "scriptable", "robust", "portable", "friendly", "minimal", "parallel"}
	debThings     = []string{
		"log processor", "file synchronizer", "metrics collector", "job scheduler", "config manager",
		"archive tool", "network probe", "cache server", "search indexer", "backup agent",
	}
	debMaintainers = []string{
		"Tomas Novak <tomas@example.org>", "Priya Raman <priya@example.org>",
		"Demo Packaging Team <packages@example.org>",
	}
)

// demoDeb is one .deb file of the debian repo.
type demoDeb struct {
	name string // <package>_<version>_<arch>.deb
	data []byte
	by   int64
	at   int64
}

// debFlavor is one binary package built from a source package.
type debFlavor struct {
	kind, name, arch, section string
}

// demoRepos are the Debian repos of the demo dataset.
var demoRepos = []module.DemoRepo{{Name: "debian", Description: "Debian packages for stable/main", Public: true}}

// Demo fills a Debian demo repo: it ingests the packages through the asset pipeline, each with
// its demo time (the rebuild queue builds the indexes once the server serves).
func (h *Handler) Demo(ctx context.Context, run *demo.Run, repo *repos.Repo, rng *rand.Rand) error {
	debs := planDebian(run, repo, rng)
	run.Expect(len(debs))
	return h.uploadDebs(ctx, run, repo, debs)
}

// planDebian builds about demoDebPackages binary packages from sources named after debWords,
// each source with 1 to 5 versions, as real .deb files for amd64 and arm64, or for "all".
func planDebian(run *demo.Run, repo *repos.Repo, rng *rand.Rand) []demoDeb {
	var out []demoDeb
	packages := 0
	for si, wi := range rng.Perm(len(debWords)) {
		if packages >= demoDebPackages {
			break
		}
		src := debWords[wi]
		so := fmt.Sprint(1 + rng.IntN(6))
		flavors := []debFlavor{{"bin", src, "any", "utils"}}
		for _, f := range []debFlavor{
			{"lib", "lib" + src + so, "any", "libs"},
			{"dev", "lib" + src + "-dev", "any", "libdevel"},
			{"utils", src + "-utils", "any", "utils"},
			{"common", src + "-common", "all", "misc"},
			{"doc", src + "-doc", "all", "doc"},
			{"py", "python3-" + src, "all", "python"},
			{"daemon", src + "d", "any", "net"},
		} {
			if rng.IntN(100) < 30 {
				flavors = append(flavors, f)
			}
		}
		// lib<src>-dev needs lib<src><so>.
		has := func(kind string) bool {
			return slices.ContainsFunc(flavors, func(f debFlavor) bool { return f.kind == kind })
		}
		if has("dev") && !has("lib") {
			flavors = slices.DeleteFunc(flavors, func(f debFlavor) bool { return f.kind == "dev" })
		}
		packages += len(flavors)

		n := 1 + int(4*math.Pow(rng.Float64(), 1.5))
		epoch := ""
		if rng.IntN(20) == 0 {
			epoch = "1:"
		}
		major, minor, patch, rev := rng.IntN(4), rng.IntN(10), rng.IntN(5), 1
		offsets := demo.Times(rng, n)
		maint := demo.Pick(rng, debMaintainers)
		desc := "a " + demo.Pick(rng, debAdjectives) + " " + demo.Pick(rng, debThings)
		for vi := range n {
			if vi > 0 {
				switch x := rng.IntN(10); {
				case x < 3:
					rev++
				case x < 8:
					patch, rev = patch+1, 1
				default:
					minor, patch, rev = minor+1, 0, 1
				}
			}
			upstream := fmt.Sprintf("%d.%d.%d-%d", major, minor, patch, rev)
			version := epoch + upstream
			at := run.Anchor - offsets[vi]
			by := run.Uploader(rng, repo, si)
			for _, f := range flavors {
				archs := []string{"amd64", "arm64"}
				if f.arch == "all" {
					archs = []string{"all"}
				}
				for _, arch := range archs {
					control, files := debContents(f, src, so, version, arch, maint, desc, flavors)
					out = append(out, demoDeb{
						name: f.name + "_" + upstream + "_" + arch + ".deb",
						data: buildDeb(control, files),
						by:   by, at: at,
					})
				}
			}
		}
	}
	return out
}

type debData struct {
	path string // relative, no leading "./"
	mode int64
	body string
}

// debContents returns the control file and data files of one binary package.
func debContents(f debFlavor, src, so, version, arch, maint, desc string, flavors []debFlavor) (string, []debData) {
	has := func(kind string) bool {
		return slices.ContainsFunc(flavors, func(x debFlavor) bool { return x.kind == kind })
	}
	var depends []string
	var short string
	var files []debData
	switch f.kind {
	case "bin":
		short = "command-line tools for " + src
		depends = append(depends, "libc6 (>= 2.36)")
		if has("lib") {
			depends = append(depends, "lib"+src+so+" (= "+version+")")
		}
		files = append(files, debData{"usr/bin/" + src, 0o755, "#!/bin/sh\necho \"" + src + " " + version + "\"\n"})
	case "lib":
		short = "shared library for " + src
		depends = append(depends, "libc6 (>= 2.36)")
	case "dev":
		short = "development files for lib" + src
		depends = append(depends, "lib"+src+so+" (= "+version+")")
		files = append(files, debData{"usr/include/" + src + ".h", 0o644, "/* " + src + " " + version + " */\n#pragma once\nint " + src + "_version(void);\n"})
	case "utils":
		short = "extra utilities for " + src
		depends = append(depends, src+" (= "+version+")")
		files = append(files, debData{"usr/bin/" + src + "-admin", 0o755, "#!/bin/sh\necho \"" + src + "-admin " + version + "\"\n"})
	case "common":
		short = "architecture-independent files for " + src
		files = append(files, debData{"usr/share/" + src + "/defaults.conf", 0o644, "# Defaults for " + src + "\nlog_level = info\n"})
	case "doc":
		short = "documentation for " + src
		files = append(files, debData{"usr/share/doc/" + f.name + "/README", 0o644, src + " " + version + "\n\nSee https://" + src + ".example.org for the manual.\n"})
	case "py":
		short = "Python 3 bindings for " + src
		depends = append(depends, "python3:any")
		files = append(files, debData{"usr/lib/python3/dist-packages/" + src + "/__init__.py", 0o644, "__version__ = \"" + version + "\"\n"})
	case "daemon":
		short = "background service for " + src
		depends = append(depends, "adduser", src+" (= "+version+")")
		if has("common") {
			depends = append(depends, src+"-common (= "+version+")")
		}
		files = append(files, debData{"usr/sbin/" + f.name, 0o755, "#!/bin/sh\nexec " + src + " --serve \"$@\"\n"})
	}
	files = append(files, debData{"usr/share/doc/" + f.name + "/copyright", 0o644,
		"Format: https://www.debian.org/doc/packaging-manuals/copyright-format/1.0/\nUpstream-Name: " + src + "\n\nFiles: *\nCopyright: 2024 The " + src + " authors\nLicense: Apache-2.0\n"})

	size := 0
	for _, d := range files {
		size += len(d.body)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Package: %s\n", f.name)
	if f.name != src {
		fmt.Fprintf(&b, "Source: %s\n", src)
	}
	fmt.Fprintf(&b, "Version: %s\nArchitecture: %s\nMaintainer: %s\nInstalled-Size: %d\n", version, arch, maint, max(1, (size+1023)/1024))
	if len(depends) > 0 {
		fmt.Fprintf(&b, "Depends: %s\n", strings.Join(depends, ", "))
	}
	fmt.Fprintf(&b, "Section: %s\nPriority: optional\nHomepage: https://%s.example.org\n", f.section, src)
	fmt.Fprintf(&b, "Description: %s\n %s is %s.\n .\n This package contains the %s.\n", short, src, desc, short)
	return b.String(), files
}

// buildDeb returns a .deb: an ar archive of debian-binary, control.tar.gz (control, md5sums)
// and data.tar.gz.
func buildDeb(control string, files []debData) []byte {
	var md5sums strings.Builder
	for _, f := range files {
		s := md5.Sum([]byte(f.body))
		fmt.Fprintf(&md5sums, "%s  %s\n", hex.EncodeToString(s[:]), f.path)
	}
	controlTar := tarGz([]debData{{"control", 0o644, control}, {"md5sums", 0o644, md5sums.String()}})
	dataTar := tarGz(files)

	var ar bytes.Buffer
	ar.WriteString("!<arch>\n")
	for _, m := range []struct {
		name string
		data []byte
	}{{"debian-binary", []byte("2.0\n")}, {"control.tar.gz", controlTar}, {"data.tar.gz", dataTar}} {
		fmt.Fprintf(&ar, "%-16s%-12d%-6d%-6d%-8s%-10d`\n", m.name, demo.ZipTime.Unix(), 0, 0, "100644", len(m.data))
		ar.Write(m.data)
		if len(m.data)%2 == 1 {
			ar.WriteByte('\n')
		}
	}
	return ar.Bytes()
}

// tarGz writes files under "./" with their parent directories, in path order.
func tarGz(files []debData) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	dirs := map[string]bool{}
	var names []string
	for _, f := range files {
		for d := path.Dir(f.path); d != "."; d = path.Dir(d) {
			dirs[d] = true
		}
	}
	for d := range dirs {
		names = append(names, d)
	}
	slices.Sort(names)
	tw.WriteHeader(&tar.Header{Name: "./", Mode: 0o755, ModTime: demo.ZipTime, Typeflag: tar.TypeDir})
	for _, d := range names {
		tw.WriteHeader(&tar.Header{Name: "./" + d + "/", Mode: 0o755, ModTime: demo.ZipTime, Typeflag: tar.TypeDir})
	}
	sorted := slices.Clone(files)
	slices.SortFunc(sorted, func(a, b debData) int { return strings.Compare(a.path, b.path) })
	for _, f := range sorted {
		tw.WriteHeader(&tar.Header{Name: "./" + f.path, Mode: f.mode, Size: int64(len(f.body)), ModTime: demo.ZipTime, Typeflag: tar.TypeReg})
		tw.Write([]byte(f.body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// uploadDebs ingests the packages through the asset pipeline, each with its demo time and
// uploader.
func (h *Handler) uploadDebs(ctx context.Context, run *demo.Run, repo *repos.Repo, debs []demoDeb) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	idx := make(chan int)
	var wg sync.WaitGroup
	for range demoDebWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range idx {
				if ctx.Err() != nil {
					continue
				}
				if err := h.uploadDeb(ctx, repo, debs[i]); err != nil {
					cancel(err)
					continue
				}
				run.Stored(1)
			}
		}()
	}
feed:
	for i := range debs {
		select {
		case idx <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(idx)
	wg.Wait()
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	return nil
}

func (h *Handler) uploadDeb(ctx context.Context, repo *repos.Repo, d demoDeb) error {
	if _, _, err := h.ingest(ctx, repo, demoDebDist, demoDebComp, blobs.Bytes(d.data), demo.Origin(d.by, 0, d.at)); err != nil {
		return fmt.Errorf("upload %s: %w", d.name, err)
	}
	return nil
}
