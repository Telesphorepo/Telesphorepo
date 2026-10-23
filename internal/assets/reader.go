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
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/access"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/blobs"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
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
// live, when not nil, is the client of the request: a fetch of a file that is not cached may
// send the remote's bytes to it while it stores them (Live).
type Fetcher interface {
	Ensure(ctx context.Context, repo *repos.Repo, path repopath.Path, c Class, live *Live) (*Asset, error)
}

const (
	maxMergeBytes = 4 << 20   // the largest file of a group member that a merge reads
	mergedEntries = 1024      // merged files kept in memory
	mergedBytes   = 128 << 20 // the bytes of the merged files kept in memory
)

// Reader is the read path of hosted, proxy and group repos.
type Reader struct {
	p      *Pipeline
	repos  *repos.Service
	access *access.Engine
	remote Fetcher
	ttl    func() time.Duration // the presign lifetime of an S3 redirect
	log    *slog.Logger
	merged *mergeCache
	count  func(repo, bytes int64) // counts a download (SetDownloads); nil counts nothing
}

// NewReader returns the read path. ttl returns the lifetime of a presigned S3 redirect.
func NewReader(p *Pipeline, rs *repos.Service, e *access.Engine, ttl func() time.Duration, log *slog.Logger) *Reader {
	return &Reader{p: p, repos: rs, access: e, ttl: ttl, log: log, merged: newMergeCache(mergedEntries, mergedBytes)}
}

// SetDownloads gives the reader the download counter: count(repo, bytes) runs once per GET that
// gets a file's bytes (a 200, a 206 or an S3 redirect from ServeAsset, a live proxy stream once it
// completed, a merged group file). HEAD, 304 and failed answers do not count. It runs before the
// server serves.
func (rd *Reader) SetDownloads(count func(repo, bytes int64)) { rd.count = count }

// counted counts one download of bytes from repo.
func (rd *Reader) counted(repo, bytes int64) {
	if rd.count != nil {
		rd.count(repo, bytes)
	}
}

// SetFetcher gives the reader the proxy fetcher. It runs before the server serves.
func (rd *Reader) SetFetcher(f Fetcher) { rd.remote = f }

