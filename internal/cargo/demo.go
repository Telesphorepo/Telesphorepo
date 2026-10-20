package cargo

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/blobs"
	"github.com/Telesphorepo/Telesphorepo/internal/demo"
	"github.com/Telesphorepo/Telesphorepo/internal/module"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// demoRepos are the Cargo repos of the demo dataset.
var demoRepos = []module.DemoRepo{{Name: "crates", Description: "Rust crates of the platform team", Public: true}}

// demoCrates are the crates of the demo repo; each later crate may depend on earlier ones.
var demoCrates = []string{"acme-core", "acme-config", "acme-log", "acme-http", "acme-db", "acme-cli", "telemetry", "retry_policy"}

// demoCrate returns a .crate of name and vers with deps (crate -> requirement) of this registry and
// one dependency of crates.io: its normalised Cargo.toml and src/lib.rs.
func demoCrate(name, vers string, deps map[string]string, desc string) ([]byte, error) {
	var toml strings.Builder
	fmt.Fprintf(&toml, "[package]\nedition = \"2021\"\nname = %q\nversion = %q\ndescription = %q\nlicense = \"Apache-2.0\"\n\n", name, vers, desc)
	toml.WriteString("[features]\ndefault = [\"std\"]\nstd = []\nserde = [\"dep:serde\"]\n\n")
	toml.WriteString("[dependencies.serde]\nversion = \"1.0\"\noptional = true\nfeatures = [\"derive\"]\n")
	for d, req := range deps {
		fmt.Fprintf(&toml, "\n[dependencies.%s]\nversion = %q\nregistry-index = \"sparse+https://repo.example.com/cargo/crates/index/\"\n", d, req)
	}
	files := []struct{ name, body string }{
		{"Cargo.toml", toml.String()},
		{"src/lib.rs", fmt.Sprintf("//! %s\n\npub fn version() -> &'static str {\n    %q\n}\n", desc, vers)},
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name + "-" + vers + "/" + f.name, Mode: 0o644, Size: int64(len(f.body)),
			ModTime: time.Unix(1, 0), Typeflag: tar.TypeReg}); err != nil {
			return nil, err
		}
		if _, err := tw.Write([]byte(f.body)); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Demo fills a Cargo demo repo: a few versions of each crate, published through the same path as
// an upload, each with its demo time.
func (h *Handler) Demo(ctx context.Context, run *demo.Run, repo *repos.Repo, rng *rand.Rand) error {
	type plan struct {
		name, vers string
		deps       map[string]string
		at         int64
		owner      int
	}
	var all []plan
	for i, c := range demoCrates {
		n := 2 + rng.IntN(4)
		offsets := demo.Times(rng, n)
		minor := rng.IntN(3)
		for vi := range n {
			deps := map[string]string{}
			if i > 0 && rng.IntN(2) == 0 {
				deps[demoCrates[rng.IntN(i)]] = "0.1"
			}
			all = append(all, plan{name: c, vers: fmt.Sprintf("0.%d.%d", minor+vi/3, vi%3), deps: deps, at: run.Anchor - offsets[vi], owner: i})
		}
	}
	run.Expect(len(all))
	for _, p := range all {
		data, err := demoCrate(p.name, p.vers, p.deps, "The "+strings.ReplaceAll(p.name, "-", " ")+" crate of the demo.")
		if err != nil {
			return err
		}
		if _, err := h.storeCrate(ctx, repo, blobs.Bytes(data), demo.Origin(run.Uploader(rng, repo, p.owner), 0, p.at)); err != nil {
			return fmt.Errorf("demo crate %s %s: %w", p.name, p.vers, err)
		}
		run.Stored(1)
	}
	return nil
}
