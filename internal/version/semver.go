package version

import (
	"strconv"
	"strings"
)

// SemVer is a SemVer 2.0 version (npm, Cargo): MAJOR.MINOR.PATCH, prerelease labels after "-",
// build metadata after "+".
type SemVer struct {
	Major, Minor, Patch uint64
	Pre                 []string // the prerelease labels; nil for a release
	Build               string   // the build metadata without "+"; "" for none
}

// ParseSemVer parses a strict SemVer 2.0 version: no leading "v" or "=", no leading zeros in a
// number or a numeric prerelease label, no spaces. A registry receives versions in this form,
// because npm and Cargo send them so.
func ParseSemVer(s string) (SemVer, error) {
	if len(s) > maxLength {
		return SemVer{}, invalid(s)
	}
	rest, build, hasBuild := strings.Cut(s, "+")
	main, pre, hasPre := strings.Cut(rest, "-")
	var v SemVer
	parts := strings.Split(main, ".")
	if len(parts) != 3 {
		return SemVer{}, invalid(s)
	}
	for i, p := range parts {
		if !digits(p) || len(p) > 1 && p[0] == '0' {
			return SemVer{}, invalid(s)
		}
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil {
			return SemVer{}, invalid(s)
		}
		v.set(i, n)
	}
	if hasPre {
		v.Pre = strings.Split(pre, ".")
		for _, l := range v.Pre {
			if !identifier(l) || digits(l) && (len(l) > 1 && l[0] == '0' || len(l) > 99) {
				return SemVer{}, invalid(s)
			}
		}
	}
	if hasBuild {
		for _, l := range strings.Split(build, ".") {
			if !identifier(l) {
				return SemVer{}, invalid(s)
			}
		}
		v.Build = build
	}
	return v, nil
}

// CleanSemVer parses a version the way npm cleans the version of a package.json before it
// publishes it (normalize-package-data with semver.clean, loose): spaces around it and a leading
// "v" or "=" go, a number may have leading zeros, and a prerelease may follow the patch without
// "-" ("1.2.3beta"). The result prints in strict form (String). Use it for a version read from a
// package.json that no npm client normalised, e.g. in a tarball of an import.
func CleanSemVer(s string) (SemVer, error) {
	t := strings.TrimLeft(strings.TrimSpace(s), "=v \t")
	if len(t) > maxLength {
		return SemVer{}, invalid(s)
	}
	rest, build, hasBuild := strings.Cut(t, "+")
	var v SemVer
	for i := range 3 {
		j := 0
		for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
			j++
		}
		if j == 0 {
			return SemVer{}, invalid(s)
		}
		n, err := strconv.ParseUint(rest[:j], 10, 64)
		if err != nil {
			return SemVer{}, invalid(s)
		}
		v.set(i, n)
		rest = rest[j:]
		if i < 2 {
			if !strings.HasPrefix(rest, ".") {
				return SemVer{}, invalid(s)
			}
			rest = rest[1:]
		}
	}
	if rest != "" {
		rest = strings.TrimPrefix(rest, "-")
		v.Pre = strings.Split(rest, ".")
		for i, l := range v.Pre {
			if !identifier(l) || digits(l) && len(trimZeros(l)) > 99 {
				return SemVer{}, invalid(s)
			}
			if digits(l) {
				v.Pre[i] = trimZeros(l) // npm turns a numeric label into a number
			}
		}
	}
	if hasBuild {
		for _, l := range strings.Split(build, ".") {
			if !identifier(l) {
				return SemVer{}, invalid(s)
			}
		}
		v.Build = build
	}
	return v, nil
}

// set sets the number i of v: 0 major, 1 minor, 2 patch.
func (v *SemVer) set(i int, n uint64) {
	switch i {
	case 0:
		v.Major = n
	case 1:
		v.Minor = n
	default:
		v.Patch = n
	}
}

// String prints v in strict form.
func (v SemVer) String() string {
	var b strings.Builder
	b.WriteString(strconv.FormatUint(v.Major, 10))
	b.WriteByte('.')
	b.WriteString(strconv.FormatUint(v.Minor, 10))
	b.WriteByte('.')
	b.WriteString(strconv.FormatUint(v.Patch, 10))
	if len(v.Pre) > 0 {
		b.WriteByte('-')
		b.WriteString(strings.Join(v.Pre, "."))
	}
	if v.Build != "" {
		b.WriteByte('+')
		b.WriteString(v.Build)
	}
	return b.String()
}

// Prerelease reports whether v has prerelease labels.
func (v SemVer) Prerelease() bool { return len(v.Pre) > 0 }

// Compare orders v and o by SemVer 2.0 precedence: -1, 0 or 1. Build metadata does not count, so
// "1.0.0+a" and "1.0.0+b" compare equal (Cargo refuses the second as a version that exists).
func (v SemVer) Compare(o SemVer) int {
	for _, c := range [][2]uint64{{v.Major, o.Major}, {v.Minor, o.Minor}, {v.Patch, o.Patch}} {
		if c[0] != c[1] {
			if c[0] < c[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(v.Pre) == 0 && len(o.Pre) == 0:
		return 0
	case len(v.Pre) == 0:
		return 1
	case len(o.Pre) == 0:
		return -1
	}
	return cmpLabels(v.Pre, o.Pre, false)
}

// SortKey returns the sort key of v: the order of Compare, then the build metadata by bytes, so
// two versions that differ only in it get distinct keys.
//
// Encoding: num(major) num(minor) num(patch), then "-" and the prerelease labels (labelsKey) for
// a prerelease or "~" for a release ("-" < "~": a prerelease sorts before its release), then "+"
// and the build metadata when there is one. "+" sorts after "!" (the end of an alphanumeric
// label) and before "0" and "1" (the start of a further label).
func (v SemVer) SortKey() string {
	var b strings.Builder
	b.WriteString(num(strconv.FormatUint(v.Major, 10)))
	b.WriteString(num(strconv.FormatUint(v.Minor, 10)))
	b.WriteString(num(strconv.FormatUint(v.Patch, 10)))
	if len(v.Pre) > 0 {
		b.WriteByte('-')
		labelsKey(&b, v.Pre, false)
	} else {
		b.WriteByte('~')
	}
	if v.Build != "" {
		b.WriteByte('+')
		b.WriteString(v.Build)
	}
	return b.String()
}
