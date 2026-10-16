// Package changelog reads the change log changes.toml: the entries that tell an older and a newer
// side (the server and the CLI) when one of them fails in a scope. It uses only the standard
// library, the TOML parser and the root package, so the server and the CLI both import it.
package changelog

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"

	telesphorepo "github.com/Telesphorepo/Telesphorepo"
)

// Dev is the version of an entry before a release, and of a build that has no version stamp.
const Dev = "dev"

// The values of Change.Needs. An unknown value counts as NeedsCLI.
const (
	NeedsServer = "server"
	NeedsCLI    = "cli"
)

// Change is one entry of the change log. A field that this version does not know is ignored.
type Change struct {
	Version string `toml:"version" json:"version"`
	Scope   string `toml:"scope" json:"scope"`
	Needs   string `toml:"needs" json:"needs"`
	Summary string `toml:"summary" json:"summary"`
}

// NeedsServer reports whether the server must be at least c.Version for the scope to work.
// Every other value of Needs means the CLI.
func (c Change) NeedsServer() bool { return c.Needs == NeedsServer }

// Matches reports whether c concerns scope: the scope of c is scope, or a scope above it
// ("publish" matches "publish/npm").
func (c Change) Matches(scope string) bool {
	return c.Scope == scope || strings.HasPrefix(scope, c.Scope+"/")
}

// Parse reads a change log. Each entry needs a version (Dev or a release version), a scope and a
// summary.
func Parse(data []byte) ([]Change, error) {
	var f struct {
		Change []Change `toml:"change"`
	}
	if err := toml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("changes.toml: %w", err)
	}
	for i, c := range f.Change {
		if c.Version != Dev {
			if _, err := ParseVersion(c.Version); err != nil {
				return nil, fmt.Errorf("changes.toml: entry %d: %w", i+1, err)
			}
		}
		if c.Scope == "" || c.Summary == "" {
			return nil, fmt.Errorf("changes.toml: entry %d: scope and summary are required", i+1)
		}
	}
	return f.Change, nil
}

var embedded = sync.OnceValues(func() ([]Change, error) { return Parse(telesphorepo.ChangesTOML) })

// Embedded returns the entries of the change log that this program embeds.
func Embedded() ([]Change, error) { return embedded() }

// Since returns the released entries of changes that are newer than version since, in their
// order. An empty since returns every released entry. Entries of Dev count for nothing.
func Since(changes []Change, since string) ([]Change, error) {
	var from Version
	if since != "" {
		v, err := ParseVersion(since)
		if err != nil {
			return nil, err
		}
		from = v
	}
	out := []Change{}
	for _, c := range changes {
		if c.Version == Dev {
			continue
		}
		v, err := ParseVersion(c.Version)
		if err != nil {
			return nil, err
		}
		if since == "" || v.Compare(from) > 0 {
			out = append(out, c)
		}
	}
	return out, nil
}

// Version is a release version: MAJOR.MINOR.PATCH with an optional "v" before it, an optional
// pre-release after "-" and optional build data after "+" (semantic versioning).
type Version struct {
	nums [3]int
	pre  []string
}

var versionRE = regexp.MustCompile(`^v?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$`)

// ErrVersion is the error of a text that is not a release version.
var ErrVersion = errors.New("not a release version (want MAJOR.MINOR.PATCH, e.g. v1.2.3)")

// ParseVersion reads a release version, e.g. "v1.2.3" or "1.2.3-rc.1".
func ParseVersion(s string) (Version, error) {
	m := versionRE.FindStringSubmatch(s)
	if m == nil {
		return Version{}, fmt.Errorf("%q: %w", s, ErrVersion)
	}
	var v Version
	for i := range 3 {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return Version{}, fmt.Errorf("%q: %w", s, ErrVersion)
		}
		v.nums[i] = n
	}
	if m[4] != "" {
		v.pre = strings.Split(m[4], ".")
		for _, id := range v.pre {
			if id == "" {
				return Version{}, fmt.Errorf("%q: %w", s, ErrVersion)
			}
		}
	}
	return v, nil
}

// Compare returns -1, 0 or 1 when v is older than, the same as or newer than o. A pre-release is
// older than its release. Build data does not count.
func (v Version) Compare(o Version) int {
	for i := range 3 {
		if c := cmpInt(v.nums[i], o.nums[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(v.pre) == 0 && len(o.pre) == 0:
		return 0
	case len(v.pre) == 0:
		return 1
	case len(o.pre) == 0:
		return -1
	}
	for i := 0; i < len(v.pre) && i < len(o.pre); i++ {
		if c := cmpIdent(v.pre[i], o.pre[i]); c != 0 {
			return c
		}
	}
	return cmpInt(len(v.pre), len(o.pre))
}

// cmpIdent compares two pre-release identifiers: numbers by value, and a number before text.
func cmpIdent(a, b string) int {
	na, ea := strconv.Atoi(a)
	nb, eb := strconv.Atoi(b)
	switch {
	case ea == nil && eb == nil:
		return cmpInt(na, nb)
	case ea == nil:
		return -1
	case eb == nil:
		return 1
	}
	return strings.Compare(a, b)
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// devVersionRE finds the "dev" value of each version field of a change log.
var devVersionRE = regexp.MustCompile(`(?m)^(\s*version\s*=\s*)"dev"`)

// Stamp returns the change log data with version in place of each "dev" version, and the number
// of entries it changed. It keeps every other byte of data, so comments and layout stay.
func Stamp(data []byte, version string) ([]byte, int, error) {
	if _, err := ParseVersion(version); err != nil {
		return nil, 0, err
	}
	n := len(devVersionRE.FindAllIndex(data, -1))
	out := devVersionRE.ReplaceAll(data, []byte(`${1}"`+version+`"`))
	if _, err := Parse(out); err != nil {
		return nil, 0, err
	}
	return out, n, nil
}
