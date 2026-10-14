// Package maven serves hosted Maven repos at /maven/{repo}/{path...} (spec §8).
package maven

import (
	"errors"
	"net/http"
	"path"
	"strings"

	"telesphorepo/internal/assets"
	"telesphorepo/internal/auth"
	"telesphorepo/internal/config"
	"telesphorepo/internal/httpx"
)

type Handler struct {
	auth   *auth.Service
	assets *assets.Service
	cfg    *config.Config
}

func New(a *auth.Service, as *assets.Service, cfg *config.Config) *Handler {
	return &Handler{auth: a, assets: as, cfg: cfg}
}

func (h *Handler) Name() string { return "maven" }

// Register mounts GET/HEAD/PUT /maven/{repo}/{path...}.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /maven/{repo}/{path...}", h.handle)
	mux.HandleFunc("PUT /maven/{repo}/{path...}", h.handle)
}

func (h *Handler) handle(w http.ResponseWriter, r *http.Request) {
	p := r.PathValue("path")
	if !httpx.ValidRepoPath(r, p) {
		httpx.Error(w, http.StatusBadRequest, "invalid path")
		return
	}
	need := auth.Read
	if r.Method == http.MethodPut {
		need = auth.Write
	}
	repo, pr, ok := h.auth.RepoGate(w, r, r.PathValue("repo"), "maven", need, false)
	if !ok {
		return
	}
	if need == auth.Read {
		h.assets.Serve(w, r, repo, p)
		return
	}
	if r.ContentLength > h.cfg.MaxUploadBytes {
		httpx.Error(w, http.StatusRequestEntityTooLarge, "upload too large")
		return
	}
	sp, err := h.assets.Spool(http.MaxBytesReader(w, r.Body, h.cfg.MaxUploadBytes), h.cfg.MaxUploadBytes)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.Is(err, assets.ErrTooLarge) || errors.As(err, &mbe) {
			httpx.Error(w, http.StatusRequestEntityTooLarge, "upload too large")
		} else {
			httpx.Error(w, http.StatusInternalServerError, "upload failed")
		}
		return
	}
	defer sp.Close()

	unlock := h.assets.Lock(repo.ID, p)
	defer unlock()
	existing, err := h.assets.Get(r.Context(), repo.ID, p)
	if err != nil && !errors.Is(err, assets.ErrNotFound) {
		httpx.Error(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	if existing != nil && !mutable(p) {
		if existing.SHA256 == sp.SHA256 {
			w.WriteHeader(http.StatusOK)
			return
		}
		httpx.Error(w, http.StatusConflict, "release artifacts are immutable")
		return
	}
	kind := assets.KindArtifact
	if strings.HasPrefix(path.Base(p), "maven-metadata.xml") {
		kind = assets.KindMetadata
	}
	var uid int64
	if pr != nil {
		uid = pr.UserID
	}
	_, created, err := h.assets.Put(r.Context(), repo, p, sp, assets.ContentType(p), kind, uid)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "store failed")
		return
	}
	if created {
		w.WriteHeader(http.StatusCreated)
	} else {
		w.WriteHeader(http.StatusOK)
	}
}

func mutable(p string) bool {
	return strings.HasPrefix(path.Base(p), "maven-metadata.xml") || strings.HasSuffix(path.Base(path.Dir(p)), "-SNAPSHOT")
}
