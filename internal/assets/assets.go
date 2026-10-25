// Package assets is the format-agnostic file layer of the kernel: the one write pipeline
// (Commit), the one delete pipeline (Remove), the read path of hosted, proxy and group repos
// (Reader) and resumable uploads (Uploads). An asset is a path in a repo that points to a
// content-addressed blob (internal/blobs). Every writer goes through the pipeline: format
// writes, the web UI upload, imports, proxy fetches, generated index files and the demo data.
// A format takes part through its Hooks.
package assets

import (
	"bytes"
	"cmp"
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/blobs"
	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/ops"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
)

var (
	ErrNotFound error = &Refusal{Status: http.StatusNotFound, Message: "not found"}
	ErrTooLarge       = blobs.ErrTooLarge
	// ErrBlobUnknown is a write of a digest that no blob has (a mount of an unknown blob).
	ErrBlobUnknown = blobs.ErrUnknown
)

// Asset kinds.
const (
	KindArtifact = "artifact"
	KindMetadata = "metadata"
	KindIndex    = "index"
)

// FallbackContentType is the content type of a path that the format does not describe.
const FallbackContentType = "application/octet-stream"

// Spooled is a body with its size and hashes (see blobs.Spooled).
type Spooled = blobs.Spooled

// Asset is a file of a repo.
type Asset struct {
	ID          int64
	RepoID      int64
	Path        string
	BlobID      int64
	Size        int64
	SHA256      string
	MD5         string // lowercase hex; "" when the blob has none (a synthetic row)
	SHA1        string
	ContentType string
	Kind        string
	UploadedBy  int64 // 0 = none
	CreatedAt   time.Time
	UpdatedAt   time.Time
	// BuildID is the CI build record (internal/ci) that wrote the file; 0 = none.
	BuildID int64
	// CheckedAt is when the remote of a proxy repo last confirmed the file, RemoteETag its ETag;
	// zero for a hosted file.
	CheckedAt  time.Time
	RemoteETag string
}

// SearchHit is one search result.
type SearchHit struct {
	Asset    *Asset
	RepoName string
	Key      int64 // its key in the search index: the cursor of the page after it
}

// Pipeline writes, removes and reads assets.
type Pipeline struct {
	db    *db.DB
	blobs *blobs.Store
	set   settings.Provider
	audit *audit.Log
	hooks func(format string) *Hooks
	watch []Watcher
	base  context.Context // ends when the server shuts down

	mu    sync.Mutex
	locks map[string]*lockEntry
}

// New returns the pipeline. base is the server's context: a write that runs detached from its
// request ends when base ends.
func New(base context.Context, d *db.DB, bs *blobs.Store, set settings.Provider, au *audit.Log) *Pipeline {
	return &Pipeline{base: base, db: d, blobs: bs, set: set, audit: au, locks: map[string]*lockEntry{},
		hooks: func(string) *Hooks { return nil }}
}

// SetHooks gives the pipeline the hooks of each format (the format registry). It runs before
// the server serves.
func (p *Pipeline) SetHooks(fn func(format string) *Hooks) { p.hooks = fn }

// Watch adds w to the watchers of every commit and remove, called in the order they were added.
// It runs before the server serves (a module's Init).
func (p *Pipeline) Watch(w Watcher) { p.watch = append(p.watch, w) }

// Hooks returns the hooks of format, never nil.
func (p *Pipeline) Hooks(format string) *Hooks {
	if h := p.hooks(format); h != nil {
		return h
	}
	return &Hooks{}
}

// Blobs returns the blob store.
func (p *Pipeline) Blobs() *blobs.Store { return p.blobs }

// Spool copies r to a temp file on the data volume with its hashes, at most max bytes
// (ErrTooLarge above).
func (p *Pipeline) Spool(r io.Reader, max int64) (*Spooled, error) {
	return blobs.Spool(p.set().TmpDir(), r, max)
}

// MaxUploadSize is the max_upload_size setting.
func (p *Pipeline) MaxUploadSize() int64 { return settings.MaxUploadSize.Get(p.set()) }

