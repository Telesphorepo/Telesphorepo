// Package proxy fetches the files of proxy repos from their remotes (spec "Proxy repositories").
// A format plugin classifies a path (format.Proxied); this package fetches it once, stores it
// as an asset of the proxy repo, checks a changing file again after the repo's metadata_ttl, and
// remembers a remote 404 for its not_found_ttl. Every read then serves from storage.
package proxy

import (
	"container/list"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/config"
	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

var (
	// ErrNotFound is a path that the remote does not have.
	ErrNotFound = errors.New("the remote repository does not have this file")
	// ErrRemote is a remote that did not answer for a path with no cached copy.
	ErrRemote = errors.New("the remote repository did not answer")
)

const (
	firstByteTimeout = 30 * time.Second // the remote must send its answer headers in this time
	fetchTimeout     = 30 * time.Minute // a whole fetch, body included
	maxRedirects     = 5
	missingEntries   = 10000 // remote 404s remembered per server
)

// client fetches from remotes. It follows at most maxRedirects redirects and sends the
// credentials only to the remote's own host: a redirect to another host drops them.
var client = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: firstByteTimeout,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConnsPerHost:   8,
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > maxRedirects {
			return fmt.Errorf("more than %d redirects", maxRedirects)
		}
		if req.URL.Host != via[0].URL.Host {
			req.Header.Del("Authorization")
		}
		return nil
	},
}

// Service fetches and caches the files of proxy repos.
type Service struct {
	db      *db.DB
	repos   *repos.Service
	assets  *assets.Service
	cfg     config.Provider
	log     *slog.Logger
	missing *missingCache
}

// New returns the proxy service.
func New(d *db.DB, rs *repos.Service, as *assets.Service, cfg config.Provider, log *slog.Logger) *Service {
	return &Service{db: d, repos: rs, assets: as, cfg: cfg, log: log, missing: newMissingCache(missingEntries)}
}

// remoteURL is the address of p at the remote base (which ends with "/").
func remoteURL(base, p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return base + strings.Join(segs, "/")
}

func newRequest(ctx context.Context, px *repos.Proxy, p string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, remoteURL(px.RemoteURL, p), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Telesphorepo proxy")
	if px.HasCredentials() {
		req.SetBasicAuth(px.Username, px.Password)
	}
	return req, nil
}

// reason turns a failed request into the text that last_error keeps. It never holds credentials:
// a remote URL has no user part (repos.ProxyInput).
func reason(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "the remote did not answer in time"
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "the remote did not answer in time"
	}
	return "the remote did not answer: " + err.Error()
}

// Check requests probe (a path; "" for the remote's root) from the remote of px once and returns
// why the remote does not answer, or nil. Any answer of the root counts, except a refusal of the
// credentials or a server error; a probe path must exist.
func Check(ctx context.Context, px *repos.Proxy, probe string) error {
	ctx, cancel := context.WithTimeout(ctx, firstByteTimeout+5*time.Second)
	defer cancel()
	req, err := newRequest(ctx, px, probe)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return errors.New(reason(err))
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	switch st := resp.StatusCode; {
	case st == http.StatusUnauthorized || st == http.StatusForbidden || st == http.StatusProxyAuthRequired:
		if px.HasCredentials() {
			return fmt.Errorf("the remote refused the username and password (%s)", resp.Status)
		}
		return fmt.Errorf("the remote needs a username and password (%s)", resp.Status)
	case st >= 500:
		return fmt.Errorf("the remote answered %s", resp.Status)
	case probe != "" && st >= 400:
		return fmt.Errorf("the remote has no %s (%s)", probe, resp.Status)
	}
	return nil
}

