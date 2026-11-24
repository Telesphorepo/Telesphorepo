package debian

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/debian/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// onRemoved runs in the transaction that deletes asset rows of repo, while the rows are still
// readable (format.Spec OnRemoved): it removes the packages of every deleted pool file and marks
// their (dist, component, architecture) units for a rebuild, so the indexes drop the packages. An
// arch:all package marks every configured architecture. The pipeline does not call it while the
// whole repo is deleted.
func (h *Handler) onRemoved(ctx context.Context, tx *sql.Tx, repo *repos.Repo, removed []*assets.Asset, _ []repopath.Path) ([]repopath.Path, error) {
	q := dbq.New(tx)
	type mark struct{ dist, unit string }
	var marks []mark
	var ids []int64
	for _, a := range removed {
		if strings.HasPrefix(a.Path, "pool/") {
			ids = append(ids, a.ID)
		}
	}
	if len(ids) > 0 {
		js, err := json.Marshal(ids)
		if err != nil {
			return nil, err
		}
		rows, err := q.DeletePackagesOfAssets(ctx, string(js)) // one statement for the batch
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			archs := []string{r.Architecture}
			if r.Architecture == "all" {
				archs = architectures(repo)
			}
			for _, arch := range archs {
				m := mark{r.Distribution, unit(r.Component, arch)}
				if !slices.Contains(marks, m) {
					marks = append(marks, m)
				}
			}
		}
	}
	for _, m := range marks {
		if err := h.rebuilds.MarkTx(ctx, tx, repo.ID, m.dist, m.unit); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

// byHashBatch is the page size of the by-hash cleanup.
const byHashBatch = 200

// pruneByHash deletes the by-hash files under prefix (dists/{dist}/{comp}/binary-{arch}/by-hash/
// SHA256/) whose hash is not in keep: the current Packages and Packages.gz and the generation
// they replaced, so a client that read the previous Release still finds its files. It runs
// after a rebuild of the unit and reads the files a page of byHashBatch at a time.
func (h *Handler) pruneByHash(ctx context.Context, repo *repos.Repo, prefix string, keep []string) error {
	after := ""
	for {
		page, next, err := h.assets.List(ctx, repo.ID, prefix, after, byHashBatch)
		if err != nil {
			return err
		}
		var stale []repopath.Path
		for _, a := range page {
			if !slices.Contains(keep, strings.TrimPrefix(a.Path, prefix)) {
				stale = append(stale, repopath.Path(a.Path))
			}
		}
		if len(stale) > 0 {
			if _, err := h.assets.Remove(ctx, assets.Removal{Repo: repo, Paths: stale, Origin: generated}); err != nil {
				return err
			}
		}
		if next == "" {
			return nil
		}
		after = next
	}
}
