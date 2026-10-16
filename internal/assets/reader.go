package assets

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/access"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/blobs"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
	"github.com/Telesphorepo/Telesphorepo/internal/storage"
)

var (
	// ErrRemoteMissing is a path that the remote of a proxy does not have.
	ErrRemoteMissing = errors.New("the remote repository does not have this file")
	// ErrRemoteFailed is a remote that did not answer for a path with no cached copy.
	ErrRemoteFailed = errors.New("the remote repository did not answer")
)

// Fetcher fetches the files of proxy repos (internal/proxy): it returns the cached asset of path
// when it is fresh, else the one the remote sends now (stored through Commit with SourceProxy).
type Fetcher interface {
	Ensure(ctx context.Context, repo *repos.Repo, path repopath.Path, c Class) (*Asset, error)
}

// maxMergeBytes is the largest file of a group member that a merge reads.
const maxMergeBytes = 4 << 20

// Reader is the read path of hosted, proxy and group repos.
type Reader struct {
	p      *Pipeline
	repos  *repos.Service
	access *access.Engine
	remote Fetcher
	ttl    func() time.Duration // the presign lifetime of an S3 redirect
	log    *slog.Logger
}

// NewReader returns the read path. ttl returns the lifetime of a presigned S3 redirect.
func NewReader(p *Pipeline, rs *repos.Service, e *access.Engine, ttl func() time.Duration, log *slog.Logger) *Reader {
	return &Reader{p: p, repos: rs, access: e, ttl: ttl, log: log}
}

// SetFetcher gives the reader the proxy fetcher. It runs before the server serves.
func (rd *Reader) SetFetcher(f Fetcher) { rd.remote = f }

// Serve answers a GET or HEAD of path in repo c.Repo, which the route policy authorized:
//
//   - hosted: the asset, or 404.
//   - proxy: the format's Classify (a refused path is 404), then the remote fetch, then the asset.
//   - group: the members in read order that the caller may read; the first that has the file
//     serves it (a proxy member fetches only when no member before it has the file). With a
//     format Merger that matches the path and two or more members that have it, the server
//     merges the members' files and serves the merged bytes. No member: 404, or 502 when a
//     proxy member's remote failed.
func (rd *Reader) Serve(w http.ResponseWriter, r *http.Request, c *route.Call, path repopath.Path) {
	ctx := r.Context()
	repo := c.Repo
	switch repo.Kind {
	case repos.KindGroup:
		rd.serveGroup(w, r, c.P, repo, path)
		return
	}
	a, err := rd.has(ctx, repo, path)
	switch {
	case errors.Is(err, ErrRemoteFailed):
		httpx.Error(w, http.StatusBadGateway, "The remote repository did not answer.")
	case err != nil:
		rd.log.Error("asset read failed", "repo", repo.Name, "path", path, "err", err)
		httpx.Error(w, http.StatusInternalServerError, "internal error")
	case a == nil:
		httpx.Error(w, http.StatusNotFound, "not found")
	default:
		rd.serveAsset(w, r, a)
	}
}

// Find returns the asset of path in a hosted or proxy repo: nil when there is none. A proxy
// fetches it from its remote (ErrRemoteFailed when the remote fails with nothing cached).
func (rd *Reader) Find(ctx context.Context, repo *repos.Repo, path repopath.Path) (*Asset, error) {
	return rd.has(ctx, repo, path)
}

func (rd *Reader) has(ctx context.Context, repo *repos.Repo, path repopath.Path) (*Asset, error) {
	if repo.Kind == repos.KindProxy {
		hooks := rd.p.Hooks(repo.Format)
		if hooks.Classify == nil || rd.remote == nil {
			return nil, nil
		}
		class, ok := hooks.Classify(path)
		if !ok {
			return nil, nil
		}
		a, err := rd.remote.Ensure(ctx, repo, path, class)
		if errors.Is(err, ErrRemoteMissing) {
			return nil, nil
		}
		return a, err
	}
	a, err := rd.p.Get(ctx, repo.ID, string(path))
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return a, err
}

