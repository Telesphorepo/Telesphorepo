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
	"mime"
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

// RemoteAuth answers a remote's 401 challenge with the headers of a retry (format.Spec
// RemoteAuth).
type RemoteAuth func(ctx context.Context, remote *repos.Proxy, challenge http.Header) (http.Header, error)

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

// remoteURL is the address of p at the remote base (which ends with "/"). A p that starts with
// "/" is a path from the root of the base's host. A query of p (after "?") is a query only for a
// path from the root: a file path may hold "?".
func remoteURL(base, p string) string {
	query := ""
	if strings.HasPrefix(p, "/") {
		if i := strings.IndexByte(p, '?'); i >= 0 {
			p, query = p[:i], p[i:]
		}
		if u, err := url.Parse(base); err == nil {
			base = u.Scheme + "://" + u.Host + "/"
		}
		p = strings.TrimPrefix(p, "/")
	}
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return base + strings.Join(segs, "/") + query
}

// newRequest builds a request of p at the remote of px with the extra headers. basic sends the
// remote's credentials as HTTP Basic.
func newRequest(ctx context.Context, method string, px *repos.Proxy, p string, extra http.Header, basic bool) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, remoteURL(px.RemoteURL, p), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Telesphorepo proxy")
	if basic && px.HasCredentials() {
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
	if errors.Is(err, ErrRefused) || errors.Is(err, ErrNeedsCredentials) {
		return err.Error()
	}
	return "the remote did not answer: " + err.Error()
}

