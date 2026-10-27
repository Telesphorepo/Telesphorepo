package rpm

import (
	"database/sql/driver"
	"errors"
	"strconv"
	"strings"

	"modernc.org/sqlite"
)

// The order of versions is rpm's (rpmio/rpmvercmp.cc, as dnf and libsolv use it): the epoch as a
// number, then the version, then the release, each compared with rpmvercmp. rpmvercmp skips every
// run of separators (they carry no weight), then compares segments: a "~" sorts before
// everything, the end of the string included; a "^" sorts after the end and before any segment;
// digits are newer than letters; digits compare without leading zeros by length, then as text;
// letters compare as text. When one side ends, the side with a segment left is newer.
//
// versionKey encodes that order as bytes, one token per segment: "~" 0x01, the end of a part
// 0x02, "^" 0x03, letters 0x04 + the letters + 0x00, digits 0x05 + their count + the digits.
// The epoch is a fixed-width number first. The key ends with 0x00 and the version's text, so
// versions that rpm holds equal ("1.0" and "1.00") keep distinct keys. The SQL function
// rpm_version_key computes the key in queries.

func init() {
	sqlite.MustRegisterDeterministicScalarFunction("rpm_version_key", 3,
		func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			e, ok1 := args[0].(int64)
			v, ok2 := args[1].(string)
			r, ok3 := args[2].(string)
			if !ok1 || !ok2 || !ok3 {
				return nil, errors.New("rpm_version_key takes (epoch, version, release)")
			}
			return versionKey(e, v, r), nil
		})
}

const (
	tokTilde  = 0x01
	tokEnd    = 0x02
	tokCaret  = 0x03
	tokAlpha  = 0x04
	tokDigits = 0x05
)

func isAlpha(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// versionKey is the order key of epoch:version-release (an epoch below 0 counts as 0).
func versionKey(epoch int64, version, release string) []byte {
	e := uint32(max(epoch, 0))
	out := []byte{byte(e >> 24), byte(e >> 16), byte(e >> 8), byte(e)}
	out = appendPart(out, version)
	out = appendPart(out, release)
	out = append(out, 0)
	out = append(out, version...)
	out = append(out, '-')
	return append(out, release...)
}

// appendPart appends the tokens of one part and its end.
func appendPart(out []byte, s string) []byte {
	for {
		for s != "" && !isAlpha(s[0]) && !isDigit(s[0]) && s[0] != '~' && s[0] != '^' {
			s = s[1:]
		}
		if s == "" {
			return append(out, tokEnd)
		}
		switch {
		case s[0] == '~':
			out, s = append(out, tokTilde), s[1:]
		case s[0] == '^':
			out, s = append(out, tokCaret), s[1:]
		case isDigit(s[0]):
			i := 0
			for i < len(s) && isDigit(s[i]) {
				i++
			}
			d := strings.TrimLeft(s[:i], "0")
			out = append(out, tokDigits, byte(min(len(d), 255)))
			out = append(out, d...)
			s = s[i:]
		default:
			i := 0
			for i < len(s) && isAlpha(s[i]) {
				i++
			}
			out = append(append(append(out, tokAlpha), s[:i]...), 0)
			s = s[i:]
		}
	}
}

// rpmvercmp compares two version strings as rpm does: 1 when a is newer, -1 when b is, 0 when
// they are the same version.
func rpmvercmp(a, b string) int {
	if a == b {
		return 0
	}
	for a != "" || b != "" {
		for a != "" && !isAlpha(a[0]) && !isDigit(a[0]) && a[0] != '~' && a[0] != '^' {
			a = a[1:]
		}
		for b != "" && !isAlpha(b[0]) && !isDigit(b[0]) && b[0] != '~' && b[0] != '^' {
			b = b[1:]
		}
		if strings.HasPrefix(a, "~") || strings.HasPrefix(b, "~") {
			if !strings.HasPrefix(a, "~") {
				return 1
			}
			if !strings.HasPrefix(b, "~") {
				return -1
			}
			a, b = a[1:], b[1:]
			continue
		}
		if strings.HasPrefix(a, "^") || strings.HasPrefix(b, "^") {
			switch {
			case a == "":
				return -1
			case b == "":
				return 1
			case a[0] != '^':
				return 1
			case b[0] != '^':
				return -1
			}
			a, b = a[1:], b[1:]
			continue
		}
		if a == "" || b == "" {
			break
		}
		num := isDigit(a[0])
		seg := func(s string) (string, string) {
			i := 0
			for i < len(s) && (num && isDigit(s[i]) || !num && isAlpha(s[i])) {
				i++
			}
			return s[:i], s[i:]
		}
		sa, ra := seg(a)
		sb, rb := seg(b)
		if sb == "" {
			if num {
				return 1
			}
			return -1
		}
		if num {
			sa, sb = strings.TrimLeft(sa, "0"), strings.TrimLeft(sb, "0")
			if len(sa) != len(sb) {
				if len(sa) > len(sb) {
					return 1
				}
				return -1
			}
		}
		if c := strings.Compare(sa, sb); c != 0 {
			return c
		}
		a, b = ra, rb
	}
	if a == "" && b == "" {
		return 0
	}
	if a == "" {
		return -1
	}
	return 1
}

// parseEVR splits a version string [epoch:]version-release; epoch is -1 without one.
func parseEVR(s string) (epoch int64, version, release string, ok bool) {
	epoch = -1
	if i := strings.IndexByte(s, ':'); i >= 0 {
		e, err := strconv.ParseInt(s[:i], 10, 64)
		if err != nil || e < 0 || e > 1<<31-1 {
			return 0, "", "", false
		}
		epoch, s = e, s[i+1:]
	}
	i := strings.LastIndexByte(s, '-')
	if i <= 0 || i == len(s)-1 {
		return 0, "", "", false
	}
	return epoch, s[:i], s[i+1:], true
}
