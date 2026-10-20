package cargo

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/version"
)

// The sparse index of the Cargo book (Registry Index): one file per crate, one JSON line per
// version, at a path computed from the lowercased name.

// crateNameRE is a crate name as crates.io takes it: an ASCII letter, then letters, digits, "-"
// and "_", at most 64 characters.
var crateNameRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)

// cratesIOIndex is the index URL that Cargo names crates.io by, in a dependency of a crate that
// another registry holds.
const cratesIOIndex = "https://github.com/rust-lang/crates.io-index"

// fold is the name that two crate names clash on: lowercase, with "-" as "_".
func fold(name string) string { return strings.ReplaceAll(strings.ToLower(name), "-", "_") }

// indexPath is the path of the index file of a crate under index/, from its lowercased name:
// 1/{n}, 2/{n}, 3/{c}/{n} or {ab}/{cd}/{n}.
func indexPath(name string) string {
	n := strings.ToLower(name)
	switch len(n) {
	case 1:
		return "1/" + n
	case 2:
		return "2/" + n
	case 3:
		return "3/" + n[:1] + "/" + n
	}
	return n[:2] + "/" + n[2:4] + "/" + n
}

// prefixOf is the {prefix} of a dl template: the folders of the index path of name, in the case
// of name.
func prefixOf(name string) string {
	switch len(name) {
	case 1:
		return "1"
	case 2:
		return "2"
	case 3:
		return "3/" + name[:1]
	}
	return name[:2] + "/" + name[2:4]
}

// nameOfIndexPath returns the crate name of an index path (lowercase), or false when p is not the
// index path of its last segment.
func nameOfIndexPath(p string) (string, bool) {
	name := p[strings.LastIndexByte(p, '/')+1:]
	if !crateNameRE.MatchString(name) || name != strings.ToLower(name) || indexPath(name) != p {
		return "", false
	}
	return name, true
}

// cratePath is the path of the .crate file of a version: crates/{name}/{name}-{version}.crate,
// the path of the dl URL of config.json.
func cratePath(name, vers string) string {
	return "crates/" + name + "/" + name + "-" + vers + ".crate"
}

// parseCratePath splits a crate path into the crate name and the version; false when p is not
// one.
func parseCratePath(p string) (name, vers string, ok bool) {
	segs := strings.Split(p, "/")
	if len(segs) != 3 || segs[0] != "crates" || !crateNameRE.MatchString(segs[1]) {
		return "", "", false
	}
	rest, found := strings.CutPrefix(segs[2], segs[1]+"-")
	if !found {
		return "", "", false
	}
	vers, found = strings.CutSuffix(rest, ".crate")
	if !found {
		return "", "", false
	}
	if _, err := version.ParseSemVer(vers); err != nil {
		return "", "", false
	}
	return segs[1], vers, true
}

// entry is one line of an index file, with its fields in the order of the Cargo book.
type entry struct {
	Name        string              `json:"name"`
	Vers        string              `json:"vers"`
	Deps        []dep               `json:"deps"`
	Cksum       string              `json:"cksum"`
	Features    map[string][]string `json:"features"`
	Yanked      bool                `json:"yanked"`
	Links       *string             `json:"links"`
	V           int                 `json:"v,omitempty"`
	Features2   map[string][]string `json:"features2,omitempty"`
	RustVersion *string             `json:"rust_version,omitempty"`
}

// dep is one dependency of an index entry. Name is the name the crate uses (a renamed dependency's
// new name) and Package the name of the package when it is renamed. Registry nil is this
// registry.
type dep struct {
	Name            string   `json:"name"`
	Req             string   `json:"req"`
	Features        []string `json:"features"`
	Optional        bool     `json:"optional"`
	DefaultFeatures bool     `json:"default_features"`
	Target          *string  `json:"target"`
	Kind            string   `json:"kind"`
	Registry        *string  `json:"registry"`
	Package         *string  `json:"package,omitempty"`
}

