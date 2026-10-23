package docker

import (
	"context"
	"database/sql"

	"github.com/Telesphorepo/Telesphorepo/internal/docker/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// onRepoChange runs in the transaction of a repo update (format.Spec OnRepoChange): a rename
// gives every image of the repo its new full name "<repo>/<image>", so the catalog lists the
// images under the new name.
func (f *Format) onRepoChange(ctx context.Context, tx *sql.Tx, old, new *repos.Repo) error {
	if old.Name == new.Name {
		return nil
	}
	return dbq.New(tx).RenameImages(ctx, dbq.RenameImagesParams{RepoName: new.Name, RepoID: new.ID})
}
