package nuget

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// A NuGet proxy keeps what it reads from its remote under .upstream/, a folder no client asks
// for:
//
//	.upstream/index.json                   the remote's service index
//	.upstream/flat/<id>/index.json          the version list of a package (PackageBaseAddress)
//	.upstream/registration/<id>/index.json  the registration index of a package (RegistrationsBaseUrl/3.6.0)
//	.upstream/page/<path>                   a registration page, at its path on the remote host
//
// and stores the .nupkg and .nuspec files at their own flat-container paths. Every document is
// fetched again after the repo's metadata_ttl; a package file is fetched once. The remote URL is
// the service index ("https://api.nuget.org/v3/index.json") or the folder that holds it. The
// proxy follows only resources of the service index on the remote's scheme and host, and a
// registration page only under the registration base of the service index.

// upstream is what a proxy reads from the service index of its remote: the paths of the flat
// container and of the registration base on the remote's host, each with a trailing "/".
type upstream struct {
	origin string // scheme://host of the remote, lowered
	flat   string // e.g. "/v3-flatcontainer/"
	reg    string // e.g. "/v3/registration5-gz-semver2/"
}

// serviceIndexPath is the path of the service index on the remote's host: the remote URL itself
// when it names a .json file, else index.json in its folder.
func serviceIndexPath(remote string) string {
	u, err := url.Parse(remote)
	if err != nil {
		return "/index.json"
	}
	p := u.Path
	if strings.HasSuffix(strings.ToLower(p), ".json/") {
		return strings.TrimSuffix(p, "/")
	}
	if !strings.HasSuffix(p, "/") {
		p += "/"
	}
	return p + "index.json"
}

// originOf is the lowered scheme://host of u.
func originOf(u *url.URL) string { return strings.ToLower(u.Scheme + "://" + u.Host) }

// classify tells the proxy where a path of a NuGet proxy is on the remote (format.Spec Classify).
// The paths of the version lists, the registrations and the packages need the resources of the
// remote's service index, which upstreamOf reads before every such fetch.
func (h *Handler) classify(px *repos.Proxy, p repopath.Path) (assets.Class, bool) {
	s := string(p)
	if s == upstreamDir+"index.json" {
		return assets.Class{Changing: true, Remote: serviceIndexPath(px.RemoteURL)}, true
	}
	v, ok := h.upstreams.Load(px.RemoteURL)
	if !ok {
		return assets.Class{}, false
	}
	up := v.(*upstream)
	if rest, ok := strings.CutPrefix(s, upstreamDir+"flat/"); ok {
		return assets.Class{Changing: true, Remote: up.flat + rest}, true
	}
	if rest, ok := strings.CutPrefix(s, upstreamDir+"registration/"); ok {
		return assets.Class{Changing: true, Remote: up.reg + rest}, true
	}
	if rest, ok := strings.CutPrefix(s, upstreamDir+"page/"); ok {
		if !strings.HasPrefix("/"+rest, up.reg) {
			return assets.Class{}, false
		}
		return assets.Class{Changing: true, Remote: "/" + rest}, true
	}
	if _, _, _, ok := flatFile(s); ok {
		return assets.Class{Remote: up.flat + strings.TrimPrefix(s, flatDir)}, true
	}
	return assets.Class{}, false
}

// maxDocument is the largest remote document a proxy reads.
const maxDocument = 64 << 20

// remoteDoc returns the asset of a cached remote document of proxy repo and its bytes: nil when
// the remote does not have it, assets.ErrRemoteFailed when the remote failed with nothing cached.
func (h *Handler) remoteDoc(ctx context.Context, r *http.Request, p *auth.Principal, repo *repos.Repo, path string) (*assets.Asset, []byte, error) {
	rp, err := repopath.Parse(path)
	if err != nil {
		return nil, nil, nil
	}
	a, err := h.host.Reader.Resolve(ctx, r, p, repo, rp)
	if err != nil || a == nil {
		return nil, nil, err
	}
	b, err := h.host.Assets.ReadAll(ctx, a, maxDocument)
	if err != nil {
		return nil, nil, err
	}
	return a, b, nil
}

