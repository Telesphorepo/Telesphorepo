package debian

import (
	"database/sql/driver"
	"errors"
	"strconv"
	"strings"

	"modernc.org/sqlite"
	"pault.ag/go/debian/version"
)

// The order of the versions and builds of a package (Versions and Builds) is stored with each
// row of deb_packages, in deb_package_order, as two byte keys whose byte order is the order of
// the pages, so SQL seeks and pages them:
//
//   - name_key: the version a row is a build of (splitSnapshot) in dpkg --compare-versions order,
//     its bytes after a 0x00 breaking a tie of two equal versions;
//   - build_key: a build without a stamp first, then the builds by stamp (8 digits read as
//     midnight), then dpkg order, then the bytes of the version.
//
// The migration that adds the table fills it through the SQL functions deb_name_key and
// deb_build_key, which every connection of the driver has.

func init() {
	key := func(fn func(string) []byte) func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
		return func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			s, ok := args[0].(string)
			if !ok {
				return nil, errors.New("the argument is not text")
			}
			return fn(s), nil
		}
	}
	sqlite.MustRegisterDeterministicScalarFunction("deb_name_key", 1, key(nameKey))
	sqlite.MustRegisterDeterministicScalarFunction("deb_build_key", 1, key(buildKey))
}

// nameKey is the name_key of a row of Debian version v.
func nameKey(v string) []byte {
	name, _ := splitSnapshot(v)
	return keyOfName(name)
}

// keyOfName is the name_key of the rows of the builds of version name.
func keyOfName(name string) []byte { return append(append(dpkgKey(name), 0), name...) }

// buildKey is the build_key of a row of Debian version v.
func buildKey(v string) []byte {
	var out []byte
	if _, stamp := splitSnapshot(v); stamp != "" {
		out = append(append(out, 2), stamp+strings.Repeat("0", 14-len(stamp))...)
	} else {
		out = append(out, 1)
	}
	return append(append(append(out, dpkgKey(v)...), 0), v...)
}

// dpkgKey encodes Debian version v so that byte order is dpkg's order (equal versions such as 1.0
// and 1.00 encode alike). Each of the epoch, the upstream version and the revision is a run of
// parts: the non-digits, mapped so that ~ sorts first and other punctuation after the letters,
// a 0x02 for their end (where dpkg reads order 0), then the digits without leading zeros after
// their count + 1. A 0x02 ends the upstream version and the revision; an empty revision encodes
// as 0, which dpkg reads it as. No byte is 0x00. A version
// that does not parse (the upload refuses it) encodes as its bytes.
func dpkgKey(v string) []byte {
	pv, err := version.Parse(v)
	if err != nil {
		return []byte(v)
	}
	out := appendNumber(nil, strconv.FormatUint(uint64(pv.Epoch), 10))
	out = appendPart(out, pv.Version)
	return appendPart(out, pv.Revision)
}

func appendPart(out []byte, s string) []byte {
	if s == "" {
		s = "0" // dpkg reads an empty revision as 0
	}
	for s != "" {
		i := 0
		for ; i < len(s) && !isDigit(s[i]); i++ {
			c := s[i]
			switch {
			case c == '~':
				out = append(out, 0x01)
			case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
				out = append(out, c)
			default:
				out = append(out, c|0x80)
			}
		}
		out = append(out, 0x02)
		j := i
		for ; j < len(s) && isDigit(s[j]); j++ {
		}
		out = appendNumber(out, s[i:j])
		s = s[j:]
	}
	return append(out, 0x02)
}

// appendNumber appends the digits d without leading zeros after their count + 1.
func appendNumber(out []byte, d string) []byte {
	d = strings.TrimLeft(d, "0")
	return append(append(out, byte(min(len(d)+1, 255))), d...)
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