// Serve answers a GET or HEAD of p in proxy repo (already authorized): from storage when the
// cached file is fresh, else after a fetch (see Ensure). changing says that the file can change
// at the remote (format.ProxyChanging); kind is its assets kind.
func (s *Service) Serve(w http.ResponseWriter, r *http.Request, repo *repos.Repo, p string, changing bool, kind string) {
	_, err := s.Ensure(r.Context(), repo, p, changing, kind)
	switch {
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, http.StatusNotFound, "not found")
	case errors.Is(err, ErrRemote):
		httpx.Error(w, http.StatusBadGateway, "The remote repository did not answer.")
	case err != nil:
		s.log.Error("proxy fetch failed", "repo", repo.Name, "path", p, "err", err)
		httpx.Error(w, http.StatusInternalServerError, "internal error")
	default:
		s.assets.Serve(w, r, repo, p)
	}
}

// Ensure makes p of proxy repo available in storage and returns its asset: the cached file when
// it is fresh, else the file the remote sends now. A remote that fails leaves a cached file in
// place (stale) and logs a warning. It returns ErrNotFound when the remote does not have p, and
// ErrRemote when the remote fails and nothing is cached. One fetch per (repo, path) runs at a
// time; a second request waits for it and uses its result.
func (s *Service) Ensure(ctx context.Context, repo *repos.Repo, p string, changing bool, kind string) (*assets.Asset, error) {
	px, ok := s.repos.Proxy(repo.ID)
	if !ok {
		return nil, fmt.Errorf("proxy repo %s has no remote", repo.Name)
	}
	key := strconv.FormatInt(repo.ID, 10) + "\x00" + p
	look := func() (*assets.Asset, bool, error) {
		a, err := s.assets.Get(ctx, repo.ID, p)
		if errors.Is(err, assets.ErrNotFound) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		return a, !changing || time.Since(a.CheckedAt) < px.MetadataTTL, nil
	}
	a, fresh, err := look()
	if err != nil || fresh {
		return a, err
	}
	if a == nil && s.missing.has(key) {
		return nil, ErrNotFound
	}
	unlock := s.assets.Lock(repo.ID, p)
	defer unlock()
	if a, fresh, err = look(); err != nil || fresh {
		return a, err // another request fetched it while this one waited
	}
	if a == nil && s.missing.has(key) {
		return nil, ErrNotFound
	}
	return s.fetch(ctx, repo, px, p, a, kind, key)
}

// fetch requests p from the remote (conditionally when cached is set) and stores the answer.
// The caller holds the path lock.
func (s *Service) fetch(ctx context.Context, repo *repos.Repo, px *repos.Proxy, p string, cached *assets.Asset, kind, key string) (*assets.Asset, error) {
	// A client that goes away does not stop a fetch: the next request uses the stored file.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fetchTimeout)
	defer cancel()
	req, err := newRequest(ctx, px, p)
	if err != nil {
		return nil, err
	}
	if cached != nil {
		if cached.RemoteETag != "" {
			req.Header.Set("If-None-Match", cached.RemoteETag)
		}
		req.Header.Set("If-Modified-Since", cached.UpdatedAt.Format(http.TimeFormat))
	}
	resp, err := client.Do(req)
	if err != nil {
		return s.failed(ctx, repo, p, cached, reason(err))
	}
	defer resp.Body.Close()
	now := time.Now()
	switch st := resp.StatusCode; {
	case st == http.StatusNotModified && cached != nil:
		etag := resp.Header.Get("ETag")
		if etag == "" {
			etag = cached.RemoteETag
		}
		if err := s.assets.SetChecked(ctx, repo.ID, p, now, etag); err != nil {
			return nil, err
		}
		s.repos.ProxyOK(ctx, repo.ID)
		return cached, nil
	case st == http.StatusOK:
		a, err := s.store(ctx, repo, p, resp, kind, now)
		if errors.Is(err, assets.ErrTooLarge) {
			return s.failed(ctx, repo, p, cached, fmt.Sprintf("the remote file %s is larger than the upload limit (max_upload_size)", p))
		}
		if err != nil {
			return nil, err
		}
		s.missing.drop(key)
		s.repos.ProxyOK(ctx, repo.ID)
		return a, nil
	case st == http.StatusNotFound || st == http.StatusGone:
		s.repos.ProxyOK(ctx, repo.ID)
		if cached != nil {
			// The remote removed a changing file: the cached copy keeps builds working.
			s.log.Warn("the remote no longer has a cached file; the cached copy stays", "repo", repo.Name, "path", p)
			return cached, s.assets.SetChecked(ctx, repo.ID, p, now, cached.RemoteETag)
		}
		if px.NotFoundTTL > 0 {
			s.missing.add(key, now.Add(px.NotFoundTTL))
		}
		return nil, ErrNotFound
	default:
		return s.failed(ctx, repo, p, cached, fmt.Sprintf("the remote answered %s for %s", resp.Status, p))
	}
}

