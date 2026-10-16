package assets

import (
	"context"
	"database/sql"
	"errors"
	"slices"

	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// removeBatch is how many files one remove transaction deletes.
const removeBatch = 500

// Removal is a delete of files of a repo.
type Removal struct {
	Repo     *repos.Repo
	Paths    []repopath.Path
	Prefixes []repopath.Path // folders, each ending in "/"; "" is every file of the repo
	Origin   Origin
	// Progress, when set, gets the number of files removed so far after each batch.
	Progress func(removed int)
}

// Remove deletes the files of rm in batches of removeBatch rows: the path locks in path order,
// then one transaction with the format's OnRemoved (the rows still readable), the row deletes
// and the file.delete events (one per file of Paths, one per folder of Prefixes). Then
// AfterRemove. The follow-up paths of OnRemoved are removed as the next batches. A path that does
// not exist is skipped. Blob objects stay: the blob GC removes a blob that no asset references.
//
// While the repo has an active delete operation, Remove calls no OnRemoved and no AfterRemove
// and records no file.delete event (the repo.delete event counts the files). It runs detached
// from ctx's cancel and returns how many files it deleted.
func (p *Pipeline) Remove(ctx context.Context, rm Removal) (int, error) {
	ctx, cancel := p.detached(ctx)
	defer cancel()
	repo := rm.Repo
	del, err := deleting(ctx, dbq.New(p.db.R), repo.ID)
	if err != nil {
		return 0, err
	}
	hooks := p.Hooks(repo.Format)
	record := rm.Origin.Source != SourceGenerated && !del && p.audit != nil
	total := 0
	var follow []string
	batch := func(paths []string, each bool) (int, error) {
		n, more, err := p.removeBatch(ctx, repo, hooks, del, paths, each && record)
		total += n
		follow = append(follow, more...)
		if rm.Progress != nil && n > 0 {
			rm.Progress(total)
		}
		return n, err
	}

	paths := make([]string, len(rm.Paths))
	for i, x := range rm.Paths {
		paths[i] = string(x)
	}
	paths = sortedUnique(paths)
	for len(paths) > 0 {
		k := min(len(paths), removeBatch)
		if _, err := batch(paths[:k], true); err != nil {
			return total, err
		}
		paths = paths[k:]
	}
	for _, prefix := range rm.Prefixes {
		dir := string(prefix)
		count := 0
		for {
			ps, err := p.firstPaths(ctx, repo.ID, dir, removeBatch)
			if err != nil {
				return total, err
			}
			if len(ps) == 0 {
				break
			}
			n, err := batch(ps, false)
			count += n
			if err != nil {
				return total, err
			}
			if n == 0 {
				break // every path was gone already; the next read sees the same rows
			}
		}
		if record && count > 0 && dir != "" {
			_ = p.audit.Record(ctx, audit.Event{Action: audit.FileDelete, RepoID: repo.ID, Repo: repo.Name, Target: dir,
				Detail: map[string]any{"count": count}})
		}
	}
	// Follow-up paths of OnRemoved: a path removed already is skipped, so the walk ends.
	for rounds := 0; len(follow) > 0 && rounds < 100; rounds++ {
		next := sortedUnique(follow)
		follow = nil
		for len(next) > 0 {
			k := min(len(next), removeBatch)
			if _, err := batch(next[:k], false); err != nil {
				return total, err
			}
			next = next[k:]
		}
	}
	return total, nil
}

// removeBatch deletes the rows of paths (sorted, each once) under their path locks in one
// transaction, and returns how many it deleted and the follow-up paths of OnRemoved. each
// records a file.delete event per deleted file.
func (p *Pipeline) removeBatch(ctx context.Context, repo *repos.Repo, hooks *Hooks, del bool, paths []string, each bool) (int, []string, error) {
	keys := make([]string, len(paths))
	for i, x := range paths {
		keys[i] = lockKey(repo.ID, x)
	}
	release := p.lock(keys)
	var removed []*Asset
	var follow []string
	err := p.db.Tx(ctx, func(tx *sql.Tx) error {
		removed, follow = removed[:0], follow[:0]
		q := dbq.New(tx)
		for _, x := range paths {
			a, err := get(ctx, q, repo.ID, x)
			if errors.Is(err, ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			removed = append(removed, a)
		}
		if len(removed) == 0 {
			return nil
		}
		if !del && hooks.OnRemoved != nil {
			more, err := hooks.OnRemoved(ctx, tx, repo, removed)
			if err != nil {
				return err
			}
			for _, m := range more {
				follow = append(follow, string(m))
			}
		}
		for _, a := range removed {
			if _, err := q.DeleteAsset(ctx, dbq.DeleteAssetParams{RepoID: repo.ID, Path: a.Path}); err != nil {
				return err
			}
			if each {
				if err := p.audit.RecordTx(ctx, tx, audit.Event{Action: audit.FileDelete, RepoID: repo.ID, Repo: repo.Name,
					Target: a.Path, Detail: map[string]any{"count": 1}}); err != nil {
					return err
				}
			}
		}
		return nil
	})
	release()
	if err != nil {
		return 0, nil, err
	}
	if !del && len(removed) > 0 && hooks.AfterRemove != nil {
		hooks.AfterRemove(ctx, repo, removed)
	}
	return len(removed), follow, nil
}

// sortedUnique returns xs sorted, each once.
func sortedUnique(xs []string) []string {
	out := slices.Clone(xs)
	slices.Sort(out)
	return slices.Compact(out)
}
