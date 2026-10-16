// Package rebuild is the durable queue of format rebuilds: a format marks a unit of a group of
// a repo (e.g. the Debian index of "main/amd64" in the distribution "trixie") in the
// transaction that changes what the unit is built from, and the worker (the core.rebuild job)
// calls the format's Rebuild.Run after a quiet period. The rows survive a restart: at startup the
// worker picks up every pending group.
//
// Debounce is per (repo, group): the worker waits index.debounce after the last mark, but at
// most index.max_delay after the first one. Rebuilds of one group never overlap; different
// groups rebuild at the same time up to a small limit. A rebuild deletes only the rows marked
// before it started, so a mark during a rebuild leads to another rebuild.
package rebuild

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/ops"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
)

// Run rebuilds units of group grp of repo.
type Run func(ctx context.Context, repo *repos.Repo, grp string, units []string) error

// parallel is how many groups rebuild at the same time.
const parallel = 2

type key struct {
	repoID int64
	grp    string
}

type state struct {
	first   time.Time // first mark since the last rebuild started
	gen     int       // invalidates superseded timers
	timer   *time.Timer
	running bool
	again   bool // marked while running
	fails   int  // consecutive failed rebuilds; >0 means a backoff timer is armed
}

// Queue is the rebuild queue and its worker.
type Queue struct {
	db    *db.DB
	repos *repos.Service
	set   *settings.Service
	log   *slog.Logger
	spawn func(name string, fn func(ctx context.Context))
	runOf func(format string) Run

	clockMu sync.Mutex
	clock   int64 // last issued mark (monotonic, unix nanoseconds)

	mu     sync.Mutex
	ctx    context.Context // set by Start
	states map[key]*state
	sem    chan struct{}
}

// New returns the queue. spawn runs a rebuild as a task of the core module, so the server waits
// for it before the database closes. runOf returns the Rebuild.Run of a format (nil: none).
func New(d *db.DB, rs *repos.Service, set *settings.Service, log *slog.Logger,
	spawn func(name string, fn func(ctx context.Context)), runOf func(format string) Run) *Queue {
	return &Queue{db: d, repos: rs, set: set, log: log, spawn: spawn, runOf: runOf,
		states: map[key]*state{}, sem: make(chan struct{}, parallel)}
}

// tick returns a strictly increasing timestamp for marks and rebuild starts. Callers take it while
// they hold the single SQLite writer, so a rebuild start is greater than the mark of every
// committed change and smaller than every later one.
func (q *Queue) tick() int64 {
	q.clockMu.Lock()
	defer q.clockMu.Unlock()
	n := time.Now().UnixNano()
	if n <= q.clock {
		n = q.clock + 1
	}
	q.clock = n
	return n
}

// MarkTx marks unit of group grp of repo repoID for a rebuild, in tx. It marks nothing while the
// repo is being deleted.
func (q *Queue) MarkTx(ctx context.Context, tx *sql.Tx, repoID int64, grp, unit string) error {
	op, err := dbq.New(tx).GetRepoOp(ctx, repoID)
	switch {
	case err == nil && op.Type == ops.TypeDelete:
		return nil
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return err
	}
	if err := dbq.New(tx).MarkRebuild(ctx, dbq.MarkRebuildParams{RepoID: repoID, Grp: grp, Unit: unit, MarkedAt: q.tick()}); err != nil {
		return err
	}
	// The rebuild reads the units in a write transaction of its own, so it runs after tx
	// commits; a rolled back mark leaves nothing to build.
	q.notify(key{repoID, grp})
	return nil
}

// Pending returns how many units of repo wait for a rebuild, the ones being rebuilt included.
func (q *Queue) Pending(ctx context.Context, repoID int64) (int, error) {
	n, err := dbq.New(q.db.R).CountRebuilds(ctx, repoID)
	return int(n), err
}

// Start picks up every pending group. ctx is the core module's context: the rebuilds stop when
// it ends.
func (q *Queue) Start(ctx context.Context) error {
	r := dbq.New(q.db.R)
	maxMark, err := r.MaxRebuildMark(ctx)
	if err != nil {
		return fmt.Errorf("rebuild: load the queue: %w", err)
	}
	q.clockMu.Lock()
	q.clock = max(q.clock, maxMark)
	q.clockMu.Unlock()
	groups, err := r.ListRebuildGroups(ctx)
	if err != nil {
		return fmt.Errorf("rebuild: load the queue: %w", err)
	}
	q.mu.Lock()
	q.ctx = ctx
	q.mu.Unlock()
	for _, g := range groups {
		q.notify(key{g.RepoID, g.Grp})
	}
	return nil
}

