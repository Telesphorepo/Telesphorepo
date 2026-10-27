package rpm

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/blobs"
	"github.com/Telesphorepo/Telesphorepo/internal/demo"
	"github.com/Telesphorepo/Telesphorepo/internal/module"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// demoRepos are the RPM repos of the demo dataset.
var demoRepos = []module.DemoRepo{{Name: "rpm", Description: "RPM packages for Fedora and RHEL", Public: true}}

// demoPackage is one package of the demo repo.
type demoPackage struct {
	name, summary string
	noarch        bool
	versions      []string // [epoch:]version-release
	requires      []string
}

var demoPackages = []demoPackage{
	{"cairn", "Command line client for the cairn sync service", false, []string{"1.0.0-1", "1.1.0-1", "1.2.0~rc1-1", "1.2.0-1"}, []string{"glibc"}},
	{"lodestar", "Tray app that shows the state of a home server", false, []string{"0.9.2-1", "1.0.0-1", "1.0.0-2"}, []string{"glibc", "gtk3"}},
	{"tarn", "Small static file server", false, []string{"2.3.1-1", "2:0.1.0-1"}, []string{"glibc"}},
	{"cairn-completions", "Shell completions of cairn", true, []string{"1.0.0-1", "1.2.0-1"}, []string{"cairn"}},
}

// Demo fills an RPM demo repo: packages built here for x86_64 and aarch64 (or noarch), ingested
// through the upload path with their demo times, so they are signed; the rebuild queue builds the
// repodata once the server serves.
func (h *Handler) Demo(ctx context.Context, run *demo.Run, repo *repos.Repo, rng *rand.Rand) error {
	type item struct {
		data   []byte
		by, at int64
		label  string
	}
	var items []item
	for i, p := range demoPackages {
		archs := []string{"x86_64", "aarch64"}
		if p.noarch {
			archs = []string{"noarch"}
		}
		times := demo.Times(rng, len(p.versions))
		for vi, v := range p.versions {
			for _, a := range archs {
				data, err := demoRPM(p, v, a, times[vi])
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

// demoRPM builds a package with a script in /usr/bin (a README for noarch).
func demoRPM(p demoPackage, v, arch string, at int64) ([]byte, error) {
	epoch, ver, rel, ok := parseEVR(v)
	if !ok {
		return nil, fmt.Errorf("demo version %q", v)
	}
	file := "/usr/bin/" + p.name
	body := fmt.Sprintf("#!/bin/sh\necho '%s %s'\n", p.name, v)
	if p.noarch {
		file = "/usr/share/" + p.name + "/README"
		body = p.summary + "\n"
	}
	when := time.Unix(at, 0)
	return writeRPM(buildSpec{Name: p.name, Version: ver, Release: rel, Arch: arch, Epoch: epoch,
		Summary: p.summary, Description: p.summary + ".", License: "MIT", URL: "https://example.org/" + p.name,
		Requires: p.requires, Files: map[string][]byte{file: []byte(body)}, BuildTime: when,
		Changelog: []changelogEntry{{when, "Demo Packaging Team <packages@example.org> - " + v, "- Release " + v}}})
}
