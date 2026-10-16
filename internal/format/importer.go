package format

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// Importer takes files from another repository manager into a repo of the format. The import
// runner names no format: it offers each source file to the target repo's format through Run,
// which does the shared work once (the target path, the skip check, the download with the size
// limit and the checksum check, the commit).
type Importer struct {
	// Sources lists the source format IDs the format accepts, e.g. "maven2", "apt", "raw".
	Sources []string
	// Target returns the path of a source file in repo; ok=false ignores the file (e.g. a source
	// index that the format builds itself). A file whose target already has the source's
	// SHA-256 is skipped without a download.
	Target func(ctx context.Context, repo *repos.Repo, it ImportItem) (path repopath.Path, ok bool, err error)
	// Store stores a downloaded file (e.g. a .deb through the format's own pipeline step); nil:
	// Commit at the target path with source "import". Its result Unchanged counts as skipped.
	Store func(ctx context.Context, repo *repos.Repo, it ImportItem, sp *assets.Spooled) (*assets.Result, error)
	// Plan plans the target repo of a source repo: the config and the field values of the target,
	// the questions that the UI asks before a save, the facts it shows, and the work to do before
	// the save (e.g. a key import). target is nil when no target is chosen yet (the source detail),
	// and has ID 0 for a target that the save creates. answers are the answers to the questions;
	// nil for the source detail. nil Plan: the target gets the format's default config.
	Plan func(ctx context.Context, src SourceRepo, target *repos.Repo, answers map[string]string) (*TargetPlan, error)
	// Header holds extra headers of every download from the source, e.g. the Accept of the OCI
	// manifest types, so the source sends a file as it stores it.
	Header http.Header
}

// SourceRepo is a repository of the source server.
type SourceRepo struct {
	Name   string
	Format string // the source's format ID, e.g. "apt"
	Kind   string // "hosted", "proxy" or "group"
	// Attributes is the source's own settings object of the repository (a Nexus
	// repositorySettings item); nil when the source did not give it. It can hold secrets (e.g. a
	// signing key pair): it is never logged or shown.
	Attributes json.RawMessage
}

// TargetPlan is what Importer.Plan answers.
type TargetPlan struct {
	// Config is the config of a new target, or the merged config of an existing one; nil keeps
	// the config (a new target gets the format's default).
	Config json.RawMessage
	// Fields are repo field values of the target, e.g. {"signing_key": "\"nexus-apt\""}.
	Fields map[string]json.RawMessage
	// Questions are what the UI asks before a save; the answers come back in the next Plan.
	Questions []Question
	// Facts are shown in the source detail, e.g. {"key_in_source": true}. The fact "note" is a
	// sentence that the UI shows before a save creates the target.
	Facts map[string]any
	// Prepare runs before the repo save, e.g. it imports a signing key; nil: nothing.
	Prepare func(ctx context.Context) error
}

// Question is one question of a TargetPlan.
type Question struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Help     string   `json:"help,omitempty"`
	Type     string   `json:"type"` // "choice" or "secret"
	Options  []Option `json:"options,omitempty"`
	Required bool     `json:"required"`
	Default  string   `json:"default,omitempty"`
}

// Option is one choice of a Question.
type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// ImportItem is one source file. CreatedAt and UpdatedAt are the source's dates of the file;
// zero means now. The stored file gets them, and a skipped file gets them too.
type ImportItem struct {
	Path        string // source path, forward slashes
	Size        int64
	SHA256      string // lowercase hex; "" when the source does not give one
	ContentType string // the source's content type of the file; "" when it gives none
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Open        func(ctx context.Context) (io.ReadCloser, error)
	// Fetch downloads another path of the same source repo (e.g. a layer that a manifest names)
	// with the Importer's Header and the extra header, and returns its body and content type. It
	// answers the source's authentication challenges (e.g. a registry's token exchange).
	Fetch func(ctx context.Context, sourcePath string, header http.Header) (io.ReadCloser, string, error)
}

// Origin is the origin of the item's file: an import with the source's dates.
func (it ImportItem) Origin() assets.Origin {
	return assets.Origin{Source: assets.SourceImport, Created: it.CreatedAt, Updated: it.UpdatedAt}
}

// ImportOutcome is what an import did with an item.
type ImportOutcome string

const (
	ImportImported ImportOutcome = "imported" // stored
	ImportSkipped  ImportOutcome = "skipped"  // already present with the same bytes
	ImportIgnored  ImportOutcome = "ignored"  // not a file of this format, e.g. a source index
)

// Run imports item into repo: the target path (ignored when the format does not take the file),
// the skip of a file that is at the target with the same SHA-256 (it gets the source's dates),
// the download under max_upload_size with the source checksum checked, then Store or Commit. An
// error fails this item only.
func (im *Importer) Run(ctx context.Context, p *assets.Pipeline, repo *repos.Repo, it ImportItem) (ImportOutcome, error) {
	target, ok, err := im.Target(ctx, repo, it)
	if err != nil {
		return "", err
	}
	if !ok {
		return ImportIgnored, nil
	}
	if it.SHA256 != "" {
		a, err := p.Get(ctx, repo.ID, string(target))
		switch {
		case err == nil && a.SHA256 == it.SHA256:
			return ImportSkipped, p.SetTimes(ctx, a, it.CreatedAt, it.UpdatedAt)
		case err != nil && !errors.Is(err, assets.ErrNotFound):
			return "", err
		}
	}
	sp, err := download(ctx, p, it)
	if err != nil {
		return "", err
	}
	defer sp.Close()
	var res *assets.Result
	if im.Store != nil {
		res, err = im.Store(ctx, repo, it, sp)
	} else {
		res, err = p.Commit(ctx, assets.Write{Repo: repo, Path: target, Body: assets.Body{Spool: sp}, Origin: it.Origin()})
	}
	if err != nil {
		return "", err
	}
	if res.Status == assets.Unchanged {
		if res.Asset != nil {
			if err := p.SetTimes(ctx, res.Asset, it.CreatedAt, it.UpdatedAt); err != nil {
				return "", err
			}
		}
		return ImportSkipped, nil
	}
	return ImportImported, nil
}

// download spools item under the upload size limit and checks the source checksum when there is
// one.
func download(ctx context.Context, p *assets.Pipeline, it ImportItem) (*assets.Spooled, error) {
	limit := p.MaxUploadSize()
	if it.Size > limit {
		return nil, fmt.Errorf("file is larger than the upload limit of %d bytes", limit)
	}
	rc, err := it.Open(ctx)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	sp, err := p.Spool(rc, limit)
	if err != nil {
		return nil, err
	}
	if it.SHA256 != "" && sp.SHA256 != it.SHA256 {
		sp.Close()
		return nil, fmt.Errorf("checksum mismatch: the source gives sha256 %s, the download has %s", it.SHA256, sp.SHA256)
	}
	return sp, nil
}

// SourcePath is the Target of a format that stores a source file at its own path: the path, when
// it is a valid repo path.
func SourcePath(_ context.Context, _ *repos.Repo, it ImportItem) (repopath.Path, bool, error) {
	p, err := repopath.Parse(it.Path)
	if err != nil {
		return "", false, errors.New("invalid path")
	}
	return p, true, nil
}
