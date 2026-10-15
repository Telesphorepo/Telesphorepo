// Package demo writes the demo dataset of "telesphorepo dev". The command creates the users and
// the repos that the format plugins declare (format.DemoData) and hands each repo to its plugin
// with a Run. A plugin generates its files and stores them through the Run's fast writer, or
// through its own upload pipeline. The same repos, users, paths and file contents come out on
// every fresh run; only the times move: they spread over the two years before the day of the run.
package demo

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"math"
	mrand "math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/provenance"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/storage"
)

const (
	// Day is one day in seconds.
	Day = int64(24 * 60 * 60)

	blobWorkers = 64   // concurrent blob writes
	rowBatch    = 5000 // asset rows per transaction
)

// ZipTime is the modification time of every entry in a demo archive, so the bytes never change.
var ZipTime = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

// File is one file of the demo data.
type File struct {
	Path    string
	Data    []byte
	Kind    string // assets.KindArtifact or assets.KindMetadata
	By      int64  // uploader user ID
	At      int64  // created and updated, unix seconds
	Session int64  // the build record (Run.Build) that published it; 0 = none
}

// Run is one creation of the demo dataset.
type Run struct {
	ctx    context.Context
	db     *db.DB
	assets *assets.Service
	// Anchor is 00:00 UTC of the run day; every demo time is before it.
	Anchor int64

	writers map[int64][]int64 // repo ID -> the user IDs that publish to it
	w       *writer
	done    atomic.Int64
	total   atomic.Int64

	sessMu      sync.Mutex
	nextSession int64
	sessions    []build // build records not written yet; the next row batch writes them first
}

// NewRun starts a run on a fresh database; Close finishes it.
func NewRun(ctx context.Context, d *db.DB, as *assets.Service, now time.Time) (*Run, error) {
	now = now.UTC()
	r := &Run{
		ctx: ctx, db: d, assets: as,
		Anchor:  time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).Unix(),
		writers: map[int64][]int64{},
	}
	var last sql.NullInt64
	if err := d.R.QueryRowContext(ctx, `SELECT max(id) FROM publish_sessions`).Scan(&last); err != nil {
		return nil, fmt.Errorf("demo: read publish sessions: %w", err)
	}
	r.nextSession = last.Int64 + 1
	r.w = newWriter(r)
	return r, nil
}

// Context is the context of the run.
func (r *Run) Context() context.Context { return r.ctx }

// Assets is the asset service, for a plugin that stores through its own upload pipeline.
func (r *Run) Assets() *assets.Service { return r.assets }

// DB is the database, for a plugin that dates the files its upload pipeline stored.
func (r *Run) DB() *db.DB { return r.db }

// SetWriters sets the users who publish to repo: admins and users with write permission.
func (r *Run) SetWriters(repoID int64, users []int64) { r.writers[repoID] = users }

// Uploader picks who uploaded a file of repo: usually owner (an index into the repo's writers),
// sometimes another writer. 0 when the repo has no writers.
func (r *Run) Uploader(rng *mrand.Rand, repo *repos.Repo, owner int) int64 {
	ws := r.writers[repo.ID]
	if len(ws) == 0 {
		return 0
	}
	if rng.IntN(100) < 85 {
		return ws[owner%len(ws)]
	}
	return ws[rng.IntN(len(ws))]
}

// Expect adds n files to the total that the progress report shows.
func (r *Run) Expect(n int) { r.total.Add(int64(n)) }

// Stored counts n files that a plugin stored through its own pipeline.
func (r *Run) Stored(n int) { r.done.Add(int64(n)) }

// Progress returns the files stored and the files expected so far.
func (r *Run) Progress() (done, total int64) { return r.done.Load(), r.total.Load() }

// Add stores f in repo through the fast writer: the blob through a pool of workers, the asset
// row in a batched transaction.
func (r *Run) Add(repo *repos.Repo, f File) error { return r.w.add(repo, f) }

// Close writes the last rows and waits for the blob workers.
func (r *Run) Close() error { return r.w.close() }

type build struct {
	id     int64
	repoID int64
	rec    provenance.Record
	at     int64
}