// finish puts e in its stored form: the features that use "dep:" or "?/" go to features2 with
// v 2 (as crates.io writes them, so older Cargo versions skip them), every list is non-nil, and
// the dependencies are in one order whichever way the entry came, so two entries of one crate
// compare equal.
func (e *entry) finish() {
	if e.Features == nil {
		e.Features = map[string][]string{}
	}
	e.Features2, e.V = nil, 0
	for k, vals := range e.Features {
		if vals == nil {
			e.Features[k] = []string{}
		}
		if slices.ContainsFunc(vals, func(v string) bool { return strings.HasPrefix(v, "dep:") || strings.Contains(v, "?/") }) {
			if e.Features2 == nil {
				e.Features2 = map[string][]string{}
			}
			e.Features2[k] = vals
			delete(e.Features, k)
		}
	}
	if e.Features2 != nil {
		e.V = 2
	}
	if e.Deps == nil {
		e.Deps = []dep{}
	}
	for i := range e.Deps {
		if e.Deps[i].Features == nil {
			e.Deps[i].Features = []string{}
		}
	}
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	slices.SortStableFunc(e.Deps, func(a, b dep) int {
		return cmp.Or(strings.Compare(a.Name, b.Name), strings.Compare(a.Kind, b.Kind), strings.Compare(str(a.Target), str(b.Target)))
	})
}

// line is the stored JSON of e (yanked false), one line without a newline.
func (e *entry) line() (string, error) {
	c := *e
	c.Yanked = false
	b, err := encodeLine(&c)
	return strings.TrimSuffix(string(b), "\n"), err
}

// encodeLine is the JSON of e with a newline, without HTML escapes ("<" stays "<" in a req).
func encodeLine(e *entry) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(e); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// withYanked returns the stored line with yanked set to y, and a newline.
func withYanked(line string, y bool) ([]byte, error) {
	var e entry
	if err := json.Unmarshal([]byte(line), &e); err != nil {
		return nil, err
	}
	e.Yanked = y
	return encodeLine(&e)
}

// publishMeta is the JSON part of a publish request (Cargo book, Web API: Publish).
type publishMeta struct {
	Name        string              `json:"name"`
	Vers        string              `json:"vers"`
	Deps        []publishDep        `json:"deps"`
	Features    map[string][]string `json:"features"`
	Links       *string             `json:"links"`
	RustVersion *string             `json:"rust_version"`
}

type publishDep struct {
	Name               string   `json:"name"`
	VersionReq         string   `json:"version_req"`
	Features           []string `json:"features"`
	Optional           bool     `json:"optional"`
	DefaultFeatures    bool     `json:"default_features"`
	Target             *string  `json:"target"`
	Kind               string   `json:"kind"`
	Registry           *string  `json:"registry"`
	ExplicitNameInToml *string  `json:"explicit_name_in_toml"`
}

// entryOfPublish converts the JSON of a publish into an index entry with cksum (the Cargo book's
// conversion): version_req becomes req, and a dependency renamed in Cargo.toml has the new name
// in name and the package name in package. A dependency of another kind than dev or build is
// normal.
func entryOfPublish(m *publishMeta, cksum string) *entry {
	e := &entry{Name: m.Name, Vers: m.Vers, Cksum: cksum, Features: maps.Clone(m.Features), Links: m.Links, RustVersion: m.RustVersion}
	for _, d := range m.Deps {
		x := dep{Name: d.Name, Req: d.VersionReq, Features: d.Features, Optional: d.Optional, DefaultFeatures: d.DefaultFeatures,
			Target: d.Target, Kind: kindOf(d.Kind), Registry: d.Registry}
		if n := d.ExplicitNameInToml; n != nil && *n != "" && *n != d.Name {
			pkg := d.Name
			x.Name, x.Package = *n, &pkg
		}
		e.Deps = append(e.Deps, x)
	}
	e.finish()
	return e
}

func kindOf(k string) string {
	if k == "dev" || k == "build" {
		return k
	}
	return "normal"
}

// checkEntry checks the name and the version of e and returns the parsed version.
func checkEntry(e *entry) (version.SemVer, error) {
	if !crateNameRE.MatchString(e.Name) {
		return version.SemVer{}, fmt.Errorf("invalid crate name %q: a crate name starts with a letter and holds letters, digits, - and _, at most 64", e.Name)
	}
	v, err := version.ParseSemVer(e.Vers)
	if err != nil {
		return version.SemVer{}, fmt.Errorf("invalid version %q of crate %s: a crate version is SemVer 2.0, e.g. 1.2.0", e.Vers, e.Name)
	}
	for _, d := range e.Deps {
		if d.Name == "" || d.Req == "" {
			return version.SemVer{}, errors.New("a dependency of the crate has no name or no version requirement")
		}
	}
	return v, nil
}

// baseKey is the sort key of v without its build metadata: two versions with one base key are
// one version for Cargo.
func baseKey(v version.SemVer) string {
	v.Build = ""
	return v.SortKey()
}
