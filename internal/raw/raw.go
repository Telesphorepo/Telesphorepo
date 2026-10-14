// Package raw serves generic hosted files at /raw/{repo}/{path...}: any client that speaks
// HTTP PUT and GET (curl, wget, CI scripts) can publish and fetch files by path.
package raw

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"

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

// Usage returns curl commands to upload and download a file. An upload takes a publish session
// token (Publishing page); a download takes a password or read token.
func (f *Format) Usage(repo *repos.Repo, baseURL string) []format.Snippet {
	u := baseURL + "/raw/" + repo.Name + "/path/to/file.tar.gz"
	dl := "curl -fL -u USER:READ_TOKEN -O " + u
	if repo.AnonymousRead {
		dl = "curl -fL -O " + u
	}
	return []format.Snippet{
		{Title: "Upload (publish session token from the Publishing page)", Lang: "sh", Code: "curl -f -u publish:PUBLISH_TOKEN -T file.tar.gz " + u},
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
	c, err := repoConfig(repo)
	if err != nil {
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

	var uid int64
	if pr != nil {
		uid = pr.UserID
	}
	created, unchanged, err := f.put(r.Context(), repo, c, p, sp, uid)
	switch {
	case errors.Is(err, errExists):
		httpx.Error(w, http.StatusConflict, errExists.Error())
	case errors.Is(err, errLookup):
		httpx.Error(w, http.StatusInternalServerError, "lookup failed")
	case err != nil:
		httpx.Error(w, http.StatusInternalServerError, "store failed")
	case created:
		f.h.RecordUpload(r, pr, repo, p, sp.Size, sp.SHA256)
		w.WriteHeader(http.StatusCreated)
	default:
		if !unchanged {
			f.h.RecordUpload(r, pr, repo, p, sp.Size, sp.SHA256)
		}
		w.WriteHeader(http.StatusOK)
	}
}

// Upload implements format.ManualUploader: every file goes to the folder in the field "folder"
// ("" or missing: the repo root) under the repo's overwrite policy.
func (f *Format) Upload(ctx context.Context, repo *repos.Repo, files []format.UploadFile, fields map[string]string, userID int64) ([]format.Uploaded, error) {
	for k := range fields {
		if k != "folder" {
			return nil, format.BadUpload("unknown field %q; raw uploads take folder", k)
		}
	}
	folder := strings.Trim(strings.TrimSpace(fields["folder"]), "/")
	if folder != "" && !validPath(folder) {
		return nil, format.BadUpload("invalid folder %q", folder)
	}
	c, err := repoConfig(repo)
	if err != nil {
		return nil, err
	}
	out := make([]format.Uploaded, 0, len(files))
	for _, file := range files {
		p := path.Join(folder, file.Name)
		if !validPath(p) {
			return out, format.BadUpload("invalid file name %q", file.Name)
		}
		created, unchanged, err := f.put(ctx, repo, c, p, file.Spool, userID)
		if errors.Is(err, errExists) {
			return out, &format.UploadError{Status: http.StatusConflict, Message: p + ": " + errExists.Error()}
		}
		if err != nil {
			return out, err
		}
		st := format.UploadReplaced
		switch {
		case created:
			st = format.UploadCreated
		case unchanged:
			st = format.UploadUnchanged
		}
		out = append(out, format.Uploaded{Path: p, Status: st, Size: file.Spool.Size, SHA256: file.Spool.SHA256})
	}
	return out, nil
}

var (
	// errExists rejects different bytes at an existing path when the repo denies overwrite.
	errExists = errors.New("file exists and the repo does not allow overwrite")
	// errLookup wraps a failed lookup of the asset at the target path.
	errLookup = errors.New("lookup failed")
)

// repoConfig decodes the repo config, which was validated when it was stored.
func repoConfig(repo *repos.Repo) (Config, error) {
	var c Config
	err := json.Unmarshal(repo.Config, &c)
	return c, err
}

// put stores the spooled file at p and holds the path lock while it does. A file that exists
// with the same bytes stays as it is (unchanged). Different bytes replace it when c allows
// overwrite; otherwise put returns errExists. Uploads (PUT) and imports share it.
func (f *Format) put(ctx context.Context, repo *repos.Repo, c Config, p string, sp *assets.Spooled, uid int64) (created, unchanged bool, err error) {
	unlock := f.h.Assets.Lock(repo.ID, p)
	defer unlock()
	existing, err := f.h.Assets.Get(ctx, repo.ID, p)
	if err != nil && !errors.Is(err, assets.ErrNotFound) {
		return false, false, fmt.Errorf("%w: %w", errLookup, err)
	}
	if existing != nil && existing.SHA256 == sp.SHA256 {
		return false, true, nil
	}
	if existing != nil && c.Overwrite != OverwriteAllow {
		return false, false, errExists
	}
	_, created, err = f.h.Assets.Put(ctx, repo, p, sp, assets.ContentType(p), assets.KindArtifact, uid)
	return created, false, err
}