func (rd *Reader) serveGroup(w http.ResponseWriter, r *http.Request, p *auth.Principal, group *repos.Repo, path repopath.Path) {
	ctx := r.Context()
	members, err := rd.repos.MemberRepos(ctx, group)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "internal error")
		return
	}
	readable := members[:0]
	for _, m := range members {
		ok, err := rd.access.Allows(ctx, r, p, m, access.Read, perm.RepoDownload)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "internal error")
			return
		}
		if ok {
			readable = append(readable, m)
		}
	}
	if mg := rd.p.Hooks(group.Format).Merge; mg != nil {
		if base, checksum, ok := mg.Match(path); ok {
			rd.serveMerged(w, r, group, readable, mg, path, base, checksum)
			return
		}
	}
	remoteFailed := false
	for _, m := range readable {
		a, err := rd.has(ctx, m, path)
		switch {
		case errors.Is(err, ErrRemoteFailed):
			remoteFailed = true
		case err != nil:
			rd.log.Error("group read failed", "group", group.Name, "member", m.Name, "path", path, "err", err)
			httpx.Error(w, http.StatusInternalServerError, "internal error")
			return
		case a != nil:
			rd.serveAsset(w, r, a)
			return
		}
	}
	notInGroup(w, remoteFailed)
}

func notInGroup(w http.ResponseWriter, remoteFailed bool) {
	if remoteFailed {
		httpx.Error(w, http.StatusBadGateway, "The remote repository did not answer.")
		return
	}
	httpx.Error(w, http.StatusNotFound, "not found")
}

// checksums are the checksum names of Merger.Match.
var checksums = map[string]func() hash.Hash{"md5": md5.New, "sha1": sha1.New, "sha256": sha256.New, "sha512": sha512.New}

// serveMerged answers path of a group, whose base file the format merges. With one member that
// has the base file, that member's file (or its checksum file, when it has one) serves; with more,
// the server merges them. A checksum path is the checksum of the bytes the base path serves.
func (rd *Reader) serveMerged(w http.ResponseWriter, r *http.Request, group *repos.Repo, members []*repos.Repo, mg *Merger,
	path, base repopath.Path, checksum string) {
	ctx := r.Context()
	type source struct {
		repo *repos.Repo
		a    *Asset
	}
	var srcs []source
	remoteFailed := false
	for _, m := range members {
		a, err := rd.has(ctx, m, base)
		switch {
		case errors.Is(err, ErrRemoteFailed):
			remoteFailed = true
		case err != nil:
			httpx.Error(w, http.StatusInternalServerError, "internal error")
			return
		case a != nil:
			srcs = append(srcs, source{m, a})
		}
	}
	if len(srcs) == 0 {
		notInGroup(w, remoteFailed)
		return
	}
	if len(srcs) == 1 {
		if checksum == "" {
			rd.serveAsset(w, r, srcs[0].a)
			return
		}
		if a, err := rd.has(ctx, srcs[0].repo, path); err == nil && a != nil {
			rd.serveAsset(w, r, a)
			return
		}
	}
	var parts [][]byte
	var modified time.Time
	for _, s := range srcs {
		b, err := rd.p.ReadAll(ctx, s.a, maxMergeBytes)
		if err != nil {
			rd.log.Warn("group: read member file", "member", s.repo.Name, "path", base, "err", err)
			continue
		}
		parts = append(parts, b)
		if s.a.UpdatedAt.After(modified) {
			modified = s.a.UpdatedAt
		}
	}
	if len(parts) == 0 {
		httpx.Error(w, http.StatusNotFound, "not found")
		return
	}
	data := parts[0]
	if len(parts) > 1 {
		var err error
		if data, err = mg.Merge(ctx, base, parts); err != nil {
			rd.log.Warn("group: merge", "group", group.Name, "path", base, "err", err)
			httpx.Error(w, http.StatusNotFound, "not found")
			return
		}
	}
	ct := FallbackContentType
	if d := rd.p.Hooks(group.Format).Describe; d != nil {
		if desc, ok := d(base); ok {
			ct = desc.ContentType
		}
	}
	if checksum != "" {
		h := checksums[checksum]
		if h == nil {
			httpx.Error(w, http.StatusNotFound, "not found")
			return
		}
		sum := h()
		sum.Write(data)
		data, ct = []byte(hex.EncodeToString(sum.Sum(nil))), "text/plain"
	}
	etag := sha256.Sum256(data)
	if contentHeaders(w, r, ct, `"`+hex.EncodeToString(etag[:])+`"`, modified) {
		return
	}
	http.ServeContent(w, r, "", modified, bytes.NewReader(data))
}

