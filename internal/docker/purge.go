package docker

import (
	"context"

	"github.com/Telesphorepo/Telesphorepo/internal/docker/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// purge is the repos.Purge of the module: it deletes one batch of the rows of a repo, children first,
// so the delete of the repo row cascades to none of them.
func purge(q *dbq.Queries) repos.Purge {
	steps := []func(context.Context, int64) (int64, error){
		func(ctx context.Context, repoID int64) (int64, error) {
			return q.PurgeDockerTags(ctx, dbq.PurgeDockerTagsParams{RepoID: repoID, Limit: repos.PurgeBatch})
		},
		func(ctx context.Context, repoID int64) (int64, error) {
			return q.PurgeDockerRefs(ctx, dbq.PurgeDockerRefsParams{RepoID: repoID, Limit: repos.PurgeBatch})
		},
		func(ctx context.Context, repoID int64) (int64, error) {
			return q.PurgeDockerLinks(ctx, dbq.PurgeDockerLinksParams{RepoID: repoID, Limit: repos.PurgeBatch})
		},
		func(ctx context.Context, repoID int64) (int64, error) {
			return q.PurgeDockerManifests(ctx, dbq.PurgeDockerManifestsParams{RepoID: repoID, Limit: repos.PurgeBatch})
		},
		func(ctx context.Context, repoID int64) (int64, error) {
			return q.PurgeDockerImages(ctx, dbq.PurgeDockerImagesParams{RepoID: repoID, Limit: repos.PurgeBatch})
		},
	}
	return func(ctx context.Context, repoID int64) (int64, error) {
		for _, step := range steps {
			if n, err := step(ctx, repoID); err != nil || n > 0 {
				return n, err
			}
		}
		return 0, nil
	}
}
