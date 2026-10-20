package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// Mirror is the store during a storage move. Reads and listings come from Src,
// which holds every blob until the move is done. Writes and deletes go to Dst first, then to
// Src, so both stores see every change made while the copier runs; an error on either fails
// the call.
type Mirror struct {
	Src, Dst Store
}

// NewMirror returns a Mirror that reads from src and writes to dst and src.
func NewMirror(src, dst Store) *Mirror { return &Mirror{Src: src, Dst: dst} }

// Put writes to Dst, rewinds r, then writes to Src. r must be an io.Seeker (callers pass the
// spooled upload file).
func (m *Mirror) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	sk, ok := r.(io.Seeker)
	if !ok {
		return errors.New("storage: a mirror write needs a seekable reader")
	}
	start, err := sk.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	if err := m.Dst.Put(ctx, key, r, size, contentType); err != nil {
		return fmt.Errorf("write to move target: %w", err)
	}
	if _, err := sk.Seek(start, io.SeekStart); err != nil {
		return err
	}
	return m.Src.Put(ctx, key, r, size, contentType)
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

// Delete removes key from Dst, then from Src.
func (m *Mirror) Delete(ctx context.Context, key string) error {
	if err := m.Dst.Delete(ctx, key); err != nil {
		return fmt.Errorf("delete from move target: %w", err)
	}
	return m.Src.Delete(ctx, key)
}

// DeleteMany removes keys from Dst, then from Src.
func (m *Mirror) DeleteMany(ctx context.Context, keys []string) error {
	if err := m.Dst.DeleteMany(ctx, keys); err != nil {
		return fmt.Errorf("delete from move target: %w", err)
	}
	return m.Src.DeleteMany(ctx, keys)
}

func (m *Mirror) List(ctx context.Context, prefix, after string, limit int) ([]ObjectInfo, error) {
	return m.Src.List(ctx, prefix, after, limit)
}

func (m *Mirror) PresignGet(ctx context.Context, key string, ttl time.Duration, resp Response) (string, bool, error) {
	return m.Src.PresignGet(ctx, key, ttl, resp)
}
