// Package proxy fetches the files of proxy repos from their remotes (SPEC.md "Group and proxy repositories").
// A format classifies a path (format.Spec.Classify); this package fetches it once, stores it
// through the asset pipeline (source "proxy"), checks a changing file again after the repo's
// metadata_ttl, and remembers a remote 404 for its not_found_ttl. Every read then serves from
// storage (assets.Reader).
package proxy

import (
	"container/list"
	"context"
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
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
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

// Service fetches and caches the files of proxy repos. It is the assets.Fetcher of the read
// path.
type Service struct {
	repos   *repos.Service
	assets  *assets.Pipeline
	log     *slog.Logger
	missing *missingCache
}

// New returns the proxy service.
func New(rs *repos.Service, as *assets.Pipeline, log *slog.Logger) *Service {
	return &Service{repos: rs, assets: as, log: log, missing: newMissingCache(missingEntries)}
}

// remoteURL is the address of p at the remote base (which ends with "/").
func remoteURL(base, p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return base + strings.Join(segs, "/")
}

func newRequest(ctx context.Context, px *repos.Proxy, p string, extra http.Header) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, remoteURL(px.RemoteURL, p), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Telesphorepo proxy")
	if px.HasCredentials() {
		req.SetBasicAuth(px.Username, px.Password)
	}
	for k, vs := range extra {
		req.Header[k] = vs
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
	req, err := newRequest(ctx, px, probe, nil)
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

// Ensure makes p of proxy repo available and returns its asset: the cached file when it is fresh
// (an immutable file is always fresh; a changing one for the repo's metadata_ttl), else the file
// the remote sends now. A remote that fails leaves a cached file in place (stale) and logs a
// warning. It returns assets.ErrRemoteMissing when the remote does not have p, and
// assets.ErrRemoteFailed when the remote fails and nothing is cached. One fetch per (repo, path)
// runs at a time; a second request waits for it and uses its result.
func (s *Service) Ensure(ctx context.Context, repo *repos.Repo, p repopath.Path, class assets.Class) (*assets.Asset, error) {
	px, ok := s.repos.Proxy(repo.ID)
	if !ok {
		return nil, fmt.Errorf("proxy repo %s has no remote", repo.Name)
	}
	key := strconv.FormatInt(repo.ID, 10) + "\x00" + string(p)
	look := func() (*assets.Asset, bool, error) {
		a, err := s.assets.Get(ctx, repo.ID, string(p))
		if errors.Is(err, assets.ErrNotFound) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		return a, !class.Changing || time.Since(a.CheckedAt) < px.MetadataTTL, nil
	}
	a, fresh, err := look()
	if err != nil || fresh {
		return a, err
	}
	if a == nil && s.missing.has(key) {
		return nil, assets.ErrRemoteMissing
	}
	// The fetch lock is not the path lock: the commit of the fetched file takes that one.
	unlock := s.assets.Lock(repo.ID, "proxy-fetch:"+string(p))
	defer unlock()
	if a, fresh, err = look(); err != nil || fresh {
		return a, err // another request fetched it while this one waited
	}
	if a == nil && s.missing.has(key) {
		return nil, assets.ErrRemoteMissing
	}
	return s.fetch(ctx, repo, px, p, a, key)
}

// fetch requests p from the remote (conditionally when cached is set) and stores the answer.
func (s *Service) fetch(ctx context.Context, repo *repos.Repo, px *repos.Proxy, p repopath.Path, cached *assets.Asset, key string) (*assets.Asset, error) {
	// A client that goes away does not stop a fetch: the next request uses the stored file.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fetchTimeout)
	defer cancel()
	cond := http.Header{}
	if cached != nil {
		if cached.RemoteETag != "" {
			cond.Set("If-None-Match", cached.RemoteETag)
		}
		cond.Set("If-Modified-Since", cached.UpdatedAt.Format(http.TimeFormat))
	}
	resp, err := s.do(ctx, repo, px, string(p), cond)
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
		if err := s.assets.SetChecked(ctx, repo.ID, string(p), now, etag); err != nil {
			return nil, err
		}
		s.repos.ProxyOK(ctx, repo.ID)
		return cached, nil
	case st == http.StatusOK:
		a, err := s.store(ctx, repo, p, resp, now)
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
			return cached, s.assets.SetChecked(ctx, repo.ID, string(p), now, cached.RemoteETag)
		}
		if px.NotFoundTTL > 0 {
			s.missing.add(key, now.Add(px.NotFoundTTL))
		}
		return nil, assets.ErrRemoteMissing
	default:
		return s.failed(ctx, repo, p, cached, fmt.Sprintf("the remote answered %s for %s", resp.Status, p))
	}
}

// do sends the request of p. A 401 with a challenge goes to the format's RemoteAuth once (e.g.
// a registry's token exchange), and the request is sent again with the headers it returns.
func (s *Service) do(ctx context.Context, repo *repos.Repo, px *repos.Proxy, p string, extra http.Header) (*http.Response, error) {
	req, err := newRequest(ctx, px, p, extra)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	auth := s.assets.Hooks(repo.Format).RemoteAuth
	if auth == nil {
		return resp, nil
	}
	challenge := resp.Header.Clone()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	h, err := auth(ctx, px, challenge)
	if err != nil {
		return nil, err
	}
	retry := extra.Clone()
	for k, vs := range h {
		retry[k] = vs
	}
	if req, err = newRequest(ctx, px, p, retry); err != nil {
		return nil, err
	}
	if h.Get("Authorization") != "" {
		req.Header.Set("Authorization", h.Get("Authorization"))
	}
	return client.Do(req)
}

// failed records a remote failure. A cached file serves although it is stale.
func (s *Service) failed(ctx context.Context, repo *repos.Repo, p repopath.Path, cached *assets.Asset, why string) (*assets.Asset, error) {
	s.repos.ProxyFailed(ctx, repo.ID, why)
	if cached != nil {
		s.log.Warn("remote failed; serving the cached copy", "repo", repo.Name, "path", p, "reason", why)
		return cached, nil
	}
	s.log.Warn("remote failed", "repo", repo.Name, "path", p, "reason", why)
	return nil, assets.ErrRemoteFailed
}

// store spools the body of resp under the upload size limit and commits it through the asset
// pipeline with the remote's Last-Modified and ETag.
func (s *Service) store(ctx context.Context, repo *repos.Repo, p repopath.Path, resp *http.Response, now time.Time) (*assets.Asset, error) {
	max := s.assets.MaxUploadSize()
	if resp.ContentLength > max {
		return nil, assets.ErrTooLarge
	}
	sp, err := s.assets.Spool(resp.Body, max)
	if err != nil {
		return nil, err
	}
	defer sp.Close()
	mod := now
	if t, err := http.ParseTime(resp.Header.Get("Last-Modified")); err == nil && !t.After(now) {
		mod = t
	}
	r, err := s.assets.Commit(ctx, assets.Write{Repo: repo, Path: p, Body: assets.Body{Spool: sp},
		Origin: assets.Origin{Source: assets.SourceProxy, Updated: mod,
			Remote: &assets.Remote{ETag: resp.Header.Get("ETag"), CheckedAt: now}}})
	if err != nil {
		return nil, err
	}
	return r.Asset, nil
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
