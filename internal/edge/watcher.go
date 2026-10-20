package edge

import (
	"context"
	"database/sql"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/edge/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// watcher is the pipeline watcher of the module (assets.Watcher). Before a replacement or a
// remove it makes the old pointers unusable in every bucket, under the path locks, after a dirty
// outbox row in its own transaction. In the commit and remove transactions it queues the final
// state. A failed pre-step fails the commit or the remove batch.
type watcher struct{ m *mod }

// edgeOf returns the edge spec of the format of repo, nil when the format has none.
func (m *mod) edgeOf(repo *repos.Repo) *format.EdgeSpec {
	s := m.h.Formats.Get(repo.Format)
	if s == nil {
		return nil
	}
	return s.Edge
}

// eligible reports whether path of repo gets a pointer: every file of a hosted repo, and a file
// of a proxy whose class is not changing (the server checks a changing file again, so the edge
// never serves it). A group has no files of its own.
func (m *mod) eligible(repo *repos.Repo, path string) bool {
	switch repo.Kind {
	case repos.KindHosted:
		return true
	case repos.KindProxy:
		hk := m.h.Assets.Hooks(repo.Format)
		if hk.Classify == nil {
			return false
		}
		remote, ok := m.h.Repos.Proxy(repo.ID)
		if !ok {
			return false
		}
		c, ok := hk.Classify(remote, repopath.Path(path))
		return ok && !c.Changing
	}
	return false
}

func nowMS() int64 { return time.Now().UnixMilli() }

// dirty writes an outbox row with an empty digest (apply the current state) for each path of
// repo, in its own transaction.
func (m *mod) dirty(ctx context.Context, repoID int64, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	now := nowMS()
	return m.h.DB.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		for _, p := range paths {
			if err := q.UpsertOutbox(ctx, dbq.UpsertOutboxParams{RepoID: repoID, Path: p, DueAt: now}); err != nil {
				return err
			}
		}
		return nil
	})
}

func (w watcher) Replacing(ctx context.Context, repo *repos.Repo, prev []*assets.Asset) error {
	m := w.m
	v := m.cur.Load()
	es := m.edgeOf(repo)
	if !v.writing() || es == nil {
		return nil
	}
	var paths, pending, gone []string
	for _, a := range prev {
		if !m.eligible(repo, a.Path) {
			continue
		}
		paths = append(paths, a.Path)
		if es.IsImmutable(a.Path) {
			pending = append(pending, a.Path)
		} else {
			gone = append(gone, pointerKey(repo.ID, a.Path))
		}
	}
	if len(paths) == 0 {
		return nil
	}
	if v.mode == modeOn {
		if err := m.dirty(ctx, repo.ID, paths); err != nil {
			return err
		}
	}
	type job struct {
		b *bucket
		p string
	}
	var jobs []job
	for _, b := range v.pre() {
		for _, p := range pending {
			jobs = append(jobs, job{b, p})
		}
	}
	if err := parallel(8, jobs, func(j job) error {
		return retry429(ctx, func() error { return m.put(ctx, j.b, pointerKey(repo.ID, j.p), pendingOf(repo.ID, j.p)) })
	}); err != nil {
		return err
	}
	return retry429(ctx, func() error { return m.del(ctx, v.home, gone...) })
}

// Creating puts {pending} in every bucket for a new immutable path of a repo that is a member of a
// group. The Worker skips a hosted member without a pointer, so until the outbox writes the new
// pointer a group would serve the copy of a later member; a pending pointer sends the request to
// the server instead. A repo in no group needs nothing: no pointer means the server answers.
func (w watcher) Creating(ctx context.Context, repo *repos.Repo, paths []string) error {
	m := w.m
	v := m.cur.Load()
	es := m.edgeOf(repo)
	if !v.writing() || es == nil || len(m.h.Repos.GroupsOf(repo.ID)) == 0 {
		return nil
	}
	var pending []string
	for _, p := range paths {
		if es.IsImmutable(p) && m.eligible(repo, p) {
			pending = append(pending, p)
		}
	}
	if len(pending) == 0 {
		return nil
	}
	if v.mode == modeOn {
		if err := m.dirty(ctx, repo.ID, pending); err != nil {
			return err
		}
	}
	type job struct {
		b *bucket
		p string
	}
	var jobs []job
	for _, b := range v.pre() {
		for _, p := range pending {
			jobs = append(jobs, job{b, p})
		}
	}
	return parallel(8, jobs, func(j job) error {
		return retry429(ctx, func() error { return m.put(ctx, j.b, pointerKey(repo.ID, j.p), pendingOf(repo.ID, j.p)) })
	})
}

func (w watcher) Committed(ctx context.Context, tx *sql.Tx, repo *repos.Repo, stored []*assets.Asset) error {
	m := w.m
	if m.cur.Load().mode != modeOn || m.edgeOf(repo) == nil {
		return nil
	}
	q := dbq.New(tx)
	now := nowMS()
	n := 0
	for _, a := range stored {
		if !m.eligible(repo, a.Path) {
			continue
		}
		if err := q.UpsertOutbox(ctx, dbq.UpsertOutboxParams{RepoID: repo.ID, Path: a.Path, Sha256: a.SHA256, DueAt: now}); err != nil {
			return err
		}
		n++
	}
	if n > 0 {
		time.AfterFunc(50*time.Millisecond, m.wake) // after the commit
	}
	return nil
}

func (w watcher) Removing(ctx context.Context, repo *repos.Repo, paths []string) error {
	m := w.m
	v := m.cur.Load()
	es := m.edgeOf(repo)
	if !v.writing() || es == nil || len(paths) == 0 {
		return nil
	}
	if v.mode == modeOn {
		if err := m.dirty(ctx, repo.ID, paths); err != nil {
			return err
		}
	}
	all := make([]string, len(paths))
	var immutable []string
	for i, p := range paths {
		all[i] = pointerKey(repo.ID, p)
		if es.IsImmutable(p) {
			immutable = append(immutable, all[i])
		}
	}
	return parallel(8, v.pre(), func(b *bucket) error {
		keys := all
		if b != v.home {
			keys = immutable
		}
		return retry429(ctx, func() error { return m.del(ctx, b, keys...) })
	})
}

func (w watcher) Removed(ctx context.Context, tx *sql.Tx, repo *repos.Repo, removed []*assets.Asset) error {
	m := w.m
	if m.cur.Load().mode != modeOn || m.edgeOf(repo) == nil {
		return nil
	}
	q := dbq.New(tx)
	now := nowMS()
	for _, a := range removed {
		if err := q.UpsertOutbox(ctx, dbq.UpsertOutboxParams{RepoID: repo.ID, Path: a.Path, DueAt: now}); err != nil {
			return err
		}
	}
	time.AfterFunc(50*time.Millisecond, m.wake)
	return nil
}

func (w watcher) Touched(ctx context.Context, repo *repos.Repo, paths []string) error {
	m := w.m
	if m.cur.Load().mode != modeOn || m.edgeOf(repo) == nil {
		return nil
	}
	var eligible []string
	for _, p := range paths {
		if m.eligible(repo, p) {
			eligible = append(eligible, p)
		}
	}
	if err := m.dirty(ctx, repo.ID, eligible); err != nil {
		return err
	}
	m.wake()
	return nil
}
