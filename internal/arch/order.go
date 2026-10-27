package arch

import (
	"database/sql/driver"
	"errors"
	"strings"

	"modernc.org/sqlite"
)

// The order of the versions of a package is pacman's (alpm_pkg_vercmp): the epoch, then the
// pkgver, then the pkgrel, each compared with rpmvercmp. versionKey encodes a version as bytes
// whose byte order is that order, so the newest version of a name and a page of versions are
// index seeks. Each part (epoch, pkgver, pkgrel) is a run of segments: a run of letters or of
// digits after the separators before it. A segment encodes as its kind (letters 0x02, digits
// 0x04), its count of separators, then the letters and 0x01, or the digits without leading zeros
// after their count. 0x03 ends a part: it sorts after letters and before digits, as rpmvercmp
// ranks a version that ends against one that goes on ("1.0a" < "1.0" < "1.0.1"). The key ends
// with 0x00 and the version's bytes, so two versions that rpmvercmp holds equal ("1.0" and
// "1.00") keep distinct keys. rpmvercmp compares the separator counts before the kinds; the key
// compares the kinds first, so the two disagree only on a letter segment after more separators
// than a digit segment ("1..a" and "1.2"), where rpmvercmp is not transitive itself.
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
	segLetters = 0x02
	partEnd    = 0x03
	segDigits  = 0x04
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
			return append(out, partEnd)
		}
		i := 0
		if isDigit(s[0]) {
			for i < len(s) && isDigit(s[i]) {
				i++
			}
			d := strings.TrimLeft(s[:i], "0")
			out = append(out, segDigits, byte(min(seps, 255)), byte(min(len(d)+1, 255)))
			out = append(out, d...)
		} else {
			for i < len(s) && isAlpha(s[i]) {
				i++
			}
			out = append(out, segLetters, byte(min(seps, 255)))
			out = append(append(out, s[:i]...), 0x01)
		}
		s = s[i:]
	}
}
