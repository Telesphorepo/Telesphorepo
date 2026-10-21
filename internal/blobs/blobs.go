// Package blobs stores file content by SHA-256 on the live store. One blob backs any number of
// assets in any repos: the same bytes uploaded twice are stored once. The blobs table counts the
// asset rows that point to each blob (the assets triggers keep refs), and the garbage collector
// deletes a blob that no asset referenced for longer than a grace period.
//
// Locks: the digest lock is in-process (one server process) and not reentrant. The asset
// pipeline and the garbage collector take it through Lock only. A holder of path locks takes them
// before digest locks, each set in sorted order, and the garbage collector takes digest locks
// only, so no two holders wait on each other in a cycle.
package blobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/storage"
)

// ErrUnknown is a digest that no blob row has.
var ErrUnknown = errors.New("blob unknown")

// Blob is a stored blob.
type Blob struct {
	ID     int64
	SHA256 string
	Size   int64
	MD5    string // lowercase hex; "" for a synthetic row of the scale test
	SHA1   string
}

// Store stores and finds blobs.
type Store struct {
	db *db.DB
	st *storage.Live

	mu    sync.Mutex
	locks map[string]*lockEntry
}

type lockEntry struct {
	mu   sync.Mutex
	refs int
}

// New returns the blob store on the live store st.
func New(d *db.DB, st *storage.Live) *Store {
	return &Store{db: d, st: st, locks: map[string]*lockEntry{}}
}

// Key is the storage key of the blob with digest sha256: blobs/sha256/ab/abcd...
func Key(sha256 string) string {
	if len(sha256) < 2 {
		return "blobs/sha256/" + sha256
	}
	return "blobs/sha256/" + sha256[:2] + "/" + sha256
}

// Storage returns the live store.
func (s *Store) Storage() *storage.Live { return s.st }

// Lock takes the digest locks of the distinct digests, in sorted order, and returns the release.
func (s *Store) Lock(digests []string) (release func()) {
	ds := slices.Clone(digests)
	slices.Sort(ds)
	ds = slices.Compact(ds)
	entries := make([]*lockEntry, len(ds))
	for i, d := range ds {
		s.mu.Lock()
		e := s.locks[d]
		if e == nil {
			e = &lockEntry{}
			s.locks[d] = e
		}
		e.refs++
		s.mu.Unlock()
		e.mu.Lock()
		entries[i] = e
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			for i := len(ds) - 1; i >= 0; i-- {
				entries[i].mu.Unlock()
				s.mu.Lock()
				if entries[i].refs--; entries[i].refs == 0 {
					delete(s.locks, ds[i])
				}
				s.mu.Unlock()
			}
		})
	}
}

// Get returns the blob with digest sha256, or ErrUnknown.
func (s *Store) Get(ctx context.Context, sha256 string) (*Blob, error) {
	r, err := dbq.New(s.db.R).GetBlob(ctx, sha256)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUnknown
	}
	if err != nil {
		return nil, err
	}
	return &Blob{ID: r.ID, SHA256: r.Sha256, Size: r.Size, MD5: r.Md5, SHA1: r.Sha1}, nil
}

// Open opens the content of the blob with digest sha256.
func (s *Store) Open(ctx context.Context, sha256 string) (io.ReadCloser, storage.ObjectInfo, error) {
	return s.st.Get(ctx, Key(sha256))
}

// putWorkers is how many blobs EnsureAll writes at a time.
const putWorkers = 8

// Ensure makes sure the blob of sp exists, under a digest lock the caller holds. It writes the
// object only when no row has the digest, then inserts the row (refs 0: the commit's asset rows
// add the references).
func (s *Store) Ensure(ctx context.Context, sp *Spooled) (*Blob, error) {
	m, err := s.EnsureAll(ctx, []*Spooled{sp}, false)
	if err != nil {
		return nil, err
	}
	return m[sp.SHA256], nil
}

