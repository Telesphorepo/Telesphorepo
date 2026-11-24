package assets

import (
	"context"

	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
)

// GetMany returns the assets that repo repoID has at paths, by path: one query (getAll) for the
// files of a page (the stored copies of a proxy's versions, the files of a build), where Get would
// cost one query per path. A path the repo has no file at is not in the map.
func (p *Pipeline) GetMany(ctx context.Context, repoID int64, paths []string) (map[string]*Asset, error) {
	return getAll(ctx, dbq.New(p.db.R), repoID, paths)
}
