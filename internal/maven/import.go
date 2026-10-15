package maven

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// ImportFormats accepts Nexus maven2 repos.
func (h *Handler) ImportFormats() []string { return []string{"maven2"} }

// Import stores the file at its source path under the PUT rules. A file that is present with
// the same bytes is skipped; when the source gives a checksum, Import compares it with the
// stored asset before it downloads anything.
func (h *Handler) Import(ctx context.Context, repo *repos.Repo, item format.ImportItem) (format.ImportOutcome, error) {
	if !validPath(item.Path) {
		return "", errors.New("invalid path")
	}
	existing, err := h.assets.Get(ctx, repo.ID, item.Path)
	if err != nil && !errors.Is(err, assets.ErrNotFound) {
		return "", err
	}
	if existing != nil && item.SHA256 != "" {
		if existing.SHA256 == item.SHA256 {
			return format.ImportSkipped, nil
		}
		if !mutable(item.Path) {
			return "", errImmutable
		}
	}
	sp, err := spoolItem(ctx, h.assets, h.cfg().MaxUploadBytes, item)
	if err != nil {
		return "", err
	}
	defer sp.Close()
	if existing != nil && existing.SHA256 == sp.SHA256 {
		return format.ImportSkipped, nil
	}
	_, unchanged, err := h.put(ctx, repo, item.Path, sp, assets.By{})
	if err != nil {
		return "", err
	}
	if unchanged {
		return format.ImportSkipped, nil
	}
	return format.ImportImported, nil
}

// validPath applies the repo path rules of spec §6 to a source path.
func validPath(p string) bool {
	return httpx.ValidRepoPath(&http.Request{URL: &url.URL{Path: p}}, p)
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
