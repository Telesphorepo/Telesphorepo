package debian

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/debian/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
)

const debContentType = "application/vnd.debian.binary-package"

type uploadResult struct {
	Package      string `json:"package"`
	Version      string `json:"version"`
	Architecture string `json:"architecture"`
	Filename     string `json:"filename"`
	SHA256       string `json:"sha256"`
}

// debMeta is the Meta of the write of a .deb: where it goes and its parsed control data.
type debMeta struct {
	dist, comp string
	info       *debInfo
	sp         *assets.Spooled
}

func (h *Handler) upload(w http.ResponseWriter, r *http.Request, c *route.Call) {
	repo := c.Repo
	dist, comp := r.PathValue("dist"), r.PathValue("component")
	if !repos.NameRE.MatchString(dist) || !repos.NameRE.MatchString(comp) {
		httpx.Error(w, http.StatusBadRequest, "invalid distribution or component name")
		return
	}
	sp, ok := format.Kit{Assets: h.assets}.SpoolBody(w, r)
	if !ok {
		return
	}
	defer sp.Close()
	res, out, err := h.ingest(r.Context(), repo, dist, comp, sp, format.OriginOf(c.P, assets.SourcePut))
	switch {
	case err != nil:
		route.Fail(w, r, err)
	case res.Status != assets.Unchanged:
		httpx.WriteJSON(w, http.StatusCreated, out)
	default:
		httpx.WriteJSON(w, http.StatusOK, out)
	}
}

// readerAt returns the content of sp for the .deb parser.
func readerAt(sp *assets.Spooled) io.ReaderAt {
	if sp.File != nil {
		return sp.File
	}
	return bytes.NewReader(sp.Data)
}

// ingest adds the .deb of sp to dist/comp of repo: it parses and checks the package, then
// commits its pool file with the package identity as an extra lock. Under the locks, admit
// refuses a package version that exists with other bytes and a pool file that holds other
// bytes; onCommitted writes the deb_packages row and marks the units for a rebuild in the
// commit's transaction. The result is unchanged when the same package version is present with
// the same bytes. A client error is an *assets.Refusal. Uploads, imports and the demo data share
// it.
func (h *Handler) ingest(ctx context.Context, repo *repos.Repo, dist, comp string, sp *assets.Spooled, o assets.Origin) (*assets.Result, uploadResult, error) {
	var out uploadResult
	info, err := parseDeb(readerAt(sp))
	if err != nil {
		var bad badDeb
		if errors.As(err, &bad) {
			return nil, out, assets.Refuse(http.StatusBadRequest, "%s", bad.Error())
		}
		return nil, out, fmt.Errorf("parse package: %w", err)
	}
	if info.Architecture != "all" && !slices.Contains(architectures(repo), info.Architecture) {
		return nil, out, assets.Refuse(http.StatusBadRequest, "architecture %s is not configured for this repository", info.Architecture)
	}
	pool := info.poolPath(comp)
	p, err := repopath.Parse(pool)
	if err != nil || !validKey(pool) {
		return nil, out, assets.Refuse(http.StatusBadRequest, "package name, source or version too long")
	}
	out = uploadResult{info.Package, info.Version, info.Architecture, pool, sp.SHA256}
	id := "deb:" + dist + "/" + comp + "/" + info.Package + "/" + info.Version + "/" + info.Architecture
	res, err := h.assets.Commit(ctx, assets.Write{Repo: repo, Path: p, Body: assets.Body{Spool: sp},
		ContentType: debContentType, Kind: assets.KindArtifact, Origin: o, Locks: []string{id},
		Meta: &debMeta{dist: dist, comp: comp, info: info, sp: sp}})
	return res, out, err
}

