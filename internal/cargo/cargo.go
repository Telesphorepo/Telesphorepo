// Package cargo serves Cargo registries at /cargo/{repo}/: the sparse index at index/, the .crate
// files at crates/ and the publish and yank API at api/v1/. Hosted repos render their index files
// from their rows; a proxy serves the index files of its remote (crates.io) unchanged and fetches
// the crates from its download URL; a group serves each crate from the first member that has it.
package cargo

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/cargo/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/module"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/version"
)

// name is the module ID and the format name: URL segment, repos.format value and schema
// component.
const name = "cargo"

//go:embed migrations/*.sql
var migrationsFS embed.FS

//go:embed queries/*.sql
var queriesFS embed.FS

// Audit actions of the module.
const (
	ActionYank   = "cargo.yank"
	ActionUnyank = "cargo.unyank"
)

// Handler serves the Cargo format.
type Handler struct {
	host *module.Host
	q    *dbq.Queries // on the reader pool
	// docs keeps rendered index files: a hosted crate's by its revision, a proxy's by the SHA-256
	// of the remote's file.
	docs *format.DocumentCache
}

// Module returns the Cargo module: the Cargo format with proxies, groups, uploads, imports, demo
// data and seeds. The Telesphorepo CLI publishes to it with cargo publish.
func Module() module.Module {
	h := &Handler{docs: format.NewDocumentCache(1024, 64<<20, 16<<20)}
	return module.Module{
		ID: name, Title: "Cargo (Rust)",
		Register: func(x *module.Extender) {
			x.Schema(db.Component{Migrations: db.Sub(migrationsFS, "migrations"), Queries: db.Sub(queriesFS, "queries")})
			x.AuditActions(
				audit.ActionDef{ID: ActionYank, Label: "Crate version yanked", Subsystem: "repo"},
				audit.ActionDef{ID: ActionUnyank, Label: "Crate version unyanked", Subsystem: "repo"},
			)
			x.Init(func(ctx context.Context, host *module.Host) error {
				h.host, h.q = host, dbq.New(host.DB.R)
				return nil
			})
			x.Format(format.Spec{
				Name: name, Title: "Cargo (Rust)", Icon: icon,
				Description:   "Rust crates, for cargo builds.",
				Kinds:         []string{repos.KindHosted, repos.KindProxy, repos.KindGroup},
				Config:        format.ConfigSpec{Default: json.RawMessage(`{}`), Fields: configFields, Validate: validateConfig},
				Routes:        h.routes,
				Describe:      describe,
				Admit:         h.admit,
				OnCommitted:   h.onCommitted,
				OnRemoved:     h.onRemoved,
				Classify:      classify,
				ProxyProbe:    func(json.RawMessage) string { return "config.json" },
				Versions:      h.Versions,
				Builds:        h.Builds,
				Usage:         h.Usage,
				ArtifactUsage: h.ArtifactUsage,
				Tools:         tools(),
				Publish:       format.PublishCLI,
				Upload:        h.uploadSpec(),
				Importer:      &format.Importer{Sources: []string{"cargo"}, Target: importTarget, Store: h.importStore},
				UI:            ui,
				Edge:          &format.EdgeSpec{Shape: format.EdgeStandard, Immutable: []string{"crates/*/*.crate"}},
			})
			x.Demo(module.DemoData{Repos: demoRepos, Fill: h.Demo})
			x.Seed(seed)
		},
	}
}

// configFields are the options of the repo config that the web UI edits.
var configFields = []format.Field{{Key: "download", Label: "Download URL", Type: format.FieldString, Placeholder: defaultDownload,
	Help: "Proxy repositories only: the address of the remote's crates, the dl of its config.json, with {crate}, {version}, {prefix} or {lowerprefix}. Empty: static.crates.io, the crates of crates.io."}}

// crateContentType is the content type of a .crate file.
const crateContentType = "application/gzip"

