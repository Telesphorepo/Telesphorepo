package debian

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"telesphorepo/internal/repos"
)

func nowNanos() int64 { return time.Now().UnixNano() }

type distKey struct {
	repoID int64
	dist   string
}

type distState struct {
	first   time.Time // first mark since the last rebuild started
	gen     int       // invalidates superseded timers
	timer   *time.Timer
	running bool
	again   bool // notified while running
	fails   int  // consecutive failed rebuilds; >0 means a backoff timer is armed
}

// indexer debounces rebuilds per (repo, dist) (spec §9.3). Rebuilds of one key never
// overlap; different keys may rebuild concurrently up to cap(sem).
type indexer struct {
	h      *Handler
	mu     sync.Mutex
	ctx    context.Context
	states map[distKey]*distState
	sem    chan struct{}
}

func (ix *indexer) init(h *Handler) {
	ix.h = h
	ix.states = map[distKey]*distState{}
	ix.sem = make(chan struct{}, 2)
}

func (ix *indexer) start(ctx context.Context) {
	ix.mu.Lock()
	ix.ctx = ctx
	ix.mu.Unlock()
	go func() {
		<-ctx.Done()
		ix.mu.Lock()
		for _, st := range ix.states {
			if st.timer != nil {
				st.timer.Stop()
			}
		}
		ix.mu.Unlock()
	}()

	keys, maxMark, err := ix.h.loadDirty(ctx)
	if err != nil {
		ix.h.log.Error("debian indexer: load pending rebuilds", "err", err)
		return
	}
	ix.h.raiseClock(maxMark)
	for _, k := range keys {
		ix.notify(k)
	}
}

func (h *Handler) loadDirty(ctx context.Context) ([]distKey, int64, error) {
	var maxMark sql.NullInt64
	if err := h.db.R.QueryRowContext(ctx, `SELECT MAX(marked_at) FROM deb_dirty`).Scan(&maxMark); err != nil {
		return nil, 0, err
	}
	rows, err := h.db.R.QueryContext(ctx, `SELECT DISTINCT repo_id, distribution FROM deb_dirty`)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var keys []distKey
	for rows.Next() {
		var k distKey
		if err := rows.Scan(&k.repoID, &k.dist); err != nil {
			return nil, 0, err
		}
		keys = append(keys, k)
	}
	return keys, maxMark.Int64, rows.Err()
}

// notify records a mark for k. Before Start it is a no-op: Start enqueues all durable rows.
func (ix *indexer) notify(k distKey) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if ix.ctx == nil || ix.ctx.Err() != nil {
		return
	}
	st := ix.states[k]
	if st == nil {
		st = &distState{}
		ix.states[k] = st
	}
	if st.running {
		st.again = true
		if st.first.IsZero() {
			st.first = time.Now() // max delay counts from this mark, not the rebuild's end
		}
		return
	}
	if st.fails > 0 {
		return // the pending backoff retry covers this mark; don't shorten it
	}
	ix.scheduleLocked(k, st)
}

// scheduleLocked (re)arms the debounce timer: quiet period, capped at first mark + max delay.
func (ix *indexer) scheduleLocked(k distKey, st *distState) {
	now := time.Now()
	if st.first.IsZero() {
		st.first = now
	}
	d := ix.h.cfg.DebDebounce
	if limit := st.first.Add(ix.h.cfg.DebMaxDelay).Sub(now); limit < d {
		d = max(limit, 0)
	}
	ix.armLocked(k, st, d)
}

func (ix *indexer) armLocked(k distKey, st *distState, d time.Duration) {
	if st.timer != nil {
		st.timer.Stop()
	}
	st.gen++
	gen := st.gen
	st.timer = time.AfterFunc(d, func() { ix.run(k, gen) })
}

func (ix *indexer) run(k distKey, gen int) {
	ix.mu.Lock()
	st := ix.states[k]
	ctx := ix.ctx
	if st == nil || st.gen != gen || st.running || ctx.Err() != nil {
		ix.mu.Unlock()
		return
	}
	st.running, st.again, st.timer, st.first = true, false, nil, time.Time{}
	ix.mu.Unlock()

	select {
	case ix.sem <- struct{}{}:
	case <-ctx.Done():
		return
	}
	began := time.Now()
	remaining, err := ix.h.rebuild(ctx, k)
	<-ix.sem

	ix.mu.Lock()
	defer ix.mu.Unlock()
	st.running = false
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		st.fails++
		backoff := min(time.Second<<min(st.fails, 9), 5*time.Minute)
		ix.h.log.Error("debian index rebuild failed", "repo_id", k.repoID, "dist", k.dist,
			"err", err, "retry_in", backoff.String())
		ix.armLocked(k, st, backoff)
		return
	}
	st.fails = 0
	ix.h.log.Info("debian index rebuilt", "repo_id", k.repoID, "dist", k.dist,
		"duration_ms", time.Since(began).Milliseconds())
	if remaining || st.again {
		ix.scheduleLocked(k, st)
		return
	}
	delete(ix.states, k)
}

type dirtyUnit struct{ component, arch string }

// rebuild regenerates the dirty Packages files of one dist, then its Release files, and
// clears the dirty rows that were marked before it started. remaining reports dirty rows
// left over (marked during the rebuild).
func (h *Handler) rebuild(ctx context.Context, k distKey) (remaining bool, err error) {
	var start int64
	var units []dirtyUnit
	// Taking the start under the single writer orders it after every committed mark.
	err = h.db.Tx(ctx, func(tx *sql.Tx) error {
		start = h.tick()
		rows, err := tx.QueryContext(ctx, `SELECT component, architecture FROM deb_dirty
			WHERE repo_id = ? AND distribution = ? ORDER BY component, architecture`, k.repoID, k.dist)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var u dirtyUnit
			if err := rows.Scan(&u.component, &u.arch); err != nil {
				return err
			}
			units = append(units, u)
		}
		return rows.Err()
	})
	if err != nil || len(units) == 0 {
		return false, err
	}

	repo, err := h.repos.ByID(ctx, k.repoID)
	if errors.Is(err, repos.ErrNotFound) {
		return false, nil // deleted repo: its dirty rows cascade away
	}
	if err != nil {
		return false, err
	}

	for _, u := range units {
		if err := h.buildPackages(ctx, repo, k.dist, u.component, u.arch); err != nil {
			return false, err
		}
	}
	if err := h.buildRelease(ctx, repo, k.dist); err != nil {
		return false, err
	}

	err = h.db.Tx(ctx, func(tx *sql.Tx) error {
		for _, u := range units {
			if _, err := tx.ExecContext(ctx, `DELETE FROM deb_dirty
				WHERE repo_id = ? AND distribution = ? AND component = ? AND architecture = ? AND marked_at <= ?`,
				k.repoID, k.dist, u.component, u.arch, start); err != nil {
				return err
			}
		}
		return tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM deb_dirty WHERE repo_id = ? AND distribution = ?)`,
			k.repoID, k.dist).Scan(&remaining)
	})
	return remaining, err
}
