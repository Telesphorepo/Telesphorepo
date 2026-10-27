package edge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/edge/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// The outbox worker.
const (
	// outboxWorkers rows are written at once, each by its own worker. A pointer row of an
	// immutable path writes every region at once, so the S3 calls in flight are up to
	// outboxWorkers × (1 + regions); 32 keeps that within the idle connections that the S3 client
	// keeps per host, and each row also reads its repo and asset from the 8 readers of the database.
	outboxWorkers = 32
	outboxRead    = 512                    // rows per read: above the rows in flight, so each read finds new ones
	deleteBatch   = 128                    // finished rows deleted per transaction
	deleteEvery   = 200 * time.Millisecond // the longest wait of a finished row for its delete
	outboxPoll    = time.Second            // the wait when no row is due
	maxBackoff    = 5 * time.Minute        // the longest wait after failed attempts
	recheckAfter  = time.Minute            // the second application after a put that timed out
	countEvery    = 5 * time.Second        // the count of the status page while it is open
	countIdle     = 30 * time.Second       // the page is open: a status request came within this time
)

// outboxKey names the row of one object.
type outboxKey struct {
	repo int64
	path string
}

// outboxJob is a row for a worker, with the view of the read that found it.
type outboxJob struct {
	v *view
	r dbq.DueOutboxRow
}

// outbox is the state of the job edge.outbox: the rows that a worker writes or whose delete is
// pending (busy), so a read never hands a row out twice, and the rows freed since the last read
// began (recent): that read may hold the state from before their delete or their next try.
type outbox struct {
	m      *mod
	jobs   chan outboxJob        // unbuffered: the reader hands a row out only to a free worker
	done   chan dbq.DueOutboxRow // the rows whose writes all succeeded, for the deleter
	freed  chan struct{}         // a busy row was freed
	mu     sync.Mutex
	busy   map[outboxKey]bool
	recent map[outboxKey]bool
}

// runOutbox is the job edge.outbox: while edge delivery is on, outboxWorkers workers write the due
// rows. One reader hands them out, the access objects and config.json first, and a new marked
// access object before the next pointer, so a revocation never waits behind a large import. A row
// is deleted only after every write of it succeeded, in transactions of up to deleteBatch rows; a
// row whose delete did not commit is written again, which leaves the same state.
func (m *mod) runOutbox(ctx context.Context) {
	o := &outbox{m: m, jobs: make(chan outboxJob), done: make(chan dbq.DueOutboxRow, deleteBatch),
		freed: make(chan struct{}, 1), busy: map[outboxKey]bool{}, recent: map[outboxKey]bool{}}
	var wg sync.WaitGroup
	for range outboxWorkers {
		wg.Go(func() { o.work(ctx) })
	}
	deleted := make(chan struct{})
	go func() {
		o.deleter(ctx)
		close(deleted)
	}()
	go m.runCount(ctx)
	o.read(ctx)
	wg.Wait()
	close(o.done)
	<-deleted
}

// read hands out the due rows until ctx ends.
func (o *outbox) read(ctx context.Context) {
	m := o.m
	for ctx.Err() == nil {
		v := m.cur.Load()
		if v.mode != modeOn {
			m.idle(ctx)
			continue
		}
		// A row freed from here on committed its change before its free, so only this read can
		// hold its earlier state.
		o.mu.Lock()
		clear(o.recent)
		o.mu.Unlock()
		now := nowMS()
		access, err := m.rq.DueAccess(ctx, dbq.DueAccessParams{Now: now, Limit: outboxRead})
		if err != nil {
			o.readFailed(ctx, err)
			continue
		}
		first := make([]dbq.DueOutboxRow, len(access))
		for i, a := range access {
			first[i] = dbq.DueOutboxRow(a)
		}
		sent, _ := o.send(ctx, v, first, nil)
		rows, err := m.rq.DueOutbox(ctx, dbq.DueOutboxParams{Now: now, Limit: outboxRead})
		if err != nil {
			o.readFailed(ctx, err)
			continue
		}
		n, cut := o.send(ctx, v, rows, m.akick)
		if sent+n == 0 && !cut {
			o.wait(ctx) // nothing due, or every due row is busy
		}
	}
}

func (o *outbox) readFailed(ctx context.Context, err error) {
	if ctx.Err() == nil {
		o.m.h.Log.Error("edge outbox read failed", "error", err.Error())
	}
	o.m.idle(ctx)
}