// Serve answers a GET or HEAD of path in repo c.Repo, which the route policy authorized:
//
//   - hosted: the asset, or 404.
//   - proxy: the format's Classify (a refused path is 404), then the remote fetch, then the asset.
//     A GET of a file that is not cached gets the remote's bytes while the proxy stores them
//     (Live).
//   - group: the members in read order that the caller may read; the first that has the file
//     serves it (a proxy member fetches only when no member before it has the file; see
//     ResolveLive). With a format Merger that matches the path and two or more members that have
//     it, the server merges the members' files and serves the merged bytes. No member: 404, or
//     502 when a proxy member's remote failed.
func (rd *Reader) Serve(w http.ResponseWriter, r *http.Request, c *route.Call, path repopath.Path) {
	repo := c.Repo
	if repo.Kind == repos.KindGroup {
		if mg := rd.p.Hooks(repo.Format).Merge; mg != nil {
			if base, checksum, ok := mg.Match(path); ok {
				members, err := rd.access.GroupMembers(r.Context(), r, c.P, repo)
				if err != nil {
					httpx.Error(w, r, http.StatusInternalServerError, "internal error")
					return
				}
				rd.serveMerged(w, r, repo, members, mg, path, base, checksum)
				return
			}
		}
	}
	live := rd.Live(w, r, nil)
	a, err := rd.ResolveLive(r.Context(), r, c.P, repo, path, live)
	if live.Done() {
		return
	}
	switch {
	case errors.Is(err, ErrRemoteFailed):
		httpx.Error(w, r, http.StatusBadGateway, "The remote repository did not answer.")
	case err != nil:
		rd.log.Error("asset read failed", "repo", repo.Name, "path", path, "err", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal error")
	case a == nil:
		httpx.Error(w, r, http.StatusNotFound, "not found")
	default:
		rd.ServeAsset(w, r, a, ServeOptions{})
	}
}

// Resolve returns the asset of path in repo for p, who reads through r: in a hosted repo the
// stored asset; in a proxy the cached file, or the one its remote sends now; in a group the
// first member in read order that p may read and that has the file (a proxy member fetches only
// when no member before it has the file). nil when there is none. ErrRemoteFailed: a proxy's
// remote failed and nothing else has the file.
func (rd *Reader) Resolve(ctx context.Context, r *http.Request, p *auth.Principal, repo *repos.Repo, path repopath.Path) (*Asset, error) {
	return rd.ResolveLive(ctx, r, p, repo, path, nil)
}

// ResolveLive is Resolve for a request whose client is live (Reader.Live; nil: none): a proxy
// that fetches a file that is not cached sends the remote's bytes to it while it stores them.
// The caller checks live.Done before it answers.
//
// In a group, the stored copies come first as far as the answer is the same as the read in
// order (storedFirst): a path that names its bytes (Class.Addressed) serves from any member that
// stores it before a proxy member asks its remote.
func (rd *Reader) ResolveLive(ctx context.Context, r *http.Request, p *auth.Principal, repo *repos.Repo, path repopath.Path, live *Live) (*Asset, error) {
	if repo.Kind != repos.KindGroup {
		return rd.has(ctx, repo, path, live)
	}
	members, err := rd.access.GroupMembers(ctx, r, p, repo)
	if err != nil {
		return nil, err
	}
	from, a, err := rd.storedFirst(ctx, members, path)
	if err != nil || a != nil {
		return a, err
	}
	remoteFailed := false
	for _, m := range members[from:] {
		a, err := rd.has(ctx, m, path, live)
		if live != nil && live.started {
			return a, err // the member's answer went out: no other member serves
		}
		switch {
		case errors.Is(err, ErrRemoteFailed):
			remoteFailed = true
		case err != nil:
			return nil, err
		case a != nil:
			return a, nil
		}
	}
	if remoteFailed {
		return nil, ErrRemoteFailed
	}
	return nil, nil
}

// Owner returns the member of group that owns a package name for p, who reads through r: the
// first member in read order that p may read and for which has reports the name. A format whose
// group renders documents from its members (npm, PyPI, Cargo, NuGet) serves every version of the
// name from that member alone, so a later member cannot shadow it. A member whose has fails is
// logged and skipped. nil when no member has the name; ErrRemoteFailed when, in addition, a
// member failed with it (a proxy whose remote is down with nothing cached).
func (rd *Reader) Owner(ctx context.Context, r *http.Request, p *auth.Principal, group *repos.Repo,
	has func(ctx context.Context, member *repos.Repo) (bool, error)) (*repos.Repo, error) {
	members, err := rd.access.GroupMembers(ctx, r, p, group)
	if err != nil {
		return nil, err
	}
	remoteFailed := false
	for _, m := range members {
		ok, err := has(ctx, m)
		switch {
		case ctx.Err() != nil:
			return nil, ctx.Err() // the client went away: no member failed
		case err != nil:
			if errors.Is(err, ErrRemoteFailed) {
				remoteFailed = true
			}
			rd.log.Warn("group: member skipped", "group", group.Name, "member", m.Name, "err", err)
		case ok:
			return m, nil
		}
	}
	if remoteFailed {
		return nil, ErrRemoteFailed
	}
	return nil, nil
}

// Stored returns the stored asset of path in repo for p, who reads through r, and never
// contacts a remote: a hosted file, a proxy's cached file, or in a group the first member in read
// order that p may read and that stores the file. nil when there is none.
func (rd *Reader) Stored(ctx context.Context, r *http.Request, p *auth.Principal, repo *repos.Repo, path repopath.Path) (*Asset, error) {
	list := []*repos.Repo{repo}
	if repo.Kind == repos.KindGroup {
		var err error
		if list, err = rd.access.GroupMembers(ctx, r, p, repo); err != nil {
			return nil, err
		}
	}
	for _, m := range list {
		a, err := rd.p.Get(ctx, m.ID, string(path))
		if errors.Is(err, ErrNotFound) {
			continue
		}
		return a, err
	}
	return nil, nil
}

// storedFirst looks for path in the stored rows of members, in read order and without a remote,
// as far as the answer cannot differ from the read in order. A hosted member without the file
// and a proxy member that refuses the path would not serve it; a proxy member without a stored
// copy of a path that names its bytes (Class.Addressed) would serve the same bytes as any stored
// copy, so the pass goes on past it. It stops at a proxy member that would fetch a changing file
// or a file that does not name its bytes: an immutable file at the same path in a later member
// may hold other bytes than the remote's (a Maven release), and the first member wins. It returns
// the stored asset, else the index from which the read in order goes on (the members before it
// do not serve the file; len(members) when none does).
func (rd *Reader) storedFirst(ctx context.Context, members []*repos.Repo, path repopath.Path) (int, *Asset, error) {
	from := -1 // the first proxy member that the pass went past
	for i, m := range members {
		if m.Kind == repos.KindProxy {
			class, ok := rd.classify(m, path)
			if !ok {
				continue
			}
			if class.Changing {
				return firstOf(from, i), nil, nil
			}
			a, err := rd.p.Get(ctx, m.ID, string(path))
			switch {
			case err == nil:
				return 0, a, nil // an immutable cached file is always fresh
			case !errors.Is(err, ErrNotFound):
				return 0, nil, err
			case !class.Addressed:
				return firstOf(from, i), nil, nil
			case from < 0:
				from = i
			}
			continue
		}
		a, err := rd.p.Get(ctx, m.ID, string(path))
		if err == nil || !errors.Is(err, ErrNotFound) {
			return 0, a, err
		}
	}
	return firstOf(from, len(members)), nil, nil
}

// firstOf is from when it is set (>= 0), else i.
func firstOf(from, i int) int {
	if from >= 0 {
		return from
	}
	return i
}

// classify is the Class of path in proxy repo repo; false when the repo does not fetch it.
func (rd *Reader) classify(repo *repos.Repo, path repopath.Path) (Class, bool) {
	hooks := rd.p.Hooks(repo.Format)
	px, isProxy := rd.repos.Proxy(repo.ID)
	if hooks.Classify == nil || rd.remote == nil || !isProxy {
		return Class{}, false
	}
	return hooks.Classify(repo, px, path)
}

func (rd *Reader) has(ctx context.Context, repo *repos.Repo, path repopath.Path, live *Live) (*Asset, error) {
	if repo.Kind == repos.KindProxy {
		class, ok := rd.classify(repo, path)
		if !ok {
			return nil, nil
		}
		a, err := rd.remote.Ensure(ctx, repo, path, class, live)
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

func notInGroup(w http.ResponseWriter, r *http.Request, remoteFailed bool) {
	if remoteFailed {
		httpx.Error(w, r, http.StatusBadGateway, "The remote repository did not answer.")
		return
	}
	httpx.Error(w, r, http.StatusNotFound, "not found")
}

// checksums are the checksum names of Merger.Match.
var checksums = map[string]func() hash.Hash{"md5": md5.New, "sha1": sha1.New, "sha256": sha256.New, "sha512": sha512.New}

// serveMerged answers path of a group, whose base file the format merges. With one member that
// has the base file, that member's file (or its checksum file, when it has one) serves; with more,
// the server merges them. A checksum path is the checksum of the bytes the base path serves. The
// members are asked and read at the same time; the merge keeps member order. The merged bytes
// stay in memory under the SHA-256s of the members' files, so a checksum request and the next
// request of the same files reuse them.
func (rd *Reader) serveMerged(w http.ResponseWriter, r *http.Request, group *repos.Repo, members []*repos.Repo, mg *Merger,
	path, base repopath.Path, checksum string) {
	ctx := r.Context()
	type source struct {
		repo *repos.Repo
		a    *Asset
		err  error
	}
	found := make([]source, len(members))
	var wg sync.WaitGroup
	for i, m := range members {
		wg.Go(func() {
			a, err := rd.has(ctx, m, base, nil)
			found[i] = source{m, a, err}
		})
	}
	wg.Wait()
	var srcs []source
	remoteFailed := false
	for _, s := range found {
		switch {
		case errors.Is(s.err, ErrRemoteFailed):
			remoteFailed = true
		case s.err != nil:
			rd.log.Error("group: member file", "group", group.Name, "member", s.repo.Name, "path", base, "err", s.err)
			httpx.Error(w, r, http.StatusInternalServerError, "internal error")
			return
		case s.a != nil:
			srcs = append(srcs, s)
		}
	}
	if len(srcs) == 0 {
		notInGroup(w, r, remoteFailed)
		return
	}
	if len(srcs) == 1 {
		if checksum == "" {
			rd.ServeAsset(w, r, srcs[0].a, ServeOptions{})
			return
		}
		if a, err := rd.has(ctx, srcs[0].repo, path, nil); err == nil && a != nil {
			rd.ServeAsset(w, r, a, ServeOptions{})
			return
		}
	}
	var modified time.Time
	key := make([]string, 0, 2+2*len(srcs))
	key = append(key, strconv.FormatInt(group.ID, 10), string(base))
	for _, s := range srcs {
		key = append(key, strconv.FormatInt(s.repo.ID, 10), s.a.SHA256)
		if s.a.UpdatedAt.After(modified) {
			modified = s.a.UpdatedAt
		}
	}
	mkey := strings.Join(key, "\x00")
	data, ok := rd.merged.get(mkey)
	if !ok {
		parts := make([][]byte, len(srcs))
		errs := make([]error, len(srcs))
		for i, s := range srcs {
			wg.Go(func() { parts[i], errs[i] = rd.p.ReadAll(ctx, s.a, maxMergeBytes) })
		}
		wg.Wait()
		read := parts[:0]
		for i, s := range srcs {
			if errs[i] != nil {
				rd.log.Warn("group: read member file", "member", s.repo.Name, "path", base, "err", errs[i])
				continue
			}
			read = append(read, parts[i])
		}
		if len(read) == 0 {
			httpx.Error(w, r, http.StatusNotFound, "not found")
			return
		}
		data = read[0]
		if len(read) > 1 {
			var err error
			if data, err = mg.Merge(ctx, base, read); err != nil {
				rd.log.Warn("group: merge", "group", group.Name, "path", base, "err", err)
				httpx.Error(w, r, http.StatusNotFound, "not found")
				return
			}
		}
		if len(read) == len(srcs) {
			rd.merged.put(mkey, data) // a merge without a member that failed to read
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
			httpx.Error(w, r, http.StatusNotFound, "not found")
			return
		}
		sum := h()
		sum.Write(data)
		data, ct = []byte(hex.EncodeToString(sum.Sum(nil))), "text/plain"
	}
	etag := sha256.Sum256(data)
	s1, s5 := sha1.Sum(data), md5.Sum(data)
	checksumHeaders(w, hex.EncodeToString(etag[:]), hex.EncodeToString(s1[:]), hex.EncodeToString(s5[:]))
	if contentHeaders(w, r, ct, `"`+hex.EncodeToString(etag[:])+`"`, modified) {
		return
	}
	if r.Method == http.MethodGet {
		rd.counted(group.ID, int64(len(data)))
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

// ServeOptions changes how ServeAsset answers.
type ServeOptions struct {
	// NoRedirect streams the blob even when the store presigns downloads, e.g. for a file that
	// clients read with their own headers (a registry manifest).
	NoRedirect bool
}

// checksumHeaders sets X-Checksum-SHA256, and X-Checksum-SHA1 and X-Checksum-MD5 when known.
func checksumHeaders(w http.ResponseWriter, sha256, sha1, md5 string) {
	h := w.Header()
	h.Set("X-Checksum-SHA256", sha256)
	if sha1 != "" {
		h.Set("X-Checksum-SHA1", sha1)
	}
	if md5 != "" {
		h.Set("X-Checksum-MD5", md5)
	}
}

// ServeAsset answers a GET or HEAD of a with its checksum headers. A GET redirects to a presigned
// URL of the blob when the store has one (s3.redirect) and opts allows it: the URL signs the
// response content type and disposition of a, so each path of a shared blob gets its own.
// Otherwise it streams the blob. A request for one byte range answers 206 with that range on
// every store.
func (rd *Reader) ServeAsset(w http.ResponseWriter, r *http.Request, a *Asset, opts ServeOptions) {
	ctx := r.Context()
	checksumHeaders(w, a.SHA256, a.SHA1, a.MD5)
	etag := `"` + a.SHA256 + `"`
	if contentHeaders(w, r, a.ContentType, etag, a.UpdatedAt) {
		return
	}
	h := w.Header()
	h.Set("Accept-Ranges", "bytes")
	if r.Method == http.MethodHead {
		h.Set("Content-Length", strconv.FormatInt(a.Size, 10))
		w.WriteHeader(http.StatusOK)
		return
	}
	key := blobs.Key(a.SHA256)
	st := rd.p.blobs.Storage()
	if !opts.NoRedirect {
		// The presigned URL ends in the blob key, so the disposition names the file: else a
		// browser saves the download under the SHA-256.
		kind := "attachment"
		if inline(a.ContentType) {
			kind = "inline"
		}
		disposition := mime.FormatMediaType(kind, map[string]string{"filename": path.Base(a.Path)})
		if u, ok, err := st.PresignGet(ctx, key, rd.ttl(), storage.Response{ContentType: a.ContentType, ContentDisposition: disposition}); err != nil {
			rd.log.Error("presign failed", "key", key, "err", err)
			httpx.Error(w, r, http.StatusInternalServerError, "internal error")
			return
		} else if ok {
			rd.counted(a.RepoID, a.Size)
			h.Del("Last-Modified")
			h.Set("Cache-Control", "no-store")
			h.Set("Location", u)
			w.WriteHeader(http.StatusFound)
			return
		}
	}
	if off, n, ok := byteRange(r, a.Size, etag); ok {
		if n == 0 {
			h.Set("Content-Range", "bytes */"+strconv.FormatInt(a.Size, 10))
			httpx.Error(w, r, http.StatusRequestedRangeNotSatisfiable, "the range is outside the file")
			return
		}
		body, _, err := st.GetRange(ctx, key, off, n)
		if err != nil {
			rd.storageFailed(w, r, a, err)
			return
		}
		defer body.Close()
		rd.counted(a.RepoID, n)
		h.Set("Content-Range", "bytes "+strconv.FormatInt(off, 10)+"-"+strconv.FormatInt(off+n-1, 10)+"/"+strconv.FormatInt(a.Size, 10))
		h.Set("Content-Length", strconv.FormatInt(n, 10))
		w.WriteHeader(http.StatusPartialContent)
		if _, err := io.Copy(w, body); err != nil {
			rd.log.Debug("asset stream aborted", "repo_id", a.RepoID, "path", a.Path, "err", err)
		}
		return
	}
	body, info, err := st.Get(ctx, key)
	if err != nil {
		rd.storageFailed(w, r, a, err)
		return
	}
	defer body.Close()
	rd.counted(a.RepoID, a.Size)
	if rs, ok := body.(io.ReadSeeker); ok {
		r.Header.Del("Range") // several ranges: the whole file serves
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

// storageFailed answers a blob of a that the store could not open.
func (rd *Reader) storageFailed(w http.ResponseWriter, r *http.Request, a *Asset, err error) {
	h := w.Header()
	for _, k := range []string{"ETag", "Last-Modified", "Content-Range", "X-Checksum-SHA256", "X-Checksum-SHA1", "X-Checksum-MD5"} {
		h.Del(k)
	}
	if errors.Is(err, storage.ErrNotFound) {
		rd.log.Warn("asset row without blob", "repo_id", a.RepoID, "path", a.Path)
		httpx.Error(w, r, http.StatusNotFound, "not found")
		return
	}
	rd.log.Error("storage get failed", "repo_id", a.RepoID, "path", a.Path, "err", err)
	httpx.Error(w, r, http.StatusInternalServerError, "internal error")
}

// byteRange reads the Range header of r for a file of size bytes with ETag etag: one range
// "bytes=a-b", "bytes=a-" or "bytes=-n". ok is false without a Range header, for several ranges,
// for a header that does not parse, or when If-Range names another version (the whole file
// serves). n is 0 for a range outside the file (416).
func byteRange(r *http.Request, size int64, etag string) (off, n int64, ok bool) {
	spec, found := strings.CutPrefix(r.Header.Get("Range"), "bytes=")
	if !found || strings.Contains(spec, ",") {
		return 0, 0, false
	}
	if ir := r.Header.Get("If-Range"); ir != "" && ir != etag {
		return 0, 0, false
	}
	from, to, found := strings.Cut(strings.TrimSpace(spec), "-")
	if !found {
		return 0, 0, false
	}
	if from == "" {
		last, err := strconv.ParseInt(to, 10, 64)
		if err != nil || last < 0 {
			return 0, 0, false
		}
		if last == 0 || size == 0 {
			return 0, 0, true
		}
		last = min(last, size)
		return size - last, last, true
	}
	start, err := strconv.ParseInt(from, 10, 64)
	if err != nil || start < 0 {
		return 0, 0, false
	}
	end := size - 1
	if to != "" {
		if end, err = strconv.ParseInt(to, 10, 64); err != nil || end < start {
			return 0, 0, false
		}
		end = min(end, size-1)
	}
	if start >= size {
		return 0, 0, true
	}
	return start, end - start + 1, true
}
