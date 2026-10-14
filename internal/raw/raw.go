// Package raw serves generic hosted files at /raw/{repo}/{path...}: any client that speaks
// HTTP PUT and GET (curl, wget, CI scripts) can publish and fetch files by path.
package raw

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

const name = "raw"

// Overwrite policies of the repo config.
const (
	OverwriteDeny  = "deny"  // re-upload of identical bytes is 200, different bytes 409
	OverwriteAllow = "allow" // a re-upload replaces the file
)

// Config is the repo config of a raw repo.
type Config struct {
	Overwrite string `json:"overwrite"`
}

type Format struct {
	h format.Host
}

func New() format.Format { return &Format{} }

func (f *Format) Name() string  { return name }
func (f *Format) Title() string { return "Raw files" }

func (f *Format) DefaultConfig() json.RawMessage {
	return json.RawMessage(`{"overwrite":"deny"}`)
}

// ValidateConfig accepts {"overwrite": "allow"|"deny"}; a missing overwrite is "deny".
func (f *Format) ValidateConfig(raw json.RawMessage) (json.RawMessage, error) {
	var c Config
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("invalid raw config: %w", err)
	}
	switch c.Overwrite {
	case "":
		c.Overwrite = OverwriteDeny
	case OverwriteDeny, OverwriteAllow:
	default:
		return nil, fmt.Errorf("overwrite must be %q or %q", OverwriteAllow, OverwriteDeny)
	}
	return json.Marshal(c)
}

// Register mounts GET/HEAD/PUT /raw/{repo}/{path...}. The GET pattern also matches HEAD.
func (f *Format) Register(mux *http.ServeMux, h format.Host) {
	f.h = h
	mux.HandleFunc("GET /raw/{repo}/{path...}", f.handle)
	mux.HandleFunc("PUT /raw/{repo}/{path...}", f.handle)
}

// Usage returns curl commands to upload and download a file.
func (f *Format) Usage(repo *repos.Repo, baseURL string) []format.Snippet {
	u := baseURL + "/raw/" + repo.Name + "/path/to/file.tar.gz"
	dl := "curl -fL -u USER:TOKEN -O " + u
	if repo.AnonymousRead {
		dl = "curl -fL -O " + u
	}
	return []format.Snippet{
		{Title: "Upload", Lang: "sh", Code: "curl -f -u USER:TOKEN -T file.tar.gz " + u},
		{Title: "Download", Lang: "sh", Code: dl},
	}
}

func (f *Format) handle(w http.ResponseWriter, r *http.Request) {
	p := r.PathValue("path")
	if !httpx.ValidRepoPath(r, p) {
		httpx.Error(w, http.StatusBadRequest, "invalid path")
		return
	}
	need := auth.Read
	if r.Method == http.MethodPut {
		need = auth.Write
	}
	repo, pr, ok := f.h.Auth.RepoGate(w, r, r.PathValue("repo"), name, need, false)
	if !ok {
		return
	}
	if need == auth.Read {
		f.h.Assets.Serve(w, r, repo, p)
		return
	}
	var c Config
	if err := json.Unmarshal(repo.Config, &c); err != nil {
		f.h.Log.Error("bad repo config", "repo", repo.Name, "err", err)
		httpx.Error(w, http.StatusInternalServerError, "bad repo config")
		return
	}
	limit := f.h.Config().MaxUploadBytes
	if r.ContentLength > limit {
		httpx.Error(w, http.StatusRequestEntityTooLarge, "upload too large")
		return
	}
	sp, err := f.h.Assets.Spool(http.MaxBytesReader(w, r.Body, limit), limit)
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

	unlock := f.h.Assets.Lock(repo.ID, p)
	defer unlock()
	existing, err := f.h.Assets.Get(r.Context(), repo.ID, p)
	if err != nil && !errors.Is(err, assets.ErrNotFound) {
		httpx.Error(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	if existing != nil && existing.SHA256 == sp.SHA256 {
		w.WriteHeader(http.StatusOK)
		return
	}
	if existing != nil && c.Overwrite != OverwriteAllow {
		httpx.Error(w, http.StatusConflict, "file exists and the repo does not allow overwrite")
		return
	}
	var uid int64
	if pr != nil {
		uid = pr.UserID
	}
	_, created, err := f.h.Assets.Put(r.Context(), repo, p, sp, assets.ContentType(p), assets.KindArtifact, uid)
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