// send hands each row that is neither busy nor recent to a free worker and returns how many it
// handed out. cut: a signal on stop came first, and the rest of rows stays for the next read.
func (o *outbox) send(ctx context.Context, v *view, rows []dbq.DueOutboxRow, stop <-chan struct{}) (int, bool) {
	n := 0
	for _, r := range rows {
		k := outboxKey{r.RepoID, r.Path}
		o.mu.Lock()
		if o.busy[k] || o.recent[k] {
			o.mu.Unlock()
			continue
		}
		o.busy[k] = true
		o.mu.Unlock()
		select {
		case o.jobs <- outboxJob{v, r}:
			n++
			continue
		case <-stop:
		case <-ctx.Done():
		}
		o.mu.Lock()
		delete(o.busy, k) // not handed out: its row is unchanged
		o.mu.Unlock()
		return n, true
	}
	return n, false
}

// wait waits for a wake-up, a freed row, a second, or the end of ctx.
func (o *outbox) wait(ctx context.Context) {
	t := time.NewTimer(outboxPoll)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-o.m.kick:
	case <-o.m.akick:
	case <-o.freed:
	case <-t.C:
	}
}

// free ends the busy state of row r, after its delete or its next try committed (or failed).
func (o *outbox) free(r dbq.DueOutboxRow) {
	k := outboxKey{r.RepoID, r.Path}
	o.mu.Lock()
	delete(o.busy, k)
	o.recent[k] = true
	o.mu.Unlock()
	select {
	case o.freed <- struct{}{}:
	default:
	}
}

// work writes the rows that the reader hands out until ctx ends.
func (o *outbox) work(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-o.jobs:
			if !o.m.process(ctx, j.v, j.r) {
				o.free(j.r)
				continue
			}
			select {
			case o.done <- j.r:
			case <-ctx.Done():
				o.free(j.r)
			}
		}
	}
}

