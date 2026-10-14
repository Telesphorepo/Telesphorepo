// Package assets is the format-agnostic file layer: spooling uploads, writing blobs,
// the assets table, and serving downloads (spec §5, §8 GET/HEAD).
package assets

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"time"

	"telesphorepo/internal/config"
	"telesphorepo/internal/db"
	"telesphorepo/internal/repos"
	"telesphorepo/internal/storage"
)

var (
	ErrNotFound = errors.New("asset not found")
	ErrTooLarge = errors.New("upload too large")
)

const (
	KindArtifact = "artifact"
	KindMetadata = "metadata"
	KindIndex    = "index"
)

type Asset struct {
	ID          int64
	RepoID      int64
	Path        string
	Size        int64
	SHA256      string
	ContentType string
	Kind        string
	UploadedBy  int64 // 0 = none
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Spooled is an upload buffered to a temp file under cfg.TmpDir() with its hashes
// (lowercase hex) computed in one pass. Close removes the temp file.
type Spooled struct {
	File   *os.File
	Size   int64
	SHA256 string
	SHA1   string
	MD5    string
}

func (s *Spooled) Close() error { panic("TODO") }

// Reader returns a fresh reader positioned at offset 0.
func (s *Spooled) Reader() (io.ReadSeeker, error) { panic("TODO") }

type SearchHit struct {
	Asset    *Asset
	RepoName string
}

type Service struct {
	db  *db.DB
	st  storage.Store
	cfg *config.Config
}

func New(d *db.DB, st storage.Store, cfg *config.Config) *Service {
	return &Service{db: d, st: st, cfg: cfg}
}

func (s *Service) Store() storage.Store { return s.st }

// Key is the storage key for a repo-relative path.
func Key(repoName, p string) string { return path.Join("repos", repoName, p) }

// ContentType picks a Content-Type by extension (spec §8).
func ContentType(p string) string { panic("TODO") }

// Spool copies r to a temp file, failing with ErrTooLarge beyond max bytes.
func (s *Service) Spool(r io.Reader, max int64) (*Spooled, error) { panic("TODO") }

// Lock takes an in-process lock for (repoID, key) — key is any string, e.g. a path or a
// deb identity. Returns the unlock func.
func (s *Service) Lock(repoID int64, key string) (unlock func()) { panic("TODO") }

// Get is an indexed point lookup on (repo_id, path).
func (s *Service) Get(ctx context.Context, repoID int64, p string) (*Asset, error) { panic("TODO") }

// PutBlob writes the spooled file to storage at Key(repoName, p).
func (s *Service) PutBlob(ctx context.Context, repoName, p string, sp *Spooled, contentType string) error {
	panic("TODO")
}

// UpsertTx inserts or updates (size, sha256, content_type, updated_at, uploaded_by) the
// asset row keyed by (RepoID, Path) and sets a.ID. Returns created=true on insert.
func UpsertTx(ctx context.Context, tx *sql.Tx, a *Asset) (created bool, err error) { panic("TODO") }

// Put = PutBlob + UpsertTx in its own transaction. Caller holds Lock.
func (s *Service) Put(ctx context.Context, repo *repos.Repo, p string, sp *Spooled, contentType, kind string, uploadedBy int64) (a *Asset, created bool, err error) {
	panic("TODO")
}

// Serve answers GET/HEAD for an already-authorized request: 404 if no row; HEAD from the
// row (Content-Length, Content-Type, ETag "\"<sha256>\"", Last-Modified); GET via 302 to
// PresignGet when available, otherwise stream with http.ServeContent (range support) when
// the body is seekable, else io.Copy.
func (s *Service) Serve(w http.ResponseWriter, r *http.Request, repo *repos.Repo, p string) {
	panic("TODO")
}

// List: keyset by path within prefix range (path >= prefix AND path < prefix||x'FFFF'..),
// path > after. Returns next = last path when the page is full, else "".
func (s *Service) List(ctx context.Context, repoID int64, prefix, after string, limit int) ([]*Asset, string, error) {
	panic("TODO")
}

// Search: FTS5 trigram MATCH on assets_fts (q quoted as a phrase, len(q) >= 3), restricted to
// repoIDs when non-nil, optional single repo filter, keyset on asset id > after.
func (s *Service) Search(ctx context.Context, q string, repoIDs []int64, after int64, limit int) ([]SearchHit, int64, error) {
	panic("TODO")
}
