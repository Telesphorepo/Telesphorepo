// Package storage abstracts blob storage. Keys always use forward slashes
// (package path), identical across backends.
package storage

import (
	"context"
	"errors"
	"io"
	"time"
)

var ErrNotFound = errors.New("storage: not found")

type ObjectInfo struct {
	Key     string
	Size    int64
	ModTime time.Time
}

type Store interface {
	// Put writes size bytes from r to key, atomically replacing any existing object.
	// r is usually an *os.File (seekable); implementations may rely on size being exact.
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	// Get opens key for reading. FS returns an *os.File (implements io.ReadSeeker).
	Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error)
	// GetRange opens n bytes of key from offset off (off and n inside the object). The info has
	// the size of the whole object.
	GetRange(ctx context.Context, key string, off, n int64) (io.ReadCloser, ObjectInfo, error)
	Head(ctx context.Context, key string) (ObjectInfo, error)
	// Delete removes key; deleting a missing key is not an error.
	Delete(ctx context.Context, key string) error
	// List returns up to limit objects under prefix with Key > after, ordered by key.
	// Not used on request paths.
	List(ctx context.Context, prefix, after string, limit int) ([]ObjectInfo, error)
	// PresignGet returns a time-limited GET URL whose answer carries the headers of resp.
	// ok=false when the backend cannot presign (FS) or redirects are disabled; callers then
	// stream via Get.
	PresignGet(ctx context.Context, key string, ttl time.Duration, resp Response) (url string, ok bool, err error)
}

// Response is what a presigned GET answers with: the store signs these headers into the URL, so
// one object can be served under several content types.
type Response struct {
	ContentType        string
	ContentDisposition string // "" for none
}