// admit decides the write of a pool file (format.Spec Admit). A .deb of an upload keeps a
// package version that exists with the same bytes (unchanged), and refuses one with other bytes
// or a pool file that holds other bytes (409). A pool file that holds the same bytes is stored
// again, so the package goes into another distribution. A write without a parsed .deb comes only
// from the rebuild or a proxy fetch: it keeps the same bytes and replaces other ones. Every other
// write without a parsed .deb, such as a core upload session, is refused (400).
func (h *Handler) admit(ctx context.Context, w *assets.Write, existing *assets.Asset) (assets.Decision, error) {
	m, ok := w.Meta.(*debMeta)
	if !ok {
		if w.Origin.Source != assets.SourceGenerated && w.Origin.Source != assets.SourceProxy {
			return 0, assets.Refuse(http.StatusBadRequest, "Debian files are uploaded through /debian/{repo}/upload/{dist}/{component}")
		}
		if existing != nil && existing.SHA256 == w.Body.Digest() {
			return assets.Keep, nil
		}
		return assets.Store, nil
	}
	sum, err := h.q.GetPackageSHA256(ctx, dbq.GetPackageSHA256Params{RepoID: w.Repo.ID, Distribution: m.dist,
		Component: m.comp, Architecture: m.info.Architecture, Package: m.info.Package, Version: m.info.Version})
	switch {
	case err == nil && sum == w.Body.Digest():
		return assets.Keep, nil
	case err == nil:
		return 0, assets.Refuse(http.StatusConflict, "package version already exists with different content")
	case !errors.Is(err, sql.ErrNoRows):
		return 0, fmt.Errorf("look up deb package: %w", err)
	}
	if existing != nil && existing.SHA256 != w.Body.Digest() {
		return 0, assets.Refuse(http.StatusConflict, "pool file %s already exists with different content", w.Path)
	}
	return assets.Store, nil
}

// onCommitted writes the package of a stored .deb and marks its units for a rebuild, in the
// transaction of the write (format.Spec OnCommitted). An arch:all package marks every
// configured architecture.
func (h *Handler) onCommitted(ctx context.Context, tx *sql.Tx, w *assets.Write, a *assets.Asset, _ *assets.Asset) error {
	m, ok := w.Meta.(*debMeta)
	if !ok {
		return nil
	}
	info, sp := m.info, m.sp
	if err := dbq.New(tx).InsertPackage(ctx, dbq.InsertPackageParams{RepoID: w.Repo.ID, Distribution: m.dist, Component: m.comp,
		Architecture: info.Architecture, Package: info.Package, Version: info.Version, AssetID: a.ID,
		Filename: a.Path, Size: sp.Size, Md5: sp.MD5, Sha1: sp.SHA1, Sha256: sp.SHA256,
		Control: info.Control, CreatedAt: time.Now().Unix()}); err != nil {
		return err
	}
	archs := []string{info.Architecture}
	if info.Architecture == "all" {
		archs = architectures(w.Repo)
	}
	for _, arch := range archs {
		if err := h.rebuilds.MarkTx(ctx, tx, w.Repo.ID, m.dist, unit(m.comp, arch)); err != nil {
			return err
		}
	}
	return nil
}

// headBytes is how much of a .deb a plan reads. The control member comes right after the ar
// header; a package whose control member lies past these bytes is checked when the whole file
// arrives.
const headBytes = 1 << 20

// uploadFields are the fields of a web UI upload: the distribution and the component that the
// packages go into.
var uploadFields = []format.Field{
	{Key: "distribution", Label: "Distribution", Type: format.FieldString, Required: true, Placeholder: "trixie",
		Help: "The distribution that apt names, e.g. trixie."},
	{Key: "component", Label: "Component", Type: format.FieldString, Required: true, Placeholder: "main",
		Default: json.RawMessage(`"main"`)},
}

func (h *Handler) uploadSpec() *format.UploadSpec {
	return &format.UploadSpec{
		Single: true, Folder: true,
		Accept:  []string{".deb"},
		Hint:    "One or more .deb packages, or a folder of them.",
		Keep:    []string{"**/*.deb"},
		One:     "package",
		Many:    "packages",
		Note:    "The server reads each package's name, version and architecture, stores it under pool/, and rebuilds the apt indexes of the distribution.",
		Ignored: "Only .deb packages are uploaded.",
		Empty:   "The upload has no .deb package.",
		Fields:  uploadFields,
		Reads:   []format.ReadRule{{Glob: "**/*.deb", MaxBytes: headBytes}},
		Plan:    h.Plan,
		Store:   h.Store,
	}
}

