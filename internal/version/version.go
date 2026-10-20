// Package version parses, normalises and orders the version schemes of the package formats: SemVer
// 2.0 (npm, Cargo), PEP 440 (PyPI) and NuGet. Each scheme has a SortKey: a string whose byte
// order is the scheme's order, so SQL keyset-pages versions by a sort_key column.
//
// The keys share one encoding of a number: its decimal digits without leading zeros, after two
// digits that give their count (0 is "010", 42 is "0242"). A longer number then sorts after a
// shorter one, and numbers of one length sort by their digits. A number has at most 99 digits.
package version

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrInvalid is a version that does not parse.
var ErrInvalid = errors.New("invalid version")

// maxLength is the longest version string that parses (npm's limit).
const maxLength = 256

func invalid(s string) error {
	if len(s) > 64 {
		s = s[:64] + "..."
	}
	return fmt.Errorf("%w %q", ErrInvalid, s)
}

// num encodes the digit string d (no leading zeros, "0" for zero) for a sort key.
func num(d string) string {
	if len(d) < 10 {
		return "0" + strconv.Itoa(len(d)) + d
	}
	return strconv.Itoa(len(d)) + d
}

// digits reports whether s is one or more ASCII digits.
func digits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// trimZeros returns the digit string d without leading zeros ("0" for zero).
func trimZeros(d string) string {
	d = strings.TrimLeft(d, "0")
	if d == "" {
		return "0"
	}
	return d
}

// cmpDigits compares two digit strings without leading zeros by their value.
func cmpDigits(a, b string) int {
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}

// identifier reports whether s is a SemVer identifier: one or more of [0-9A-Za-z-].
func identifier(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-') {
			return false
		}
	}
	return true
}

// cmpLabels compares prerelease labels by SemVer 2.0 precedence: a numeric label by its value and
// before an alphanumeric one, alphanumeric labels by bytes (fold lowers the case first), and a
// shorter list before a longer one that starts with it.
func cmpLabels(a, b []string, fold bool) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		x, y := a[i], b[i]
		xn, yn := digits(x), digits(y)
		switch {
		case xn && yn:
			if c := cmpDigits(trimZeros(x), trimZeros(y)); c != 0 {
				return c
			}
		case xn:
			return -1
		case yn:
			return 1
		default:
			if fold {
				x, y = strings.ToLower(x), strings.ToLower(y)
			}
			if c := strings.Compare(x, y); c != 0 {
				return c
			}
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	}
	return 0
}

// labelsKey encodes prerelease labels for a sort key, in the order of cmpLabels: each label is
// "0" and its number, or "1", the label (lowered by fold) and "!". "!" sorts before every label
// byte, so "a" sorts before "ab"; a list that ends sorts before one that goes on.
func labelsKey(b *strings.Builder, labels []string, fold bool) {
	for _, l := range labels {
		if digits(l) {
			b.WriteString("0")
			b.WriteString(num(trimZeros(l)))
			continue
		}
		if fold {
			l = strings.ToLower(l)
		}
		b.WriteString("1")
		b.WriteString(l)
		b.WriteString("!")
	}
}
