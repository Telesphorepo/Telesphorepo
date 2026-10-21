package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/access"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/route"

	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/proxy"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// Group and proxy repos (SPEC.md "Group and proxy repositories").

func (a *API) registerRepoKinds(mux *route.Router) {
	mux.Handle("PUT /api/repos/{name}/members", route.UIRepo(access.Manage, perm.RepoSettings), a.setMembers)
	mux.Handle("POST /api/repos/{name}/proxy/test", route.UIRepo(access.Manage, perm.RepoSettings), a.testProxy)
}

// memberJSON is one member of a group.
type memberJSON struct {
	Name   string `json:"name"`
	Format string `json:"format"`
	Kind   string `json:"kind"`
	Public bool   `json:"public"`
	// Permissions are the caller's permissions on the member: the Publish tab of a group offers
	// the hosted members that the caller publishes to.
	Permissions []string `json:"permissions"`
}

// proxyJSON is the remote of a proxy, without its credentials.
type proxyJSON struct {
	RemoteURL      string     `json:"remote_url"`
	HasCredentials bool       `json:"has_credentials"`
	MetadataTTL    int64      `json:"metadata_ttl"`  // seconds
	NotFoundTTL    int64      `json:"not_found_ttl"` // seconds
	LastOKAt       *time.Time `json:"last_ok_at"`
	LastError      string     `json:"last_error"`
	LastErrorAt    *time.Time `json:"last_error_at"`
}

func timeOrNil(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

// writeRepo answers the repo JSON of repo for p, who holds perms on it, with the details of its
// kind: the members of a group and the groups of a member (only those p can download from), and
// the remote of a proxy (only for a holder of settings). A group's counters are the sums of those
// members' counters (a path that two members hold counts twice) and the newest of their publishes.
func (a *API) writeRepo(w http.ResponseWriter, r *http.Request, status int, p *auth.Principal, repo *repos.Repo, perms perm.Set) {
	ctx := r.Context()
	out := a.toRepoJSON(repo, perms)
	readable := func(x *repos.Repo) (bool, error) {
		set, err := a.auth.RepoPerms(ctx, p, x)
		return set.Has(perm.RepoDownload), err
	}
	switch repo.Kind {
	case repos.KindGroup:
		members, err := a.repos.MemberRepos(ctx, repo)
		if err != nil {
			route.Fail(w, r, err)
			return
		}
		out.Members = []memberJSON{}
		for _, m := range members {
			set, err := a.auth.RepoPerms(ctx, p, m)
			if err != nil {
				route.Fail(w, r, err)
				return
			}
			if set.Has(perm.RepoDownload) {
				out.Members = append(out.Members, memberJSON{m.Name, m.Format, m.Kind, m.AnonymousRead, set.Strings()})
				// A group counts the files of the members that p reads, as its Files tab shows them.
				out.FileCount += m.FileCount
				out.TotalSize += m.TotalSize
				if t := timeOrNil(m.LastPublishedAt); t != nil && (out.LastPublishedAt == nil || t.After(*out.LastPublishedAt)) {
					out.LastPublishedAt = t
				}
			}
		}
	case repos.KindProxy:
		if px, ok := a.repos.Proxy(repo.ID); ok && perms.Has(perm.RepoSettings) {
			out.Proxy = &proxyJSON{
				RemoteURL: px.RemoteURL, HasCredentials: px.HasCredentials(),
				MetadataTTL: int64(px.MetadataTTL / time.Second), NotFoundTTL: int64(px.NotFoundTTL / time.Second),
				LastOKAt: timeOrNil(px.LastOK), LastError: px.LastError, LastErrorAt: timeOrNil(px.LastErrorAt),
			}
		}
	}
	if repo.Kind != repos.KindGroup {
		for _, id := range a.repos.GroupsOf(repo.ID) {
			g, err := a.repos.ByID(ctx, id)
			if err != nil {
				continue // deleted after the copy was read
			}
			ok, err := readable(g)
			if err != nil {
				route.Fail(w, r, err)
				return
			}
			if ok {
				out.Groups = append(out.Groups, g.Name)
			}
		}
	}
	httpx.WriteJSON(w, status, out)
}

// checkNewProxy checks the remote of a new proxy of format: in is valid, the format serves
// proxies, and the remote answers (unless force). It answers the request and returns false
// otherwise.
func (a *API) checkNewProxy(w http.ResponseWriter, r *http.Request, formatName string, config json.RawMessage, in *repos.ProxyInput, force bool) (*repos.Proxy, bool) {
	if in == nil {
		httpx.Error(w, r, http.StatusBadRequest, "a proxy needs proxy: {remote_url}")
		return nil, false
	}
	if err := a.formats.SupportsKind(formatName, repos.KindProxy); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, err.Error())
		return nil, false
	}
	px, err := in.Apply(nil)
	if err != nil {
		route.Fail(w, r, err)
		return nil, false
	}
	if !force {
		f := a.formats.Get(formatName)
		if err := proxy.Check(r.Context(), px, f.ProxyProbe(config), f.RemoteAuth); err != nil {
			httpx.Error(w, r, http.StatusBadGateway, "The remote repository did not answer the check: "+err.Error()+".")
			return nil, false
		}
	}
	return px, true
}

// setMembers replaces the members of a group and their order (settings): {"members": [names]}.
// A member serves its files through the group only to those who can download from the member.
func (a *API) setMembers(w http.ResponseWriter, r *http.Request, c *route.Call) {
	p, perms := c.P, c.RepoPerms
	var in struct {
		Members []string `json:"members"`
	}
	if !a.kit.Decode(w, r, &in) {
		return
	}
	repo, err := a.repos.Update(r.Context(), c.Repo.Name, repos.Patch{Members: in.Members})
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	a.writeRepo(w, r, http.StatusOK, p, repo, perms)
}

// testProxy checks the remote of a proxy now (settings): 200 {ok: true}, or 502 {ok: false,
// error}. The result is the proxy's last answer or last error.
func (a *API) testProxy(w http.ResponseWriter, r *http.Request, c *route.Call) {
	repo := c.Repo
	px, isProxy := a.repos.Proxy(repo.ID)
	if repo.Kind != repos.KindProxy || !isProxy {
		httpx.Error(w, r, http.StatusBadRequest, repo.Name+" is not a proxy")
		return
	}
	probe := ""
	var auth proxy.RemoteAuth
	if f := a.formats.Get(repo.Format); f != nil && f.ProxyProbe != nil {
		probe, auth = f.ProxyProbe(repo.Config), f.RemoteAuth
	}
	if err := proxy.Check(r.Context(), px, probe, auth); err != nil {
		a.repos.ProxyFailed(r.Context(), repo.ID, err.Error())
		httpx.WriteProblem(w, r, httpx.Blank(http.StatusBadGateway, "The remote repository did not answer the check: "+err.Error()+".").With(map[string]any{"ok": false}))
		return
	}
	a.repos.ProxyOK(r.Context(), repo.ID)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}
