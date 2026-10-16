package debian

import (
	"context"
	"database/sql"
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
func (h *Handler) onRemoved(ctx context.Context, tx *sql.Tx, repo *repos.Repo, removed []*assets.Asset) ([]repopath.Path, error) {
	q := dbq.New(tx)
	type mark struct{ dist, unit string }
	var marks []mark
	for _, a := range removed {
		if !strings.HasPrefix(a.Path, "pool/") {
			continue
		}
		rows, err := q.DeletePackagesOfAsset(ctx, a.ID)
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
// after a rebuild of the unit.
func (h *Handler) pruneByHash(ctx context.Context, repo *repos.Repo, prefix string, keep []string) error {
	end, _ := assets.PrefixEnd(prefix)
	lower, inclusive := prefix, true
	for {
		var stale []repopath.Path
		n := 0
		for ; n < byHashBatch; n++ {
			a, err := h.assets.Seek(ctx, repo.ID, lower, inclusive, end)
			if err != nil {
				return err
			}
			if a == nil {
				break
			}
			lower, inclusive = a.Path, false
			if !slices.Contains(keep, strings.TrimPrefix(a.Path, prefix)) {
				stale = append(stale, repopath.Path(a.Path))
			}
		}
		if len(stale) > 0 {
			if _, err := h.assets.Remove(ctx, assets.Removal{Repo: repo, Paths: stale, Origin: generated}); err != nil {
				return err
			}
		}
		if n < byHashBatch {
			return nil
		}
	}
}