// describe gives the content type and the kind of a Cargo path: the .crate files are artifacts;
// the index files that a proxy keeps under .index/ are indexes. Every other path is refused.
func describe(p repopath.Path) (assets.Describe, bool) {
	s := string(p)
	if _, _, ok := parseCratePath(s); ok {
		return assets.Describe{ContentType: crateContentType, Kind: assets.KindArtifact}, true
	}
	if rest, ok := strings.CutPrefix(s, ".index/"); ok {
		if _, ok := nameOfIndexPath(rest); ok {
			return assets.Describe{ContentType: "application/json", Kind: assets.KindIndex}, true
		}
	}
	return assets.Describe{}, false
}

// classify tells the proxy how to cache a path: an index file under .index/ changes at the remote
// (the path under the remote URL); a .crate never changes and comes from the download URL of the
// repo config. Other paths are refused.
func classify(repo *repos.Repo, _ *repos.Proxy, p repopath.Path) (assets.Class, bool) {
	s := string(p)
	if rest, ok := strings.CutPrefix(s, ".index/"); ok {
		if _, ok := nameOfIndexPath(rest); ok {
			return assets.Class{Changing: true, Remote: rest}, true
		}
		return assets.Class{}, false
	}
	n, v, ok := parseCratePath(s)
	if !ok {
		return assets.Class{}, false
	}
	files, remote, ok := fileSource(downloadOf(repo.Config), n, v)
	if !ok {
		return assets.Class{}, false
	}
	return assets.Class{Files: files, Remote: remote}, true
}

// crateMeta is the Meta of the write of a .crate: its index entry, parsed.
type crateMeta struct {
	entry *entry
	line  string // the stored entry
	vers  version.SemVer
}

// ingest stores the .crate of sp with its index entry e: the commit takes the folded crate name as
// an extra lock, admit refuses a name that clashes and a version that exists, and onCommitted
// writes the rows in the commit's transaction. A client mistake is an *assets.Refusal. Publish,
// uploads, imports and the demo data share it.
func (h *Handler) ingest(ctx context.Context, repo *repos.Repo, e *entry, sp *assets.Spooled, o assets.Origin) (*assets.Result, error) {
	v, err := checkEntry(e)
	if err != nil {
		return nil, assets.Refuse(http.StatusBadRequest, "%s", err.Error())
	}
	if e.Cksum != sp.SHA256 {
		return nil, fmt.Errorf("cargo: entry checksum %s of %s differs from the file", e.Cksum, e.Name)
	}
	p, err := repopath.Parse(cratePath(e.Name, e.Vers))
	if err != nil {
		return nil, assets.Refuse(http.StatusBadRequest, "the version %s of %s is too long", e.Vers, e.Name)
	}
	line, err := e.line()
	if err != nil {
		return nil, err
	}
	return h.host.Assets.Commit(ctx, assets.Write{Repo: repo, Path: p, Body: assets.Body{Spool: sp},
		ContentType: crateContentType, Kind: assets.KindArtifact, Origin: o, Locks: []string{"cargo:" + fold(e.Name)},
		Meta: &crateMeta{entry: e, line: line, vers: v}})
}

// errVersionExists is the refusal of a version that a repo holds already.
func errVersionExists(crate, vers string) error {
	return assets.Refuse(http.StatusConflict, "crate %s version %s is already uploaded", crate, vers)
}

// admit decides the write of a Cargo path (format.Spec Admit). A .crate of a publish, an upload
// or an import is refused (409) when its name folds to a name that another crate of the repo
// holds, or when the repo has the version ignoring build metadata: with the same bytes at the
// same path it is kept (unchanged). A write without an entry comes only from a proxy fetch: it
// keeps the same bytes and replaces other ones. Every other write is refused (400).
func (h *Handler) admit(ctx context.Context, w *assets.Write, existing *assets.Asset) (assets.Decision, error) {
	m, ok := w.Meta.(*crateMeta)
	if !ok {
		if w.Origin.Source != assets.SourceProxy {
			return 0, assets.Refuse(http.StatusBadRequest, "crates are published with cargo publish, or uploaded as .crate files")
		}
		if existing != nil && existing.SHA256 == w.Body.Digest() {
			return assets.Keep, nil
		}
		return assets.Store, nil
	}
	e := m.entry
	held, err := h.q.GetName(ctx, dbq.GetNameParams{RepoID: w.Repo.ID, FoldedName: fold(e.Name)})
	switch {
	case err == nil && held != e.Name:
		return 0, assets.Refuse(http.StatusConflict, "the crate name %s clashes with the crate %s of this repository: crate names compare ignoring case and - versus _", e.Name, held)
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return 0, fmt.Errorf("look up crate name: %w", err)
	}
	row, err := h.q.GetVersionByBase(ctx, dbq.GetVersionByBaseParams{RepoID: w.Repo.ID, LowerName: strings.ToLower(e.Name), BaseKey: baseKey(m.vers)})
	switch {
	case err == nil:
		if existing != nil && existing.SHA256 == w.Body.Digest() && row.Path == string(w.Path) {
			return assets.Keep, nil
		}
		return 0, errVersionExists(e.Name, row.Version)
	case !errors.Is(err, sql.ErrNoRows):
		return 0, fmt.Errorf("look up crate version: %w", err)
	}
	if existing != nil && existing.SHA256 != w.Body.Digest() {
		return 0, assets.Refuse(http.StatusConflict, "%s already exists with different content", w.Path)
	}
	return assets.Store, nil
}

