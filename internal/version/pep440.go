package version

import (
	"regexp"
	"strings"
)

// PEP440 is a Python version (PEP 440): [N!]N(.N)*[{a|b|rc}N][.postN][.devN][+local]. Numbers
// are digit strings without leading zeros.
type PEP440 struct {
	Epoch   string   // "0" when the version has none
	Release []string // as written, trailing zeros kept ("1.0" stays two parts)
	PreL    string   // "a", "b" or "rc"; "" for none
	PreN    string
	Post    string // "" for none
	Dev     string // "" for none
	Local   []string
}

// pep440 is the version pattern of PEP 440 Appendix B, which accepts every spelling that
// normalises: any case, a leading "v", "-", "_" or "." between parts, "alpha", "beta", "c",
// "pre", "preview", "rev", "r", an implicit 0, and "1.0-1" for a post release.
var pep440 = regexp.MustCompile(`(?i)^\s*v?` +
	`(?:([0-9]+)!)?` +
	`([0-9]+(?:\.[0-9]+)*)` +
	`(?:[-_.]?(alpha|a|beta|b|preview|pre|c|rc)[-_.]?([0-9]+)?)?` +
	`(?:-([0-9]+)|[-_.]?(post|rev|r)[-_.]?([0-9]+)?)?` +
	`(?:[-_.]?(dev)[-_.]?([0-9]+)?)?` +
	`(?:\+([a-z0-9]+(?:[-_.][a-z0-9]+)*))?\s*$`)

// ParsePEP440 parses a PEP 440 version in any spelling that normalises.
func ParsePEP440(s string) (PEP440, error) {
	if len(s) > maxLength {
		return PEP440{}, invalid(s)
	}
	m := pep440.FindStringSubmatch(s)
	if m == nil {
		return PEP440{}, invalid(s)
	}
	var v PEP440
	numbers := []*string{&v.Epoch}
	v.Epoch = trimZeros(m[1])
	for _, p := range strings.Split(m[2], ".") {
		v.Release = append(v.Release, trimZeros(p))
	}
	for i := range v.Release {
		numbers = append(numbers, &v.Release[i])
	}
	if m[3] != "" {
		switch strings.ToLower(m[3]) {
		case "a", "alpha":
			v.PreL = "a"
		case "b", "beta":
			v.PreL = "b"
		default:
			v.PreL = "rc"
		}
		v.PreN = trimZeros(m[4])
		numbers = append(numbers, &v.PreN)
	}
	switch {
	case m[5] != "":
		v.Post = trimZeros(m[5])
	case m[6] != "":
		v.Post = trimZeros(m[7])
	}
	if m[8] != "" {
		v.Dev = trimZeros(m[9])
	}
	numbers = append(numbers, &v.Post, &v.Dev)
	if m[10] != "" {
		v.Local = strings.FieldsFunc(strings.ToLower(m[10]), func(r rune) bool { return r == '-' || r == '_' || r == '.' })
		for i, l := range v.Local {
			if digits(l) {
				v.Local[i] = trimZeros(l)
				numbers = append(numbers, &v.Local[i])
			}
		}
	}
	for _, n := range numbers {
		if len(*n) > 99 {
			return PEP440{}, invalid(s)
		}
	}
	return v, nil
}

// String prints v in normal form (PEP 440 "Normalization"), e.g. "1.0rc1.post2.dev3+ubuntu.1".
func (v PEP440) String() string {
	var b strings.Builder
	if v.Epoch != "0" {
		b.WriteString(v.Epoch)
		b.WriteByte('!')
	}
	b.WriteString(strings.Join(v.Release, "."))
	if v.PreL != "" {
		b.WriteString(v.PreL)
		b.WriteString(v.PreN)
	}
	if v.Post != "" {
		b.WriteString(".post")
		b.WriteString(v.Post)
	}
	if v.Dev != "" {
		b.WriteString(".dev")
		b.WriteString(v.Dev)
	}
	if len(v.Local) > 0 {
		b.WriteByte('+')
		b.WriteString(strings.Join(v.Local, "."))
	}
	return b.String()
}

// Prerelease reports whether v is a pre or dev release (pip leaves them out unless asked).
func (v PEP440) Prerelease() bool { return v.PreL != "" || v.Dev != "" }

// Public returns v without its local part.
func (v PEP440) Public() PEP440 {
	v.Local = nil
	return v
}

// Compare orders v and o as PEP 440 does: -1, 0 or 1. The release compares without its trailing
// zeros (1.0 == 1.0.0). A dev release of a version without pre or post comes before its pre
// releases (1.0.dev0 < 1.0a0 < 1.0). A local version comes after its public version, and local
// parts compare numeric by value and after alphanumeric ones.
func (v PEP440) Compare(o PEP440) int {
	return strings.Compare(v.SortKey(), o.SortKey())
}

// release returns the release numbers without trailing zeros.
func (v PEP440) release() []string {
	r := v.Release
	for len(r) > 1 && r[len(r)-1] == "0" {
		r = r[:len(r)-1]
	}
	if len(r) == 1 && r[0] == "0" {
		return nil
	}
	return r
}

// SortKey returns the sort key of v, in the order of Compare. Two spellings of one version get
// one key.
//
// Encoding, in the order of PEP 440 (packaging's _cmpkey): num(epoch); num of each release number
// without trailing zeros; then the pre part: "(" for a dev release without pre and post, ")" num
// for "a", "*" num for "b", "+" num for "rc", or "," for none; then "-" for no post or "." num;
// then "-" num for a dev release or "." for none; then "-" for no local part or "." and each
// local part: "0" label "!" for alphanumeric or "1" num for numeric. Every marker byte sorts
// before "0", so a release that ends sorts before a longer one.
func (v PEP440) SortKey() string {
	var b strings.Builder
	b.WriteString(num(v.Epoch))
	for _, n := range v.release() {
		b.WriteString(num(n))
	}
	switch {
	case v.PreL == "" && v.Post == "" && v.Dev != "":
		b.WriteByte('(')
	case v.PreL == "a":
		b.WriteByte(')')
		b.WriteString(num(v.PreN))
	case v.PreL == "b":
		b.WriteByte('*')
		b.WriteString(num(v.PreN))
	case v.PreL == "rc":
		b.WriteByte('+')
		b.WriteString(num(v.PreN))
	default:
		b.WriteByte(',')
	}
	if v.Post == "" {
		b.WriteByte('-')
	} else {
		b.WriteByte('.')
		b.WriteString(num(v.Post))
	}
	if v.Dev != "" {
		b.WriteByte('-')
		b.WriteString(num(v.Dev))
	} else {
		b.WriteByte('.')
	}
	if len(v.Local) == 0 {
		b.WriteByte('-')
	} else {
		b.WriteByte('.')
		for _, l := range v.Local {
			if digits(l) {
				b.WriteByte('1')
				b.WriteString(num(l))
			} else {
				b.WriteByte('0')
				b.WriteString(l)
				b.WriteByte('!')
			}
		}
	}
	return b.String()
}
