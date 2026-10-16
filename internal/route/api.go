package route

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
)

// API holds what the JSON API handlers of every module share: the limits of a request and the
// public address. The route policy has checked the caller already.
type API struct {
	Settings settings.Provider
}

// Decode reads the JSON body into v under the max_json_size setting. On failure it has answered
// and returns false.
func (a *API) Decode(w http.ResponseWriter, r *http.Request, v any) bool {
	return httpx.DecodeJSON(w, r, settings.MaxJSONSize.Get(a.Settings()), v)
}

// BaseURL is the base_url setting, else the URL the client reached the server at (the scheme of
// httpx.ClientScheme and the host of the request).
func (a *API) BaseURL(r *http.Request) string {
	if base := settings.BaseURL.Get(a.Settings()); base != "" {
		return base
	}
	return httpx.ClientScheme(r) + "://" + r.Host
}

// Fail maps a kernel error to its status code without leaking internals.
func Fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, auth.ErrInvalid), errors.Is(err, repos.ErrInvalid), errors.Is(err, perm.ErrUnknown), errors.As(err, new(*perm.NeedsError)):
		httpx.Error(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, auth.ErrNotHeld):
		httpx.Error(w, http.StatusForbidden, err.Error())
	case errors.Is(err, auth.ErrLastPeople), errors.Is(err, auth.ErrManaged):
		httpx.Error(w, http.StatusConflict, err.Error())
	case errors.As(err, new(*repos.MissingError)):
		httpx.Error(w, http.StatusNotFound, err.Error())
	case errors.As(err, new(httpx.StatusError)):
		var se httpx.StatusError
		errors.As(err, &se)
		httpx.Error(w, se.HTTPStatus(), se.Error())
	case errors.Is(err, auth.ErrNotFound), errors.Is(err, repos.ErrNotFound):
		httpx.Error(w, http.StatusNotFound, "not found")
	case errors.Is(err, auth.ErrExists), errors.Is(err, repos.ErrExists):
		httpx.Error(w, http.StatusConflict, "already exists")
	case errors.Is(err, auth.ErrSSOAccount), errors.Is(err, auth.ErrPasswordLoginDisabled):
		httpx.Error(w, http.StatusForbidden, err.Error())
	case errors.Is(err, auth.ErrBadCredentials):
		httpx.Error(w, http.StatusUnauthorized, "invalid credentials")
	case errors.Is(err, auth.ErrRateLimited):
		if w.Header().Get("Retry-After") == "" {
			w.Header().Set("Retry-After", "6")
		}
		httpx.Error(w, http.StatusTooManyRequests, "too many failed attempts")
	case errors.Is(err, context.Canceled):
		httpx.Error(w, http.StatusServiceUnavailable, "request canceled")
	default:
		slog.ErrorContext(r.Context(), "api error", "method", r.Method, "path", r.URL.Path, "err", err)
		httpx.Error(w, http.StatusInternalServerError, "internal error")
	}
}

// Page is one keyset page of a list: the items and the cursor of the next page (null at the end).
type Page[T any] struct {
	Items []T     `json:"items"`
	Next  *string `json:"next"`
}

// Cursor returns the next-page cursor s, or nil at the end ("").
func Cursor(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// IDNext returns the next-page cursor of an id-ordered list, or nil at the end (0).
func IDNext(n int64) *string {
	if n == 0 {
		return nil
	}
	s := strconv.FormatInt(n, 10)
	return &s
}

// PathID parses the {id} path value. It answers 400 with msg and returns false when it is not
// an id.
func PathID(w http.ResponseWriter, r *http.Request, msg string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		httpx.Error(w, http.StatusBadRequest, msg)
		return 0, false
	}
	return id, true
}

// IDCursor parses the ?after= keyset cursor of an id-ordered list; 0 when absent. It answers 400
// and returns false when the cursor is not a number.
func IDCursor(w http.ResponseWriter, r *http.Request) (int64, bool) {
	s := r.URL.Query().Get("after")
	if s == "" {
		return 0, true
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		httpx.Error(w, http.StatusBadRequest, "invalid cursor")
		return 0, false
	}
	return n, true
}