// failed records a remote failure. A cached file serves although it is stale.
func (s *Service) failed(ctx context.Context, repo *repos.Repo, p string, cached *assets.Asset, why string) (*assets.Asset, error) {
	s.repos.ProxyFailed(ctx, repo.ID, why)
	if cached != nil {
		s.log.Warn("remote failed; serving the cached copy", "repo", repo.Name, "path", p, "reason", why)
		return cached, nil
	}
	s.log.Warn("remote failed", "repo", repo.Name, "path", p, "reason", why)
	return nil, ErrRemote
}

// store spools the body of resp through the upload pipeline (size limit, hashes), writes the
// blob and upserts the asset row with the remote's Last-Modified and ETag.
func (s *Service) store(ctx context.Context, repo *repos.Repo, p string, resp *http.Response, kind string, now time.Time) (*assets.Asset, error) {
	max := s.cfg().MaxUploadBytes
	if resp.ContentLength > max {
		return nil, assets.ErrTooLarge
	}
	sp, err := s.assets.Spool(resp.Body, max)
	if err != nil {
		return nil, err
	}
	defer sp.Close()
	ct := assets.ContentType(p)
	if err := s.assets.PutBlob(ctx, repo.Name, p, sp, ct); err != nil {
		return nil, err
	}
	mod := now
	if t, err := http.ParseTime(resp.Header.Get("Last-Modified")); err == nil && !t.After(now) {
		mod = t
	}
	a := &assets.Asset{RepoID: repo.ID, Path: p, Size: sp.Size, SHA256: sp.SHA256, ContentType: ct, Kind: kind,
		UpdatedAt: mod, CheckedAt: now, RemoteETag: resp.Header.Get("ETag")}
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		_, err := assets.UpsertTx(ctx, tx, a)
		return err
	})
	if err != nil {
		return nil, err
	}
	return a, nil
}

// missingCache remembers remote 404s until a time, for at most max paths (least recently used
// out first).
type missingCache struct {
	mu  sync.Mutex
	max int
	ll  *list.List
	m   map[string]*list.Element
}

type missingEntry struct {
	key   string
	until time.Time
}

func newMissingCache(max int) *missingCache {
	return &missingCache{max: max, ll: list.New(), m: map[string]*list.Element{}}
}

func (c *missingCache) has(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.m[key]
	if !ok {
		return false
	}
	if time.Now().After(el.Value.(*missingEntry).until) {
		c.ll.Remove(el)
		delete(c.m, key)
		return false
	}
	c.ll.MoveToFront(el)
	return true
}

func (c *missingCache) add(key string, until time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.m[key]; ok {
		el.Value.(*missingEntry).until = until
		c.ll.MoveToFront(el)
		return
	}
	c.m[key] = c.ll.PushFront(&missingEntry{key, until})
	for c.ll.Len() > c.max {
		old := c.ll.Back()
		c.ll.Remove(old)
		delete(c.m, old.Value.(*missingEntry).key)
	}
}

func (c *missingCache) drop(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.m[key]; ok {
		c.ll.Remove(el)
		delete(c.m, key)
	}
}