// detached returns a context of ctx's values that a client disconnect does not end, but the end
// of the server does: a write that started finishes, or stops at shutdown.
func (p *Pipeline) detached(ctx context.Context) (context.Context, context.CancelFunc) {
	d, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stop := context.AfterFunc(p.base, cancel)
	return d, func() { stop(); cancel() }
}

type lockEntry struct {
	mu   sync.Mutex
	refs int
}

// lockKey is the key of a path lock or an extra lock of a write in repo repoID.
func lockKey(repoID int64, k string) string { return strconv.FormatInt(repoID, 10) + "\x00" + k }

// lock takes the locks of keys (sorted, each once) and returns the release.
func (p *Pipeline) lock(keys []string) func() {
	ks := sortedUnique(keys)
	entries := make([]*lockEntry, len(ks))
	for i, k := range ks {
		p.mu.Lock()
		e := p.locks[k]
		if e == nil {
			e = &lockEntry{}
			p.locks[k] = e
		}
		e.refs++
		p.mu.Unlock()
		e.mu.Lock()
		entries[i] = e
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			for i := len(ks) - 1; i >= 0; i-- {
				entries[i].mu.Unlock()
				p.mu.Lock()
				if entries[i].refs--; entries[i].refs == 0 {
					delete(p.locks, ks[i])
				}
				p.mu.Unlock()
			}
		})
	}
}

// Lock takes the lock of key in repo repoID: a path, or a name that a format serializes on (e.g.
// "maven-metadata-merge:" + a path). Commit and Remove take the lock of every path they touch.
func (p *Pipeline) Lock(repoID int64, key string) (unlock func()) {
	return p.lock([]string{lockKey(repoID, key)})
}

func fromRow(r dbq.Asset) *Asset {
	a := &Asset{
		ID:          r.ID,
		RepoID:      r.RepoID,
		Path:        r.Path,
		BlobID:      r.BlobID,
		Size:        r.Size,
		SHA256:      r.Sha256,
		MD5:         r.Md5,
		SHA1:        r.Sha1,
		ContentType: r.ContentType,
		Kind:        r.Kind,
		UploadedBy:  r.UploadedBy.Int64,
		CreatedAt:   time.Unix(r.CreatedAt, 0).UTC(),
		UpdatedAt:   time.Unix(r.UpdatedAt, 0).UTC(),

		BuildID:    r.BuildID.Int64,
		RemoteETag: r.RemoteEtag,
	}
	if r.CheckedAt > 0 {
		a.CheckedAt = time.Unix(r.CheckedAt, 0).UTC()
	}
	return a
}

func get(ctx context.Context, q *dbq.Queries, repoID int64, p string) (*Asset, error) {
	r, err := q.GetAsset(ctx, dbq.GetAssetParams{RepoID: repoID, Path: p})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return fromRow(r), nil
}

// Get is an indexed point lookup on (repo_id, path).
func (p *Pipeline) Get(ctx context.Context, repoID int64, path string) (*Asset, error) {
	return get(ctx, dbq.New(p.db.R), repoID, path)
}

// Open opens the content of a.
func (p *Pipeline) Open(ctx context.Context, a *Asset) (io.ReadCloser, error) {
	rc, _, err := p.blobs.Open(ctx, a.SHA256)
	return rc, err
}

// ReadAll reads the content of a, at most max bytes (ErrTooLarge above).
func (p *Pipeline) ReadAll(ctx context.Context, a *Asset, max int64) ([]byte, error) {
	if a.Size > max {
		return nil, ErrTooLarge
	}
	rc, err := p.Open(ctx, a)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	var b bytes.Buffer
	if _, err := io.Copy(&b, io.LimitReader(rc, max+1)); err != nil {
		return nil, err
	}
	if int64(b.Len()) > max {
		return nil, ErrTooLarge
	}
	return b.Bytes(), nil
}

