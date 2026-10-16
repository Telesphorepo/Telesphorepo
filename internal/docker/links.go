package docker

import (
	"context"
	"errors"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/docker/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
)

const (
	// sweepMaxWait is the longest wait of the job docker.links between two sweeps.
	sweepMaxWait = time.Minute
	// sweepBatch is how many links one round of a sweep reads.
	sweepBatch = 100
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

// sweep removes the links unused for grace. A link that a manifest took meanwhile is refused by
// the hooks and stays.
func (f *Format) sweep(ctx context.Context, grace time.Duration) error {
	cutoff := time.Now().Add(-grace).Unix()
	for {
		rows, err := f.q.ListUnreferencedLinks(ctx, dbq.ListUnreferencedLinksParams{Cutoff: cutoff, Limit: sweepBatch})
		if err != nil {
			return err
		}
		removed := 0
		for _, l := range rows {
			repo, err := f.h.Repos.ByID(ctx, l.RepoID)
			if err != nil {
				return err
			}
			n, err := f.h.Assets.Remove(ctx, assets.Removal{Repo: repo, Paths: []repopath.Path{repopath.Path(linkPath(l.Image, l.Digest))},
				Origin: assets.Origin{Source: assets.SourceGenerated}})
			var ref *assets.Refusal
			if errors.As(err, &ref) {
				continue
			}
			if err != nil {
				return err
			}
			if n == 0 {
				// The link's asset is gone already: drop the row, so it does not come back.
				if err := dbq.New(f.h.DB.W).DeleteStaleLink(ctx, dbq.DeleteStaleLinkParams{RepoID: l.RepoID, Image: l.Image, Digest: l.Digest, UnreferencedAt: cutoff}); err != nil {
					return err
				}
			}
			removed++
		}
		if len(rows) < sweepBatch || removed == 0 {
			return nil
		}
	}
}
