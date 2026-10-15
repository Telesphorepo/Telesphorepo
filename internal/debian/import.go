package debian

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/debian/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// importComponent is the component that imported packages go into.
const importComponent = "main"

// ImportFormats accepts Nexus apt repos.
func (h *Handler) ImportFormats() []string { return []string{"apt"} }

// Import takes a pool/**/*.deb file through the upload pipeline into the distribution of the
// repo config and component main, with the source's dates. The package gets this server's
// pool path. A package that is present with the same bytes is skipped and gets the source's
// dates; when the source gives a checksum, Import looks it up before it downloads anything.
// Every other file (dists/ indexes, keys) is ignored: the indexer builds and signs the indexes,
// which keep the time they are built.
func (h *Handler) Import(ctx context.Context, repo *repos.Repo, item format.ImportItem) (format.ImportOutcome, error) {
	if !strings.HasPrefix(item.Path, "pool/") || !strings.HasSuffix(item.Path, ".deb") {
		return format.ImportIgnored, nil
	}
	dist, err := importDistribution(repo)
	if err != nil {
		return "", err
	}
	skip := func(pool string) (format.ImportOutcome, error) {
		a, err := h.assets.Get(ctx, repo.ID, pool)
		if err != nil {
			return "", err
		}
		return format.ImportSkipped, h.assets.SetTimes(ctx, a, item.By())
	}
	if item.SHA256 != "" {
		pool, err := h.q.GetPackageFileBySHA256(ctx, dbq.GetPackageFileBySHA256Params{RepoID: repo.ID,
			Distribution: dist, Component: importComponent, Sha256: item.SHA256})
		switch {
		case err == nil:
			return skip(pool)
		case !errors.Is(err, sql.ErrNoRows):
			return "", err
		}
	}
	sp, err := spoolItem(ctx, h.assets, h.cfg().MaxUploadBytes, item)
	if err != nil {
		return "", err
	}
	defer sp.Close()
	res, created, err := h.ingest(ctx, repo, dist, importComponent, sp, item.By())
	if err != nil {
		return "", err
	}
	if !created {
		return skip(res.Filename)
	}
	return format.ImportImported, nil
}

// spoolItem downloads item into a spool file under the upload size limit and checks the
// source checksum when there is one.
func spoolItem(ctx context.Context, a *assets.Service, limit int64, item format.ImportItem) (*assets.Spooled, error) {
	if item.Size > limit {
		return nil, fmt.Errorf("file is larger than the upload limit of %d bytes", limit)
	}
	rc, err := item.Open(ctx)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	sp, err := a.Spool(rc, limit)
	if err != nil {
		return nil, err
	}
	if item.SHA256 != "" && sp.SHA256 != item.SHA256 {
		sp.Close()
		return nil, fmt.Errorf("checksum mismatch: the source gives sha256 %s, the download has %s", item.SHA256, sp.SHA256)
	}
	return sp, nil
}