// deleter deletes the finished rows, deleteBatch per transaction and at most deleteEvery after
// the first of a batch, until done is closed. A failed delete leaves the rows, which the workers
// write again.
func (o *outbox) deleter(ctx context.Context) {
	dctx := context.WithoutCancel(ctx) // the last batch is deleted after the end of ctx
	var batch []dbq.DueOutboxRow
	var due <-chan time.Time
	flush := func() {
		due = nil
		if len(batch) == 0 {
			return
		}
		fctx, cancel := context.WithTimeout(dctx, opTimeout)
		err := o.m.h.DB.Tx(fctx, func(tx *sql.Tx) error {
			q := dbq.New(tx)
			for _, r := range batch {
				if _, err := q.DeleteOutbox(fctx, dbq.DeleteOutboxParams{RepoID: r.RepoID, Path: r.Path, Seq: r.Seq}); err != nil {
					return err
				}
			}
			return nil
		})
		cancel()
		if err != nil && ctx.Err() == nil {
			o.m.h.Log.Error("edge outbox delete failed", "error", err.Error())
		}
		for _, r := range batch {
			o.free(r)
		}
		batch = batch[:0]
	}
	for {
		select {
		case r, ok := <-o.done:
			if !ok {
				flush()
				return
			}
			batch = append(batch, r)
			if len(batch) >= deleteBatch {
				flush()
			} else if due == nil {
				due = time.After(deleteEvery)
			}
		case <-due:
			flush()
		}
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

// runCount counts the outbox for the status page: every countEvery while the page is open, and at
// once when it asks for a count after a pause (askCounts). Nothing counts while it is closed.
func (m *mod) runCount(ctx context.Context) {
	t := time.NewTicker(countEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.countKick:
		case <-t.C:
			if time.Since(time.UnixMilli(m.countAsked.Load())) > countIdle {
				continue
			}
		}
		if m.cur.Load().mode != modeOn {
			continue
		}
		c, err := m.rq.CountOutbox(ctx)
		if err != nil {
			if ctx.Err() == nil {
				m.h.Log.Error("edge outbox count failed", "error", err.Error())
			}
			continue
		}
		m.counts.Store(outboxCounts{Total: c.Total, Pointers: c.Pointers, LastError: c.LastError, At: time.Now()})
		next := make(chan struct{})
		close(*m.counted.Swap(&next))
	}
}

// askCounts returns the counts of the outbox for a status request. While the page polls, the
// counter keeps them at most countEvery old. After a pause it asks for a count and waits for it,
// at most a second.
func (m *mod) askCounts(ctx context.Context) outboxCounts {
	m.countAsked.Store(time.Now().UnixMilli())
	c := m.counts.Load().(outboxCounts)
	if time.Since(c.At) < countIdle {
		return c
	}
	counted := *m.counted.Load()
	select {
	case m.countKick <- struct{}{}:
	default:
	}
	t := time.NewTimer(time.Second)
	defer t.Stop()
	select {
	case <-counted:
		return m.counts.Load().(outboxCounts)
	case <-t.C:
	case <-ctx.Done():
	}
	return c
}

// objectKey is the key under root that the row writes.
func objectKey(r dbq.DueOutboxRow) string {
	if r.RepoID == 0 {
		return r.Path
	}
	return pointerKey(r.RepoID, r.Path)
}

// process writes one row and reports whether the row is finished: every write of it succeeded,
// and the deleter may delete it. A key written less than a second ago waits outside the path
// lock. A failed write sets the next try with a backoff.
func (m *mod) process(ctx context.Context, v *view, r dbq.DueOutboxRow) bool {
	if wait := m.sp.wait(objectKey(r)); wait > 0 {
		m.delay(ctx, r, nowMS()+wait.Milliseconds()+1, r.TimedOutAt)
		return false
	}
	var err error
	drop := false
	if r.RepoID == 0 {
		err = m.applyObject(ctx, v, r.Path)
	} else {
		drop, err = m.applyPointer(ctx, v, r)
	}
	if ctx.Err() != nil {
		return false
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
		return false
	}
	if !drop && r.TimedOutAt > 0 {
		// R2 reads do not wait for earlier writes, so a put that timed out may still land: apply
		// the final state once more a minute after it. The row stays marked until then, so no
		// region becomes ready and no walk ends before (AnyTimedOut, RepoTimedOut).
		if again := r.TimedOutAt + recheckAfter.Milliseconds(); now < again {
			m.delay(ctx, r, again, r.TimedOutAt)
			return false
		}
	}
	return true
}

// putUnknown passes on err, the result of a put of the pointer of path in repo outside the outbox
// (a region fill, the reclass walk). A put that timed out has an unknown outcome and may land
// later, over a pending pointer or after a remove: the path gets an outbox row marked timed out,
// which applies the current state in every bucket now and again a minute later. Until that row
// goes, no region becomes ready and no walk of the repo ends.
func (m *mod) putUnknown(ctx context.Context, repoID int64, path string, err error) error {
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	now := nowMS()
	if qerr := m.q.MarkTimedOut(context.WithoutCancel(ctx), dbq.MarkTimedOutParams{RepoID: repoID, Path: path, DueAt: now, TimedOutAt: now}); qerr != nil {
		m.h.Log.Error("edge outbox update failed", "error", qerr.Error())
	}
	m.wake()
	return err
}

func (m *mod) delay(ctx context.Context, r dbq.DueOutboxRow, due, timedOut int64) {
	if _, err := m.q.DelayOutbox(ctx, dbq.DelayOutboxParams{DueAt: due, TimedOutAt: timedOut, RepoID: r.RepoID, Path: r.Path, Seq: r.Seq}); err != nil {
		m.h.Log.Error("edge outbox update failed", "error", err.Error())
	}
}

// applyPointer writes the pointer of a row under the path lock, from the asset row read again
// under the lock: the pointer of the asset (home, and every region for an immutable path) when it
// exists with the row's digest (or the row has none); a delete in every bucket when the asset is
// gone or gets no pointer. A mutable path that a recorded rule list made immutable loses its
// regional pointers (paths.go). drop: the asset has another digest, whose commit has its own row.
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
	rules, perRepo := m.pathRules(repo)
	if !m.immutable(repo, a.Path) {
		if !perRepo {
			return false, nil
		}
		// Mutable now, immutable under rules of an earlier regional pointer: delete it.
		old, err := m.oldImmutable(ctx, repo, a.Path)
		if err != nil || !old {
			return false, err
		}
		if err := parallel(len(v.regions)+1, v.regions, func(b *bucket) error { return m.del(ctx, b, key) }); err != nil {
			return false, err
		}
		return false, m.purgePointers(ctx, v.regions, []string{key}, time.Now())
	}
	if perRepo {
		if err := m.record(ctx, repo.ID, rules); err != nil {
			return false, err
		}
	}
	return false, parallel(len(v.regions)+1, v.regions, func(b *bucket) error { return m.put(ctx, b, key, body) })
}

// deleteEverywhere deletes key in the home bucket and in every region, then purges its cached
// copies.
func (m *mod) deleteEverywhere(ctx context.Context, v *view, key string) error {
	if err := parallel(len(v.regions)+1, v.all(), func(b *bucket) error { return m.del(ctx, b, key) }); err != nil {
		return err
	}
	return m.purgePointers(ctx, v.all(), []string{key}, time.Now())
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
	var gen int64
	if key == configKey {
		gen = m.pointersWriting(ctx, body, v.home)
	}
	err := m.put(ctx, v.home, key, body)
	if key == configKey {
		var c struct {
			AccessTTL int64 `json:"access_ttl"`
		}
		_ = json.Unmarshal(body, &c)
		m.notePublished(ctx, c.AccessTTL, err == nil)
		m.pointersPublished(ctx, gen, err == nil)
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
	select { // the reader reads the access objects again before its next pointer
	case m.akick <- struct{}{}:
	default:
	}
}
