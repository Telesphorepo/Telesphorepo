package docker

import (
	"context"
	"errors"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/docker/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
)

const (
	// sweepMaxWait is the longest wait of the job docker.links between two sweeps.
	sweepMaxWait = time.Minute
	// sweepBatch is how many links one round of a sweep reads.
	sweepBatch = 1000
)

// sweepLinks is the job docker.links: every min(docker.link_grace, 1 min), and at once when the
// settings change, it removes the links that no manifest used for docker.link_grace. The asset
// row of a link goes; the core blob GC deletes the blob when no asset uses it any more.
func (f *Format) sweepLinks(ctx context.Context) {
	changed := make(chan struct{}, 1)
	f.h.Settings.Subscribe(func(_, _ *settings.Snapshot) {
		select {
		case changed <- struct{}{}:
		default:
		}
	})
	for {
		grace := LinkGrace.Get(f.h.Settings.Current())
		select {
		case <-ctx.Done():
			return
		case <-changed:
		case <-time.After(min(grace, sweepMaxWait)):
		}
		if err := f.sweep(ctx, LinkGrace.Get(f.h.Settings.Current())); err != nil && ctx.Err() == nil {
			f.h.Log.Error("sweep of unused layers failed", "err", err)
		}
	}
}

// sweep removes the links unused for grace, the links of one repo with one Assets.Remove (its
// batches of rows in one transaction each). When the hooks refuse a group (a manifest took one
// of its links meanwhile), the group goes link by link and the refused link stays.
func (f *Format) sweep(ctx context.Context, grace time.Duration) error {
	cutoff := time.Now().Add(-grace).Unix()
	for {
		rows, err := f.q.ListUnreferencedLinks(ctx, dbq.ListUnreferencedLinksParams{Cutoff: cutoff, Limit: sweepBatch})
		if err != nil {
			return err
		}
		var order []int64
		byRepo := map[int64][]dbq.ListUnreferencedLinksRow{}
		for _, l := range rows {
			if _, ok := byRepo[l.RepoID]; !ok {
				order = append(order, l.RepoID)
			}
			byRepo[l.RepoID] = append(byRepo[l.RepoID], l)
		}
		removed := 0
		for _, id := range order {
			n, err := f.sweepRepo(ctx, id, byRepo[id], cutoff)
			if err != nil {
				return err
			}
			removed += n
		}
		if len(rows) < sweepBatch || removed == 0 {
			return nil
		}
	}
}

// sweepRepo removes the unused links ls of repo id and returns how many of them went (their
// asset removed, or their row dropped because the asset was gone already).
func (f *Format) sweepRepo(ctx context.Context, id int64, ls []dbq.ListUnreferencedLinksRow, cutoff int64) (int, error) {
	repo, err := f.h.Repos.ByID(ctx, id)
	if errors.Is(err, repos.ErrNotFound) {
		return 0, nil // the rows go with the repo
	}
	if err != nil {
		return 0, err
	}
	remove := func(ls []dbq.ListUnreferencedLinksRow) (int, error) {
		paths := make([]repopath.Path, len(ls))
		for i, l := range ls {
			paths[i] = repopath.Path(linkPath(l.Image, l.Digest))
		}
		return f.h.Assets.Remove(ctx, assets.Removal{Repo: repo, Paths: paths, Origin: assets.Origin{Source: assets.SourceGenerated}})
	}
	// A link whose asset is gone already: drop the row, so it does not come back.
	stale := func(l dbq.ListUnreferencedLinksRow) error {
		return dbq.New(f.h.DB.W).DeleteStaleLink(ctx, dbq.DeleteStaleLinkParams{RepoID: l.RepoID, Image: l.Image, Digest: l.Digest, UnreferencedAt: cutoff})
	}
	n, err := remove(ls)
	var ref *assets.Refusal
	switch {
	case err == nil:
		if n < len(ls) {
			// Some assets were gone already. A removed link's row went with its asset, so the
			// rows left are the stale ones.
			for _, l := range ls {
				if err := stale(l); err != nil {
					return n, err
				}
			}
		}
		return len(ls), nil
	case !errors.As(err, &ref):
		return 0, err
	}
	// The hooks refused the group: one link at a time, and a refused link stays.
	done := 0
	for _, l := range ls {
		k, err := remove([]dbq.ListUnreferencedLinksRow{l})
		if errors.As(err, &ref) {
			continue
		}
		if err != nil {
			return done, err
		}
		if k == 0 {
			if err := stale(l); err != nil {
				return done, err
			}
		}
		done++
	}
	return done, nil
}
