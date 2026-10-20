package cargo

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// A .crate file is a gzipped tar of the folder <name>-<version>/, with the Cargo.toml that
// cargo package normalised: dependencies with their version requirement as written, a
// dependency of another registry with registry-index, a renamed dependency with package, no
// path or git keys.

// maxManifest is the largest Cargo.toml that a crate may hold.
const maxManifest = 1 << 20

// badCrate is a .crate that does not parse: the client's mistake.
type badCrate struct{ msg string }

func (b badCrate) Error() string { return b.msg }

func bad(format string, args ...any) error { return badCrate{fmt.Sprintf(format, args...)} }

// readManifest returns the Cargo.toml of the crate in r: the first file <folder>/Cargo.toml.
// A gzip or tar stream that ends early is io.ErrUnexpectedEOF.
func readManifest(r io.Reader) ([]byte, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, io.ErrUnexpectedEOF
		}
		return nil, bad("the file is not a .crate: it is not gzip")
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		switch {
		case errors.Is(err, io.EOF):
			return nil, bad("the .crate holds no Cargo.toml")
		case errors.Is(err, io.ErrUnexpectedEOF):
			return nil, io.ErrUnexpectedEOF
		case err != nil:
			return nil, bad("the file is not a .crate: %v", err)
		}
		folder, file, ok := strings.Cut(strings.TrimPrefix(h.Name, "./"), "/")
		if !ok || folder == "" || file != "Cargo.toml" || h.Typeflag != tar.TypeReg {
			continue
		}
		if h.Size > maxManifest {
			return nil, bad("the Cargo.toml of the .crate is larger than %d bytes", maxManifest)
		}
		b, err := io.ReadAll(io.LimitReader(tr, maxManifest+1))
		if err != nil {
			return nil, io.ErrUnexpectedEOF
		}
		return b, nil
	}
}

// manifest is the part of a normalised Cargo.toml that an index entry holds.
type manifest struct {
	Package struct {
		Name        string  `toml:"name"`
		Version     string  `toml:"version"`
		Links       *string `toml:"links"`
		RustVersion *string `toml:"rust-version"`
	} `toml:"package"`
	Features map[string][]string `toml:"features"`
	// The dependency tables, decoded by hand: a dependency is a string or a table.
	Dependencies      map[string]any            `toml:"dependencies"`
	DevDependencies   map[string]any            `toml:"dev-dependencies"`
	DevDependencies2  map[string]any            `toml:"dev_dependencies"`
	BuildDependencies map[string]any            `toml:"build-dependencies"`
	BuildDeps2        map[string]any            `toml:"build_dependencies"`
	Target            map[string]map[string]any `toml:"target"`
}

// entryOfManifest converts a normalised Cargo.toml into an index entry with cksum. It is the
// conversion Cargo makes when it publishes: a requirement "1.2" is "^1.2", a dependency without
// registry-index is from crates.io, and one from ownIndex (this registry) has registry nil. A
// renamed dependency has its key as name and the package name in package.
func entryOfManifest(b []byte, cksum string, ownIndex func(string) bool) (*entry, error) {
	var m manifest
	if _, err := toml.Decode(string(b), &m); err != nil {
		return nil, bad("the Cargo.toml of the .crate does not parse: %v", err)
	}
	if m.Package.Name == "" || m.Package.Version == "" {
		return nil, bad("the Cargo.toml of the .crate names no package name or version")
	}
	e := &entry{Name: m.Package.Name, Vers: m.Package.Version, Cksum: cksum, Features: m.Features,
		Links: m.Package.Links, RustVersion: m.Package.RustVersion}
	add := func(table map[string]any, kind string, target *string) error {
		for key, spec := range table {
			d, err := depOf(key, spec, kind, target, ownIndex)
			if err != nil {
				return err
			}
			if d != nil {
				e.Deps = append(e.Deps, *d)
			}
		}
		return nil
	}
	for _, t := range []struct {
		table map[string]any
		kind  string
	}{{m.Dependencies, "normal"}, {m.DevDependencies, "dev"}, {m.DevDependencies2, "dev"}, {m.BuildDependencies, "build"}, {m.BuildDeps2, "build"}} {
		if err := add(t.table, t.kind, nil); err != nil {
			return nil, err
		}
	}
	for platform, tables := range m.Target {
		p := normalizePlatform(platform)
		for key, kind := range map[string]string{"dependencies": "normal", "dev-dependencies": "dev", "dev_dependencies": "dev",
			"build-dependencies": "build", "build_dependencies": "build"} {
			t, _ := tables[key].(map[string]any)
			if err := add(t, kind, &p); err != nil {
				return nil, err
			}
		}
	}
	e.finish()
	return e, nil
}

