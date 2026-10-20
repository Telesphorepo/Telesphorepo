package docker

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/docker/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/proxy"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
)

// Proxy repos of the docker format. The remote is any OCI registry; a path in the remote URL is
// a name prefix (https://ghcr.io/acme/ proxies the images of acme). A docker proxy sends the
// remote's credentials only to the remote's token realm (or as Basic to a Basic challenge), and
// keeps each bearer token until shortly before it expires.

// dockerHub is the host of the Docker Hub registry API. A one-segment image there is an official
// image under library/.
const dockerHub = "registry-1.docker.io"

// manifestAccept is the Accept header of a manifest request: the four manifest types that the
// registry stores.
var manifestAccept = strings.Join([]string{mtOCIManifest, mtOCIIndex, mtDockerManifest, mtDockerList}, ", ")

// remoteImage is the name of image at the remote of px: the name prefix of the remote URL and the
// image, and library/<image> for a one-segment image of Docker Hub.
func remoteImage(px *repos.Proxy, image string) string {
	u, err := url.Parse(px.RemoteURL)
	if err != nil {
		return image
	}
	if prefix := strings.Trim(u.Path, "/"); prefix != "" {
		return prefix + "/" + image
	}
	if u.Host == dockerHub && !strings.Contains(image, "/") {
		return "library/" + image
	}
	return image
}

// pullScope is the token scope of a pull of the remote image.
func pullScope(rimage string) string { return "repository:" + rimage + ":pull" }

// classify tells the proxy how to cache an OCI object path: a link and a manifest by digest never
// change; a tag changes, and a HEAD of the tag that names the cached digest keeps it. Manifests
// keep the remote's media type.
func (f *Format) classify(px *repos.Proxy, p repopath.Path) (assets.Class, bool) {
	op, ok := parsePath(string(p))
	if !ok {
		return assets.Class{}, false
	}
	rimage := remoteImage(px, op.image)
	hdr := http.Header{}
	if auth, ok := f.authFor(px, rimage); ok {
		for k, vs := range auth {
			hdr[k] = vs
		}
	}
	switch op.kind {
	case kindLink:
		return assets.Class{Remote: "/v2/" + rimage + "/blobs/sha256:" + op.ref, Header: hdr}, true
	case kindManifest:
		hdr.Set("Accept", manifestAccept)
		return assets.Class{Remote: "/v2/" + rimage + "/manifests/sha256:" + op.ref, Header: hdr, KeepType: true}, true
	default:
		hdr.Set("Accept", manifestAccept)
		return assets.Class{Remote: "/v2/" + rimage + "/manifests/" + op.ref, Header: hdr, KeepType: true,
			Changing: true, Unchanged: sameDigest}, true
	}
}

// sameDigest reports whether the HEAD answer of a remote tag names the cached manifest.
func sameDigest(head *http.Response, cached *assets.Asset) bool {
	return head.Header.Get("Docker-Content-Digest") == "sha256:"+cached.SHA256
}

// proxyProbe is the path of the check of a remote: the registry's API root.
func proxyProbe(json.RawMessage) string { return "/v2/" }

// remoteAuth answers a challenge of the remote of px (format.Spec RemoteAuth) through
// proxy.Answer with the proxy's credentials, and keeps a bearer token for its scope.
func (f *Format) remoteAuth(ctx context.Context, px *repos.Proxy, challenge http.Header) (http.Header, error) {
	h, life, err := proxy.Answer(ctx, nil, challenge, px.Username, px.Password)
	if err != nil {
		return nil, err
	}
	scope := ""
	for _, c := range proxy.ParseChallenges(challenge) {
		if c.Scheme == "bearer" {
			scope = c.Params["scope"]
		}
	}
	switch {
	case life > 0 && scope != "":
		f.tokens.put(px, scope, h, life)
	case life == 0:
		// A Basic answer holds the credentials and stays good while they do.
		f.tokens.put(px, "", h, basicLife)
	}
	return h, nil
}

// basicLife is how long the proxy keeps the answer to a Basic challenge.
const basicLife = time.Hour

// authFor returns the kept answer to a challenge of the remote of px for a pull of rimage: its
// bearer token, else the Basic answer.
func (f *Format) authFor(px *repos.Proxy, rimage string) (http.Header, bool) {
	if h, ok := f.tokens.get(px, pullScope(rimage)); ok {
		return h, true
	}
	return f.tokens.get(px, "")
}

