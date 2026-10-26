package assets

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/blobs"
	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// Source is where a write comes from.
type Source string

const (
	SourcePut       Source = "put"       // a format route or a resumable upload
	SourceUI        Source = "ui"        // the web UI upload
	SourceImport    Source = "import"    // a Nexus sync
	SourceProxy     Source = "proxy"     // a file a proxy fetched from its remote
	SourceGenerated Source = "generated" // a file the server builds: checksums, metadata, indexes
	SourceDemo      Source = "demo"      // the dev demo data
)

// recorded reports whether a write of s records a file.upload event: the uploads and imports,
// not the files the server fetches, builds or makes up.
func (s Source) recorded() bool { return s == SourcePut || s == SourceUI || s == SourceImport }

// Origin is who and what writes a file.
type Origin struct {
	Source Source
	UserID int64 // 0: none
	// BuildID is the CI build record of the publish session that writes the file; 0: none.
	BuildID int64
	Created time.Time // zero: now (an import gives the source's dates)
	Updated time.Time
	Remote  *Remote // a proxy fetch: the remote's answer
}

// Remote is the answer of a proxy's remote for a fetched file.
type Remote struct {
	ETag      string
	CheckedAt time.Time
}

// Body is the content of a write: bytes with their hashes, or the digest of a blob that exists
// (a mount from another repo, or a second path of the same content).
type Body struct {
	Spool *Spooled
	Blob  string // SHA-256 hex
}

// digest is the SHA-256 of the body.
func (b Body) digest() string {
	if b.Spool != nil {
		return b.Spool.SHA256
	}
	return b.Blob
}

// Write is one file to store.
type Write struct {
	Repo        *repos.Repo
	Path        repopath.Path
	Body        Body
	ContentType string // "": the format's Describe
	Kind        string // "": the format's Describe
	Origin      Origin
	Locks       []string // extra lock keys in the repo, e.g. a Debian package identity
	Meta        any      // format data for Admit and OnCommitted, e.g. a parsed .deb control
	// Precondition is checked against the file at the path before Admit; nil: none.
	Precondition *Precondition
}

// Status is what a write did.
type Status string

const (
	Created   Status = "created"
	Replaced  Status = "replaced"
	Unchanged Status = "unchanged"
)

// Result is the outcome of one write.
type Result struct {
	Write  *Write
	Asset  *Asset
	Status Status
	Prev   *Asset // the asset that a Replaced write replaced
}

// commitRetries is how often a failed commit transaction is tried again.
const commitRetries = 3

// Commit stores one file (see CommitAll).
func (p *Pipeline) Commit(ctx context.Context, w Write) (*Result, error) {
	rs, err := p.CommitAll(ctx, []Write{w})
	if err != nil {
		return nil, err
	}
	return rs[0], nil
}

// CommitAll stores the files of ws, all in one repo, in one transaction:
//
//  1. It takes the path locks and the extra Locks (sorted), and reads each existing asset.
//  2. Describe fills an empty ContentType or Kind; a path that Describe refuses is a 400.
//  3. The Precondition of each write is checked against its existing asset: a false one fails
//     the whole commit with a *PreconditionError (412).
//     Admit decides each write: Store, Keep or a *Refusal. The watchers get Replacing with the
//     assets that the stored writes replace, and Creating with the paths that they create; an
//     error fails the commit.
//  4. It takes the digest locks of the writes that store, after the path locks, and makes sure
//     each blob exists (blobs.Store.EnsureAll); a Body.Blob that no blob has is a 404.
//  5. One transaction writes the asset rows, runs the format's OnCommitted, records one
//     file.upload event per stored upload or import and calls the watchers' Committed. A failed
//     transaction is tried again.
//  6. It releases the digest locks, then the path locks, then calls AfterCommit.
//
// The writes run detached from ctx's cancel (a client that goes away does not leave a blob
// without its row) and stop only when the server shuts down. A repo that is being deleted
// refuses every write with 409.
func (p *Pipeline) CommitAll(ctx context.Context, ws []Write) ([]*Result, error) {
	if len(ws) == 0 {
		return nil, nil
	}
	repo := ws[0].Repo
	keys := make([]string, 0, len(ws))
	seen := make(map[repopath.Path]bool, len(ws))
	for i := range ws {
		w := &ws[i]
		if w.Repo.ID != repo.ID {
			return nil, errors.New("assets: a commit writes one repo")
		}
		if w.Body.Spool == nil && w.Body.Blob == "" {
			return nil, errors.New("assets: a write without a body")
		}
		if seen[w.Path] {
			return nil, Refuse(http.StatusBadRequest, "two files go to %s", w.Path)
		}
		seen[w.Path] = true
		keys = append(keys, lockKey(repo.ID, string(w.Path)))
		for _, l := range w.Locks {
			keys = append(keys, lockKey(repo.ID, l))
		}
	}
	ctx, cancel := p.detached(ctx)
	defer cancel()

	release := p.lock(keys)
	results, stored, err := p.admit(ctx, repo, ws)
	if err != nil {
		release()
		return nil, err
	}
	if err := p.replacing(ctx, repo, stored); err != nil {
		release()
		return nil, err
	}
	releaseBlobs, blobsBy, err := p.ensure(ctx, stored)
	if err != nil {
		release()
		return nil, err
	}
	hooks := p.Hooks(repo.Format)
	var written []*Asset
	var ref *Refusal
	for attempt := 0; ; attempt++ {
		err = p.db.Tx(ctx, func(tx *sql.Tx) error {
			var err error
			written, err = p.write(ctx, tx, hooks, results, blobsBy)
			return err
		})
		if err == nil || errors.As(err, &ref) || attempt+1 >= commitRetries || ctx.Err() != nil {
			break
		}
		time.Sleep(time.Duration(attempt+1) * 100 * time.Millisecond)
	}
	releaseBlobs()
	release()
	if err != nil {
		if !errors.As(err, &ref) {
			slog.Error("asset commit failed", "repo", repo.Name, "files", len(ws), "err", err)
		}
		return nil, err
	}
	for i, a := range written {
		if a != nil {
			if results[i].Status == Replaced {
				results[i].Prev = results[i].Asset
			}
			results[i].Asset = a
		}
	}
	if hooks.AfterCommit != nil {
		hooks.AfterCommit(ctx, repo, results)
	}
	return results, nil
}