// onCommitted writes the version and the name of a stored .crate, in the transaction of the write
// (format.Spec OnCommitted).
func (h *Handler) onCommitted(ctx context.Context, tx *sql.Tx, w *assets.Write, a *assets.Asset, _ *assets.Asset) error {
	m, ok := w.Meta.(*crateMeta)
	if !ok {
		return nil
	}
	e, q := m.entry, dbq.New(tx)
	if err := q.InsertVersion(ctx, dbq.InsertVersionParams{RepoID: w.Repo.ID, Name: e.Name, LowerName: strings.ToLower(e.Name),
		Version: e.Vers, SortKey: m.vers.SortKey(), BaseKey: baseKey(m.vers), Entry: m.line, AssetID: a.ID, Path: a.Path,
		CreatedAt: time.Now().Unix()}); err != nil {
		return err
	}
	return q.InsertName(ctx, dbq.InsertNameParams{RepoID: w.Repo.ID, FoldedName: fold(e.Name), Name: e.Name, Rev: time.Now().UnixNano()})
}

// onRemoved removes the version of each deleted .crate, and the name of a crate that has no
// version left, while the rows are still readable (format.Spec OnRemoved); a crate with versions
// left gets a new revision. The index stops listing the version at once.
func (h *Handler) onRemoved(ctx context.Context, tx *sql.Tx, repo *repos.Repo, removed []*assets.Asset, _ []repopath.Path) ([]repopath.Path, error) {
	q := dbq.New(tx)
	for _, a := range removed {
		if !strings.HasPrefix(a.Path, "crates/") {
			continue
		}
		rows, err := q.DeleteVersionsOfAsset(ctx, a.ID)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			_, err := q.CrateName(ctx, dbq.CrateNameParams{RepoID: repo.ID, LowerName: r.LowerName})
			switch {
			case errors.Is(err, sql.ErrNoRows):
				if err := q.DeleteName(ctx, dbq.DeleteNameParams{RepoID: repo.ID, FoldedName: fold(r.Name)}); err != nil {
					return nil, err
				}
			case err != nil:
				return nil, err
			default:
				if err := q.BumpRev(ctx, dbq.BumpRevParams{At: time.Now().UnixNano(), RepoID: repo.ID, FoldedName: fold(r.Name)}); err != nil {
					return nil, err
				}
			}
		}
	}
	return nil, nil
}

// ui is what the web UI shows of Cargo repos.
var ui = format.UIDescriptor{
	DeleteNotes: []format.DeleteNote{{Match: "crates/*/*.crate", One: "version", Many: "versions",
		Text: "The index stops listing the deleted versions."}},
	RemotePresets: []format.RemotePreset{{Label: "crates.io", URL: "https://index.crates.io/"}},
	ProxyNote:     "The proxy serves the remote's index files unchanged; Cargo checks each crate against the checksum in them.",
	ArtifactKinds: map[string]string{"crate": "Crate"},
	ArtifactName:  map[string]string{"crate": "Crate"},
	SnippetTools:  map[string]string{"cargo": "Cargo", "telesphorepo": "Telesphorepo CLI"},
	BuildFiles:    []format.BuildFileLabel{{Extension: "crate", Classifier: "*", Label: "Crate"}},
}
