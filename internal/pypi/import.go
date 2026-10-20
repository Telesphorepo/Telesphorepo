package pypi

import (
	"context"
	"path"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// importTarget takes the wheels and sdists of a Nexus pypi repo (format.Importer Target), at
// packages/<name>/<version>/<file> there; each goes to packages/<project>/<file>. Every other file
// (the simple pages, signatures) is ignored: the server renders its own pages.
func importTarget(_ context.Context, _ *repos.Repo, it format.ImportItem) (repopath.Path, bool, error) {
	base := path.Base(it.Path)
	d, err := parseFilename(base)
	if err != nil {
		return "", false, nil
	}
	p, err := repopath.Parse("packages/" + normalize(d.name) + "/" + base)
	if err != nil {
		return "", false, nil
	}
	return p, true, nil
}

// importStore ingests a downloaded distribution file with the source's dates (format.Importer
// Store).
func (h *Handler) importStore(ctx context.Context, repo *repos.Repo, it format.ImportItem, sp *assets.Spooled) (*assets.Result, error) {
	rs, _, err := h.ingest(ctx, repo, path.Base(it.Path), sp, it.Origin(), wantMeta{})
	if err != nil {
		return nil, err
	}
	return rs[0], nil
}