// Build returns the ID of a new build record: an ended CI publish session of repo at time at
// with the GitHub Actions provenance rec. The files of that build name it in File.Session. The
// record is written with the next batch of rows, before them.
func (r *Run) Build(repo *repos.Repo, rec provenance.Record, at int64) int64 {
	r.sessMu.Lock()
	defer r.sessMu.Unlock()
	id := r.nextSession
	r.nextSession++
	r.sessions = append(r.sessions, build{id, repo.ID, rec, at})
	return id
}

func (r *Run) takeBuilds() []build {
	r.sessMu.Lock()
	defer r.sessMu.Unlock()
	b := r.sessions
	r.sessions = nil
	return b
}

// insertBuilds writes build records in tx.
func insertBuilds(ctx context.Context, tx *sql.Tx, builds []build) error {
	if len(builds) == 0 {
		return nil
	}
	st, err := tx.PrepareContext(ctx, `INSERT INTO publish_sessions (id, public_id, secret_hash, repo_id, via, subject,
  created_at, expires_at, closed_at, ci_server, ci_repository, ci_sha, ci_ref, ci_run_id, ci_run_attempt, ci_workflow,
  ci_workflow_ref, ci_actor, kept)
VALUES (?, ?, ?, ?, 'oidc', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)`)
	if err != nil {
		return err
	}
	defer st.Close()
	for _, b := range builds {
		pid := make([]byte, 6)
		secret := make([]byte, 32)
		rand.Read(pid) // never fails (Go 1.24+)
		rand.Read(secret)
		hash := sha256.Sum256(secret)
		subject := "repo:" + b.rec.Repository + ":ref:" + b.rec.Ref
		if _, err := st.ExecContext(ctx, b.id, hex.EncodeToString(pid), hash[:], b.repoID, subject,
			b.at-120, b.at+780, b.at+5, b.rec.Server, b.rec.Repository, b.rec.SHA, b.rec.Ref, b.rec.RunID,
			b.rec.RunAttempt, b.rec.Workflow, b.rec.WorkflowRef, b.rec.Actor); err != nil {
			return fmt.Errorf("insert build record %d: %w", b.id, err)
		}
	}
	return nil
}

// writer stores files: blobs through a bounded pool of workers, asset rows in batched
// transactions (the assets triggers keep the repo counters and the search index).
type writer struct {
	run    *Run
	ctx    context.Context
	cancel context.CancelCauseFunc
	st     storage.Store
	fast   interface{ WriteUnsynced(string, []byte) error } // nil when the store has no fast path
	jobs   chan job
	wg     sync.WaitGroup
	mu     sync.Mutex // guards rows: plugins may add from several goroutines
	rows   []row
	// batches carries full row batches to one database goroutine, so generating files, writing
	// blobs and inserting rows overlap instead of the generator waiting for each transaction.
	batches chan batch
	dbDone  chan struct{}
}

type job struct {
	key, contentType string
	data             []byte
}

type row struct {
	repoID int64
	f      File
	sum    string
	ct     string
}

type batch struct {
	builds []build
	rows   []row
}

func newWriter(r *Run) *writer {
	ctx, cancel := context.WithCancelCause(r.ctx)
	st := r.assets.Store()
	w := &writer{run: r, ctx: ctx, cancel: cancel, st: st, jobs: make(chan job, 4*blobWorkers)}
	// The filesystem store has a fast path for throwaway data: no fsync and no rename per file,
	// which is most of the cost of 170,000 small files (on Windows above all).
	if l, ok := st.(*storage.Live); ok {
		st = l.Active()
	}
	if f, ok := st.(interface{ WriteUnsynced(string, []byte) error }); ok {
		w.fast = f
	}
	w.batches = make(chan batch, 2)
	w.dbDone = make(chan struct{})
	go func() {
		defer close(w.dbDone)
		for b := range w.batches {
			if w.ctx.Err() != nil {
				continue // drain after a failure
			}
			if err := w.insert(b); err != nil {
				w.cancel(err)
			}
		}
	}()
	for range blobWorkers {
		w.wg.Add(1)
		go func() {
			defer w.wg.Done()
			for j := range w.jobs {
				if w.ctx.Err() != nil {
					continue // drain after a failure
				}
				var err error
				if w.fast != nil {
					err = w.fast.WriteUnsynced(j.key, j.data)
				} else {
					err = w.st.Put(w.ctx, j.key, bytes.NewReader(j.data), int64(len(j.data)), j.contentType)
				}
				if err != nil {
					w.cancel(fmt.Errorf("store %s: %w", j.key, err))
					continue
				}
				r.done.Add(1)
			}
		}()
	}
	return w
}