// admit runs steps 1 to 3 of CommitAll under the path locks. It returns a result per write
// (Asset is the existing one until the write) and the writes that store.
func (p *Pipeline) admit(ctx context.Context, repo *repos.Repo, ws []Write) ([]*Result, []*Result, error) {
	q := dbq.New(p.db.R)
	if del, err := deleting(ctx, q, repo.ID); err != nil {
		return nil, nil, err
	} else if del {
		return nil, nil, Refuse(http.StatusConflict, "the repository is being deleted")
	}
	hooks := p.Hooks(repo.Format)
	results := make([]*Result, len(ws))
	var stored []*Result
	for i := range ws {
		w := &ws[i]
		if w.ContentType == "" || w.Kind == "" {
			d := Describe{ContentType: FallbackContentType, Kind: KindArtifact}
			if hooks.Describe != nil {
				var ok bool
				if d, ok = hooks.Describe(w.Path); !ok {
					return nil, nil, Refuse(http.StatusBadRequest, "%s is not a path of a %s repository", w.Path, repo.Format)
				}
			}
			if w.ContentType == "" {
				w.ContentType = d.ContentType
			}
			if w.Kind == "" {
				w.Kind = d.Kind
			}
		}
		existing, err := get(ctx, q, repo.ID, string(w.Path))
		if err != nil && !errors.Is(err, ErrNotFound) {
			return nil, nil, err
		}
		if err := w.Precondition.check(w.Path, existing); err != nil {
			return nil, nil, err
		}
		decision := Store
		switch {
		case hooks.Admit != nil:
			if decision, err = hooks.Admit(ctx, w, existing); err != nil {
				return nil, nil, err
			}
		case existing != nil && existing.SHA256 == w.Body.digest():
			decision = Keep
		}
		r := &Result{Write: w, Asset: existing, Status: Unchanged}
		if decision == Store {
			r.Status = Created
			if existing != nil {
				r.Status = Replaced
			}
			stored = append(stored, r)
		}
		results[i] = r
	}
	return results, stored, nil
}

// replacing calls Watcher.Replacing with the existing assets that the stored writes replace, and
// Watcher.Creating with the paths that they create.
func (p *Pipeline) replacing(ctx context.Context, repo *repos.Repo, stored []*Result) error {
	if len(p.watch) == 0 {
		return nil
	}
	var prev []*Asset
	var created []string
	for _, r := range stored {
		if r.Status == Replaced {
			prev = append(prev, r.Asset)
		} else {
			created = append(created, string(r.Write.Path))
		}
	}
	for _, w := range p.watch {
		if len(prev) > 0 {
			if err := w.Replacing(ctx, repo, prev); err != nil {
				return err
			}
		}
		if len(created) > 0 {
			if err := w.Creating(ctx, repo, created); err != nil {
				return err
			}
		}
	}
	return nil
}

// ensure takes the digest locks of the stored writes and makes sure their blobs exist. It
// returns the release of the digest locks and the blobs by digest.
func (p *Pipeline) ensure(ctx context.Context, stored []*Result) (func(), map[string]*blobs.Blob, error) {
	digests := make([]string, 0, len(stored))
	var spools []*Spooled
	fast := false
	for _, r := range stored {
		digests = append(digests, r.Write.Body.digest())
		if sp := r.Write.Body.Spool; sp != nil {
			spools = append(spools, sp)
		}
		fast = fast || r.Write.Origin.Source == SourceDemo
	}
	release := p.blobs.Lock(digests)
	by, err := p.blobs.EnsureAll(ctx, spools, fast)
	if err != nil {
		release()
		return nil, nil, err
	}
	for _, r := range stored {
		d := r.Write.Body.digest()
		if by[d] != nil {
			continue
		}
		b, err := p.blobs.Get(ctx, d)
		if errors.Is(err, blobs.ErrUnknown) {
			release()
			return nil, nil, &Refusal{Status: http.StatusNotFound, Message: "blob unknown: sha256:" + d}
		}
		if err != nil {
			release()
			return nil, nil, err
		}
		by[d] = b
	}
	return release, by, nil
}

