package npm

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/version"
)

// Package names and the URLs of the registry. A name is "name" or "@scope/name". A client
// sends a scoped name in one segment with an encoded slash ("@scope%2fname") or in two segments
// ("@scope/name"); httpx.RepoPath refuses %2f, so the routes read the escaped path themselves.

// maxName is the longest package name (npm's limit).
const maxName = 214

var errBadName = errors.New("invalid package name")

// nameChar reports whether c may appear in a part of a name that the registry reads: the URL-safe
// characters of npm names (legacy names may hold upper case letters).
func nameChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '.' || c == '_' || c == '~'
}

// validPart checks one part of a name: a scope without "@" or the name after it.
func validPart(s string) bool {
	if s == "" || s[0] == '.' || s[0] == '_' {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !nameChar(s[i]) {
			return false
		}
	}
	return true
}

// validName checks a name that the registry reads or serves.
func validName(n string) bool {
	if n == "" || len(n) > maxName {
		return false
	}
	if rest, ok := strings.CutPrefix(n, "@"); ok {
		scope, name, ok := strings.Cut(rest, "/")
		return ok && validPart(scope) && validPart(name)
	}
	return validPart(n)
}

// checkNewName checks the name of a version that a publish, an upload or an import adds: the
// rules of new npm packages, so no upper case letters.
func checkNewName(n string) error {
	if !validName(n) {
		return errBadName
	}
	if strings.ToLower(n) != n {
		return errors.New("a package name must be lower case")
	}
	return nil
}

// unscoped returns the name without its scope: "@acme/lib" -> "lib".
func unscoped(n string) string {
	if i := strings.IndexByte(n, '/'); i >= 0 {
		return n[i+1:]
	}
	return n
}

// tarballPath is the asset path of the tarball of version v of name: <name>/-/<unscoped>-<v>.tgz,
// the path registry.npmjs.org uses.
func tarballPath(name, v string) string { return name + "/-/" + unscoped(name) + "-" + v + ".tgz" }

// sigstorePath is the asset path of the provenance bundle of version v of name, next to its
// tarball.
func sigstorePath(name, v string) string {
	return name + "/-/" + unscoped(name) + "-" + v + ".sigstore"
}

// target is what the path of a request under /npm/{repo}/ names.
type target struct {
	name string   // the package; "" for a route under /-/
	rest []string // the segments after the name (or after "-"), unescaped
	dash bool     // the path starts with "/-/"
}

// parseTarget reads the path of r after /npm/{repo}/.
func parseTarget(r *http.Request) (target, error) {
	esc := r.URL.EscapedPath()
	prefix := "/npm/" + url.PathEscape(r.PathValue("repo")) + "/"
	rest, ok := strings.CutPrefix(esc, prefix)
	if !ok {
		return target{}, errBadName
	}
	segs := strings.Split(rest, "/")
	if segs[0] == "-" {
		out := target{dash: true}
		for _, s := range segs[1:] {
			u, err := url.PathUnescape(s)
			if err != nil {
				return target{}, errBadName
			}
			out.rest = append(out.rest, u)
		}
		return out, nil
	}
	name, n, err := nameOf(segs)
	if err != nil {
		return target{}, err
	}
	out := target{name: name}
	for _, s := range segs[n:] {
		u, err := url.PathUnescape(s)
		if err != nil || strings.Contains(u, "/") {
			return target{}, errBadName
		}
		out.rest = append(out.rest, u)
	}
	return out, nil
}

// nameOf reads a package name from the escaped segments segs: "@scope%2fname", "@scope" "name"
// or "name". It returns the name and the number of segments it took.
func nameOf(segs []string) (string, int, error) {
	first, err := url.PathUnescape(segs[0])
	if err != nil {
		return "", 0, errBadName
	}
	n := 1
	if strings.HasPrefix(first, "@") && !strings.Contains(first, "/") {
		if len(segs) < 2 {
			return "", 0, errBadName
		}
		second, err := url.PathUnescape(segs[1])
		if err != nil || strings.Contains(second, "/") {
			return "", 0, errBadName
		}
		first, n = first+"/"+second, 2
	} else if strings.Contains(first, "/") && !strings.HasPrefix(first, "@") {
		return "", 0, errBadName
	}
	if !validName(first) {
		return "", 0, errBadName
	}
	return first, n, nil
}

// distTagsTarget is a route /-/package/<name>/dist-tags[/<tag>]: the name and the tag ("" for
// the list). ok is false for another route under /-/.
func distTagsTarget(t target) (name, tag string, ok bool, err error) {
	if !t.dash || len(t.rest) < 3 || t.rest[0] != "package" {
		return "", "", false, nil
	}
	segs := make([]string, len(t.rest)-1)
	for i, s := range t.rest[1:] {
		segs[i] = url.PathEscape(s)
	}
	name, n, err := nameOf(segs)
	if err != nil {
		return "", "", true, err
	}
	rest := t.rest[1+n:]
	switch {
	case len(rest) == 1 && rest[0] == "dist-tags":
		return name, "", true, nil
	case len(rest) == 2 && rest[0] == "dist-tags":
		if !validTag(rest[1]) {
			return "", "", true, errors.New("invalid tag: a tag is letters, digits, '.', '-' and '_', and not a version")
		}
		return name, rest[1], true, nil
	}
	return "", "", false, nil
}

// validTag checks a dist-tag name: up to 64 URL-safe characters, and not a version (npm reads a
// version where a tag could be one).
func validTag(t string) bool {
	if t == "" || len(t) > 64 {
		return false
	}
	for i := 0; i < len(t); i++ {
		if !nameChar(t[i]) {
			return false
		}
	}
	_, err := version.CleanSemVer(t)
	return err != nil
}