func (w *writer) add(repo *repos.Repo, f File) error {
	if w.ctx.Err() != nil {
		return context.Cause(w.ctx)
	}
	ct := assets.ContentType(f.Path)
	select {
	case w.jobs <- job{assets.Key(repo.Name, f.Path), ct, f.Data}:
	case <-w.ctx.Done():
		return context.Cause(w.ctx)
	}
	sum := sha256.Sum256(f.Data)
	w.mu.Lock()
	w.rows = append(w.rows, row{repo.ID, f, hex.EncodeToString(sum[:]), ct})
	full := len(w.rows) >= rowBatch
	var rows []row
	if full {
		rows, w.rows = w.rows, make([]row, 0, rowBatch)
	}
	w.mu.Unlock()
	if full {
		return w.send(rows)
	}
	return nil
}

// send hands rows, after the build records not written yet, to the database goroutine.
func (w *writer) send(rows []row) error {
	b := batch{builds: w.run.takeBuilds(), rows: rows}
	if len(b.builds) == 0 && len(b.rows) == 0 {
		return nil
	}
	select {
	case w.batches <- b:
		return nil
	case <-w.ctx.Done():
		return context.Cause(w.ctx)
	}
}

// insert writes one batch in one transaction.
func (w *writer) insert(b batch) error {
	return w.run.db.Tx(w.ctx, func(tx *sql.Tx) error {
		if err := insertBuilds(w.ctx, tx, b.builds); err != nil {
			return err
		}
		q := dbq.New(tx)
		for _, r := range b.rows {
			if _, err := q.UpsertAsset(w.ctx, dbq.UpsertAssetParams{
				RepoID: r.repoID, Path: r.f.Path, Size: int64(len(r.f.Data)), Sha256: r.sum,
				ContentType: r.ct, Kind: r.f.Kind, UploadedBy: sql.NullInt64{Int64: r.f.By, Valid: r.f.By != 0},
				CreatedAt: r.f.At, UpdatedAt: r.f.At,
				PublishSessionID: sql.NullInt64{Int64: r.f.Session, Valid: r.f.Session != 0},
			}); err != nil {
				return fmt.Errorf("insert asset %s: %w", r.f.Path, err)
			}
		}
		return nil
	})
}

// close writes the last rows and waits for the blob workers.
func (w *writer) close() error {
	w.mu.Lock()
	rows := w.rows
	w.rows = nil
	w.mu.Unlock()
	err := w.send(rows)
	close(w.batches)
	<-w.dbDone
	close(w.jobs)
	w.wg.Wait()
	if w.ctx.Err() != nil {
		err = context.Cause(w.ctx)
	}
	w.cancel(nil)
	return err
}

// Rand is a deterministic generator for stream (seed, n).
func Rand(seed, n uint64) *mrand.Rand { return mrand.New(mrand.NewPCG(seed, n)) }

// Pick returns a random element of list.
func Pick[T any](rng *mrand.Rand, list []T) T { return list[rng.IntN(len(list))] }

// Times returns n ascending offsets in seconds before the anchor day: the newest is up to about
// two years old (usually recent), and earlier ones lie 3 to 50 days apart, all within two years.
func Times(rng *mrand.Rand, n int) []int64 {
	newest := int64(math.Pow(rng.Float64(), 2.5)*700)*Day + rng.Int64N(Day) + 60
	gap := (3 + rng.Int64N(48)) * Day
	oldest := min(newest+int64(n-1)*gap, 729*Day)
	out := make([]int64, n)
	for i := range n {
		if n == 1 {
			out[i] = newest
			continue
		}
		// Offsets fall from oldest (i=0) to newest (i=n-1).
		step := (oldest - newest) / int64(n-1)
		out[i] = oldest - int64(i)*step
		if i > 0 && i < n-1 && step > 2 {
			out[i] -= rng.Int64N(step / 2)
		}
	}
	return out
}

// Hex returns n random lowercase hex digits from rng (a demo commit SHA, for example).
func Hex(rng *mrand.Rand, n int) string {
	const digits = "0123456789abcdef"
	b := make([]byte, n)
	for i := range b {
		b[i] = digits[rng.IntN(16)]
	}
	return string(b)
}
