package storage

import (
	"context"
	"fmt"
	"io"
	"slices"
	"sync"
	"time"
)

// Mirror is the store during a storage move. Reads and listings come from Src,
// which holds every blob until the move is done. Writes and deletes go to Dst and Src at once,
// so both stores see every change made while the copier runs; an error on either fails the call.
type Mirror struct {
	Src, Dst Store
}

// NewMirror returns a Mirror that reads from src and writes to dst and src.
func NewMirror(src, dst Store) *Mirror { return &Mirror{Src: src, Dst: dst} }

// both runs dst on Dst and src on Src at once and returns their errors, the target's first.
func (m *Mirror) both(dst, src func() error) (dstErr, srcErr error) {
	var wg sync.WaitGroup
	wg.Go(func() { dstErr = dst() })
	srcErr = src()
	wg.Wait()
	return dstErr, srcErr
}

// Put writes to Dst and Src at once; each store reads r on its own (r is an io.ReaderAt).
func (m *Mirror) Put(ctx context.Context, key string, r io.ReaderAt, size int64, contentType string) error {
	dstErr, srcErr := m.both(
		func() error { return m.Dst.Put(ctx, key, r, size, contentType) },
		func() error { return m.Src.Put(ctx, key, r, size, contentType) },
	)
	if dstErr != nil {
		return fmt.Errorf("write to move target: %w", dstErr)
	}
	return srcErr
}

func (m *Mirror) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	return m.Src.Get(ctx, key)
}

func (m *Mirror) GetRange(ctx context.Context, key string, off, n int64) (io.ReadCloser, ObjectInfo, error) {
	return m.Src.GetRange(ctx, key, off, n)
}

func (m *Mirror) Head(ctx context.Context, key string) (ObjectInfo, error) {
	return m.Src.Head(ctx, key)
}

// Delete removes key from Dst and Src at once.
func (m *Mirror) Delete(ctx context.Context, key string) error {
	dstErr, srcErr := m.both(
		func() error { return m.Dst.Delete(ctx, key) },
		func() error { return m.Src.Delete(ctx, key) },
	)
	if dstErr != nil {
		return fmt.Errorf("delete from move target: %w", dstErr)
	}
	return srcErr
}

// DeleteMany removes keys from Dst and Src at once. A key that either store kept is in the
// *DeleteError.
func (m *Mirror) DeleteMany(ctx context.Context, keys []string) error {
	dstErr, srcErr := m.both(
		func() error { return m.Dst.DeleteMany(ctx, keys) },
		func() error { return m.Src.DeleteMany(ctx, keys) },
	)
	if dstErr == nil && srcErr == nil {
		return nil
	}
	left := append(slices.Clone(Undeleted(keys, dstErr)), Undeleted(keys, srcErr)...)
	slices.Sort(left)
	de := &DeleteError{Keys: slices.Compact(left), Err: srcErr}
	if dstErr != nil {
		de.Err = fmt.Errorf("delete from move target: %w", dstErr)
	}
	return de
}

func (m *Mirror) List(ctx context.Context, prefix, after string, limit int) ([]ObjectInfo, error) {
	return m.Src.List(ctx, prefix, after, limit)
}

func (m *Mirror) PresignGet(ctx context.Context, key string, ttl time.Duration, resp Response) (string, bool, error) {
	return m.Src.PresignGet(ctx, key, ttl, resp)
}
