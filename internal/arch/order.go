package arch

import (
	"database/sql/driver"
	"errors"
	"strings"

	"modernc.org/sqlite"
)

// The order of the versions of a package is pacman's (alpm_pkg_vercmp): the epoch, then the
// pkgver, then the pkgrel, each compared with rpmvercmp (lib/libalpm/version.c). versionKey
// encodes a version as bytes whose byte order is that order, so the newest version of a name and a
// page of versions are index seeks.
//
// Each part (epoch, pkgver, pkgrel) is a run of segments: a run of letters or of digits after the
// separators before it. rpmvercmp compares two segments by their count of separators first (fewer
// is older), then by kind (digits are newer than letters), then by content (digits without leading
// zeros by length, then as text; letters as text). A segment's first byte encodes the count and
// the kind in that order; then come the letters and 0x01, or the digits' count + 1 and the digits.
//
// When one part ends where the other goes on, rpmvercmp looks at what follows in the other: after
// a part that ends right after a segment, a letter (no separator) makes the ended part newer, and
// anything else (a separator, a digit) makes it older; after a part that ends in separators, a
// letter makes it newer and a digit older. partEnd and partEndSep are those two ends, placed
// between the segments without separators: letters < partEnd < partEndSep < digits < every
// segment after separators. rpmvercmp is not transitive where a part that ends in separators
// meets letters after separators ("1a.", "1a1", "1a.b"); there the key sorts the letters as
// newer. The key ends with 0x00 and the version's bytes, so versions that rpmvercmp holds equal
// ("1.0" and "1.00") keep distinct keys.
//
// The SQL function arch_version_key computes the key in queries.

func init() {
	sqlite.MustRegisterDeterministicScalarFunction("arch_version_key", 1,
		func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			s, ok := args[0].(string)
			if !ok {
				return nil, errors.New("the argument is not text")
			}
			return versionKey(s), nil
		})
}

const (
	lettersNoSep = 0x10
	partEnd      = 0x20
	partEndSep   = 0x28
	digitsNoSep  = 0x30
	afterSeps    = 0x40 // + 2*(count-1), + 1 for digits
)

// splitVersion splits a full pacman version [epoch:]pkgver[-pkgrel] as alpm's parseEVR does: the
// epoch is the digits before a ':' ("0" when there is none), the pkgrel what follows the last
// '-'.
func splitVersion(v string) (epoch, ver, rel string) {
	epoch, ver = "0", v
	if i := strings.IndexByte(v, ':'); i >= 0 {
		if i > 0 && strings.Trim(v[:i], "0123456789") == "" {
			epoch = v[:i]
		}
		ver = v[i+1:]
	}
	if i := strings.LastIndexByte(ver, '-'); i >= 0 {
		ver, rel = ver[:i], ver[i+1:]
	}
	return epoch, ver, rel
}

// versionKey is the order key of the full pacman version v.
func versionKey(v string) []byte {
	epoch, ver, rel := splitVersion(v)
	out := appendPart(nil, epoch)
	out = appendPart(out, ver)
	out = appendPart(out, rel)
	return append(append(out, 0), v...)
}

func isAlpha(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// appendPart appends the segments of one part of a version and the part's end.
func appendPart(out []byte, s string) []byte {
	for {
		seps := 0
		for s != "" && !isAlpha(s[0]) && !isDigit(s[0]) {
			s, seps = s[1:], seps+1
		}
		if s == "" {
			if seps > 0 {
				return append(out, partEndSep)
			}
			return append(out, partEnd)
		}
		digits := isDigit(s[0])
		var kind byte
		switch {
		case seps == 0 && digits:
			kind = digitsNoSep
		case seps == 0:
			kind = lettersNoSep
		default:
			kind = byte(afterSeps + 2*(min(seps, 90)-1))
			if digits {
				kind++
			}
		}
		i := 0
		if digits {
			for i < len(s) && isDigit(s[i]) {
				i++
			}
			d := strings.TrimLeft(s[:i], "0")
			out = append(out, kind, byte(min(len(d)+1, 255)))
			out = append(out, d...)
		} else {
			for i < len(s) && isAlpha(s[i]) {
				i++
			}
			out = append(append(append(out, kind), s[:i]...), 0x01)
		}
		s = s[i:]
	}
}