// afterCommit stores the manifest of a tag that a proxy fetched at its digest path, so the
// manifest serves by digest and its rows exist. When the tag named another digest before, the
// proxy drops that manifest (dropUnnamed), so the layers that only it used leave the cache.
func (f *Format) afterCommit(ctx context.Context, repo *repos.Repo, rs []*assets.Result) {
	if repo.Kind != repos.KindProxy {
		return
	}
	for _, r := range rs {
		if r.Write.Origin.Source != assets.SourceProxy || r.Asset == nil {
			continue
		}
		op, ok := parsePath(r.Asset.Path)
		if !ok || op.kind != kindTag {
			continue
		}
		_, err := f.h.Assets.Commit(ctx, assets.Write{Repo: repo, Path: repopath.Path(manifestPath(op.image, r.Asset.SHA256)),
			Body: assets.Body{Blob: r.Asset.SHA256}, ContentType: r.Asset.ContentType,
			Origin: assets.Origin{Source: assets.SourceProxy, Remote: &assets.Remote{CheckedAt: time.Now()}}})
		if err != nil {
			f.h.Log.Warn("proxy: store the manifest of a fetched tag", "repo", repo.Name, "path", r.Asset.Path, "err", err)
		}
		if r.Prev != nil && r.Prev.SHA256 != r.Asset.SHA256 {
			f.dropUnnamed(ctx, repo, op.image, r.Prev.SHA256)
		}
	}
}

// dropUnnamed removes the cached manifest digest of image from a proxy when no tag names it and
// no index lists it, and then each manifest of its index that is unnamed in the same way. The
// links of a removed manifest lose its uses; the job docker.links removes a link that no
// manifest uses after docker.link_grace, and the blob GC deletes its blob. A client that asks
// for a dropped digest again gets it from the remote.
func (f *Format) dropUnnamed(ctx context.Context, repo *repos.Repo, image, digest string) {
	queue := []string{digest}
	for len(queue) > 0 {
		d := queue[0]
		queue = queue[1:]
		tags, err := f.q.SomeTagsOfDigest(ctx, dbq.SomeTagsOfDigestParams{RepoID: repo.ID, Image: image, Digest: d, Limit: 1})
		if err != nil {
			f.h.Log.Warn("proxy: read the tags of a manifest", "repo", repo.Name, "image", image, "err", err)
			continue
		}
		parents, err := f.q.ParentIndexes(ctx, dbq.ParentIndexesParams{RepoID: repo.ID, Image: image, Child: d, Limit: 1})
		if err != nil {
			f.h.Log.Warn("proxy: read the indexes of a manifest", "repo", repo.Name, "image", image, "err", err)
			continue
		}
		if len(tags) > 0 || len(parents) > 0 {
			continue
		}
		children, err := f.q.ListRefs(ctx, dbq.ListRefsParams{RepoID: repo.ID, Image: image, Manifest: d})
		if err != nil {
			f.h.Log.Warn("proxy: read the children of a manifest", "repo", repo.Name, "image", image, "err", err)
			continue
		}
		if _, err := f.h.Assets.Remove(ctx, assets.Removal{Repo: repo, Paths: []repopath.Path{repopath.Path(manifestPath(image, d))},
			Origin: assets.Origin{Source: assets.SourceGenerated}}); err != nil {
			f.h.Log.Warn("proxy: remove an unnamed manifest", "repo", repo.Name, "image", image, "err", err)
			continue
		}
		for _, c := range children {
			if c.Kind == "manifest" {
				queue = append(queue, c.Child)
			}
		}
	}
}