// EnsureAll is Ensure for several bodies, under the digest locks of all of them: it writes the
// missing objects (a few at a time), then inserts their rows in one transaction. fast writes an
// object without the store's crash safety when the store has such a path (the dev demo data). It
// returns the blobs by digest.
func (s *Store) EnsureAll(ctx context.Context, sps []*Spooled, fast bool) (map[string]*Blob, error) {
	out := make(map[string]*Blob, len(sps))
	var missing []*Spooled
	for _, sp := range sps {
		if _, ok := out[sp.SHA256]; ok || slices.ContainsFunc(missing, func(m *Spooled) bool { return m.SHA256 == sp.SHA256 }) {
			continue
		}
		b, err := s.Get(ctx, sp.SHA256)
		switch {
		case err == nil:
			out[sp.SHA256] = b
		case errors.Is(err, ErrUnknown):
			missing = append(missing, sp)
		default:
			return nil, err
		}
	}
	if len(missing) == 0 {
		return out, nil
	}
	if err := s.putAll(ctx, missing, fast); err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		for _, sp := range missing {
			id, err := q.InsertBlob(ctx, dbq.InsertBlobParams{Sha256: sp.SHA256, Size: sp.Size, UnreferencedAt: now, CreatedAt: now,
				Md5: sp.MD5, Sha1: sp.SHA1})
			if err != nil {
				return fmt.Errorf("insert blob %s: %w", sp.SHA256, err)
			}
			out[sp.SHA256] = &Blob{ID: id, SHA256: sp.SHA256, Size: sp.Size, MD5: sp.MD5, SHA1: sp.SHA1}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// unsynced is the fast write path of the filesystem store (storage.FS WriteUnsynced).
type unsynced interface {
	WriteUnsynced(key string, data []byte) error
}

// adopter is the rename path of the filesystem store (fsStore.Adopt): a spool on the volume of
// the store becomes the object without a copy.
type adopter interface {
	Adopt(key string, f *os.File, size int64) bool
}

func (s *Store) putAll(ctx context.Context, sps []*Spooled, fast bool) error {
	active := s.st.Active()
	var fastStore unsynced
	if fast {
		fastStore, _ = active.(unsynced)
	}
	adopt, _ := active.(adopter)
	put := func(sp *Spooled) error {
		if fastStore != nil && sp.Data != nil {
			return fastStore.WriteUnsynced(Key(sp.SHA256), sp.Data)
		}
		if adopt != nil && sp.File != nil && !sp.adopted && adopt.Adopt(Key(sp.SHA256), sp.File, sp.Size) {
			sp.adopted = true
			return nil
		}
		rd, err := sp.ReaderAt()
		if err != nil {
			return err
		}
		return active.Put(ctx, Key(sp.SHA256), rd, sp.Size, "application/octet-stream")
	}
	if len(sps) == 1 {
		return put(sps[0])
	}
	sem := make(chan struct{}, putWorkers)
	errs := make([]error, len(sps))
	var wg sync.WaitGroup
	for i, sp := range sps {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			errs[i] = put(sp)
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

// gcBatch is how many blobs one garbage collector round reads, locks and deletes (one S3
// DeleteObjects).
const gcBatch = 1000

// GC deletes every blob that no asset referenced for longer than grace, in rounds of gcBatch.
// Under the digest locks of the round, which every new reference to a blob also holds, it checks
// again in one query which blobs no asset points to, deletes their objects with one DeleteMany,
// then their rows in one transaction. A blob whose object delete failed keeps its row, and a
// later run tries again. It returns how many blobs it deleted.
func (s *Store) GC(ctx context.Context, grace time.Duration) (int, error) {
	n := 0
	for {
		cutoff := time.Now().Add(-grace).Unix()
		rows, err := dbq.New(s.db.R).ListUnreferencedBlobs(ctx, dbq.ListUnreferencedBlobsParams{
			Cutoff: cutoff, Limit: gcBatch})
		if err != nil {
			return n, err
		}
		if len(rows) == 0 {
			return n, nil
		}
		digests := make([]string, len(rows))
		for i, r := range rows {
			digests[i] = r.Sha256
		}
		gone, err := s.collect(ctx, rows, digests, cutoff)
		n += gone
		if err != nil {
			return n, err
		}
		if len(rows) < gcBatch {
			return n, nil
		}
	}
}

// collect deletes the blobs of rows that are still collectable, under their digest locks, and
// returns how many it deleted.
func (s *Store) collect(ctx context.Context, rows []dbq.ListUnreferencedBlobsRow, digests []string, cutoff int64) (int, error) {
	release := s.Lock(digests)
	defer release()
	ids := make([]int64, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	ok, err := dbq.New(s.db.W).ListCollectableBlobs(ctx, dbq.ListCollectableBlobsParams{Ids: ids, Cutoff: cutoff})
	if err != nil || len(ok) == 0 {
		return 0, err
	}
	collectable := make(map[int64]bool, len(ok))
	for _, id := range ok {
		collectable[id] = true
	}
	var keys []string
	for _, r := range rows {
		if collectable[r.ID] {
			keys = append(keys, Key(r.Sha256))
		}
	}
	delErr := s.st.DeleteMany(ctx, keys)
	if delErr != nil {
		delErr = fmt.Errorf("delete blob objects: %w", delErr)
	}
	left := map[string]bool{}
	for _, k := range storage.Undeleted(keys, delErr) {
		left[k] = true
	}
	// The rows of deleted objects go even when ctx ends: a row without its object would make a
	// new upload of the same bytes reuse a blob that is gone.
	dctx := context.WithoutCancel(ctx)
	n := 0
	err = s.db.Tx(dctx, func(tx *sql.Tx) error {
		n = 0
		q := dbq.New(tx)
		for _, r := range rows {
			if !collectable[r.ID] || left[Key(r.Sha256)] {
				continue
			}
			c, err := q.DeleteUnreferencedBlob(dctx, r.ID)
			if err != nil {
				return err
			}
			n += int(c)
		}
		return nil
	})
	if err != nil {
		return 0, errors.Join(delErr, err)
	}
	return n, delErr
}
