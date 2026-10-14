package debian

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/debian/dbq"
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

	var uploadedBy int64
	if p != nil {
		uploadedBy = p.UserID
	}
	res, created, err := h.ingest(r.Context(), repo, dist, comp, sp, uploadedBy)
	var ue *uploadError
	switch {
	case errors.As(err, &ue):
		httpx.Error(w, ue.status, ue.msg)
	case err != nil:
		h.internal(w, "debian upload", repo, err)
	case created:
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
func (h *Handler) ingest(ctx context.Context, repo *repos.Repo, dist, comp string, sp *assets.Spooled, uploadedBy int64) (res uploadResult, created bool, err error) {
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
			ContentType: debContentType, Kind: assets.KindArtifact, UploadedBy: uploadedBy}
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
