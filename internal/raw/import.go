package raw

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

// ImportFormats accepts Nexus raw repos.
func (f *Format) ImportFormats() []string { return []string{"raw"} }

// Import stores the file at its source path under the PUT rules, with the source's dates:
// identical bytes are skipped and get the source's dates, and different bytes replace the file
// only when the repo allows overwrite. When the source gives a checksum, Import compares it
// with the stored asset before it downloads anything.
func (f *Format) Import(ctx context.Context, repo *repos.Repo, item format.ImportItem) (format.ImportOutcome, error) {
	if !validPath(item.Path) {
		return "", errors.New("invalid path")
	}
	c, err := repoConfig(repo)
	if err != nil {
		return "", fmt.Errorf("bad repo config: %w", err)
	}
	if item.SHA256 != "" {
		existing, err := f.h.Assets.Get(ctx, repo.ID, item.Path)
		switch {
		case err == nil && existing.SHA256 == item.SHA256:
			return format.ImportSkipped, f.h.Assets.SetTimes(ctx, existing, item.By())
		case err == nil && c.Overwrite != OverwriteAllow:
			return "", errExists
		case err != nil && !errors.Is(err, assets.ErrNotFound):
			return "", err
		}
	}
	sp, err := spoolItem(ctx, f.h.Assets, f.h.Config().MaxUploadBytes, item)
	if err != nil {
		return "", err
	}
	defer sp.Close()
	_, unchanged, err := f.put(ctx, repo, c, item.Path, sp, item.By())
	if err != nil {
		return "", err
	}
	if unchanged {
		existing, err := f.h.Assets.Get(ctx, repo.ID, item.Path)
		if err != nil {
			return "", err
		}
		return format.ImportSkipped, f.h.Assets.SetTimes(ctx, existing, item.By())
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