// write is the transaction of CommitAll. It returns the written asset of each result (nil for
// one that is unchanged); the results change only after the commit, so a retry starts over.
func (p *Pipeline) write(ctx context.Context, tx *sql.Tx, hooks *Hooks, results []*Result, blobsBy map[string]*blobs.Blob) ([]*Asset, error) {
	q := dbq.New(tx)
	written := make([]*Asset, len(results))
	for i, r := range results {
		w := r.Write
		o := w.Origin
		if r.Status == Unchanged {
			if o.Remote != nil && r.Asset != nil {
				if err := q.SetAssetChecked(ctx, dbq.SetAssetCheckedParams{CheckedAt: o.Remote.CheckedAt.Unix(),
					RemoteEtag: o.Remote.ETag, RepoID: w.Repo.ID, Path: string(w.Path)}); err != nil {
					return nil, err
				}
			}
			continue
		}
		b := blobsBy[w.Body.digest()]
		if b == nil {
			return nil, fmt.Errorf("assets: no blob for %s", w.Path)
		}
		a, err := upsert(ctx, q, w, b, hooks.unitOf(string(w.Path)))
		if err != nil {
			return nil, err
		}
		if hooks.OnCommitted != nil {
			if err := hooks.OnCommitted(ctx, tx, w, a, r.Asset); err != nil {
				return nil, err
			}
		}
		if o.Source.recorded() && p.audit != nil {
			if err := p.audit.RecordTx(ctx, tx, audit.Event{Action: audit.FileUpload, RepoID: w.Repo.ID, Repo: w.Repo.Name,
				Target: string(w.Path), Detail: map[string]any{"size": a.Size, "sha256": a.SHA256, "source": string(o.Source)}}); err != nil {
				return nil, err
			}
		}
		written[i] = a
	}
	if len(p.watch) > 0 {
		var stored []*Asset
		for _, a := range written {
			if a != nil {
				stored = append(stored, a)
			}
		}
		if len(stored) > 0 {
			repo := results[0].Write.Repo
			for _, wt := range p.watch {
				if err := wt.Committed(ctx, tx, repo, stored); err != nil {
					return nil, err
				}
			}
		}
	}
	return written, nil
}

// upsert writes the asset row of w pointing to blob b. The times are the origin's (an import,
// or a proxy's remote Last-Modified); zero means now. An insert writes created_at from Created
// (else Updated) and updated_at from Updated; a replace writes only updated_at.
func upsert(ctx context.Context, q *dbq.Queries, w *Write, b *blobs.Blob, unit string) (*Asset, error) {
	o := w.Origin
	now := time.Now()
	updated := now
	if !o.Updated.IsZero() {
		updated = o.Updated
	}
	created := updated
	if !o.Created.IsZero() {
		created = o.Created
	}
	var checked int64
	etag := ""
	if o.Remote != nil {
		checked, etag = o.Remote.CheckedAt.Unix(), o.Remote.ETag
	}
	row, err := q.UpsertAsset(ctx, dbq.UpsertAssetParams{
		RepoID:      w.Repo.ID,
		Path:        string(w.Path),
		BlobID:      b.ID,
		Size:        b.Size,
		Sha256:      b.SHA256,
		Md5:         b.MD5,
		Sha1:        b.SHA1,
		ContentType: w.ContentType,
		Kind:        w.Kind,
		UploadedBy:  sql.NullInt64{Int64: o.UserID, Valid: o.UserID != 0},
		CreatedAt:   created.Unix(),
		UpdatedAt:   updated.Unix(),

		BuildID:    sql.NullInt64{Int64: o.BuildID, Valid: o.BuildID != 0},
		CheckedAt:  checked,
		RemoteEtag: etag,
		Unit:       sql.NullString{String: unit, Valid: true},
	})
	if err != nil {
		return nil, err
	}
	a := &Asset{ID: row.ID, RepoID: w.Repo.ID, Path: string(w.Path), BlobID: b.ID, Size: b.Size, SHA256: b.SHA256, MD5: b.MD5, SHA1: b.SHA1,
		ContentType: w.ContentType, Kind: row.Kind, UploadedBy: o.UserID, BuildID: o.BuildID,
		CreatedAt: time.Unix(row.CreatedAt, 0).UTC(), UpdatedAt: time.Unix(row.UpdatedAt, 0).UTC(), RemoteETag: etag}
	if checked > 0 {
		a.CheckedAt = time.Unix(checked, 0).UTC()
	}
	return a, nil
}

// Digest is the SHA-256 of the body.
func (b Body) Digest() string { return b.digest() }