// proxyTags answers the tags list of an image of a proxy from the remote's tags list, with the
// Link of the next page on the local name. A remote that fails gives the cached tags.
func (f *Format) proxyTags(w http.ResponseWriter, r *http.Request, c *route.Call, t target, n int) {
	px, ok := f.h.Repos.Proxy(c.Repo.ID)
	if !ok {
		f.localTags(w, r, []*repos.Repo{c.Repo}, t, n)
		return
	}
	rimage := remoteImage(px, t.image)
	q := url.Values{"n": {strconv.Itoa(n)}}
	if last := r.URL.Query().Get("last"); last != "" {
		q.Set("last", last)
	}
	hdr := http.Header{"Accept": {"application/json"}}
	if auth, ok := f.authFor(px, rimage); ok {
		for k, vs := range auth {
			hdr[k] = vs
		}
	}
	resp, err := proxy.Send(r.Context(), http.MethodGet, px, "/v2/"+rimage+"/tags/list", q, hdr, f.remoteAuth)
	if errors.Is(err, proxy.ErrDenied) {
		// A registry denies a name that does not exist or that the credentials cannot read.
		writeOCI(w, ociErr(http.StatusNotFound, codeNameUnknown, "repository name not known to registry: "+t.name))
		return
	}
	if err != nil {
		f.h.Log.Warn("proxy: remote tags list failed; the cached tags serve", "repo", c.Repo.Name, "image", t.image, "err", err)
		f.localTags(w, r, []*repos.Repo{c.Repo}, t, n)
		return
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		writeOCI(w, ociErr(http.StatusNotFound, codeNameUnknown, "repository name not known to registry: "+t.name))
		return
	case resp.StatusCode != http.StatusOK:
		f.h.Log.Warn("proxy: remote tags list failed; the cached tags serve", "repo", c.Repo.Name, "image", t.image, "status", resp.StatusCode)
		f.localTags(w, r, []*repos.Repo{c.Repo}, t, n)
		return
	}
	var body struct {
		Tags []string `json:"tags"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&body); err != nil {
		writeOCI(w, ociErr(http.StatusBadGateway, codeUnknown, "The remote repository did not answer."))
		return
	}
	tags := body.Tags
	if tags == nil {
		tags = []string{}
	}
	if len(tags) > n {
		tags = tags[:n]
	}
	if link := resp.Header.Get("Link"); link != "" && len(tags) > 0 {
		nextLink(w, "/v2/"+t.name+"/tags/list", n, tags[len(tags)-1])
	}
	writeTags(w, t.name, tags)
}

// tokenCache keeps the bearer tokens of proxy remotes by remote, credentials and scope until
// shortly before they expire.
type tokenCache struct {
	mu sync.Mutex
	m  map[string]cachedToken
}

type cachedToken struct {
	header http.Header
	until  time.Time
}

// maxTokens bounds the tokens a server keeps.
const maxTokens = 10000

// tokenKey is the key of a token of scope at the remote of px. It holds a hash of the
// credentials, never the password.
func tokenKey(px *repos.Proxy, scope string) string {
	sum := sha256.Sum256([]byte(px.Username + "\x00" + px.Password))
	return px.RemoteURL + "\x00" + hex.EncodeToString(sum[:8]) + "\x00" + scope
}

// get returns the token of scope at the remote of px while it lives.
func (c *tokenCache) get(px *repos.Proxy, scope string) (http.Header, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.m[tokenKey(px, scope)]
	if !ok || time.Now().After(t.until) {
		return nil, false
	}
	return t.header, true
}

// put keeps h for scope until 30 s (at most half its life) before it expires.
func (c *tokenCache) put(px *repos.Proxy, scope string, h http.Header, life time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]cachedToken{}
	}
	now := time.Now()
	if len(c.m) >= maxTokens {
		for k, t := range c.m {
			if now.After(t.until) {
				delete(c.m, k)
			}
		}
		if len(c.m) >= maxTokens {
			c.m = map[string]cachedToken{}
		}
	}
	c.m[tokenKey(px, scope)] = cachedToken{header: h, until: now.Add(life - min(life/2, 30*time.Second))}
}

// errNoImage404 is the answer of a tags list of an image that no member has.
var errNoImage404 = errors.New("no member has the image")

// localTags answers the tags list of an image from the tag rows of repos (a hosted repo, a
// proxy's cached tags, or the readable members of a group), merged by tag after ?last=. An image
// that none of them has is 404 NAME_UNKNOWN.
func (f *Format) localTags(w http.ResponseWriter, r *http.Request, list []*repos.Repo, t target, n int) {
	tags, err := f.mergedTags(r.Context(), list, t.image, r.URL.Query().Get("last"), n)
	if errors.Is(err, errNoImage404) {
		writeOCI(w, ociErr(http.StatusNotFound, codeNameUnknown, "repository name not known to registry: "+t.name))
		return
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	if n > 0 && len(tags) == n {
		nextLink(w, "/v2/"+t.name+"/tags/list", n, tags[n-1])
	}
	writeTags(w, t.name, tags)
}

// mergedTags returns at most n distinct tags of image after last in the repos of list, in name
// order. errNoImage404: no repo of list has the image.
func (f *Format) mergedTags(ctx context.Context, list []*repos.Repo, image, last string, n int) ([]string, error) {
	found := false
	set := map[string]bool{}
	var all []string
	for _, repo := range list {
		if _, err := f.q.GetImage(ctx, dbq.GetImageParams{RepoID: repo.ID, Image: image}); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return nil, err
		}
		found = true
		if n == 0 {
			continue
		}
		rows, err := f.q.ListTags(ctx, dbq.ListTagsParams{RepoID: repo.ID, Image: image, After: last, Limit: int64(n)})
		if err != nil {
			return nil, err
		}
		for _, tag := range rows {
			if !set[tag] {
				set[tag] = true
				all = append(all, tag)
			}
		}
	}
	if !found {
		return nil, errNoImage404
	}
	slices.Sort(all)
	if len(all) > n {
		all = all[:n]
	}
	if all == nil {
		all = []string{}
	}
	return all, nil
}
