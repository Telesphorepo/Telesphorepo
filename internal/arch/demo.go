package arch

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/Telesphorepo/Telesphorepo/internal/blobs"
	"github.com/Telesphorepo/Telesphorepo/internal/demo"
	"github.com/Telesphorepo/Telesphorepo/internal/module"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// demoRepos are the Arch repos of the demo dataset.
var demoRepos = []module.DemoRepo{{Name: "arch", Description: "Arch Linux packages", Public: true}}

// demoPackage is one package of the demo repo.
type demoPackage struct {
	name, desc, arch string
	versions         []string
	depends          []string
}

var demoPackages = []demoPackage{
	{"cairn", "Command line client for the cairn sync service", "", []string{"1.0.0-1", "1.1.0-1", "1.2.0-1"}, []string{"glibc"}},
	{"lodestar", "Tray app that shows the state of a home server", "", []string{"0.9.2-1", "1.0.0-1", "1.0.0-2"}, []string{"glibc", "gtk3"}},
	{"tarn", "Small static file server", "", []string{"2.3.1-1", "2:0.1.0-1"}, []string{"glibc"}},
	{"cairn-completions", "Shell completions of cairn", "any", []string{"1.0.0-1", "1.2.0-1"}, []string{"cairn"}},
}

// Demo fills an Arch demo repo: packages built here for each configured architecture (or any),
// ingested through the asset pipeline with their demo times; the rebuild queue builds and signs
// the indexes once the server serves.
func (h *Handler) Demo(ctx context.Context, run *demo.Run, repo *repos.Repo, rng *rand.Rand) error {
	type item struct {
		data   []byte
		by, at int64
		label  string
	}
	var items []item
	for i, p := range demoPackages {
		archs := architectures(repo)
		if p.arch == "any" {
			archs = []string{"any"}
		}
		times := demo.Times(rng, len(p.versions))
		for vi, v := range p.versions {
			for _, a := range archs {
				data, err := demoPkg(p, v, a, times[vi])
				if err != nil {
					return err
				}
				items = append(items, item{data, run.Uploader(rng, repo, i), times[vi], p.name + " " + v + " " + a})
			}
		}
	}
	run.Expect(len(items))
	for _, it := range items {
		if _, _, err := h.ingest(ctx, repo, blobs.Bytes(it.data), demo.Origin(it.by, 0, it.at)); err != nil {
			return fmt.Errorf("upload %s: %w", it.label, err)
		}
		run.Stored(1)
	}
	return nil
}

// demoPkg builds a package: .PKGINFO and a script in usr/bin, a zstd-compressed tar.
func demoPkg(p demoPackage, version, arch string, at int64) ([]byte, error) {
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	mt := time.Unix(at, 0)
	script := fmt.Sprintf("#!/bin/sh\necho '%s %s'\n", p.name, version)
	file := "usr/bin/" + p.name
	if p.arch == "any" {
		file = "usr/share/" + p.name + "/README"
	}
	var info bytes.Buffer
	fmt.Fprintf(&info, "pkgname = %s\npkgbase = %s\npkgver = %s\npkgdesc = %s\nurl = https://example.org/%s\nbuilddate = %d\npackager = Demo Packaging Team <packages@example.org>\nsize = %d\narch = %s\nlicense = MIT\n",
		p.name, p.name, version, p.desc, p.name, at, len(script), arch)
	for _, d := range p.depends {
		fmt.Fprintf(&info, "depend = %s\n", d)
	}
	entries := []struct {
		name string
		body string
		mode int64
	}{{".PKGINFO", info.String(), 0o644}, {file, script, 0o755}}
	for i, e := range entries {
		if i == 1 {
			dir := ""
			for _, seg := range strings.Split(e.name, "/")[:strings.Count(e.name, "/")] {
				dir += seg + "/"
				if err := tw.WriteHeader(&tar.Header{Name: dir, Typeflag: tar.TypeDir, Mode: 0o755, ModTime: mt, Uname: "root", Gname: "root"}); err != nil {
					return nil, err
				}
			}
		}
		if err := tw.WriteHeader(&tar.Header{Name: e.name, Typeflag: tar.TypeReg, Mode: e.mode, Size: int64(len(e.body)), ModTime: mt, Uname: "root", Gname: "root"}); err != nil {
			return nil, err
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	zw, err := zstd.NewWriter(&out)
	if err != nil {
		return nil, err
	}
	if _, err := zw.Write(raw.Bytes()); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