// errNoServiceIndex is a remote without a usable service index.
var errNoServiceIndex = errors.New("the remote has no NuGet service index")

// upstreamOf reads the service index of proxy repo (cached for metadata_ttl) and records its
// resources for classify. A remote without a service index, or whose flat container or
// registration lies on another host, fails with assets.ErrRemoteFailed.
func (h *Handler) upstreamOf(ctx context.Context, r *http.Request, p *auth.Principal, repo *repos.Repo) (*upstream, error) {
	px, ok := h.host.Repos.Proxy(repo.ID)
	if !ok {
		return nil, fmt.Errorf("proxy repo %s has no remote", repo.Name)
	}
	_, body, err := h.remoteDoc(ctx, r, p, repo, upstreamDir+"index.json")
	if err != nil {
		return nil, err
	}
	if body == nil {
		h.host.Log.Warn("nuget proxy: the remote has no service index", "repo", repo.Name)
		return nil, assets.ErrRemoteFailed
	}
	up, err := parseServiceIndex(px.RemoteURL, body)
	if err != nil {
		h.host.Log.Warn("nuget proxy: the service index of the remote is not usable", "repo", repo.Name, "err", err)
		return nil, assets.ErrRemoteFailed
	}
	h.upstreams.Store(px.RemoteURL, up)
	return up, nil
}

// parseServiceIndex reads the flat container and the registration base of a service index.
// Both must be on the scheme and host of the remote URL.
func parseServiceIndex(remote string, body []byte) (*upstream, error) {
	var doc struct {
		Resources []struct {
			ID   string          `json:"@id"`
			Type json.RawMessage `json:"@type"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, errNoServiceIndex
	}
	ru, err := url.Parse(remote)
	if err != nil {
		return nil, err
	}
	origin := originOf(ru)
	found := map[string]string{}
	for _, res := range doc.Resources {
		var types []string
		var one string
		if json.Unmarshal(res.Type, &one) == nil {
			types = []string{one}
		} else {
			_ = json.Unmarshal(res.Type, &types)
		}
		for _, t := range types {
			if _, ok := found[t]; !ok {
				found[t] = res.ID
			}
		}
	}
	pathOf := func(types ...string) (string, error) {
		for _, t := range types {
			id, ok := found[t]
			if !ok {
				continue
			}
			u, err := url.Parse(id)
			if err != nil || !u.IsAbs() {
				return "", fmt.Errorf("the resource %s is not an absolute URL", t)
			}
			if originOf(u) != origin {
				return "", fmt.Errorf("the resource %s is on another host than the remote", t)
			}
			p := u.Path
			if !strings.HasSuffix(p, "/") {
				p += "/"
			}
			return p, nil
		}
		return "", fmt.Errorf("the service index has no %s", types[0])
	}
	flat, err := pathOf("PackageBaseAddress/3.0.0")
	if err != nil {
		return nil, err
	}
	reg, err := pathOf("RegistrationsBaseUrl/3.6.0", "RegistrationsBaseUrl/3.4.0", "RegistrationsBaseUrl/3.0.0-rc", "RegistrationsBaseUrl")
	if err != nil {
		return nil, err
	}
	return &upstream{origin: origin, flat: flat, reg: reg}, nil
}

// pagePath is the local path of a registration page of the remote, from the page's URL: nil when
// it is not under the registration base of up.
func (up *upstream) pagePath(pageURL string) (string, bool) {
	u, err := url.Parse(pageURL)
	if err != nil || originOf(u) != up.origin || !strings.HasPrefix(u.Path, up.reg) {
		return "", false
	}
	return upstreamDir + "page/" + strings.TrimPrefix(u.Path, "/"), true
}