// Plan plans a web UI upload: one item per .deb file, titled "<package> <version>
// (<architecture>)", that goes to its pool path in the fields "distribution" and "component". The
// open folder (dists/<distribution>/ or pool/<component>/) or the repo config gives their first
// values. A control file that does not parse, or an architecture the repo does not configure, is
// a problem. A package whose control member lies past the bytes the plan reads is checked at the
// upload. Other files are left out.
func (h *Handler) Plan(ctx context.Context, repo *repos.Repo, in format.PlanInput) (*format.Plan, error) {
	if err := format.CheckFields(in.Fields, uploadFields); err != nil {
		return nil, err
	}
	seg := strings.Split(in.Folder, "/")
	dist, okDist := in.Fields["distribution"]
	if !okDist {
		dist = importDistributionOf(repo)
		if seg[0] == "dists" && len(seg) > 1 && seg[1] != "" {
			dist = seg[1]
		}
	}
	comp, okComp := in.Fields["component"]
	if !okComp {
		comp = "main"
		if seg[0] == "pool" && len(seg) > 1 && seg[1] != "" {
			comp = seg[1]
		}
	}
	dist, comp = strings.TrimSpace(dist), strings.TrimSpace(comp)
	plan := &format.Plan{Fields: []format.PlanField{{Field: uploadFields[0], Value: dist}, {Field: uploadFields[1], Value: comp}}}
	if !repos.NameRE.MatchString(dist) {
		plan.Fields[0].Problem = "Enter a distribution, e.g. trixie."
	}
	if !repos.NameRE.MatchString(comp) {
		plan.Fields[1].Problem = "Enter a component, e.g. main."
	}
	archs := architectures(repo)
	for _, f := range in.Files {
		if !strings.HasSuffix(f.Name(), ".deb") {
			plan.Ignored = append(plan.Ignored, f.Path)
			continue
		}
		item := format.PlanItem{Title: f.Path, Files: []format.PlanTarget{{Path: f.Path}}}
		info, err := parseDeb(bytes.NewReader(f.Head))
		var bad badDeb
		switch {
		case errors.As(err, &bad) && f.Truncated():
			// The control member is past the bytes read: the upload parses the whole file.
		case errors.As(err, &bad):
			item.Problems = append(item.Problems, bad.Error())
		case err != nil:
			return nil, fmt.Errorf("parse %s: %w", f.Path, err)
		default:
			item.Title = info.Package + " " + info.Version + " (" + info.Architecture + ")"
			item.Facts = map[string]string{"package": info.Package, "version": info.Version, "architecture": info.Architecture}
			if info.Architecture != "all" && !slices.Contains(archs, info.Architecture) {
				item.Problems = append(item.Problems, "architecture "+info.Architecture+" is not configured for this repository")
			}
			if repos.NameRE.MatchString(comp) {
				item.Files[0].Target = info.poolPath(comp)
			}
		}
		plan.Items = append(plan.Items, item)
	}
	return plan, nil
}

// Store adds each package of a plan to the distribution and component of its fields (ingest).
func (h *Handler) Store(ctx context.Context, repo *repos.Repo, plan *format.Plan, files map[string]*assets.Spooled, o assets.Origin) ([]*assets.Result, error) {
	var dist, comp string
	for _, f := range plan.Fields {
		switch f.Key {
		case "distribution":
			dist = f.Value
		case "component":
			comp = f.Value
		}
	}
	var out []*assets.Result
	for _, it := range plan.Items {
		for _, t := range it.Files {
			sp := files[t.Path]
			if sp == nil {
				return out, format.BadUpload("the upload has no file %s", t.Path)
			}
			res, _, err := h.ingest(ctx, repo, dist, comp, sp, o)
			var ref *assets.Refusal
			if errors.As(err, &ref) {
				return out, assets.Refuse(ref.Status, "%s: %s", t.Path, ref.Message)
			}
			if err != nil {
				return out, err
			}
			out = append(out, res)
		}
	}
	return out, nil
}
