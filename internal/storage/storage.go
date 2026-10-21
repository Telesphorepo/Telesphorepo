// Package storage abstracts blob storage. Keys always use forward slashes
// (package path), identical across backends.
package storage

import (
	"context"
	"errors"
	"fmt"
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
	// Put writes the size bytes of r from offset 0 to key, atomically replacing any existing
	// object. r is a spool (*os.File) or a *bytes.Reader; a store may read it from several
	// goroutines at once (the parts of an S3 multipart upload, the two stores of a Mirror).
	Put(ctx context.Context, key string, r io.ReaderAt, size int64, contentType string) error
	// Get opens key for reading. FS returns an *os.File (implements io.ReadSeeker).
	Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error)
	// GetRange opens n bytes of key from offset off (off and n inside the object). The info has
	// the size of the whole object.
	GetRange(ctx context.Context, key string, off, n int64) (io.ReadCloser, ObjectInfo, error)
	Head(ctx context.Context, key string) (ObjectInfo, error)
	// Delete removes key; deleting a missing key is not an error.
	Delete(ctx context.Context, key string) error
	// DeleteMany removes keys; missing keys are not an error. S3 sends DeleteObjects with up to
	// 1000 keys per call; FS deletes one key at a time. When some keys stay, the error is a
	// *DeleteError that names them (see Undeleted).
	DeleteMany(ctx context.Context, keys []string) error
	// List returns up to limit objects under prefix with Key > after, ordered by key.
	// Not used on request paths.
	List(ctx context.Context, prefix, after string, limit int) ([]ObjectInfo, error)
	// PresignGet returns a time-limited GET URL whose answer carries the headers of resp.
	// ok=false when the backend cannot presign (FS) or redirects are disabled; callers then
	// stream via Get.
	PresignGet(ctx context.Context, key string, ttl time.Duration, resp Response) (url string, ok bool, err error)
}

// DeleteError is a DeleteMany that left Keys in the store; every other key is gone. Err is the
// first error.
type DeleteError struct {
	Keys []string
	Err  error
}

func (e *DeleteError) Error() string {
	if len(e.Keys) == 1 {
		return e.Err.Error()
	}
	return fmt.Sprintf("%v (%d objects not deleted)", e.Err, len(e.Keys))
}

func (e *DeleteError) Unwrap() error { return e.Err }

// add records keys as not deleted because of err.
func (e *DeleteError) add(keys []string, err error) {
	if e.Err == nil {
		e.Err = err
	}
	e.Keys = append(e.Keys, keys...)
}

// err returns e, or nil when every key is gone.
func (e *DeleteError) err() error {
	if len(e.Keys) == 0 {
		return nil
	}
	return e
}

// Undeleted returns the keys of a DeleteMany of keys that err left in the store: none for a nil
// err, the keys of a *DeleteError, else every key.
func Undeleted(keys []string, err error) []string {
	if err == nil {
		return nil
	}
	var de *DeleteError
	if errors.As(err, &de) {
		return de.Keys
	}
	return keys
}

// Response is what a presigned GET answers with: the store signs these headers into the URL, so
// one object can be served under several content types.
type Response struct {
	ContentType        string
	ContentDisposition string // "" for none
}
