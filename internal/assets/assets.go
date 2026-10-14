// Package assets is the format-agnostic file layer: spooling uploads, writing blobs,
// the assets table, and serving downloads (spec §5, §8 GET/HEAD).
package assets

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

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

func (s *Spooled) Close() error {
	if s.File == nil {
		return nil
	}
	name := s.File.Name()
	s.File.Close()
	s.File = nil
	if err := os.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Reader returns a fresh reader positioned at offset 0.
func (s *Spooled) Reader() (io.ReadSeeker, error) {
	if s.File == nil {
		return nil, os.ErrClosed
	}
	if _, err := s.File.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return s.File, nil
}

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
func ContentType(p string) string {
	switch base := path.Base(p); base {
	case "Release", "InRelease", "Packages":
		return "text/plain"
	}
	switch strings.ToLower(path.Ext(p)) {
	case ".pom", ".xml":
		return "application/xml"
	case ".jar":
		return "application/java-archive"
	case ".module":
		return "application/json"
	case ".sha1", ".sha256", ".sha512", ".md5":
		return "text/plain"
	case ".deb":
		return "application/vnd.debian.binary-package"
	case ".gz":
		return "application/gzip"
	case ".asc", ".gpg":
		return "application/pgp-signature"
	}
	return "application/octet-stream"
}

// Spool copies r to a temp file, failing with ErrTooLarge beyond max bytes.
func (s *Service) Spool(r io.Reader, max int64) (*Spooled, error) {
	dir := s.cfg.TmpDir()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(dir, "upload-*")
	if err != nil {
		return nil, err
	}
	sp := &Spooled{File: f}
	h256, h1, h5 := sha256.New(), sha1.New(), md5.New()
	n, err := io.Copy(io.MultiWriter(f, h256, h1, h5), io.LimitReader(r, max+1))
	if err != nil {
		sp.Close()
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return nil, ErrTooLarge
		}
		return nil, err
	}
	if n > max {
		sp.Close()
		return nil, ErrTooLarge
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		sp.Close()
		return nil, err
	}
	sp.Size = n
	sp.SHA256 = hex.EncodeToString(h256.Sum(nil))
	sp.SHA1 = hex.EncodeToString(h1.Sum(nil))
	sp.MD5 = hex.EncodeToString(h5.Sum(nil))
	return sp, nil
}

type lockEntry struct {
	mu   sync.Mutex
	refs int
}

var (
	locksMu sync.Mutex
	locks   = map[string]*lockEntry{}
)

// Lock takes an in-process lock for (repoID, key) — key is any string, e.g. a path or a
// deb identity. Returns the unlock func.
func (s *Service) Lock(repoID int64, key string) (unlock func()) {
	k := strconv.FormatInt(repoID, 10) + "\x00" + key
	locksMu.Lock()
	e := locks[k]
	if e == nil {
		e = &lockEntry{}
		locks[k] = e
	}
	e.refs++
	locksMu.Unlock()
	e.mu.Lock()
	var once sync.Once
	return func() {
		once.Do(func() {
			e.mu.Unlock()
			locksMu.Lock()
			if e.refs--; e.refs == 0 {
				delete(locks, k)
			}
			locksMu.Unlock()
		})
	}
}

const assetCols = "id, repo_id, path, size, sha256, content_type, kind, uploaded_by, created_at, updated_at"

type scanner interface{ Scan(dest ...any) error }

func scanAsset(sc scanner, extra ...any) (*Asset, error) {
	a := &Asset{}
	var by sql.NullInt64
	var created, updated int64
	dest := append([]any{&a.ID, &a.RepoID, &a.Path, &a.Size, &a.SHA256, &a.ContentType, &a.Kind, &by, &created, &updated}, extra...)
	if err := sc.Scan(dest...); err != nil {
		return nil, err
	}
	a.UploadedBy = by.Int64
	a.CreatedAt, a.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
	return a, nil
}

