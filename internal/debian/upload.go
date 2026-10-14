package debian

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
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

func (h *Handler) upload(w http.ResponseWriter, r *http.Request) {
	repo, p, ok := h.auth.RepoGate(w, r, r.PathValue("repo"), repos.FormatDebian, auth.Write, false)
	if !ok {
		return
	}
	dist, comp := r.PathValue("dist"), r.PathValue("component")
	if !repos.NameRE.MatchString(dist) || !repos.NameRE.MatchString(comp) {
		httpx.Error(w, http.StatusBadRequest, "invalid distribution or component name")
		return
	}
	max := h.cfg.MaxUploadBytes
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

	info, err := parseDeb(sp.File)
	if err != nil {
		var bad badDeb
		if errors.As(err, &bad) {
			httpx.Error(w, http.StatusBadRequest, bad.Error())
		} else {
			h.log.Error("parse debian upload", "repo", repo.Name, "err", err)
			httpx.Error(w, http.StatusInternalServerError, "internal error")
		}
		return
	}
	archs := repo.Debian().Architectures
	if info.Architecture != "all" && !slices.Contains(archs, info.Architecture) {
		httpx.Error(w, http.StatusBadRequest, "architecture "+info.Architecture+" is not configured for this repository")
		return
	}
	dirtyArchs := []string{info.Architecture}
	if info.Architecture == "all" {
		dirtyArchs = archs
	}

	pool := info.poolPath(comp)
	if !validKey(pool) {
		httpx.Error(w, http.StatusBadRequest, "package name, source or version too long")
		return
	}
	res := uploadResult{info.Package, info.Version, info.Architecture, pool, sp.SHA256}
	var uploadedBy int64
	if p != nil {
		uploadedBy = p.UserID
	}

	ctx := r.Context()
	unlockID := h.assets.Lock(repo.ID, "deb:"+dist+"/"+comp+"/"+info.Package+"/"+info.Version+"/"+info.Architecture)
	defer unlockID()
	unlockPool := h.assets.Lock(repo.ID, pool)
	defer unlockPool()

	var existing string
	err = h.db.R.QueryRowContext(ctx, `SELECT sha256 FROM deb_packages
		WHERE repo_id = ? AND distribution = ? AND component = ? AND architecture = ? AND package = ? AND version = ?`,
		repo.ID, dist, comp, info.Architecture, info.Package, info.Version).Scan(&existing)
	switch {
	case err == nil && existing == sp.SHA256:
		httpx.WriteJSON(w, http.StatusOK, res)
		return
	case err == nil:
		httpx.Error(w, http.StatusConflict, "package version already exists with different content")
		return
	case !errors.Is(err, sql.ErrNoRows):
		h.internal(w, "look up deb package", repo, err)
		return
	}

	if err := h.storePool(ctx, repo, pool, sp); err != nil {
		if errors.Is(err, errConflict) {
			httpx.Error(w, http.StatusConflict, "pool file "+pool+" already exists with different content")
		} else {
			h.internal(w, "store deb blob", repo, err)
		}
		return
	}

	err = h.db.Tx(ctx, func(tx *sql.Tx) error {
		a := &assets.Asset{RepoID: repo.ID, Path: pool, Size: sp.Size, SHA256: sp.SHA256,
			ContentType: debContentType, Kind: assets.KindArtifact, UploadedBy: uploadedBy}
		if _, err := assets.UpsertTx(ctx, tx, a); err != nil {
			return err
		}
		now := time.Now().Unix()
		if _, err := tx.ExecContext(ctx, `INSERT INTO deb_packages
			(repo_id, distribution, component, architecture, package, version, asset_id, filename,
			 size, md5, sha1, sha256, control, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			repo.ID, dist, comp, info.Architecture, info.Package, info.Version, a.ID, pool,
			sp.Size, sp.MD5, sp.SHA1, sp.SHA256, info.Control, now); err != nil {
			return err
		}
		mark := h.tick()
		for _, arch := range dirtyArchs {
			if _, err := tx.ExecContext(ctx, `INSERT INTO deb_dirty (repo_id, distribution, component, architecture, marked_at)
				VALUES (?, ?, ?, ?, ?)
				ON CONFLICT (repo_id, distribution, component, architecture) DO UPDATE SET marked_at = excluded.marked_at`,
				repo.ID, dist, comp, arch, mark); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		h.internal(w, "record deb package", repo, err)
		return
	}
	h.idx.notify(distKey{repo.ID, dist})
	httpx.WriteJSON(w, http.StatusCreated, res)
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
