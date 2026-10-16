// Package demo writes the demo dataset of "telesphorepo-server dev". The command creates the users and
// the repos that the modules declare (module.DemoData) and hands each repo to its module with a
// Run. A module generates its files and stores them through the Run (the asset pipeline with the
// source "demo"), or through its own upload step. The same repos, users, paths and file contents
// come out on every fresh run; only the times move: they spread over the two years before the day
// of the run.
package demo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	mrand "math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/blobs"
	"github.com/Telesphorepo/Telesphorepo/internal/ci"
	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

const (
	// Day is one day in seconds.
	Day = int64(24 * 60 * 60)

	commitBatch = 1000 // files per commit
)

// ZipTime is the modification time of every entry in a demo archive, so the bytes never change.
var ZipTime = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

// File is one file of the demo data.
type File struct {
	Path  string
	Data  []byte
	Kind  string // assets.KindArtifact or assets.KindMetadata
	By    int64  // uploader user ID
	At    int64  // created and updated, unix seconds
	Build int64  // the CI build record (Run.Build) that published it; 0 = none
}

// GitHubActionsIssuer is the issuer of GitHub Actions identity tokens. The demo builds carry
// claims of this issuer.
const GitHubActionsIssuer = "https://token.actions.githubusercontent.com"

// Run is one creation of the demo dataset.
type Run struct {
	ctx    context.Context
	db     *db.DB
	assets *assets.Pipeline
	builds *ci.Builds
	// Anchor is 00:00 UTC of the run day; every demo time is before it.
	Anchor int64

	writers map[int64][]int64 // repo ID -> the user IDs that publish to it
	done    atomic.Int64
	total   atomic.Int64

	mu      sync.Mutex
	pending map[int64]*batch // files not written yet, by repo ID
}

// batch is the files of one repo that the next commit writes.
type batch struct {
	repo  *repos.Repo
	files []File
}

// NewRun starts a run on a fresh database; Close finishes it.
func NewRun(ctx context.Context, d *db.DB, p *assets.Pipeline, b *ci.Builds, now time.Time) *Run {
	now = now.UTC()
	return &Run{
		ctx: ctx, db: d, assets: p, builds: b,
		Anchor:  time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).Unix(),
		writers: map[int64][]int64{},
		pending: map[int64]*batch{},
	}
}

// Context is the context of the run.
func (r *Run) Context() context.Context { return r.ctx }

// Assets is the asset pipeline, for a module that stores through its own upload step.
func (r *Run) Assets() *assets.Pipeline { return r.assets }

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

// Stored counts n files that a module stored through its own upload step.
func (r *Run) Stored(n int) { r.done.Add(int64(n)) }

// Progress returns the files stored and the files expected so far.
func (r *Run) Progress() (done, total int64) { return r.done.Load(), r.total.Load() }

// Origin is the origin of a demo file uploaded by by (or written by the CI build record build) at
// unix time at.
func Origin(by, build, at int64) assets.Origin {
	t := time.Unix(at, 0)
	return assets.Origin{Source: assets.SourceDemo, UserID: by, BuildID: build, Created: t, Updated: t}
}

// Add stores f in repo through the asset pipeline, in batches of commitBatch files.
func (r *Run) Add(repo *repos.Repo, f File) error {
	r.mu.Lock()
	b := r.pending[repo.ID]
	if b == nil {
		b = &batch{repo: repo}
		r.pending[repo.ID] = b
	}
	b.files = append(b.files, f)
	var full *batch
	if len(b.files) >= commitBatch {
		full = b
		delete(r.pending, repo.ID)
	}
	r.mu.Unlock()
	if full != nil {
		return r.commit(full)
	}
	return nil
}

// Close writes the files not written yet.
func (r *Run) Close() error {
	r.mu.Lock()
	rest := r.pending
	r.pending = map[int64]*batch{}
	r.mu.Unlock()
	var errs []error
	for _, b := range rest {
		errs = append(errs, r.commit(b))
	}
	return errors.Join(errs...)
}

// commit writes the files of b in one CommitAll (the demo source writes blobs without the store's
// crash safety, when the store has such a path).
func (r *Run) commit(b *batch) error {
	ws := make([]assets.Write, len(b.files))
	for i, f := range b.files {
		p, err := repopath.Parse(f.Path)
		if err != nil {
			return fmt.Errorf("demo file %s: %w", f.Path, err)
		}
		ws[i] = assets.Write{Repo: b.repo, Path: p, Body: assets.Body{Spool: blobs.Bytes(f.Data)}, Kind: f.Kind,
			Origin: Origin(f.By, f.Build, f.At)}
	}
	if _, err := r.assets.CommitAll(r.ctx, ws); err != nil {
		return fmt.Errorf("demo files of %s: %w", b.repo.Name, err)
	}
	r.done.Add(int64(len(ws)))
	return nil
}

// Build writes the CI build record of a demo build of a GitHub Actions workflow at unix time at:
// claims are the claims of its identity token, read by the CI provider that matches them, as a
// real CI publish does. The files of that build name it in File.Build.
func (r *Run) Build(claims map[string]any, at int64) (int64, error) {
	var id int64
	err := r.db.Tx(r.ctx, func(tx *sql.Tx) error {
		var ok bool
		var err error
		id, ok, err = r.builds.Record(r.ctx, tx, GitHubActionsIssuer, claims, time.Unix(at, 0))
		if err == nil && !ok {
			err = fmt.Errorf("demo: no CI provider reads the claims %v", claims)
		}
		return err
	})
	return id, err
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
