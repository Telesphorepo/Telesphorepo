package debian

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/debian/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

const debContentType = "application/vnd.debian.binary-package"

type uploadResult struct {
	Package      string `json:"package"`
	Version      string `json:"version"`
	Architecture string `json:"architecture"`
	Filename     string `json:"filename"`
	SHA256       string `json:"sha256"`
}

var errConflict = errors.New("conflict")

// uploadError is a client error of an upload: the HTTP status and the message.
type uploadError struct {
	status int
	msg    string
}

func (e *uploadError) Error() string { return e.msg }

func (h *Handler) upload(w http.ResponseWriter, r *http.Request) {
	repo, p, ok := h.auth.RepoGate(w, r, r.PathValue("repo"), name, auth.Write, false)
	if !ok {
		return
	}
	dist, comp := r.PathValue("dist"), r.PathValue("component")
	if !repos.NameRE.MatchString(dist) || !repos.NameRE.MatchString(comp) {
		httpx.Error(w, http.StatusBadRequest, "invalid distribution or component name")
		return
	}
	max := h.cfg().MaxUploadBytes
	if r.ContentLength > max {
		httpx.Error(w, http.StatusRequestEntityTooLarge, "upload too large")
		return
	}
	sp, err := h.assets.Spool(http.MaxBytesReader(w, r.Body, max), max)
	if err != nil {
		var mbe *http.MaxBytesError
		switch {
		case errors.Is(err, assets.ErrTooLarge) || errors.As(err, &mbe):
			httpx.Error(w, http.StatusRequestEntityTooLarge, "upload too large")
		case errors.Is(err, io.ErrUnexpectedEOF):
			httpx.Error(w, http.StatusBadRequest, "incomplete request body")
		default:
			h.log.Error("spool debian upload", "repo", repo.Name, "err", err)
			httpx.Error(w, http.StatusInternalServerError, "internal error")
		}
		return
	}
	defer sp.Close()

	res, created, err := h.ingest(r.Context(), repo, dist, comp, sp, format.ByOf(p))
	var ue *uploadError
	switch {
	case errors.As(err, &ue):
		httpx.Error(w, ue.status, ue.msg)
	case err != nil:
		h.internal(w, "debian upload", repo, err)
	case created:
		h.host.RecordUpload(r, p, repo, res.Filename, sp.Size, sp.SHA256)
		httpx.WriteJSON(w, http.StatusCreated, res)
	default:
		httpx.WriteJSON(w, http.StatusOK, res)
	}
}

