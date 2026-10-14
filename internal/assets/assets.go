// Package assets is the format-agnostic file layer: spooling uploads, writing blobs,
// the assets table, and serving downloads (spec §5, §8 GET/HEAD).
package assets

import (
	"bytes"
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

	"github.com/Telesphorepo/Telesphorepo/internal/config"
	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/storage"
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
	cfg config.Provider
}

func New(d *db.DB, st storage.Store, cfg config.Provider) *Service {
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
	dir := s.cfg().TmpDir()
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

func fromRow(r dbq.Asset) *Asset {
	return &Asset{
		ID:          r.ID,
		RepoID:      r.RepoID,
		Path:        r.Path,
		Size:        r.Size,
		SHA256:      r.Sha256,
		ContentType: r.ContentType,
		Kind:        r.Kind,
		UploadedBy:  r.UploadedBy.Int64,
		CreatedAt:   time.Unix(r.CreatedAt, 0).UTC(),
		UpdatedAt:   time.Unix(r.UpdatedAt, 0).UTC(),
	}
}

// Get is an indexed point lookup on (repo_id, path).
func (s *Service) Get(ctx context.Context, repoID int64, p string) (*Asset, error) {
	r, err := dbq.New(s.db.R).GetAsset(ctx, dbq.GetAssetParams{RepoID: repoID, Path: p})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return fromRow(r), nil
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
	q := dbq.New(tx)
	_, err = q.GetAssetID(ctx, dbq.GetAssetIDParams{RepoID: a.RepoID, Path: a.Path})
	switch {
	case errors.Is(err, sql.ErrNoRows):
		created = true
	case err != nil:
		return false, err
	}
	now := time.Now().Unix()
	row, err := q.UpsertAsset(ctx, dbq.UpsertAssetParams{
		RepoID:      a.RepoID,
		Path:        a.Path,
		Size:        a.Size,
		Sha256:      a.SHA256,
		ContentType: a.ContentType,
		Kind:        a.Kind,
		UploadedBy:  sql.NullInt64{Int64: a.UploadedBy, Valid: a.UploadedBy != 0},
		CreatedAt:   now,
		UpdatedAt:   now,
	})
	if err != nil {
		return false, err
	}
	a.ID, a.Kind = row.ID, row.Kind
	a.CreatedAt, a.UpdatedAt = time.Unix(row.CreatedAt, 0).UTC(), time.Unix(row.UpdatedAt, 0).UTC()
	return created, nil
}

// Put = PutBlob + UpsertTx in its own transaction. Caller holds Lock.
// Once the body is spooled the store and upsert run detached from ctx cancellation (a client
// disconnect must not leave a replaced blob behind a stale row), and the upsert is retried.
func (s *Service) Put(ctx context.Context, repo *repos.Repo, p string, sp *Spooled, contentType, kind string, uploadedBy int64) (a *Asset, created bool, err error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), putTimeout)
	defer cancel()
	if err := s.PutBlob(ctx, repo.Name, p, sp, contentType); err != nil {
		return nil, false, err
	}
	a = &Asset{RepoID: repo.ID, Path: p, Size: sp.Size, SHA256: sp.SHA256, ContentType: contentType, Kind: kind, UploadedBy: uploadedBy}
	for attempt := 0; ; attempt++ {
		err = s.db.Tx(ctx, func(tx *sql.Tx) error {
			var err error
			created, err = UpsertTx(ctx, tx, a)
			return err
		})
		if err == nil {
			return a, created, nil
		}
		if attempt >= 2 || ctx.Err() != nil {
			slog.Error("asset upsert failed after blob write", "repo", repo.Name, "path", p, "err", err)
			return nil, false, err
		}
		time.Sleep(time.Duration(attempt+1) * 100 * time.Millisecond)
	}
}

// putTimeout bounds a detached store+upsert in Put.
const putTimeout = 5 * time.Minute

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
	// Repo content is publisher-controlled and served from the UI/API origin: never let a
	// browser sniff or render it as active content (e.g. XHTML-namespace .xml/.pom).
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	if a.ContentType != "text/plain" {
		h.Set("Content-Disposition", "attachment")
	}
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
	if u, ok, err := s.st.PresignGet(ctx, key, s.cfg().PresignTTL); err != nil {
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
	body, info, err := s.st.Get(ctx, key)
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
	size := a.Size
	if info.Size > 0 || a.Size == 0 {
		size = info.Size // the blob's real length, in case it diverged from the row
	}
	h.Set("Content-Length", strconv.FormatInt(size, 10))
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, body); err != nil {
		slog.Debug("asset stream aborted", "repo", repo.Name, "path", p, "err", err)
	}
}

// List: keyset by path within prefix range (path >= prefix AND path < prefixEnd(prefix)),
// path > after. Returns next = last path when the page is full, else "".
func (s *Service) List(ctx context.Context, repoID int64, prefix, after string, limit int) ([]*Asset, string, error) {
	if limit <= 0 {
		limit = 50
	}
	q := dbq.New(s.db.R)
	var rows []dbq.Asset
	var err error
	// One lower bound per query (see queries/assets.sql): the cursor when it is past the prefix.
	cursor := after != "" && after >= prefix
	lower, n := prefix, int64(limit)+1
	if cursor {
		lower = after
	}
	end, bounded := prefixEnd(prefix)
	switch {
	case bounded && cursor:
		rows, err = q.ListAssetsAfterRange(ctx, dbq.ListAssetsAfterRangeParams{RepoID: repoID, Lower: lower, Upper: end, Limit: n})
	case bounded:
		rows, err = q.ListAssetsFromRange(ctx, dbq.ListAssetsFromRangeParams{RepoID: repoID, Lower: lower, Upper: end, Limit: n})
	case cursor:
		rows, err = q.ListAssetsAfter(ctx, dbq.ListAssetsAfterParams{RepoID: repoID, Lower: lower, Limit: n})
	default:
		rows, err = q.ListAssetsFrom(ctx, dbq.ListAssetsFromParams{RepoID: repoID, Lower: lower, Limit: n})
	}
	if err != nil {
		return nil, "", err
	}
	out := make([]*Asset, len(rows))
	for i, r := range rows {
		out[i] = fromRow(r)
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = out[limit-1].Path
	}
	return out, next, nil
}