// depOf converts one dependency of a Cargo.toml table. A dependency without a version (a dev
// dependency by path that cargo package left in) is not in the index: nil.
func depOf(key string, spec any, kind string, target *string, ownIndex func(string) bool) (*dep, error) {
	d := &dep{Name: key, Kind: kind, Target: target, DefaultFeatures: true, Features: []string{}}
	reg := cratesIOIndex
	switch s := spec.(type) {
	case string:
		d.Req = s
	case map[string]any:
		d.Req, _ = s["version"].(string)
		if v, ok := s["optional"].(bool); ok {
			d.Optional = v
		}
		for _, k := range []string{"default-features", "default_features"} {
			if v, ok := s[k].(bool); ok {
				d.DefaultFeatures = v
			}
		}
		if fs, ok := s["features"].([]any); ok {
			for _, f := range fs {
				if f, ok := f.(string); ok {
					d.Features = append(d.Features, f)
				}
			}
		}
		if p, ok := s["package"].(string); ok && p != "" && p != key {
			d.Package = &p
		}
		if ri, ok := s["registry-index"].(string); ok && ri != "" {
			reg = ri
		}
	default:
		return nil, bad("the dependency %s of the Cargo.toml is neither a string nor a table", key)
	}
	if d.Req == "" {
		return nil, nil
	}
	req, err := normalizeReq(d.Req)
	if err != nil {
		return nil, bad("the dependency %s of the Cargo.toml has the version requirement %q: %v", key, d.Req, err)
	}
	d.Req = req
	if !ownIndex(reg) {
		d.Registry = &reg
	}
	return d, nil
}

// normalizeReq prints a version requirement as Cargo sends it in a publish (the Display of the
// semver crate's VersionReq): each comparator with its operator, a bare version with "^", and the
// comparators joined by ", ". "*" stays "*".
func normalizeReq(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "*" || s == "" {
		return "*", nil
	}
	var out []string
	for c := range strings.SplitSeq(s, ",") {
		c = strings.TrimSpace(c)
		op := ""
		for _, o := range []string{">=", "<=", "=", ">", "<", "~", "^"} {
			if rest, ok := strings.CutPrefix(c, o); ok {
				op, c = o, strings.TrimSpace(rest)
				break
			}
		}
		if c == "" {
			return "", errors.New("a comparator has no version")
		}
		main, pre, hasPre := strings.Cut(c, "-")
		if i := strings.IndexByte(main, '+'); i >= 0 {
			main = main[:i] // build metadata does not count
		}
		parts := strings.Split(main, ".")
		if len(parts) > 3 {
			return "", errors.New("a version has more than three numbers")
		}
		wild := false
		var nums []string
		for i, p := range parts {
			if p == "*" || p == "x" || p == "X" {
				if i == 0 {
					if len(parts) == 1 && op == "" {
						return "", errors.New("* cannot be one of several comparators")
					}
					return "", errors.New("the major version cannot be a wildcard")
				}
				wild = true
				break
			}
			if _, err := strconv.ParseUint(p, 10, 64); err != nil {
				return "", fmt.Errorf("%q is not a number", p)
			}
			nums = append(nums, strings.TrimLeft(p, "0"))
			if nums[len(nums)-1] == "" {
				nums[len(nums)-1] = "0"
			}
		}
		if wild && (op == "" || op == "=") {
			op = "" // a wildcard comparator prints without an operator
		} else if op == "" {
			op = "^"
		}
		v := strings.Join(nums, ".")
		if wild {
			v += ".*"
		} else if hasPre && len(nums) == 3 {
			v += "-" + pre
		}
		out = append(out, op+v)
	}
	return strings.Join(out, ", "), nil
}

// normalizePlatform prints the target of a target table as Cargo sends it (the Display of a
// cargo-platform Platform): a cfg() expression with its spacing normalised, a target triple as it
// is. A cfg that does not parse stays as it is.
func normalizePlatform(s string) string {
	inner, ok := strings.CutPrefix(strings.TrimSpace(s), "cfg(")
	if !ok || !strings.HasSuffix(inner, ")") {
		return s
	}
	p := &cfgParser{s: inner[:len(inner)-1]}
	out, ok := p.expr()
	if !ok || strings.TrimSpace(p.s[p.i:]) != "" {
		return s
	}
	return "cfg(" + out + ")"
}

// cfgParser parses a cfg expression: name, name = "value", all(...), any(...), not(...).
type cfgParser struct {
	s string
	i int
}

func (p *cfgParser) skip() {
	for p.i < len(p.s) && (p.s[p.i] == ' ' || p.s[p.i] == '\t') {
		p.i++
	}
}

func (p *cfgParser) ident() string {
	p.skip()
	j := p.i
	for p.i < len(p.s) {
		c := p.s[p.i]
		if c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			p.i++
			continue
		}
		break
	}
	return p.s[j:p.i]
}

func (p *cfgParser) eat(c byte) bool {
	p.skip()
	if p.i < len(p.s) && p.s[p.i] == c {
		p.i++
		return true
	}
	return false
}

func (p *cfgParser) expr() (string, bool) {
	name := p.ident()
	if name == "" {
		return "", false
	}
	if (name == "all" || name == "any" || name == "not") && p.eat('(') {
		var args []string
		for !p.eat(')') {
			if len(args) > 0 && !p.eat(',') {
				return "", false
			}
			if p.eat(')') { // a trailing comma
				break
			}
			a, ok := p.expr()
			if !ok {
				return "", false
			}
			args = append(args, a)
		}
		return name + "(" + strings.Join(args, ", ") + ")", true
	}
	if p.eat('=') {
		p.skip()
		if p.i >= len(p.s) || p.s[p.i] != '"' {
			return "", false
		}
		end := strings.IndexByte(p.s[p.i+1:], '"')
		if end < 0 {
			return "", false
		}
		v := p.s[p.i : p.i+end+2]
		p.i += end + 2
		return name + " = " + v, true
	}
	return name, true
}