// ingest adds the spooled .deb to dist/comp of repo: it parses and validates the package,
// stores the pool file, records the asset, the deb_packages row and the dirty units in one
// transaction, and wakes the indexer. created is false when the same package version is
// already present with the same bytes. A client error is an *uploadError; any other error is
// internal. Uploads and imports share it.
func (h *Handler) ingest(ctx context.Context, repo *repos.Repo, dist, comp string, sp *assets.Spooled, by assets.By) (res uploadResult, created bool, err error) {
	info, err := parseDeb(sp.File)
	if err != nil {
		var bad badDeb
		if errors.As(err, &bad) {
			return res, false, &uploadError{http.StatusBadRequest, bad.Error()}
		}
		return res, false, fmt.Errorf("parse package: %w", err)
	}
	archs := architectures(repo)
	if info.Architecture != "all" && !slices.Contains(archs, info.Architecture) {
		return res, false, &uploadError{http.StatusBadRequest, "architecture " + info.Architecture + " is not configured for this repository"}
	}
	dirtyArchs := []string{info.Architecture}
	if info.Architecture == "all" {
		dirtyArchs = archs
	}

	pool := info.poolPath(comp)
	if !validKey(pool) {
		return res, false, &uploadError{http.StatusBadRequest, "package name, source or version too long"}
	}
	res = uploadResult{info.Package, info.Version, info.Architecture, pool, sp.SHA256}

	unlockID := h.assets.Lock(repo.ID, "deb:"+dist+"/"+comp+"/"+info.Package+"/"+info.Version+"/"+info.Architecture)
	defer unlockID()
	unlockPool := h.assets.Lock(repo.ID, pool)
	defer unlockPool()

	existing, err := h.q.GetPackageSHA256(ctx, dbq.GetPackageSHA256Params{RepoID: repo.ID, Distribution: dist,
		Component: comp, Architecture: info.Architecture, Package: info.Package, Version: info.Version})
	switch {
	case err == nil && existing == sp.SHA256:
		return res, false, nil
	case err == nil:
		return res, false, &uploadError{http.StatusConflict, "package version already exists with different content"}
	case !errors.Is(err, sql.ErrNoRows):
		return res, false, fmt.Errorf("look up deb package: %w", err)
	}

	if err := h.storePool(ctx, repo, pool, sp); err != nil {
		if errors.Is(err, errConflict) {
			return res, false, &uploadError{http.StatusConflict, "pool file " + pool + " already exists with different content"}
		}
		return res, false, fmt.Errorf("store deb blob: %w", err)
	}

	err = h.db.Tx(ctx, func(tx *sql.Tx) error {
		a := &assets.Asset{RepoID: repo.ID, Path: pool, Size: sp.Size, SHA256: sp.SHA256,
			ContentType: debContentType, Kind: assets.KindArtifact, UploadedBy: by.User, PublishSessionID: by.Session}
		if _, err := assets.UpsertTx(ctx, tx, a); err != nil {
			return err
		}
		q := dbq.New(tx)
		if err := q.InsertPackage(ctx, dbq.InsertPackageParams{RepoID: repo.ID, Distribution: dist, Component: comp,
			Architecture: info.Architecture, Package: info.Package, Version: info.Version, AssetID: a.ID,
			Filename: pool, Size: sp.Size, Md5: sp.MD5, Sha1: sp.SHA1, Sha256: sp.SHA256,
			Control: info.Control, CreatedAt: time.Now().Unix()}); err != nil {
			return err
		}
		mark := h.tick()
		for _, arch := range dirtyArchs {
			if err := q.MarkDirty(ctx, dbq.MarkDirtyParams{RepoID: repo.ID, Distribution: dist, Component: comp,
				Architecture: arch, MarkedAt: mark}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return res, false, fmt.Errorf("record deb package: %w", err)
	}
	h.idx.notify(distKey{repo.ID, dist})
	return res, true, nil
}

// Upload implements format.ManualUploader: each file is a .deb that goes through the upload
// pipeline into the fields "distribution" and "component" (default "main").
func (h *Handler) Upload(ctx context.Context, repo *repos.Repo, files []format.UploadFile, fields map[string]string, by assets.By) ([]format.Uploaded, error) {
	dist, comp := strings.TrimSpace(fields["distribution"]), strings.TrimSpace(fields["component"])
	if comp == "" {
		comp = "main"
	}
	if !repos.NameRE.MatchString(dist) || !repos.NameRE.MatchString(comp) {
		return nil, format.BadUpload("give a valid distribution (e.g. trixie) and component (e.g. main)")
	}
	for k := range fields {
		if k != "distribution" && k != "component" {
			return nil, format.BadUpload("unknown field %q; Debian uploads take distribution and component", k)
		}
	}
	if format.InFolder(files) {
		// A dropped folder: the .deb files in it, as Scan lists them.
		files = slices.DeleteFunc(slices.Clone(files), func(f format.UploadFile) bool { return !strings.HasSuffix(f.Name, ".deb") })
		if len(files) == 0 {
			return nil, format.BadUpload("the folder has no .deb file")
		}
	}
	for _, f := range files {
		if !strings.HasSuffix(f.Name, ".deb") {
			return nil, format.BadUpload("%s is not a .deb file", f.Name)
		}
	}
	out := make([]format.Uploaded, 0, len(files))
	for _, f := range files {
		res, created, err := h.ingest(ctx, repo, dist, comp, f.Spool, by)
		var ue *uploadError
		if errors.As(err, &ue) {
			return out, &format.UploadError{Status: ue.status, Message: f.Name + ": " + ue.msg}
		}
		if err != nil {
			return out, err
		}
		st := format.UploadCreated
		if !created {
			st = format.UploadUnchanged
		}
		out = append(out, format.Uploaded{Path: res.Filename, Status: st, Size: f.Spool.Size, SHA256: f.Spool.SHA256})
	}
	return out, nil
}

// Scan implements format.ManualUploader: one Found of kind "debian" per .deb file, titled
// "<package> <version> (<architecture>)". A control file that does not parse, or an
// architecture the repo does not configure, is a problem. Other files are ignored.
func (h *Handler) Scan(ctx context.Context, repo *repos.Repo, files []format.UploadFile) ([]format.Found, error) {
	archs := architectures(repo)
	var out []format.Found
	for _, f := range files {
		if !strings.HasSuffix(f.Name, ".deb") {
			continue
		}
		fd := format.Found{Kind: "debian", Title: f.Path, Files: []string{f.Path}}
		info, err := parseDeb(f.Spool.File)
		var bad badDeb
		switch {
		case errors.As(err, &bad):
			fd.Problems = append(fd.Problems, bad.Error())
		case err != nil:
			return nil, fmt.Errorf("parse %s: %w", f.Path, err)
		default:
			fd.Title = info.Package + " " + info.Version + " (" + info.Architecture + ")"
			if info.Architecture != "all" && !slices.Contains(archs, info.Architecture) {
				fd.Problems = append(fd.Problems, "architecture "+info.Architecture+" is not configured for this repository")
			}
		}
		out = append(out, fd)
	}
	return out, nil
}

// storePool writes the blob unless the pool key already holds identical content (so one
// .deb can be published in several dists); different content is errConflict.
func (h *Handler) storePool(ctx context.Context, repo *repos.Repo, pool string, sp *assets.Spooled) error {
	a, err := h.assets.Get(ctx, repo.ID, pool)
	switch {
	case err == nil && a.SHA256 == sp.SHA256:
		return nil
	case err == nil:
		return errConflict
	case !errors.Is(err, assets.ErrNotFound):
		return err
	}
	return h.assets.PutBlob(ctx, repo.Name, pool, sp, debContentType)
}

func (h *Handler) internal(w http.ResponseWriter, msg string, repo *repos.Repo, err error) {
	h.log.Error(msg, "repo", repo.Name, "err", err)
	httpx.Error(w, http.StatusInternalServerError, "internal error")
}
