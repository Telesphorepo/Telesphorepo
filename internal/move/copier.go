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

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/storage"
)

// pageSize is the number of assets the copier reads per page and the interval at which it
// saves its cursor and counters.
const pageSize = 500

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

// copy walks every asset with id > c.cursor in id order and copies its blob from the source
// to the target store. It saves the cursor and counters after each page, and ends the move when
// the walk is complete. It returns without ending the move when ctx ends (cancel or shutdown;
// a shutdown resumes at the next start).
func (s *Service) copy(ctx context.Context, r *run, c counters) {
	rq, wq := dbq.New(s.db.R), dbq.New(s.db.W)
	repoIDs := map[string]int64{}
	for {
		page, err := rq.WalkAssets(ctx, dbq.WalkAssetsParams{AfterID: c.cursor, Limit: pageSize})
		if err != nil {
			if ctx.Err() == nil {
				s.fail(r, "read assets: "+err.Error())
			}
			return
		}
		for _, a := range page {
			res, reason := s.copyOne(ctx, r, repoIDs, a)
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
					MoveID: r.id, AssetID: a.ID, Key: assets.Key(a.RepoName, a.Path), Reason: reason,
				})
				if err != nil {
					if ctx.Err() == nil {
						s.fail(r, "save failure: "+err.Error())
					}
					return
				}
			}
		}
		if len(page) > 0 {
			c.cursor = page[len(page)-1].ID
			n, err := wq.UpdateMoveProgress(ctx, dbq.UpdateMoveProgressParams{
				Cursor: c.cursor, Copied: c.copied, Skipped: c.skipped, Failed: c.failed, ID: r.id,
			})
			if err != nil {
				if ctx.Err() == nil {
					s.fail(r, "save progress: "+err.Error())
				}
				return
			}
			if n == 0 {
				return // cancelled
			}
		}
		if len(page) < pageSize {
			break
		}
	}
	s.finish(r, c.failed)
}

// copyOne copies the blob of asset a under the asset lock that uploads take, so no upload
// changes the blob meanwhile. It skips the asset when the target already holds a current blob
// of the asset's size, or when the asset was deleted. A missing source blob, or one whose size or
// SHA-256 differs from the asset row, is a failure; the blob is checked before it is written to
// the target.
func (s *Service) copyOne(ctx context.Context, r *run, repoIDs map[string]int64, a dbq.WalkAssetsRow) (outcome, string) {
	repoID, ok := repoIDs[a.RepoName]
	if !ok {
		repo, err := s.repos.ByName(ctx, a.RepoName)
		if errors.Is(err, repos.ErrNotFound) {
			return skipped, "" // the repo and its assets were deleted
		}
		if err != nil {
			return failed, "read repo: " + err.Error()
		}
		repoID = repo.ID
		repoIDs[a.RepoName] = repoID
	}
	unlock := s.assets.Lock(repoID, a.Path)
	defer unlock()

	// Read the row again under the lock: an upload may have replaced the blob since the page
	// was read.
	cur, err := s.assets.Get(ctx, repoID, a.Path)
	if errors.Is(err, assets.ErrNotFound) {
		return skipped, ""
	}
	if err != nil {
		return failed, "read asset: " + err.Error()
	}
	key := assets.Key(a.RepoName, a.Path)
	src, dst := r.mirror.Src, r.mirror.Dst

	// A target blob of the asset's size, written after the asset's last change, is current. An
	// older one of the same size can be left from an earlier failed move, and is copied again.
	info, err := dst.Head(ctx, key)
	switch {
	case err == nil && info.Size == cur.Size && !info.ModTime.Before(cur.UpdatedAt):
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
	if info.Size != cur.Size {
		return failed, fmt.Sprintf("source blob is %d bytes; the asset is %d bytes", info.Size, cur.Size)
	}

	// The S3 client needs a seekable body, and the hash must be checked before the target
	// holds the blob: hash a seekable source (a file) in place, spool any other to a temp file.
	var rd io.Reader
	var sum string
	if rs, ok := body.(io.ReadSeeker); ok {
		h := sha256.New()
		if _, err := io.Copy(h, rs); err != nil {
			return failed, "read source: " + err.Error()
		}
		if _, err := rs.Seek(0, io.SeekStart); err != nil {
			return failed, "read source: " + err.Error()
		}
		rd, sum = rs, hex.EncodeToString(h.Sum(nil))
	} else {
		sp, err := s.assets.Spool(body, cur.Size)
		if errors.Is(err, assets.ErrTooLarge) {
			return failed, "source blob is larger than the asset (" + strconv.FormatInt(cur.Size, 10) + " bytes)"
		}
		if err != nil {
			return failed, "read source: " + err.Error()
		}
		defer sp.Close()
		if sp.Size != cur.Size {
			return failed, fmt.Sprintf("source blob is %d bytes; the asset is %d bytes", sp.Size, cur.Size)
		}
		if rd, err = sp.Reader(); err != nil {
			return failed, "read source: " + err.Error()
		}
		sum = sp.SHA256
	}
	if sum != cur.SHA256 {
		return failed, "source blob SHA-256 " + sum + " does not match the asset (" + cur.SHA256 + ")"
	}
	if err := dst.Put(ctx, key, rd, cur.Size, cur.ContentType); err != nil {
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
		s.log.Warn("storage move failed; storage stays on the source", "move", r.id, "failed", nFailed)
		return
	}
	if patch := storagePatch(s.set.Config(), r.target); len(patch) > 0 {
		if err := s.set.Update(ctx, patch, r.startedBy); err != nil {
			s.live.Swap(r.mirror.Src)
			if _, ferr := s.finishRow(ctx, r.id, StateFailed, "save storage settings: "+err.Error()); ferr != nil {
				s.log.Error("could not save the end of the storage move", "move", r.id, "error", ferr.Error())
			}
			s.log.Error("storage move copied every blob but the storage settings could not be saved", "move", r.id, "error", err.Error())
			return
		}
	}
	s.live.Swap(r.mirror.Dst)
	if _, err := s.finishRow(ctx, r.id, StateDone, ""); err != nil {
		// The settings already point at the target; Resume marks the move done at the next start.
		s.log.Error("could not save the end of the storage move", "move", r.id, "error", err.Error())
	}
	s.log.Info("storage move done; storage switched to the target", "move", r.id, "storage", r.target.Storage)
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
	s.log.Error("storage move failed; storage stays on the source", "move", r.id, "error", msg)
}