// Search: FTS5 trigram MATCH on assets_fts (q quoted as a phrase, len(q) >= 3), restricted to
// repoIDs when non-nil, keyset on asset id > after.
// A non-nil empty repoIDs matches nothing.
func (s *Service) Search(ctx context.Context, q string, repoIDs []int64, after int64, limit int) ([]SearchHit, int64, error) {
	if utf8.RuneCountInString(q) < 3 || (repoIDs != nil && len(repoIDs) == 0) {
		return nil, 0, nil
	}
	if limit <= 0 {
		limit = 50
	}
	match := `"` + strings.ReplaceAll(q, `"`, `""`) + `"`
	dq := dbq.New(s.db.R)
	var rows []dbq.SearchAssetsRow
	var err error
	if repoIDs == nil {
		rows, err = dq.SearchAssets(ctx, dbq.SearchAssetsParams{Match: match, After: after, Limit: int64(limit)})
	} else {
		rows, err = dq.SearchAssetsInRepos(ctx, dbq.SearchAssetsInReposParams{
			Match: match, After: after, RepoIDs: repoIDs, Limit: int64(limit)})
	}
	if err != nil {
		return nil, 0, err
	}
	out := make([]SearchHit, len(rows))
	for i, r := range rows {
		out[i] = SearchHit{Asset: fromRow(r.Asset), RepoName: r.RepoName}
	}
	var next int64
	if len(out) == limit {
		next = out[len(out)-1].Asset.ID
	}
	return out, next, nil
}

// Seek returns the first asset of repo whose path is >= lower (> lower when !inclusive) and
// < upper (no upper bound when upper is ""), or nil. It is one indexed seek on (repo_id, path).
func (s *Service) Seek(ctx context.Context, repoID int64, lower string, inclusive bool, upper string) (*Asset, error) {
	q := dbq.New(s.db.R)
	var rows []dbq.Asset
	var err error
	switch {
	case upper != "" && inclusive:
		rows, err = q.ListAssetsFromRange(ctx, dbq.ListAssetsFromRangeParams{RepoID: repoID, Lower: lower, Upper: upper, Limit: 1})
	case upper != "":
		rows, err = q.ListAssetsAfterRange(ctx, dbq.ListAssetsAfterRangeParams{RepoID: repoID, Lower: lower, Upper: upper, Limit: 1})
	case inclusive:
		rows, err = q.ListAssetsFrom(ctx, dbq.ListAssetsFromParams{RepoID: repoID, Lower: lower, Limit: 1})
	default:
		rows, err = q.ListAssetsAfter(ctx, dbq.ListAssetsAfterParams{RepoID: repoID, Lower: lower, Limit: 1})
	}
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return fromRow(rows[0]), nil
}

// FirstPaths returns up to limit paths of repo that start with prefix ("" for every path), in
// path order. Callers that delete what they get call it again for the next batch.
func (s *Service) FirstPaths(ctx context.Context, repoID int64, prefix string, limit int) ([]string, error) {
	q := dbq.New(s.db.R)
	var rows []dbq.Asset
	var err error
	if end, ok := PrefixEnd(prefix); ok {
		rows, err = q.ListAssetsFromRange(ctx, dbq.ListAssetsFromRangeParams{RepoID: repoID, Lower: prefix, Upper: end, Limit: int64(limit)})
	} else {
		rows, err = q.ListAssetsFrom(ctx, dbq.ListAssetsFromParams{RepoID: repoID, Lower: prefix, Limit: int64(limit)})
	}
	if err != nil {
		return nil, err
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Path
	}
	return out, nil
}

// DeleteTx deletes the asset row (repoID, p) in tx. deleted is false when there was none. The
// caller removes the blob after the commit (DeleteBlob).
func DeleteTx(ctx context.Context, tx *sql.Tx, repoID int64, p string) (deleted bool, err error) {
	n, err := dbq.New(tx).DeleteAsset(ctx, dbq.DeleteAssetParams{RepoID: repoID, Path: p})
	return n > 0, err
}

// DeleteBlob removes the blob of p from storage; a missing blob is not an error.
func (s *Service) DeleteBlob(ctx context.Context, repoName, p string) error {
	return s.st.Delete(ctx, Key(repoName, p))
}

// SpoolBytes spools b (a generated file, e.g. a checksum) like an upload.
func (s *Service) SpoolBytes(b []byte) (*Spooled, error) {
	return s.Spool(bytes.NewReader(b), int64(len(b)))
}

// PrefixEnd returns the smallest string greater than every string starting with prefix under
// byte-wise (SQLite BINARY) ordering; ok=false when there is none (prefix is "" or all 0xFF
// bytes).
func PrefixEnd(prefix string) (string, bool) { return prefixEnd(prefix) }

// prefixEnd returns the smallest string greater than every string starting with prefix under
// byte-wise (SQLite BINARY) ordering; ok=false when there is none (prefix is all 0xFF bytes).
func prefixEnd(prefix string) (string, bool) {
	b := []byte(prefix)
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] != 0xFF {
			b[i]++
			return string(b[:i+1]), true
		}
	}
	return "", false
}