// Get is an indexed point lookup on (repo_id, path).
func (s *Service) Get(ctx context.Context, repoID int64, p string) (*Asset, error) {
	a, err := scanAsset(s.db.R.QueryRowContext(ctx,
		"SELECT "+assetCols+" FROM assets WHERE repo_id = ? AND path = ?", repoID, p))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

// PutBlob writes the spooled file to storage at Key(repoName, p).
func (s *Service) PutBlob(ctx context.Context, repoName, p string, sp *Spooled, contentType string) error {
	rd, err := sp.Reader()
	if err != nil {
		return err
	}
	return s.st.Put(ctx, Key(repoName, p), rd, sp.Size, contentType)
}

// UpsertTx inserts or updates (size, sha256, content_type, updated_at, uploaded_by) the
// asset row keyed by (RepoID, Path) and sets a.ID. Returns created=true on insert.
// Path and kind are never changed on update (the FTS triggers rely on it).
func UpsertTx(ctx context.Context, tx *sql.Tx, a *Asset) (created bool, err error) {
	var existing int64
	err = tx.QueryRowContext(ctx, "SELECT id FROM assets WHERE repo_id = ? AND path = ?", a.RepoID, a.Path).Scan(&existing)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		created = true
	case err != nil:
		return false, err
	}
	var by any
	if a.UploadedBy != 0 {
		by = a.UploadedBy
	}
	now := time.Now().Unix()
	var ct, ut int64
	err = tx.QueryRowContext(ctx, `INSERT INTO assets (repo_id, path, size, sha256, content_type, kind, uploaded_by, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (repo_id, path) DO UPDATE SET size = excluded.size, sha256 = excluded.sha256,
  content_type = excluded.content_type, updated_at = excluded.updated_at, uploaded_by = excluded.uploaded_by
RETURNING id, kind, created_at, updated_at`,
		a.RepoID, a.Path, a.Size, a.SHA256, a.ContentType, a.Kind, by, now, now).Scan(&a.ID, &a.Kind, &ct, &ut)
	if err != nil {
		return false, err
	}
	a.CreatedAt, a.UpdatedAt = time.Unix(ct, 0).UTC(), time.Unix(ut, 0).UTC()
	return created, nil
}

// Put = PutBlob + UpsertTx in its own transaction. Caller holds Lock.
func (s *Service) Put(ctx context.Context, repo *repos.Repo, p string, sp *Spooled, contentType, kind string, uploadedBy int64) (a *Asset, created bool, err error) {
	if err := s.PutBlob(ctx, repo.Name, p, sp, contentType); err != nil {
		return nil, false, err
	}
	a = &Asset{RepoID: repo.ID, Path: p, Size: sp.Size, SHA256: sp.SHA256, ContentType: contentType, Kind: kind, UploadedBy: uploadedBy}
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		created, err = UpsertTx(ctx, tx, a)
		return err
	})
	if err != nil {
		return nil, false, err
	}
	return a, created, nil
}

func etagMatch(header, etag string) bool {
	for _, t := range strings.Split(header, ",") {
		t = strings.TrimPrefix(strings.TrimSpace(t), "W/")
		if t == "*" || t == etag {
			return true
		}
	}
	return false
}

