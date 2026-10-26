package format

import (
	"errors"
	"net/http"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
)

// PreconditionOf reads the precondition of a write from the headers of r (RFC 9110 §13):
// If-None-Match: *, If-Match: *, or If-Match with a list of strong ETags. nil: r has none. Any
// other If-None-Match, an If-Unmodified-Since or a weak ETag is an error, which the caller answers
// with 400: a write never ignores a condition. A format with its own write routes passes the
// result as assets.Write.Precondition.
func PreconditionOf(r *http.Request) (*assets.Precondition, error) {
	h := r.Header
	if len(h.Values("If-Unmodified-Since")) > 0 {
		return nil, errors.New("a write takes If-Match with the ETag of the file, not If-Unmodified-Since")
	}
	inm, im := h.Values("If-None-Match"), h.Values("If-Match")
	if len(inm) == 0 && len(im) == 0 {
		return nil, nil
	}
	pc := &assets.Precondition{}
	if len(inm) > 0 {
		if strings.TrimSpace(strings.Join(inm, ",")) != "*" {
			return nil, errors.New("a write takes only If-None-Match: *")
		}
		pc.Absent = true
	}
	if len(im) > 0 {
		star, tags, err := etagList(strings.Join(im, ","))
		if err != nil {
			return nil, err
		}
		pc.Exists, pc.SHA256 = star, tags
	}
	return pc, nil
}

// etagList parses an If-Match value: "*" alone (star), or a list of strong ETags, returned
// without their quotes.
func etagList(v string) (star bool, tags []string, err error) {
	bad := errors.New(`If-Match takes * or a list of ETags in quotes, e.g. If-Match: "<sha256>"`)
	for {
		v = strings.TrimLeft(v, " \t,")
		if v == "" {
			break
		}
		switch {
		case strings.HasPrefix(v, "W/"):
			return false, nil, errors.New("a write compares ETags strongly: If-Match takes no weak ETag (W/)")
		case v[0] == '*':
			star, v = true, v[1:]
		case v[0] == '"':
			end := strings.IndexByte(v[1:], '"')
			if end < 0 {
				return false, nil, bad
			}
			tags, v = append(tags, v[1:1+end]), v[2+end:]
		default:
			return false, nil, bad
		}
		if v != "" && v[0] != ',' && v[0] != ' ' && v[0] != '\t' {
			return false, nil, bad
		}
	}
	if star == (len(tags) > 0) {
		return false, nil, bad
	}
	return star, tags, nil
}
