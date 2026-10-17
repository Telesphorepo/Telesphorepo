package ci

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
)

// Build is a build record as the API shows it: the build of a file, a version or a build of a
// version.
type Build struct {
	ID            int64  `json:"id"`
	Provider      string `json:"provider"`
	ProviderTitle string `json:"provider_title"`
	Repository    string `json:"repository"`
	Revision      string `json:"revision"`
	Ref           string `json:"ref"`
	RefName       string `json:"ref_name"`
	RefType       string `json:"ref_type"`
	Run           string `json:"run"`
	Workflow      string `json:"workflow"`
	Actor         string `json:"actor"`
	Links         []Link `json:"links"`
}

// Builds keeps the build records (the kernel table builds).
type Builds struct {
	d   *db.DB
	reg *Registry

	mu    sync.Mutex
	after int64 // the cleanup cursor
}

// NewBuilds returns the build records service.
func NewBuilds(d *db.DB, reg *Registry) *Builds { return &Builds{d: d, reg: reg} }

// Record writes the build record of a verified CI token of issuer with claims in tx, through the
// provider that matches the token, and returns its id. ok is false when no provider gives a
// record (no row is written). at is the time of the build. revision is the commit that the CI job
// names itself (the job env, not the token); the record keeps it only when the token names no
// commit, and only when it is a commit hash (ValidRevision).
func (b *Builds) Record(ctx context.Context, tx *sql.Tx, issuer string, claims map[string]any, revision string, at time.Time) (id int64, ok bool, err error) {
	provider, rec, ok := b.reg.Build(issuer, claims)
	if !ok {
		return 0, false, nil
	}
	if rec.Revision == "" && ValidRevision(revision) {
		rec.Revision = revision
	}
	id, err = b.Upsert(ctx, tx, provider, rec, at)
	return id, err == nil, err
}

// Upsert writes the build record rec of provider, built at time at, in tx and returns its id. A
// record with the same provider and key is updated.
func (b *Builds) Upsert(ctx context.Context, tx *sql.Tx, provider string, rec *BuildRecord, at time.Time) (int64, error) {
	links := rec.Links
	if links == nil {
		links = []Link{}
	}
	js, err := json.Marshal(links)
	if err != nil {
		return 0, err
	}
	return dbq.New(tx).UpsertBuild(ctx, dbq.UpsertBuildParams{
		Provider: provider, Key: rec.Key, Repository: rec.Repository, Revision: rec.Revision, Ref: rec.Ref,
		RefName: rec.RefName, RefType: rec.RefType, Run: rec.Run, Workflow: rec.Workflow, Actor: rec.Actor,
		Links: string(js), CreatedAt: at.Unix(),
	})
}

// cleanupBatch is how many old builds one Cleanup step reads.
const cleanupBatch = 100

// Cleanup deletes up to one batch of the builds created before cutoff that no file references. It
// walks the old builds with a cursor, so builds that files still reference are passed over and
// read again only on the next round.
func (b *Builds) Cleanup(ctx context.Context, cutoff time.Time) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	q := dbq.New(b.d.W)
	ids, err := q.ListOldBuilds(ctx, dbq.ListOldBuildsParams{After: b.after, Cutoff: cutoff.Unix(), Limit: cleanupBatch})
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := q.DeleteUnusedBuild(ctx, id); err != nil {
			return err
		}
		b.after = id
	}
	if len(ids) < cleanupBatch {
		b.after = 0 // the next round starts over
	}
	return nil
}

// Loader reads build records and keeps what it read, so a page of files that one build wrote
// costs one lookup. It is for one request; it is not safe for concurrent use.
type Loader struct {
	q    *dbq.Queries
	reg  *Registry
	seen map[int64]*Build
}

// Loader returns a Loader on the reader pool.
func (b *Builds) Loader() *Loader {
	return &Loader{q: dbq.New(b.d.R), reg: b.reg, seen: map[int64]*Build{}}
}

// Get returns build id (0: none), or nil when it does not exist.
func (l *Loader) Get(ctx context.Context, id int64) (*Build, error) {
	if id == 0 {
		return nil, nil
	}
	if b, ok := l.seen[id]; ok {
		return b, nil
	}
	row, err := l.q.GetBuild(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		l.seen[id] = nil
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := &Build{ID: row.ID, Provider: row.Provider, ProviderTitle: l.reg.Title(row.Provider), Repository: row.Repository,
		Revision: row.Revision, Ref: row.Ref, RefName: row.RefName, RefType: row.RefType, Run: row.Run,
		Workflow: row.Workflow, Actor: row.Actor, Links: []Link{}}
	if err := json.Unmarshal([]byte(row.Links), &out.Links); err != nil {
		out.Links = []Link{}
	}
	l.seen[id] = out
	return out, nil
}