// inline reports whether a browser may show content of type ct: plain text only.
func inline(ct string) bool {
	mt, _, err := mime.ParseMediaType(ct)
	return err == nil && mt == "text/plain"
}

// contentHeaders sets every content header of a download: Content-Type, ETag, Last-Modified,
// nosniff, a sandbox CSP and Content-Disposition attachment except for plain text. It answers
// 304 to a matching If-None-Match and returns true then.
func contentHeaders(w http.ResponseWriter, r *http.Request, ct, etag string, modified time.Time) bool {
	h := w.Header()
	// Repo content is publisher-controlled and served from the UI/API origin: never let a
	// browser sniff or render it as active content (e.g. XHTML-namespace .xml/.pom).
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	if !inline(ct) {
		h.Set("Content-Disposition", "attachment")
	}
	h.Set("Content-Type", ct)
	h.Set("ETag", etag)
	if !modified.IsZero() {
		h.Set("Last-Modified", modified.UTC().Format(http.TimeFormat))
	}
	if inm := r.Header.Get("If-None-Match"); inm != "" && etagMatch(inm, etag) {
		w.WriteHeader(http.StatusNotModified)
		return true
	}
	return false
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

// serveAsset answers a GET or HEAD of a. A GET redirects to a presigned URL of the blob when the
// store has one (s3.redirect): the URL signs the response content type and disposition of a, so
// each path of a shared blob gets its own. Otherwise it streams the blob (with ranges).
func (rd *Reader) serveAsset(w http.ResponseWriter, r *http.Request, a *Asset) {
	ctx := r.Context()
	if contentHeaders(w, r, a.ContentType, `"`+a.SHA256+`"`, a.UpdatedAt) {
		return
	}
	h := w.Header()
	if r.Method == http.MethodHead {
		h.Set("Content-Length", strconv.FormatInt(a.Size, 10))
		h.Set("Accept-Ranges", "bytes")
		w.WriteHeader(http.StatusOK)
		return
	}
	key := blobs.Key(a.SHA256)
	disposition := ""
	if !inline(a.ContentType) {
		disposition = "attachment"
	}
	st := rd.p.blobs.Storage()
	if u, ok, err := st.PresignGet(ctx, key, rd.ttl(), storage.Response{ContentType: a.ContentType, ContentDisposition: disposition}); err != nil {
		rd.log.Error("presign failed", "key", key, "err", err)
		httpx.Error(w, http.StatusInternalServerError, "internal error")
		return
	} else if ok {
		h.Del("Last-Modified")
		h.Set("Cache-Control", "no-store")
		h.Set("Location", u)
		w.WriteHeader(http.StatusFound)
		return
	}
	body, info, err := st.Get(ctx, key)
	if err != nil {
		h.Del("ETag")
		h.Del("Last-Modified")
		if errors.Is(err, storage.ErrNotFound) {
			rd.log.Warn("asset row without blob", "repo_id", a.RepoID, "path", a.Path)
			httpx.Error(w, http.StatusNotFound, "not found")
			return
		}
		rd.log.Error("storage get failed", "repo_id", a.RepoID, "path", a.Path, "err", err)
		httpx.Error(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer body.Close()
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
		rd.log.Debug("asset stream aborted", "repo_id", a.RepoID, "path", a.Path, "err", err)
	}
}
