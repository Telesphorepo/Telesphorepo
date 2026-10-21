package move

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/blobs"
	"github.com/Telesphorepo/Telesphorepo/internal/move/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/storage"
)

// pageSize is the number of blobs the copier reads per page. It saves its cursor and counters
// after each page, and at least every saveEvery within a page, so the progress shows while a slow
// store takes a page for minutes.
const (
	pageSize  = 500
	saveEvery = time.Second
)

// finishTimeout bounds the database and settings writes that end a move.
const finishTimeout = time.Minute

type counters struct {
	cursor, copied, skipped, failed int64
}

type outcome int

const (
	copied outcome = iota
	skipped
	failed
)

// copy walks every blob with id > c.cursor in id order and copies it from the source to the
// target store. It saves the cursor and counters after each page, and ends the move when the walk
// is complete. It returns without ending the move when ctx ends (cancel or shutdown; a shutdown
// resumes at the next start).
func (s *Service) copy(ctx context.Context, r *run, c counters) {
	rq, wq := dbq.New(s.db.R), dbq.New(s.db.W)
	// save writes the cursor and counters; false when the move must stop (an error, or a cancel).
	// The cursor is the last blob done, in id order, so a resume starts after it.
	lastSave := time.Now()
	save := func() bool {
		lastSave = time.Now()
		n, err := wq.UpdateMoveProgress(ctx, dbq.UpdateMoveProgressParams{
			Cursor: c.cursor, Copied: c.copied, Skipped: c.skipped, Failed: c.failed, ID: r.id,
		})
		if err != nil {
			if ctx.Err() == nil {
				s.fail(r, "save progress: "+err.Error())
			}
			return false
		}
		return n > 0 // 0: cancelled
	}
	for {
		page, err := rq.WalkBlobs(ctx, dbq.WalkBlobsParams{AfterID: c.cursor, Limit: pageSize})
		if err != nil {
			if ctx.Err() == nil {
				s.fail(r, "read blobs: "+err.Error())
			}
			return
		}
		for _, b := range page {
			res, reason := s.copyOne(ctx, r, b)
			if ctx.Err() != nil {
				return
			}
			switch res {
			case copied:
				c.copied++
			case skipped:
				c.skipped++
			case failed:
				c.failed++
				err := wq.InsertMoveFailure(ctx, dbq.InsertMoveFailureParams{
					MoveID: r.id, BlobID: b.ID, Key: blobs.Key(b.Sha256), Reason: reason,
				})
				if err != nil {
					if ctx.Err() == nil {
						s.fail(r, "save failure: "+err.Error())
					}
					return
				}
			}
			c.cursor = b.ID
			if time.Since(lastSave) >= saveEvery && !save() {
				return
			}
		}
		if len(page) > 0 && !save() {
			return
		}
		if len(page) < pageSize {
			break
		}
	}
	s.finish(r, c.failed)
}

