package debian

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	coredbq "github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/debian/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// AssetsDeleted implements format.DeleteObserver: it removes the packages of every deleted pool
// file and marks their (dist, component, architecture) units for a rebuild, so the indexes drop
// the packages. An arch:all package marks every configured architecture. While the whole repo
// is deleted nothing is marked.
func (h *Handler) AssetsDeleted(ctx context.Context, tx *sql.Tx, repo *repos.Repo, paths []string) error {
	cq, q := coredbq.New(tx), dbq.New(tx)
	type unit struct{ dist, comp, arch string }
	var units []unit
	for _, p := range paths {
		if !strings.HasPrefix(p, "pool/") {
			continue
		}
		id, err := cq.GetAssetID(ctx, coredbq.GetAssetIDParams{RepoID: repo.ID, Path: p})
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		rows, err := q.DeletePackagesOfAsset(ctx, id)
		if err != nil {
			return err
		}
		for _, r := range rows {
			archs := []string{r.Architecture}
			if r.Architecture == "all" {
				archs = architectures(repo)
			}
			for _, a := range archs {
				u := unit{r.Distribution, r.Component, a}
				if !slices.Contains(units, u) {
					units = append(units, u)
				}
			}
		}
	}
	if len(units) == 0 || repo.LockedBy == format.RepoDeleting {
		return nil
	}
	mark := h.tick()
	var dists []string
	for _, u := range units {
		if err := q.MarkDirty(ctx, dbq.MarkDirtyParams{RepoID: repo.ID, Distribution: u.dist, Component: u.comp,
			Architecture: u.arch, MarkedAt: mark}); err != nil {
			return err
		}
		if !slices.Contains(dists, u.dist) {
			dists = append(dists, u.dist)
		}
	}
	// The rebuild starts in a write transaction, so it runs after this one commits.
	for _, d := range dists {
		h.idx.notify(distKey{repo.ID, d})
	}
	return nil
}

// byHashBatch is the page size of the by-hash cleanup.
const byHashBatch = 200

// pruneByHash deletes the by-hash files of dir (dists/{dist}/{comp}/binary-{arch}) whose hash
// is not in keep: the current Packages and Packages.gz and the generation they replaced, so a
// client that read the previous Release still finds its files. It runs after a rebuild of the
// unit.
func (h *Handler) pruneByHash(ctx context.Context, repo *repos.Repo, dir string, keep []string) error {
	prefix := dir + "/by-hash/SHA256/"
	end, _ := assets.PrefixEnd(prefix)
	after := ""
	for {
		var stale []string
		lower, inclusive := prefix, true
		if after != "" {
			lower, inclusive = after, false
		}
		for len(stale) < byHashBatch {
			a, err := h.assets.Seek(ctx, repo.ID, lower, inclusive, end)
			if err != nil {
				return err
			}
			if a == nil {
				break
			}
			lower, inclusive, after = a.Path, false, a.Path
			if !slices.Contains(keep, strings.TrimPrefix(a.Path, prefix)) {
				stale = append(stale, a.Path)
			}
		}
		for _, p := range stale {
			if err := h.deleteIndexFile(ctx, repo, p); err != nil {
				return err
			}
		}
		if len(stale) < byHashBatch {
			return nil
		}
	}
}

// deleteIndexFile deletes the asset row of a generated file and then its blob, under its lock.
func (h *Handler) deleteIndexFile(ctx context.Context, repo *repos.Repo, p string) error {
	unlock := h.assets.Lock(repo.ID, p)
	defer unlock()
	deleted := false
	err := h.db.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		deleted, err = assets.DeleteTx(ctx, tx, repo.ID, p)
		return err
	})
	if err != nil || !deleted {
		return err
	}
	if err := h.assets.DeleteBlob(ctx, repo.Name, p); err != nil {
		h.log.Warn("delete by-hash blob", "repo", repo.Name, "path", p, "err", err)
	}
	return nil
}
