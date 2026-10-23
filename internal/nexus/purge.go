package nexus

import (
	"context"

	"github.com/Telesphorepo/Telesphorepo/internal/nexus/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// purge is the repos.Purge of the module: it deletes one batch of the rows of a repo, children first,
// so the delete of the repo row cascades to none of them.
func purge(w dbq.DBTX) repos.Purge {
	q := dbq.New(w)
	steps := []func(context.Context, int64) (int64, error){
		func(ctx context.Context, repoID int64) (int64, error) {
			return q.PurgeNexusSyncRepos(ctx, dbq.PurgeNexusSyncReposParams{RepoID: repoID, Limit: repos.PurgeBatch})
		},
		func(ctx context.Context, repoID int64) (int64, error) {
			return q.PurgeNexusSwitchOvers(ctx, dbq.PurgeNexusSwitchOversParams{RepoID: repoID, Limit: repos.PurgeBatch})
		},
		func(ctx context.Context, repoID int64) (int64, error) {
			return q.PurgeNexusSourceRepos(ctx, dbq.PurgeNexusSourceReposParams{RepoID: repoID, Limit: repos.PurgeBatch})
		},
		func(ctx context.Context, repoID int64) (int64, error) {
			return q.PurgeRepoRehearsals(ctx, repoID)
		},
		func(ctx context.Context, repoID int64) (int64, error) {
			return q.PurgeRepoRehearsalsFor(ctx, dbq.PurgeRepoRehearsalsForParams{RepoID: repoID, Limit: repos.PurgeBatch})
		},
		func(ctx context.Context, repoID int64) (int64, error) {
			return q.PurgeRepoMoves(ctx, repoID)
		},
		func(ctx context.Context, repoID int64) (int64, error) {
			return q.PurgeRepoMovesInto(ctx, dbq.PurgeRepoMovesIntoParams{RepoID: repoID, Limit: repos.PurgeBatch})
		},
		func(ctx context.Context, repoID int64) (int64, error) {
			return q.PurgeNexusMergeChoices(ctx, dbq.PurgeNexusMergeChoicesParams{RepoID: repoID, Limit: repos.PurgeBatch})
		},
		func(ctx context.Context, repoID int64) (int64, error) {
			return q.PurgeNexusMerges(ctx, dbq.PurgeNexusMergesParams{RepoID: repoID, Limit: repos.PurgeBatch})
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
