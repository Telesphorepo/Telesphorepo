package storage

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"io"
	"slices"
	"sync/atomic"
	"time"
)

// Open builds the Store that cfg selects (fs or s3). It performs no network I/O.
func Open(ctx context.Context, cfg Config) (Store, error) {
	if cfg.Backend == "s3" {
		return NewS3(ctx, cfg)
	}
	return NewFS(cfg.FSRoot)
}

// SameBackend reports whether a and b select the identical store (no swap needed).
func SameBackend(a, b Config) bool {
	a.PresignTTL, b.PresignTTL = 0, 0
	if a.Backend != "s3" && b.Backend != "s3" {
		return a.FSRoot == b.FSRoot
	}
	return a == b
}

// Live is a Store that delegates to a hot-swappable active Store (storage changes
// apply without restart). In-flight operations finish on the store they started on. During a
// storage move the active store is a *Mirror; the move service swaps in Mirror.Dst when the
// move is done and Mirror.Src when it fails or is cancelled.
type Live struct {
	cur atomic.Pointer[holder]
}

type holder struct{ Store }

// NewLive returns a Live store delegating to st.
func NewLive(st Store) *Live {
	l := &Live{}
	l.Swap(st)
	return l
}

// Swap makes st the active store.
func (l *Live) Swap(st Store) { l.cur.Store(&holder{st}) }

// Active returns the active store.
func (l *Live) Active() Store { return l.cur.Load().Store }

func (l *Live) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	return l.Active().Put(ctx, key, r, size, contentType)
}

func (l *Live) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	return l.Active().Get(ctx, key)
}

func (l *Live) GetRange(ctx context.Context, key string, off, n int64) (io.ReadCloser, ObjectInfo, error) {
	return l.Active().GetRange(ctx, key, off, n)
}

func (l *Live) Head(ctx context.Context, key string) (ObjectInfo, error) {
	return l.Active().Head(ctx, key)
}

func (l *Live) Delete(ctx context.Context, key string) error { return l.Active().Delete(ctx, key) }

func (l *Live) DeleteMany(ctx context.Context, keys []string) error {
	return l.Active().DeleteMany(ctx, keys)
}

func (l *Live) List(ctx context.Context, prefix, after string, limit int) ([]ObjectInfo, error) {
	return l.Active().List(ctx, prefix, after, limit)
}

func (l *Live) PresignGet(ctx context.Context, key string, ttl time.Duration, resp Response) (string, bool, error) {
	return l.Active().PresignGet(ctx, key, ttl, resp)
}

// ProbePrefix is the key prefix of storage probe objects.
const ProbePrefix = "_telesphorepo/probe-"

// Probe checks st end to end: put, head, get (content compared) and delete of a random probe
// key under ProbePrefix. The returned error is suitable to show to an administrator.
func Probe(ctx context.Context, st Store) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var rnd [8]byte
	rand.Read(rnd[:])
	key := ProbePrefix + hex.EncodeToString(rnd[:])
	body := []byte("telesphorepo storage probe " + key + "\n")

	if err := st.Put(ctx, key, bytes.NewReader(body), int64(len(body)), "text/plain"); err != nil {
		var dns *net.DNSError
		if errors.As(err, &dns) && dns.IsNotFound {
			return fmt.Errorf("the host %s does not exist. Check the S3 address: it is the address of the S3 API, such as https://<account id>.r2.cloudflarestorage.com for Cloudflare R2, not a custom domain of the bucket. Without path-style addresses, the bucket name goes in front of the host of the address", dns.Name)
		}
		return fmt.Errorf("write test object: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			st.Delete(context.WithoutCancel(ctx), key)
		}
	}()
	info, err := st.Head(ctx, key)
	if err != nil {
		return fmt.Errorf("stat test object: %w", err)
	}
	if info.Size != int64(len(body)) {
		return fmt.Errorf("stat test object: size %d, want %d", info.Size, len(body))
	}
	rc, _, err := st.Get(ctx, key)
	if err != nil {
		return fmt.Errorf("read test object: %w", err)
	}
	got, err := io.ReadAll(io.LimitReader(rc, int64(len(body))+1))
	rc.Close()
	if err != nil {
		return fmt.Errorf("read test object: %w", err)
	}
	if !slices.Equal(got, body) {
		return errors.New("read test object: content differs from what was written")
	}
	cleanup = false
	if err := st.Delete(ctx, key); err != nil {
		return fmt.Errorf("delete test object: %w", err)
	}
	if _, err := st.Head(ctx, key); !errors.Is(err, ErrNotFound) {
		if err == nil {
			return errors.New("delete test object: object still exists after delete")
		}
		return fmt.Errorf("stat deleted test object: %w", err)
	}
	return nil
}
