package version

import (
	"strconv"
	"strings"
)

// NuGet is a NuGet package version (NuGet.Versioning.NuGetVersion): 1 to 4 numbers, prerelease
// labels after "-", metadata after "+".
type NuGet struct {
	Major, Minor, Patch, Revision int
	Pre                           []string // the prerelease labels as written; nil for a release
	Metadata                      string   // without "+"; "" for none
}

// ParseNuGet parses a NuGet version as NuGet does: one to four numbers (int32 each; "1" is
// 1.0.0), leading zeros allowed in a number, prerelease labels of [0-9A-Za-z-] (no leading zero
// in a numeric one), metadata labels of [0-9A-Za-z-].
func ParseNuGet(s string) (NuGet, error) {
	if len(s) > maxLength || s == "" {
		return NuGet{}, invalid(s)
	}
	rest, meta, hasMeta := strings.Cut(s, "+")
	main, pre, hasPre := strings.Cut(rest, "-")
	parts := strings.Split(main, ".")
	if len(parts) > 4 {
		return NuGet{}, invalid(s)
	}
	var v NuGet
	for i, p := range parts {
		if !digits(p) {
			return NuGet{}, invalid(s)
		}
		n, err := strconv.ParseInt(p, 10, 32)
		if err != nil {
			return NuGet{}, invalid(s)
		}
		switch i {
		case 0:
			v.Major = int(n)
		case 1:
			v.Minor = int(n)
		case 2:
			v.Patch = int(n)
		default:
			v.Revision = int(n)
		}
	}
	if hasPre {
		v.Pre = strings.Split(pre, ".")
		for _, l := range v.Pre {
			if !identifier(l) || digits(l) && (len(l) > 1 && l[0] == '0' || len(l) > 99) {
				return NuGet{}, invalid(s)
			}
		}
	}
	if hasMeta {
		for _, l := range strings.Split(meta, ".") {
			if !identifier(l) {
				return NuGet{}, invalid(s)
			}
		}
		v.Metadata = meta
	}
	return v, nil
}

// Normalized prints the identity of v (NuGet's ToNormalizedString): three numbers, a fourth only
// when it is not 0, leading zeros dropped, the prerelease labels as written, no metadata. NuGet
// compares it case-insensitively; the flat container lowers it.
func (v NuGet) Normalized() string {
	var b strings.Builder
	b.WriteString(strconv.Itoa(v.Major))
	b.WriteByte('.')
	b.WriteString(strconv.Itoa(v.Minor))
	b.WriteByte('.')
	b.WriteString(strconv.Itoa(v.Patch))
	if v.Revision != 0 {
		b.WriteByte('.')
		b.WriteString(strconv.Itoa(v.Revision))
	}
	if len(v.Pre) > 0 {
		b.WriteByte('-')
		b.WriteString(strings.Join(v.Pre, "."))
	}
	return b.String()
}

// Full prints Normalized with the metadata (NuGet's ToFullString).
func (v NuGet) Full() string {
	if v.Metadata == "" {
		return v.Normalized()
	}
	return v.Normalized() + "+" + v.Metadata
}

// Prerelease reports whether v has prerelease labels.
func (v NuGet) Prerelease() bool { return len(v.Pre) > 0 }

// IsSemVer2 reports whether v needs a SemVer 2.0 client: it has metadata or more than one
// prerelease label. The plain RegistrationsBaseUrl leaves such versions out.
func (v NuGet) IsSemVer2() bool { return v.Metadata != "" || len(v.Pre) > 1 }

// Compare orders v and o as NuGet's default version comparer: -1, 0 or 1. The four numbers, then
// a prerelease before its release, then the labels by SemVer 2.0 precedence with alphanumeric
// labels compared case-insensitively. Metadata does not count.
func (v NuGet) Compare(o NuGet) int {
	for _, c := range [][2]int{{v.Major, o.Major}, {v.Minor, o.Minor}, {v.Patch, o.Patch}, {v.Revision, o.Revision}} {
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
	return cmpLabels(v.Pre, o.Pre, true)
}

// SortKey returns the sort key of v, in the order of Compare. Two versions get one key exactly
// when they are one package version to NuGet (the same Normalized ignoring case).
//
// Encoding: num of each of the four numbers, then "-" and the prerelease labels lowered
// (labelsKey) for a prerelease or "~" for a release ("-" < "~").
func (v NuGet) SortKey() string {
	var b strings.Builder
	for _, n := range []int{v.Major, v.Minor, v.Patch, v.Revision} {
		b.WriteString(num(strconv.Itoa(n)))
	}
	if len(v.Pre) > 0 {
		b.WriteByte('-')
		labelsKey(&b, v.Pre, true)
	} else {
		b.WriteByte('~')
	}
	return b.String()
}
