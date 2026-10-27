package arch

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/arch/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// onRemoved runs in the transaction that deletes asset rows of repo, while the rows are still
// readable (format.Spec OnRemoved). For each deleted package file it deletes the package's rows
// and gives each index entry that named it to the newest package of the name left in that
// architecture (or drops the entry), and marks the architecture for a rebuild; the package's
// signature follows. A signature or an index of a hosted repo goes only with its package or by the
// server: deleting one alone is refused. The pipeline does not call it while the whole repo is
// deleted.
func (h *Handler) onRemoved(ctx context.Context, tx *sql.Tx, repo *repos.Repo, removed []*assets.Asset, prefixes []repopath.Path) ([]repopath.Path, error) {
	if repo.Kind != repos.KindHosted {
		return nil, nil
	}
	q := dbq.New(tx)
	var follow []repopath.Path
	type entry struct{ arch, name string }
	var lost []entry
	for _, a := range removed {
		switch {
		case strings.HasPrefix(a.Path, "pool/") && strings.HasSuffix(a.Path, ".sig"):
			pkg := strings.TrimSuffix(a.Path, ".sig")
			if !slices.ContainsFunc(removed, func(x *assets.Asset) bool { return x.Path == pkg }) && !under(pkg, prefixes) {
				if _, err := h.assets.Get(ctx, repo.ID, pkg); err == nil {
					return nil, assets.Refuse(http.StatusBadRequest, "%s is the signature of %s; delete the package", a.Path, pkg)
				}
			}
			continue
		case strings.HasPrefix(a.Path, "pool/"):
		default:
			continue
		}
		row, err := q.GetPackageOfAsset(ctx, a.ID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		es, err := q.EntriesOfPackage(ctx, row.ID)
		if err != nil {
			return nil, err
		}
		for _, e := range es {
			lost = append(lost, entry{e.Arch, e.Name})
		}
		if err := q.DeletePackage(ctx, row.ID); err != nil {
			return nil, err
		}
		follow = append(follow, repopath.Path(a.Path+".sig"))
	}
	for _, e := range lost {
		if err := h.refreshEntry(ctx, q, repo.ID, e.arch, e.name); err != nil {
			return nil, err
		}
		if err := h.rebuilds.MarkTx(ctx, tx, repo.ID, e.arch, unitDB); err != nil {
			return nil, err
		}
	}
	return follow, nil
}

// under reports whether path p lies under one of the folders prefixes ("" is every path).
func under(p string, prefixes []repopath.Path) bool {
	return slices.ContainsFunc(prefixes, func(x repopath.Path) bool { return strings.HasPrefix(p, string(x)) })
}

// refreshEntry points the index entry of name in arch at the newest package of the name left in
// arch or any (the architecture's own on a tie), or deletes it when there is none.
func (h *Handler) refreshEntry(ctx context.Context, q *dbq.Queries, repoID int64, arch, name string) error {
	var best *dbq.NewestOfRow
	var bestArch string
	for _, a := range []string{arch, "any"} {
		r, err := q.NewestOf(ctx, dbq.NewestOfParams{RepoID: repoID, Arch: a, Name: name})
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if best == nil || newer(r.Vkey, a, best.Vkey, bestArch) {
			best, bestArch = &r, a
		}
	}
	if best == nil {
		return q.DeleteEntry(ctx, dbq.DeleteEntryParams{RepoID: repoID, Arch: arch, Name: name})
	}
	return q.UpsertEntry(ctx, dbq.UpsertEntryParams{RepoID: repoID, Arch: arch, Name: name, PackageID: best.ID})
}

// purge is the repos.Purge of the module: it deletes one batch of the rows of a repo, entries
// first (a package's file list goes with it), so the delete of the repo row cascades to none.
func purge(q *dbq.Queries) repos.Purge {
	steps := []func(context.Context, int64) (int64, error){
		func(ctx context.Context, repoID int64) (int64, error) {
			return q.PurgeEntries(ctx, dbq.PurgeEntriesParams{RepoID: repoID, Limit: repos.PurgeBatch})
		},
		func(ctx context.Context, repoID int64) (int64, error) {
			return q.PurgePackages(ctx, dbq.PurgePackagesParams{RepoID: repoID, Limit: repos.PurgeBatch})
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
