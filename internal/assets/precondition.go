package assets

import (
	"fmt"
	"net/http"
	"slices"

	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
)

// Precondition is the condition of a write on the file at its path (RFC 9110 §13, If-Match and
// If-None-Match), compared strongly with the ETag of the file, "<sha256>". A false condition
// fails the write with a *PreconditionError, and nothing is written.
type Precondition struct {
	Exists bool     // If-Match: *: the path holds a file
	Absent bool     // If-None-Match: *: the path holds no file
	SHA256 []string // If-Match: "<sha256>", ...: the path holds a file with one of these digests
}

// ProblemPreconditionFailed is the answer to a write whose precondition is false. Its extension
// member etag is the ETag of the file at the path now, null when the path holds none.
var ProblemPreconditionFailed = httpx.DeclareProblem(httpx.Problem{Name: "precondition-failed",
	Status: http.StatusPreconditionFailed, Title: "The file changed"})

// PreconditionError is a write whose Precondition is false (412).
type PreconditionError struct {
	Path   repopath.Path
	SHA256 string // the digest of the file at the path; "" when the path holds none
	reason string
}

func (e *PreconditionError) Error() string { return string(e.Path) + " changed: " + e.reason }

// HTTPStatus is the status of the answer (httpx.StatusError).
func (e *PreconditionError) HTTPStatus() int { return http.StatusPreconditionFailed }

// ETag is the ETag of the file at the path, "" when the path holds none.
func (e *PreconditionError) ETag() string {
	if e.SHA256 == "" {
		return ""
	}
	return `"` + e.SHA256 + `"`
}

// Problem is the answer (httpx.ProblemError): the problem precondition-failed with the members
// code and etag. A format route answers {"error", "code", "etag"}.
func (e *PreconditionError) Problem() httpx.Doc {
	var etag any
	if t := e.ETag(); t != "" {
		etag = t
	}
	return ProblemPreconditionFailed.Doc(e.Error()).With(map[string]any{"code": "precondition_failed", "etag": etag})
}

// check checks the precondition of the write at path against existing, the asset at the path
// (nil when none). A nil precondition passes.
func (pc *Precondition) check(path repopath.Path, existing *Asset) error {
	if pc == nil {
		return nil
	}
	fail := func(format string, args ...any) error {
		e := &PreconditionError{Path: path, reason: fmt.Sprintf(format, args...)}
		if existing != nil {
			e.SHA256 = existing.SHA256
		}
		return e
	}
	switch {
	case pc.Absent && existing != nil:
		return fail("it holds a file")
	case (pc.Exists || len(pc.SHA256) > 0) && existing == nil:
		return fail("it holds no file")
	case len(pc.SHA256) > 0 && !slices.Contains(pc.SHA256, existing.SHA256):
		return fail(`it holds "%s", which If-Match does not name`, existing.SHA256)
	}
	return nil
}