// copyOne copies blob b under its digest lock, which the asset pipeline and the blob GC take, so
// the GC does not delete it meanwhile. It skips a blob that the GC deleted, and one that the
// target already holds with its size (a blob never changes). A missing source object, or one
// whose size or SHA-256 differs from the blob row, is a failure; the content is checked before
// it is written to the target.
func (s *Service) copyOne(ctx context.Context, r *run, b dbq.WalkBlobsRow) (outcome, string) {
	release := s.blobs.Lock([]string{b.Sha256})
	defer release()
	if _, err := s.blobs.Get(ctx, b.Sha256); errors.Is(err, blobs.ErrUnknown) {
		return skipped, ""
	} else if err != nil {
		return failed, "read blob: " + err.Error()
	}
	key := blobs.Key(b.Sha256)
	src, dst := r.mirror.Src, r.mirror.Dst

	info, err := dst.Head(ctx, key)
	switch {
	case err == nil && info.Size == b.Size:
		return skipped, ""
	case err != nil && !errors.Is(err, storage.ErrNotFound):
		return failed, "check target: " + err.Error()
	}

	body, info, err := src.Get(ctx, key)
	if errors.Is(err, storage.ErrNotFound) {
		return failed, "blob missing in the source store"
	}
	if err != nil {
		return failed, "read source: " + err.Error()
	}
	defer body.Close()
	if info.Size != b.Size {
		return failed, fmt.Sprintf("source blob is %d bytes; the blob row says %d bytes", info.Size, b.Size)
	}

	// The S3 client needs a seekable body, and the hash must be checked before the target holds
	// the blob: hash a seekable source (a file) in place, spool any other to a temp file.
	var content io.Reader
	var sum string
	if seeker, ok := body.(io.ReadSeeker); ok {
		h := sha256.New()
		if _, err := io.Copy(h, seeker); err != nil {
			return failed, "read source: " + err.Error()
		}
		if _, err := seeker.Seek(0, io.SeekStart); err != nil {
			return failed, "read source: " + err.Error()
		}
		content, sum = seeker, hex.EncodeToString(h.Sum(nil))
	} else {
		sp, err := blobs.Spool(s.set.Current().TmpDir(), body, b.Size)
		if errors.Is(err, blobs.ErrTooLarge) {
			return failed, "source blob is larger than the blob row (" + strconv.FormatInt(b.Size, 10) + " bytes)"
		}
		if err != nil {
			return failed, "read source: " + err.Error()
		}
		defer sp.Close()
		if sp.Size != b.Size {
			return failed, fmt.Sprintf("source blob is %d bytes; the blob row says %d bytes", sp.Size, b.Size)
		}
		if content, err = sp.Reader(); err != nil {
			return failed, "read source: " + err.Error()
		}
		sum = sp.SHA256
	}
	if sum != b.Sha256 {
		return failed, "source blob SHA-256 " + sum + " does not match its key"
	}
	if err := dst.Put(ctx, key, content, b.Size, "application/octet-stream"); err != nil {
		return failed, "write target: " + err.Error()
	}
	return copied, ""
}

// finish ends the walk of r. Without failures, one settings write makes the target the active
// storage, the live store swaps to the target and the move is done. With failures the move
// fails and the live store returns to the source.
func (s *Service) finish(r *run, nFailed int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run != r {
		return // cancelled
	}
	s.run = nil
	r.cancel()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.base), finishTimeout)
	defer cancel()

	if nFailed > 0 {
		s.live.Swap(r.mirror.Src)
		msg := strconv.FormatInt(nFailed, 10) + " blobs could not be copied"
		if nFailed == 1 {
			msg = "1 blob could not be copied"
		}
		if _, err := s.finishRow(ctx, r.id, StateFailed, msg); err != nil {
			s.log.Error("could not save the end of the storage move", "move", r.id, "error", err.Error())
		}
		s.recordEnd(ctx, r.id, StateFailed, msg)
		s.log.Warn("storage move failed; storage stays on the source", "move", r.id, "failed", nFailed)
		return
	}
	if patch := storagePatch(storage.ConfigView.Get(s.set.Current()), r.target); len(patch) > 0 {
		if err := s.set.Save(ctx, patch, r.startedBy); err != nil {
			s.live.Swap(r.mirror.Src)
			if _, ferr := s.finishRow(ctx, r.id, StateFailed, "save storage settings: "+err.Error()); ferr != nil {
				s.log.Error("could not save the end of the storage move", "move", r.id, "error", ferr.Error())
			}
			s.recordEnd(ctx, r.id, StateFailed, "save storage settings: "+err.Error())
			s.log.Error("storage move copied every blob but the storage settings could not be saved", "move", r.id, "error", err.Error())
			return
		}
	}
	s.live.Swap(r.mirror.Dst)
	if _, err := s.finishRow(ctx, r.id, StateDone, ""); err != nil {
		// The settings already point at the target; Resume marks the move done at the next start.
		s.log.Error("could not save the end of the storage move", "move", r.id, "error", err.Error())
	}
	s.recordEnd(ctx, r.id, StateDone, "")
	s.log.Info("storage move done; storage switched to the target", "move", r.id, "storage", r.target.Backend)
}

// fail ends r as failed with msg and returns the live store to the source.
func (s *Service) fail(r *run, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run != r {
		return
	}
	s.run = nil
	r.cancel()
	s.live.Swap(r.mirror.Src)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.base), finishTimeout)
	defer cancel()
	if _, err := s.finishRow(ctx, r.id, StateFailed, msg); err != nil {
		s.log.Error("could not save the end of the storage move", "move", r.id, "error", err.Error())
	}
	s.recordEnd(ctx, r.id, StateFailed, msg)
	s.log.Error("storage move failed; storage stays on the source", "move", r.id, "error", msg)
}