// Check requests probe (a path; "" for the remote's root) from the remote of px once and returns
// why the remote does not answer, or nil. A 401 with a challenge goes to auth once (nil: the
// credentials go as HTTP Basic). Any answer of the root counts, except a refusal of the
// credentials or a server error; a probe path must exist.
func Check(ctx context.Context, px *repos.Proxy, probe string, auth RemoteAuth) error {
	ctx, cancel := context.WithTimeout(ctx, firstByteTimeout+5*time.Second)
	defer cancel()
	resp, err := send(ctx, http.MethodGet, px, probe, nil, auth)
	if errors.Is(err, ErrRefused) || errors.Is(err, ErrNeedsCredentials) {
		return err
	}
	st, status := 0, ""
	switch {
	case errors.Is(err, ErrDenied):
		st, status = http.StatusUnauthorized, "401 Unauthorized"
	case err != nil:
		return errors.New(reason(err))
	default:
		io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		st, status = resp.StatusCode, resp.Status
	}
	switch {
	case st == http.StatusUnauthorized || st == http.StatusForbidden || st == http.StatusProxyAuthRequired:
		if px.HasCredentials() {
			return fmt.Errorf("the remote refused the username and password (%s)", status)
		}
		return fmt.Errorf("the remote needs a username and password (%s)", status)
	case st >= 500:
		return fmt.Errorf("the remote answered %s", status)
	case probe != "" && st >= 400:
		return fmt.Errorf("the remote has no %s (%s)", probe, status)
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
	return s.fetch(ctx, repo, px, p, class, a, key)
}

// fetch requests p from the remote (revalidated as class says when cached is set) and stores the
// answer.
func (s *Service) fetch(ctx context.Context, repo *repos.Repo, px *repos.Proxy, p repopath.Path, class assets.Class, cached *assets.Asset, key string) (*assets.Asset, error) {
	// A client that goes away does not stop a fetch: the next request uses the stored file.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fetchTimeout)
	defer cancel()
	remote := class.Remote
	if remote == "" {
		remote = string(p)
	}
	auth := s.assets.Hooks(repo.Format).RemoteAuth
	now := time.Now()
	// missing answers a remote that does not have p: a 404 or 410, or a registry that denies the
	// name (ErrDenied). The remote works, so the proxy is OK.
	missing := func() (*assets.Asset, error) {
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
	}
	if cached != nil && class.Unchanged != nil {
		resp, err := send(ctx, http.MethodHead, px, remote, class.Header, auth)
		if errors.Is(err, ErrDenied) {
			return missing()
		}
		if err != nil {
			return s.failed(ctx, repo, p, cached, reason(err))
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK && class.Unchanged(resp, cached) {
			if err := s.assets.SetChecked(ctx, repo.ID, string(p), now, cached.RemoteETag); err != nil {
				return nil, err
			}
			s.repos.ProxyOK(ctx, repo.ID)
			return cached, nil
		}
	}
	hdr := class.Header.Clone()
	if hdr == nil {
		hdr = http.Header{}
	}
	if cached != nil && class.Unchanged == nil {
		if cached.RemoteETag != "" {
			hdr.Set("If-None-Match", cached.RemoteETag)
		}
		hdr.Set("If-Modified-Since", cached.UpdatedAt.Format(http.TimeFormat))
	}
	resp, err := send(ctx, http.MethodGet, px, remote, hdr, auth)
	if errors.Is(err, ErrDenied) {
		return missing()
	}
	if err != nil {
		return s.failed(ctx, repo, p, cached, reason(err))
	}
	defer resp.Body.Close()
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
		a, err := s.store(ctx, repo, p, class, resp, now)
		var ref *assets.Refusal
		switch {
		case errors.Is(err, assets.ErrTooLarge):
			return s.failed(ctx, repo, p, cached, fmt.Sprintf("the remote file %s is larger than the upload limit (max_upload_size)", p))
		case errors.As(err, &ref):
			return s.failed(ctx, repo, p, cached, "the remote sent a file that the format refuses: "+ref.Message)
		case err != nil:
			return nil, err
		}
		s.missing.drop(key)
		s.repos.ProxyOK(ctx, repo.ID)
		return a, nil
	case st == http.StatusNotFound || st == http.StatusGone:
		return missing()
	default:
		return s.failed(ctx, repo, p, cached, fmt.Sprintf("the remote answered %s for %s", resp.Status, p))
	}
}

// Send sends a request of p (a path as Class.Remote takes it) to the remote of px the way a fetch
// does, e.g. a list that a format reads from the remote. The caller closes the body.
func Send(ctx context.Context, method string, px *repos.Proxy, p string, query url.Values, header http.Header, auth RemoteAuth) (*http.Response, error) {
	if len(query) > 0 {
		p += "?" + query.Encode()
	}
	return send(ctx, method, px, p, header, auth)
}

// send sends a request of p to the remote of px. Without auth, the remote's credentials go as
// HTTP Basic. With auth, the request goes without them; a 401 with a challenge goes to auth once
// (e.g. a registry's token exchange), and the request is sent again with the headers it returns.
// ErrDenied: the remote refused that second request after a Bearer challenge.
func send(ctx context.Context, method string, px *repos.Proxy, p string, extra http.Header, auth RemoteAuth) (*http.Response, error) {
	req, err := newRequest(ctx, method, px, p, extra, auth == nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusUnauthorized || auth == nil {
		return resp, err
	}
	challenge := resp.Header.Clone()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	h, err := auth(ctx, px, challenge)
	if err != nil {
		return nil, err
	}
	retry := extra.Clone()
	if retry == nil {
		retry = http.Header{}
	}
	for k, vs := range h {
		retry[k] = vs
	}
	if req, err = newRequest(ctx, method, px, p, retry, false); err != nil {
		return nil, err
	}
	if resp, err = client.Do(req); err != nil {
		return nil, err
	}
	if (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) && bearerChallenge(challenge) {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		return nil, ErrDenied
	}
	return resp, nil
}

// bearerChallenge reports whether h holds a Bearer challenge. A retry with the token of a Bearer
// challenge that still gets 401 or 403 is a denied name; a retry with Basic credentials that gets
// 401 is a refusal of the credentials.
func bearerChallenge(h http.Header) bool {
	for _, c := range ParseChallenges(h) {
		if c.Scheme == "bearer" {
			return true
		}
	}
	return false
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
// pipeline with the remote's Last-Modified and ETag, and its Content-Type when class keeps it.
func (s *Service) store(ctx context.Context, repo *repos.Repo, p repopath.Path, class assets.Class, resp *http.Response, now time.Time) (*assets.Asset, error) {
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
	ct := ""
	if class.KeepType {
		if mt, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type")); err == nil {
			ct = mt
		}
	}
	r, err := s.assets.Commit(ctx, assets.Write{Repo: repo, Path: p, Body: assets.Body{Spool: sp}, ContentType: ct,
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
