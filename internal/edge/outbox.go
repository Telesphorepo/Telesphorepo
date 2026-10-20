package edge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/edge/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// The outbox worker.
const (
	outboxBatch   = 64              // rows per read
	outboxWorkers = 8               // rows written at once (each row is one key)
	outboxPoll    = time.Second     // the wait when no row is due
	maxBackoff    = 5 * time.Minute // the longest wait after failed attempts
	recheckAfter  = time.Minute     // the second application after a put that timed out
	countEvery    = 5 * time.Second // the count of the status page
)

// runOutbox is the job edge.outbox: while edge delivery is on, it writes the due rows of the
// outbox and deletes each row once every write of it succeeded.
func (m *mod) runOutbox(ctx context.Context) {
	var counted time.Time
	for ctx.Err() == nil {
		v := m.cur.Load()
		if v.mode != modeOn {
			m.idle(ctx)
			continue
		}
		if time.Since(counted) >= countEvery {
			counted = time.Now()
			if c, err := m.rq.CountOutbox(ctx); err == nil {
				m.counts.Store(outboxCounts{Total: c.Total, Pointers: c.Pointers, LastError: c.LastError})
			}
		}
		rows, err := m.rq.DueOutbox(ctx, dbq.DueOutboxParams{Now: nowMS(), Limit: outboxBatch})
		if err != nil {
			if ctx.Err() == nil {
				m.h.Log.Error("edge outbox read failed", "error", err.Error())
			}
			m.idle(ctx)
			continue
		}
		if len(rows) == 0 {
			m.idle(ctx)
			continue
		}
		parallel(outboxWorkers, rows, func(r dbq.DueOutboxRow) error {
			m.process(ctx, v, r)
			return nil
		})
	}
}

// idle waits for a wake-up, a second, or the end of ctx.
func (m *mod) idle(ctx context.Context) {
	t := time.NewTimer(outboxPoll)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-m.kick:
	case <-t.C:
	}
}

// objectKey is the key under root that the row writes.
func objectKey(r dbq.DueOutboxRow) string {
	if r.RepoID == 0 {
		return r.Path
	}
	return pointerKey(r.RepoID, r.Path)
}

// process writes one row. A key written less than a second ago waits outside the path lock.
func (m *mod) process(ctx context.Context, v *view, r dbq.DueOutboxRow) {
	if wait := m.sp.wait(objectKey(r)); wait > 0 {
		m.delay(ctx, r, nowMS()+wait.Milliseconds()+1, r.TimedOutAt)
		return
	}
	var err error
	drop := false
	if r.RepoID == 0 {
		err = m.applyObject(ctx, v, r.Path)
	} else {
		drop, err = m.applyPointer(ctx, v, r)
	}
	if ctx.Err() != nil {
		return
	}
	now := nowMS()
	if err != nil {
		timedOut := r.TimedOutAt
		if errors.Is(err, context.DeadlineExceeded) {
			timedOut = now
		}
		backoff := min(time.Second<<min(r.Attempts, 20), maxBackoff)
		if _, qerr := m.q.RetryOutbox(ctx, dbq.RetryOutboxParams{DueAt: now + backoff.Milliseconds(), Error: errText(err),
			TimedOutAt: timedOut, RepoID: r.RepoID, Path: r.Path, Seq: r.Seq}); qerr != nil {
			m.h.Log.Error("edge outbox update failed", "error", qerr.Error())
		}
		return
	}
	if !drop && r.TimedOutAt > 0 {
		// R2 reads do not wait for earlier writes, so a put that timed out may still land: apply
		// the final state once more a minute after it.
		if again := r.TimedOutAt + recheckAfter.Milliseconds(); now < again {
			m.delay(ctx, r, again, 0)
			return
		}
	}
	if _, err := m.q.DeleteOutbox(ctx, dbq.DeleteOutboxParams{RepoID: r.RepoID, Path: r.Path, Seq: r.Seq}); err != nil {
		m.h.Log.Error("edge outbox delete failed", "error", err.Error())
	}
}

func (m *mod) delay(ctx context.Context, r dbq.DueOutboxRow, due, timedOut int64) {
	if _, err := m.q.DelayOutbox(ctx, dbq.DelayOutboxParams{DueAt: due, TimedOutAt: timedOut, RepoID: r.RepoID, Path: r.Path, Seq: r.Seq}); err != nil {
		m.h.Log.Error("edge outbox update failed", "error", err.Error())
	}
}