// Stop waits for ctx to end and stops every debounce timer (the job's Run).
func (q *Queue) Stop(ctx context.Context) {
	<-ctx.Done()
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, st := range q.states {
		if st.timer != nil {
			st.timer.Stop()
		}
	}
}

// notify records a mark of k. Before Start it does nothing: Start picks up every durable row.
func (q *Queue) notify(k key) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.ctx == nil || q.ctx.Err() != nil {
		return
	}
	st := q.states[k]
	if st == nil {
		st = &state{}
		q.states[k] = st
	}
	if st.running {
		st.again = true
		if st.first.IsZero() {
			st.first = time.Now() // the max delay counts from this mark, not the rebuild's end
		}
		return
	}
	if st.fails > 0 {
		return // the pending backoff retry covers this mark
	}
	q.scheduleLocked(k, st)
}

// scheduleLocked arms the debounce timer: the quiet period, at most first mark + max delay.
func (q *Queue) scheduleLocked(k key, st *state) {
	now := time.Now()
	if st.first.IsZero() {
		st.first = now
	}
	snap := q.set.Current()
	d := settings.IndexDebounce.Get(snap)
	if limit := st.first.Add(settings.IndexMaxDelay.Get(snap)).Sub(now); limit < d {
		d = max(limit, 0)
	}
	q.armLocked(k, st, d)
}

func (q *Queue) armLocked(k key, st *state, d time.Duration) {
	if st.timer != nil {
		st.timer.Stop()
	}
	st.gen++
	gen := st.gen
	ctx := q.ctx
	st.timer = time.AfterFunc(d, func() {
		if ctx.Err() == nil {
			q.spawn("rebuild", func(context.Context) { q.run(k, gen) })
		}
	})
}

func (q *Queue) run(k key, gen int) {
	q.mu.Lock()
	st := q.states[k]
	ctx := q.ctx
	if st == nil || st.gen != gen || st.running || ctx.Err() != nil {
		q.mu.Unlock()
		return
	}
	st.running, st.again, st.timer, st.first = true, false, nil, time.Time{}
	q.mu.Unlock()

	select {
	case q.sem <- struct{}{}:
	case <-ctx.Done():
		return
	}
	began := time.Now()
	remaining, err := q.rebuild(ctx, k)
	<-q.sem

	q.mu.Lock()
	defer q.mu.Unlock()
	st.running = false
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		st.fails++
		backoff := min(time.Second<<min(st.fails, 9), 5*time.Minute)
		q.log.Error("index rebuild failed", "repo_id", k.repoID, "group", k.grp, "err", err, "retry_in", backoff.String())
		q.armLocked(k, st, backoff)
		return
	}
	st.fails = 0
	q.log.Info("index rebuilt", "repo_id", k.repoID, "group", k.grp, "duration_ms", time.Since(began).Milliseconds())
	if remaining || st.again {
		q.scheduleLocked(k, st)
		return
	}
	delete(q.states, k)
}

// rebuild calls the format's Run for the units of k marked before it starts, then deletes those
// units unless they were marked again. remaining reports units left over. A repo that is gone
// or being deleted is skipped (its rows go with the repo).
func (q *Queue) rebuild(ctx context.Context, k key) (remaining bool, err error) {
	var start int64
	var units []string
	// Taking the start under the single writer orders it after every committed mark.
	err = q.db.Tx(ctx, func(tx *sql.Tx) error {
		start = q.tick()
		var err error
		units, err = dbq.New(tx).ListRebuildUnits(ctx, dbq.ListRebuildUnitsParams{RepoID: k.repoID, Grp: k.grp})
		return err
	})
	if err != nil || len(units) == 0 {
		return false, err
	}
	repo, err := q.repos.ByID(ctx, k.repoID)
	if errors.Is(err, repos.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if ops.Deleting(repo.Op) {
		return false, nil
	}
	run := q.runOf(repo.Format)
	if run == nil {
		q.log.Warn("rebuild rows of a format without Rebuild", "repo", repo.Name, "format", repo.Format)
	} else if err := run(ctx, repo, k.grp, units); err != nil {
		return false, err
	}
	err = q.db.Tx(ctx, func(tx *sql.Tx) error {
		w := dbq.New(tx)
		for _, u := range units {
			if err := w.ClearRebuild(ctx, dbq.ClearRebuildParams{RepoID: k.repoID, Grp: k.grp, Unit: u, MarkedAt: start}); err != nil {
				return err
			}
		}
		remaining, err = w.HasRebuild(ctx, dbq.HasRebuildParams{RepoID: k.repoID, Grp: k.grp})
		return err
	})
	return remaining, err
}