// ByBlob returns up to limit assets of repo that point to the blob blobID, e.g. the tags that
// point to a manifest digest.
func (p *Pipeline) ByBlob(ctx context.Context, repoID, blobID int64, limit int) ([]*Asset, error) {
	rows, err := dbq.New(p.db.R).ListAssetsByBlob(ctx, dbq.ListAssetsByBlobParams{BlobID: blobID, RepoID: repoID, Limit: int64(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]*Asset, len(rows))
	for i, r := range rows {
		out[i] = fromRow(r)
	}
	return out, nil
}

// SetTimes gives file a of repo the dates created and updated when they differ from a's, to the
// second, then tells the watchers (Touched). Zero updated changes nothing; zero created is
// updated. An import calls it for a file that it skips because the bytes are here.
func (p *Pipeline) SetTimes(ctx context.Context, repo *repos.Repo, a *Asset, created, updated time.Time) error {
	if updated.IsZero() {
		return nil
	}
	if created.IsZero() {
		created = updated
	}
	if a.CreatedAt.Unix() == created.Unix() && a.UpdatedAt.Unix() == updated.Unix() {
		return nil
	}
	if err := dbq.New(p.db.W).SetAssetTimes(ctx, dbq.SetAssetTimesParams{CreatedAt: created.Unix(),
		UpdatedAt: updated.Unix(), RepoID: a.RepoID, Path: a.Path}); err != nil {
		return err
	}
	for _, w := range p.watch {
		if err := w.Touched(ctx, repo, []string{a.Path}); err != nil {
			return err
		}
	}
	return nil
}

// SetChecked records that the remote of a proxy repo confirmed the cached file at at, with its
// ETag (a 304, or a 404 while the cached copy stays).
func (p *Pipeline) SetChecked(ctx context.Context, repoID int64, path string, at time.Time, etag string) error {
	return dbq.New(p.db.W).SetAssetChecked(ctx, dbq.SetAssetCheckedParams{CheckedAt: at.Unix(), RemoteEtag: etag, RepoID: repoID, Path: path})
}

// List: keyset by path within prefix range (path >= prefix AND path < PrefixEnd(prefix)),
// path > after. Returns next = last path when the page is full, else "".
func (p *Pipeline) List(ctx context.Context, repoID int64, prefix, after string, limit int) ([]*Asset, string, error) {
	if limit <= 0 {
		limit = 50
	}
	q := dbq.New(p.db.R)
	var rows []dbq.Asset
	var err error
	// One lower bound per query (see queries/assets.sql): the cursor when it is past the prefix.
	cursor := after != "" && after >= prefix
	lower, n := prefix, int64(limit)+1
	if cursor {
		lower = after
	}
	end, bounded := PrefixEnd(prefix)
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

// SearchCountCap is the most matches that a search counts in one repo: a count of SearchCountCap
// means at least that many. It bounds the count of a common text at any size.
const SearchCountCap = 1000

// SearchCount is the matches of one repo, up to SearchCountCap.
type SearchCount struct {
	RepoID  int64
	Matches int64
}

// searchRarest is how many of the rarest trigrams of a longer text the trigram index matches,
// searchRarestSegments how many of its rarest whole segments the segment index matches, and
// searchProbe how many paths of each a search counts to rank them: a rare one counts under it, a
// common one stops there.
const (
	searchRarest         = 3
	searchRarestSegments = 2
	searchProbe          = 1000
)

// search is one search: the index it reads, the FTS5 query, the text that a path must hold (""
// when the query is exact), and the repos it may answer (nil: every repo).
type search struct {
	index       dbq.SearchIndex
	match, text string
	in          map[int64]bool
}

// newSearch returns the search of q in repoIDs; ok is false when nothing can match: q is shorter
// than 3 characters, repoIDs is empty and not nil, or a segment or a trigram of q is in no path.
// The probes read at most searchProbe rows each, and the search picks the most selective query:
//   - a segment of q (a component between its slashes, the last one as a prefix) that fewer than
//     searchProbe paths hold: the segment index matches q's searchRarestSegments rarest segments
//     and keeps the paths that hold q, so a path or a file name after a slash reads the rows of
//     its rarest segment, which stay few at any size;
//   - else a trigram of q that fewer than searchProbe paths hold: the trigram index matches q's
//     searchRarest rarest trigrams and keeps the paths that hold q;
//   - else (q of 3 characters, with a character outside ASCII, or of common pieces only) q as one
//     phrase of the trigram index.
func newSearch(ctx context.Context, dq *dbq.Queries, q string, repoIDs []int64) (search, bool, error) {
	if utf8.RuneCountInString(q) < 3 || (repoIDs != nil && len(repoIDs) == 0) {
		return search{}, false, nil
	}
	var s search
	if repoIDs != nil {
		s.in = make(map[int64]bool, len(repoIDs))
		for _, id := range repoIDs {
			s.in[id] = true
		}
	}
	quote := func(t string) string { return `"` + strings.ReplaceAll(t, `"`, `""`) + `"` }
	if len(q) == 3 || !isASCII(q) {
		s.match = quote(q)
		return s, true, nil
	}
	type piece struct {
		t    string
		rows int64
	}
	// rank probes the FTS5 queries of q's pieces in index x, rarest first; ok is false when one
	// matches no path.
	rank := func(x dbq.SearchIndex, pieces []string) ([]piece, bool, error) {
		var out []piece
		seen := map[string]bool{}
		for _, t := range pieces {
			if seen[t] {
				continue
			}
			seen[t] = true
			n, err := dq.SearchRows(ctx, x, t, searchProbe)
			if err != nil || n == 0 {
				return nil, false, err // no path holds it, so none holds q
			}
			out = append(out, piece{t, n})
		}
		slices.SortStableFunc(out, func(a, b piece) int { return cmp.Compare(a.rows, b.rows) })
		return out, true, nil
	}
	and := func(ps []piece, n int) string {
		var parts []string
		for _, p := range ps[:min(n, len(ps))] {
			parts = append(parts, p.t)
		}
		return strings.Join(parts, " AND ")
	}
	if segs := segmentQueries(q, quote); len(segs) > 0 {
		ranked, ok, err := rank(dbq.SearchSegments, segs)
		if err != nil || !ok {
			return search{}, false, err
		}
		if ranked[0].rows < searchProbe {
			s.index, s.match, s.text = dbq.SearchSegments, and(ranked, searchRarestSegments), q
			return s, true, nil
		}
	}
	lower := strings.ToLower(q)
	var tris []string
	for i := 0; i+3 <= len(lower); i++ {
		tris = append(tris, quote(lower[i:i+3]))
	}
	ranked, ok, err := rank(dbq.SearchTrigrams, tris)
	if err != nil || !ok {
		return search{}, false, err
	}
	if ranked[0].rows >= searchProbe {
		s.match = quote(q)
		return s, true, nil
	}
	s.match, s.text = and(ranked, searchRarest), q
	return s, true, nil
}

// segmentChars are the characters of a segment of the segment index: letters, digits and every
// ASCII punctuation mark but the slash and the quotes (the tokenchars of assets_segments).
const segmentChars = ".-_+~@!$%&()=,;:#[]{}^`|<>?*\\"

// segmentQueries returns the FTS5 queries of the segment index that every path holding q matches,
// in lower case: each segment of q (the components between its slashes) that q holds whole, and
// its last segment as a prefix, since q may end inside a path's segment. A first segment that q
// starts inside of matches nothing whole, so it is left out. None when q has a character that the
// index splits on but that q does not show as a slash (a quote, a space).
func segmentQueries(q string, quote func(string) string) []string {
	for i := range len(q) {
		c := q[i]
		if c != '/' && !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || strings.IndexByte(segmentChars, c) >= 0) {
			return nil
		}
	}
	parts := strings.Split(strings.ToLower(q), "/")
	if len(parts) < 2 {
		return nil // q lies inside one segment
	}
	var out []string
	for i, s := range parts {
		switch {
		case s == "" || i == 0:
			// the first segment, which q may start inside of, and the empty ones around a slash
		case i == len(parts)-1:
			out = append(out, quote(s)+"*")
		default:
			out = append(out, quote(s))
		}
	}
	return out
}
func isASCII(s string) bool {
	for i := range len(s) {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// repoEnd is the last key of repo in the search index: the cursor that skips the rest of it.
func repoEnd(repo int64) int64 { return (repo+1)<<dbq.SearchShift - 1 }

// Search: FTS5 trigram MATCH on assets_search, restricted to repoIDs when non-nil, a page of
// limit hits after the key after (0 for the first page). The index keys a path by its repo, so
// the hits of one repo come together, in the order they were stored, and a page is one seek past
// after; a repo that is not in repoIDs is one seek past its end. next is the key of the last hit,
// 0 at the end.
func (p *Pipeline) Search(ctx context.Context, q string, repoIDs []int64, after int64, limit int) ([]SearchHit, int64, error) {
	dq := dbq.New(p.db.R)
	s, ok, err := newSearch(ctx, dq, q, repoIDs)
	if err != nil || !ok {
		return nil, 0, err
	}
	if limit <= 0 {
		limit = 50
	}
	// One hit more than limit says whether a next page exists.
	var out []SearchHit
	for len(out) <= limit {
		want := int64(limit + 1 - len(out))
		rows, err := dq.SearchAssets(ctx, s.index, s.match, s.text, after, want)
		if err != nil {
			return nil, 0, err
		}
		skipped := false
		for _, r := range rows {
			if s.in != nil && !s.in[r.Asset.RepoID] {
				after, skipped = repoEnd(r.Asset.RepoID), true
				break
			}
			out = append(out, SearchHit{Asset: fromRow(r.Asset), RepoName: r.RepoName, Key: r.Key})
			after = r.Key
		}
		if !skipped && int64(len(rows)) < want {
			break // the end of the matches
		}
	}
	var next int64
	if len(out) > limit {
		out = out[:limit]
		next = out[limit-1].Key
	}
	return out, next, nil
}

// SearchFirst returns the first page of a search (Search after 0) and the matches of each repo of
// repoIDs (nil: every repo) that has some, in key order, each counted up to SearchCountCap, from
// one walk of the matches: a repo is skipped with one seek past its end once its count stops and
// the page is full, or when it is not in repoIDs. A rare text costs one walk of its few matches,
// a common one a seek and SearchCountCap rows a repo, at any number of files.
func (p *Pipeline) SearchFirst(ctx context.Context, q string, repoIDs []int64, limit int) ([]SearchHit, int64, []SearchCount, error) {
	dq := dbq.New(p.db.R)
	s, ok, err := newSearch(ctx, dq, q, repoIDs)
	if err != nil || !ok {
		return nil, 0, nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	// One hit more than limit says whether a next page exists.
	var hits []SearchHit
	var counts []SearchCount
	for after := int64(0); after >= 0; {
		from := after
		after = -1
		err := dq.WalkSearchAssets(ctx, s.index, s.match, s.text, from, func(r dbq.SearchAssetsRow) bool {
			repo := r.Asset.RepoID
			if s.in != nil && !s.in[repo] {
				after = repoEnd(repo)
				return false
			}
			if n := len(counts); n == 0 || counts[n-1].RepoID != repo {
				counts = append(counts, SearchCount{RepoID: repo})
			}
			c := &counts[len(counts)-1]
			if len(hits) <= limit {
				hits = append(hits, SearchHit{Asset: fromRow(r.Asset), RepoName: r.RepoName, Key: r.Key})
			}
			if c.Matches < SearchCountCap {
				c.Matches++
			}
			if c.Matches >= SearchCountCap && len(hits) > limit {
				after = repoEnd(repo)
				return false
			}
			return true
		})
		if err != nil {
			return nil, 0, nil, err
		}
	}
	var next int64
	if len(hits) > limit {
		hits = hits[:limit]
		next = hits[limit-1].Key
	}
	return hits, next, counts, nil
}

// Seek returns the first asset of repo whose path is >= lower (> lower when !inclusive) and
// < upper (no upper bound when upper is ""), or nil. It is one indexed seek on (repo_id, path).
func (p *Pipeline) Seek(ctx context.Context, repoID int64, lower string, inclusive bool, upper string) (*Asset, error) {
	q := dbq.New(p.db.R)
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

// Children returns up to limit direct sub-folders of folder dir ("" or ending in "/") of repo, in
// path order, each with a trailing "/". Each costs one indexed seek.
func (p *Pipeline) Children(ctx context.Context, repoID int64, dir string, limit int) ([]string, error) {
	upper, _ := PrefixEnd(dir)
	lower, inclusive := dir, true
	var out []string
	for len(out) < limit {
		a, err := p.Seek(ctx, repoID, lower, inclusive, upper)
		if err != nil || a == nil {
			return out, err
		}
		rest := a.Path[len(dir):]
		i := strings.IndexByte(rest, '/')
		if i < 0 {
			lower, inclusive = a.Path, false
			continue
		}
		sub := dir + rest[:i+1]
		out = append(out, sub)
		lower, _ = PrefixEnd(sub)
		inclusive = true
	}
	return out, nil
}

// firstPaths returns up to limit paths of repo that start with prefix ("" for every path), in
// path order.
func (p *Pipeline) firstPaths(ctx context.Context, repoID int64, prefix string, limit int) ([]string, error) {
	q := dbq.New(p.db.R)
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

// PrefixEnd returns the smallest string greater than every string starting with prefix under
// byte-wise (SQLite BINARY) ordering; ok=false when there is none (prefix is "" or all 0xFF
// bytes).
func PrefixEnd(prefix string) (string, bool) {
	b := []byte(prefix)
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] != 0xFF {
			b[i]++
			return string(b[:i+1]), true
		}
	}
	return "", false
}

// deleting reports whether repo repoID has an active delete operation, reading in q.
func deleting(ctx context.Context, q *dbq.Queries, repoID int64) (bool, error) {
	op, err := q.GetRepoOp(ctx, repoID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil && op.Type == ops.TypeDelete, err
}

// SeedRow is a synthetic file of "telesphorepo-server seed": no content is stored, only the rows.
type SeedRow struct {
	Path        string
	Size        int64
	SHA256      string
	ContentType string
	Kind        string
}

// SeedTx inserts blob and asset rows for "telesphorepo-server seed" in tx: a blob row per digest that has
// none, and the asset row of each path. It writes no content and no MD5 or SHA-1, so it is for
// the scale test only.
func SeedTx(ctx context.Context, tx *sql.Tx, repoID int64, rows []SeedRow) ([]*Asset, error) {
	q := dbq.New(tx)
	now := time.Now().Unix()
	out := make([]*Asset, 0, len(rows))
	for _, r := range rows {
		b, err := q.GetBlob(ctx, r.SHA256)
		var id int64
		switch {
		case err == nil:
			id = b.ID
		case errors.Is(err, sql.ErrNoRows):
			if id, err = q.InsertBlob(ctx, dbq.InsertBlobParams{Sha256: r.SHA256, Size: r.Size, UnreferencedAt: now, CreatedAt: now}); err != nil {
				return nil, err
			}
		default:
			return nil, err
		}
		a, err := q.UpsertAsset(ctx, dbq.UpsertAssetParams{RepoID: repoID, Path: r.Path, BlobID: id, Size: r.Size, Sha256: r.SHA256,
			ContentType: r.ContentType, Kind: r.Kind, CreatedAt: now, UpdatedAt: now})
		if err != nil {
			return nil, err
		}
		out = append(out, &Asset{ID: a.ID, RepoID: repoID, Path: r.Path, BlobID: id, Size: r.Size, SHA256: r.SHA256,
			ContentType: r.ContentType, Kind: r.Kind})
	}
	return out, nil
}