// Serve answers GET/HEAD for an already-authorized request: 404 if no row; HEAD from the
// row (Content-Length, Content-Type, ETag "\"<sha256>\"", Last-Modified); GET via 302 to
// PresignGet when available, otherwise stream with http.ServeContent (range support) when
// the body is seekable, else io.Copy.
func (s *Service) Serve(w http.ResponseWriter, r *http.Request, repo *repos.Repo, p string) {
	ctx := r.Context()
	a, err := s.Get(ctx, repo.ID, p)
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		slog.Error("asset lookup failed", "repo", repo.Name, "path", p, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	etag := `"` + a.SHA256 + `"`
	h := w.Header()
	h.Set("ETag", etag)
	h.Set("Last-Modified", a.UpdatedAt.Format(http.TimeFormat))
	if inm := r.Header.Get("If-None-Match"); inm != "" && etagMatch(inm, etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if r.Method == http.MethodHead {
		h.Set("Content-Type", a.ContentType)
		h.Set("Content-Length", strconv.FormatInt(a.Size, 10))
		h.Set("Accept-Ranges", "bytes")
		w.WriteHeader(http.StatusOK)
		return
	}
	key := Key(repo.Name, p)
	if u, ok, err := s.st.PresignGet(ctx, key, s.cfg.PresignTTL); err != nil {
		slog.Error("presign failed", "repo", repo.Name, "path", p, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	} else if ok {
		h.Del("ETag")
		h.Del("Last-Modified")
		h.Set("Cache-Control", "no-store")
		h.Set("Location", u)
		w.WriteHeader(http.StatusFound)
		return
	}
	body, _, err := s.st.Get(ctx, key)
	if errors.Is(err, storage.ErrNotFound) {
		slog.Warn("asset row without blob", "repo", repo.Name, "path", p)
		h.Del("ETag")
		h.Del("Last-Modified")
		http.NotFound(w, r)
		return
	}
	if err != nil {
		slog.Error("storage get failed", "repo", repo.Name, "path", p, "err", err)
		h.Del("ETag")
		h.Del("Last-Modified")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer body.Close()
	h.Set("Content-Type", a.ContentType)
	if rs, ok := body.(io.ReadSeeker); ok {
		http.ServeContent(w, r, "", a.UpdatedAt, rs)
		return
	}
	h.Set("Content-Length", strconv.FormatInt(a.Size, 10))
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, body); err != nil {
		slog.Debug("asset stream aborted", "repo", repo.Name, "path", p, "err", err)
	}
}

// List: keyset by path within prefix range (path >= prefix AND path < prefix||x'FFFF'..),
// path > after. Returns next = last path when the page is full, else "".
func (s *Service) List(ctx context.Context, repoID int64, prefix, after string, limit int) ([]*Asset, string, error) {
	if limit <= 0 {
		limit = 50
	}
	q := "SELECT " + assetCols + " FROM assets WHERE repo_id = ?"
	args := []any{repoID}
	if prefix != "" {
		q += " AND path >= ? AND path < ?"
		args = append(args, prefix, prefix+"\U0010FFFF")
	}
	if after != "" {
		q += " AND path > ?"
		args = append(args, after)
	}
	q += " ORDER BY path LIMIT ?"
	args = append(args, limit+1)
	rows, err := s.db.R.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []*Asset
	for rows.Next() {
		a, err := scanAsset(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = out[limit-1].Path
	}
	return out, next, nil
}

// Search: FTS5 trigram MATCH on assets_fts (q quoted as a phrase, len(q) >= 3), restricted to
// repoIDs when non-nil, optional single repo filter, keyset on asset id > after.
// A non-nil empty repoIDs matches nothing.
func (s *Service) Search(ctx context.Context, q string, repoIDs []int64, after int64, limit int) ([]SearchHit, int64, error) {
	if utf8.RuneCountInString(q) < 3 || (repoIDs != nil && len(repoIDs) == 0) {
		return nil, 0, nil
	}
	if limit <= 0 {
		limit = 50
	}
	var b strings.Builder
	b.WriteString("SELECT a.id, a.repo_id, a.path, a.size, a.sha256, a.content_type, a.kind, a.uploaded_by, a.created_at, a.updated_at, r.name" +
		" FROM assets_fts f JOIN assets a ON a.id = f.rowid JOIN repos r ON r.id = a.repo_id" +
		" WHERE assets_fts MATCH ? AND f.rowid > ?")
	args := []any{`"` + strings.ReplaceAll(q, `"`, `""`) + `"`, after}
	if repoIDs != nil {
		b.WriteString(" AND a.repo_id IN (")
		for i, id := range repoIDs {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteByte('?')
			args = append(args, id)
		}
		b.WriteByte(')')
	}
	b.WriteString(" ORDER BY f.rowid LIMIT ?")
	args = append(args, limit)
	rows, err := s.db.R.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []SearchHit
	for rows.Next() {
		var h SearchHit
		if h.Asset, err = scanAsset(rows, &h.RepoName); err != nil {
			return nil, 0, err
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var next int64
	if len(out) == limit {
		next = out[len(out)-1].Asset.ID
	}
	return out, next, nil
}