// applyPointer writes the pointer of a row under the path lock, from the asset row read again
// under the lock: the pointer of the asset (home, and every region for an immutable path) when it
// exists with the row's digest (or the row has none); a delete in every bucket when the asset is
// gone or gets no pointer. drop: the asset has another digest, whose commit has its own row.
func (m *mod) applyPointer(ctx context.Context, v *view, r dbq.DueOutboxRow) (drop bool, err error) {
	unlock := m.h.Assets.Lock(r.RepoID, r.Path)
	defer unlock()
	key := pointerKey(r.RepoID, r.Path)
	repo, err := m.h.Repos.ByID(ctx, r.RepoID)
	if errors.Is(err, repos.ErrNotFound) {
		return false, m.deleteEverywhere(ctx, v, key)
	}
	if err != nil {
		return false, err
	}
	es := m.edgeOf(repo)
	a, err := m.h.Assets.Get(ctx, repo.ID, r.Path)
	if errors.Is(err, assets.ErrNotFound) || (err == nil && (es == nil || !m.eligible(repo, a.Path))) {
		return false, m.deleteEverywhere(ctx, v, key)
	}
	if err != nil {
		return false, err
	}
	if r.Sha256 != "" && a.SHA256 != r.Sha256 {
		return true, nil
	}
	body := pointerOf(a)
	if err := m.put(ctx, v.home, key, body); err != nil {
		return false, err
	}
	if !es.IsImmutable(a.Path) {
		return false, nil
	}
	return false, parallel(len(v.regions)+1, v.regions, func(b *bucket) error { return m.put(ctx, b, key, body) })
}

// deleteEverywhere deletes key in the home bucket and in every region.
func (m *mod) deleteEverywhere(ctx context.Context, v *view, key string) error {
	return parallel(len(v.regions)+1, v.all(), func(b *bucket) error { return m.del(ctx, b, key) })
}

// applyObject writes the access object or config.json of key to the home bucket when its JSON
// differs from the one written, or deletes it when its row is gone.
func (m *mod) applyObject(ctx context.Context, v *view, key string) error {
	if key == configKey {
		return m.writeConfig(ctx, false)
	}
	body, ok, err := m.object(ctx, key)
	if err != nil {
		return err
	}
	if !ok {
		return m.forget(ctx, v, key)
	}
	return m.write(ctx, v, key, body, false)
}

// write puts body as the object key in the home bucket and records its hash, unless the hash
// written is the same (force writes anyway).
func (m *mod) write(ctx context.Context, v *view, key string, body []byte, force bool) error {
	h := hashOf(body)
	if !force {
		old, err := m.rq.GetWritten(ctx, key)
		if err == nil && old == h {
			return nil
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	err := m.put(ctx, v.home, key, body)
	if key == configKey {
		var c struct {
			AccessTTL int64 `json:"access_ttl"`
		}
		_ = json.Unmarshal(body, &c)
		m.notePublished(ctx, c.AccessTTL, err == nil)
	}
	if err != nil {
		return err
	}
	return m.q.UpsertWritten(ctx, dbq.UpsertWrittenParams{Key: key, Hash: h, WrittenAt: time.Now().Unix()})
}

// forget deletes the object key from the home bucket and its row of edge_written.
func (m *mod) forget(ctx context.Context, v *view, key string) error {
	if err := m.del(ctx, v.home, key); err != nil {
		return err
	}
	return m.q.DeleteWritten(ctx, key)
}

// mark queues the access objects of keys in the outbox, while edge delivery is on.
func (m *mod) mark(ctx context.Context, keys ...string) {
	if len(keys) == 0 || m.cur.Load().mode != modeOn {
		return
	}
	now := nowMS()
	err := m.h.DB.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		for _, k := range keys {
			if err := q.UpsertOutbox(ctx, dbq.UpsertOutboxParams{RepoID: 0, Path: k, DueAt: now}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		// The reconcile writes the object within 5 minutes.
		m.h.Log.Error("edge mark failed", "error", err.Error())
		return
	}
	m.wake()
}
