package docker

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/Telesphorepo/Telesphorepo/internal/access"
	"github.com/Telesphorepo/Telesphorepo/internal/assets"
)

// OCI error codes (distribution spec).
const (
	codeBlobUnknown         = "BLOB_UNKNOWN"
	codeBlobUploadInvalid   = "BLOB_UPLOAD_INVALID"
	codeBlobUploadUnknown   = "BLOB_UPLOAD_UNKNOWN"
	codeDigestInvalid       = "DIGEST_INVALID"
	codeManifestBlobUnknown = "MANIFEST_BLOB_UNKNOWN"
	codeManifestInvalid     = "MANIFEST_INVALID"
	codeManifestUnknown     = "MANIFEST_UNKNOWN"
	codeNameInvalid         = "NAME_INVALID"
	codeNameUnknown         = "NAME_UNKNOWN"
	codeSizeInvalid         = "SIZE_INVALID"
	codeUnauthorized        = "UNAUTHORIZED"
	codeDenied              = "DENIED"
	codeUnsupported         = "UNSUPPORTED"
	codeTooManyRequests     = "TOOMANYREQUESTS"
	codePaginationInvalid   = "PAGINATION_NUMBER_INVALID"
	codeTagInvalid          = "TAG_INVALID"
	codeUnknown             = "UNKNOWN"
)

// ociError is an answer of a /v2/ request in the OCI error format. It is an httpx.StatusError,
// so a refusal of a hook that reaches a JSON API route answers with its status and message.
type ociError struct {
	status  int
	code    string
	message string
	detail  any
}

func (e *ociError) Error() string   { return e.message }
func (e *ociError) HTTPStatus() int { return e.status }

func ociErr(status int, code, msg string) *ociError {
	return &ociError{status: status, code: code, message: msg}
}

func nameInvalid(msg string) *ociError { return ociErr(http.StatusBadRequest, codeNameInvalid, msg) }
func digestInvalid(msg string) *ociError {
	return ociErr(http.StatusBadRequest, codeDigestInvalid, msg)
}
func manifestInvalid(msg string) *ociError {
	return ociErr(http.StatusBadRequest, codeManifestInvalid, msg)
}

// blobUnknownIn is the refusal of a manifest that names a blob or a child manifest the image
// lacks.
func blobUnknownIn(hex string) *ociError {
	e := ociErr(http.StatusBadRequest, codeManifestBlobUnknown, "the manifest references sha256:"+hex+", which the image does not have")
	e.detail = map[string]string{"digest": "sha256:" + hex}
	return e
}

// writeOCI writes the OCI error JSON of e.
func writeOCI(w http.ResponseWriter, e *ociError) {
	type item struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Detail  any    `json:"detail,omitempty"`
	}
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	h.Set(apiVersionHeader, apiVersion)
	w.WriteHeader(e.status)
	_ = json.NewEncoder(w).Encode(map[string][]item{"errors": {{e.code, e.message, e.detail}}})
}

// fail answers err: an *ociError as it is, an asset refusal by its status, an engine refusal, or
// 500.
func fail(w http.ResponseWriter, r *http.Request, err error) {
	var oe *ociError
	var ref *assets.Refusal
	switch {
	case errors.As(err, &oe):
		writeOCI(w, oe)
	case errors.As(err, &ref):
		code := codeManifestInvalid
		switch ref.Status {
		case http.StatusNotFound:
			code = codeBlobUnknown
		case http.StatusConflict:
			code = codeDenied
		}
		writeOCI(w, ociErr(ref.Status, code, ref.Message))
	case errors.Is(err, assets.ErrTooLarge):
		writeOCI(w, ociErr(http.StatusRequestEntityTooLarge, codeSizeInvalid, "the blob is larger than the max upload size of the server"))
	default:
		slog.ErrorContext(r.Context(), "docker request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		writeOCI(w, ociErr(http.StatusInternalServerError, codeUnknown, "internal error"))
	}
}

// decisionError maps a refusal of the route policy or the access engine to its OCI error.
func decisionError(d access.Decision) *ociError {
	code := codeUnknown
	switch d.Status {
	case http.StatusBadRequest:
		code = codeNameInvalid
	case http.StatusUnauthorized:
		code = codeUnauthorized
	case http.StatusForbidden, http.StatusConflict:
		code = codeDenied
	case http.StatusNotFound:
		code = codeNameUnknown
	case http.StatusMethodNotAllowed:
		code = codeUnsupported
	case http.StatusTooManyRequests:
		code = codeTooManyRequests
	}
	msg := d.Message
	if d.Status == http.StatusInternalServerError {
		msg = "internal error"
	}
	e := ociErr(d.Status, code, msg)
	if len(d.Detail) > 0 {
		e.detail = d.Detail
	}
	return e
}

// refuse is the route.ErrorWriter of the /v2/ routes.
func refuse(w http.ResponseWriter, _ *http.Request, d access.Decision) {
	writeOCI(w, decisionError(d))
}
