package rpm

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/rpm/dbq"
)

// onRemoved runs in the transaction that deletes asset rows of repo, while the rows are still
// readable (format.Spec OnRemoved). For each deleted package it deletes the package's rows and
// marks the repodata. A deleted repomd.xml, its signature or a data file that the current
// repomd.xml names marks the repodata too, so it comes back within the debounce: dnf fails every
// command on a repo it cannot load. The pipeline does not call it while the whole repo is
// deleted.
func (h *Handler) onRemoved(ctx context.Context, tx *sql.Tx, repo *repos.Repo, removed []*assets.Asset, _ []repopath.Path) ([]repopath.Path, error) {
	if repo.Kind != repos.KindHosted {
		return nil, nil
	}
	q := dbq.New(tx)
	mark := false
	var current []string
	var ids []int64 // the package files: their rows go in one statement
	for _, a := range removed {
		switch {
		case strings.HasPrefix(a.Path, "Packages/"):
			ids = append(ids, a.ID)
		case a.Path == repomdPath || a.Path == repomdSig:
			mark = true
		case strings.HasPrefix(a.Path, "repodata/"):
			if current == nil {
				hrefs, err := q.GetRepodata(ctx, repo.ID)
				if err != nil && !errors.Is(err, sql.ErrNoRows) {
					return nil, err
				}
				current = append(strings.Fields(hrefs), "")
			}
			if slices.Contains(current, a.Path) {
				mark = true
			}
		}
	}
	if len(ids) > 0 {
		js, err := json.Marshal(ids)
		if err != nil {
			return nil, err
		}
		gone, err := q.DeletePackagesOfAssets(ctx, string(js))
		if err != nil {
			return nil, err
		}
		mark = mark || len(gone) > 0
	}
	if !mark {
		return nil, nil
	}
	return nil, h.rebuilds.MarkTx(ctx, tx, repo.ID, groupRepodata, unitIndex)
}

// purge is the repos.Purge of the module: it deletes one batch of the packages of a repo (their
// parts go with them), so the delete of the repo row cascades to none.
func purge(q *dbq.Queries) repos.Purge {
	return func(ctx context.Context, repoID int64) (int64, error) {
		return q.PurgePackages(ctx, dbq.PurgePackagesParams{RepoID: repoID, Limit: repos.PurgeBatch})
	}
}
