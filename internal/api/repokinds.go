package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/proxy"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// Group and proxy repos (spec "Group repositories", "Proxy repositories").

func (a *API) registerRepoKinds(mux *http.ServeMux) {
	mux.HandleFunc("PUT /api/repos/{name}/members", a.setMembers)
	mux.HandleFunc("POST /api/repos/{name}/proxy/test", a.testProxy)
}

// memberJSON is one member of a group.
type memberJSON struct {
	Name   string `json:"name"`
	Format string `json:"format"`
	Kind   string `json:"kind"`
	Public bool   `json:"public"`
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
// the remote of a proxy (only for a holder of settings).
func (a *API) writeRepo(w http.ResponseWriter, r *http.Request, status int, p *auth.Principal, repo *repos.Repo, perms auth.RepoPerm) {
	ctx := r.Context()
	out := toRepoJSON(repo, perms)
	readable := func(x *repos.Repo) (bool, error) {
		set, err := a.auth.RepoPerms(ctx, p, x)
		return set.Has(auth.RepoDownload), err
	}
	switch repo.Kind {
	case repos.KindGroup:
		members, err := a.repos.MemberRepos(ctx, repo)
		if err != nil {
			fail(w, r, err)
			return
		}
		out.Members = []memberJSON{}
		for _, m := range members {
			ok, err := readable(m)
			if err != nil {
				fail(w, r, err)
				return
			}
			if ok {
				out.Members = append(out.Members, memberJSON{m.Name, m.Format, m.Kind, m.AnonymousRead})
			}
		}
	case repos.KindProxy:
		if px, ok := a.repos.Proxy(repo.ID); ok && perms.Has(auth.RepoSettings) {
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
				fail(w, r, err)
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
		httpx.Error(w, http.StatusBadRequest, "a proxy needs proxy: {remote_url}")
		return nil, false
	}
	if err := format.SupportsKind(formatName, repos.KindProxy); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return nil, false
	}
	px, err := in.Apply(nil)
	if err != nil {
		fail(w, r, err)
		return nil, false
	}
	if !force {
		probe := format.Get(formatName).(format.Proxied).ProxyProbe(config)
		if err := proxy.Check(r.Context(), px, probe); err != nil {
			httpx.Error(w, http.StatusBadGateway, "The remote repository did not answer the check: "+err.Error()+".")
			return nil, false
		}
	}
	return px, true
}

// proxyChanges names the fields of a proxy remote that changed, for the audit log (never a
// credential value).
func proxyChanges(old, new *repos.Proxy) []string {
	var out []string
	if old.RemoteURL != new.RemoteURL {
		out = append(out, "remote_url")
	}
	if old.Username != new.Username || old.Password != new.Password {
		out = append(out, "credentials")
	}
	if old.MetadataTTL != new.MetadataTTL {
		out = append(out, "metadata_ttl")
	}
	if old.NotFoundTTL != new.NotFoundTTL {
		out = append(out, "not_found_ttl")
	}
	return out
}

// setMembers replaces the members of a group and their order (settings): {"members": [names]}.
// A member serves its files through the group only to those who can download from the member.
func (a *API) setMembers(w http.ResponseWriter, r *http.Request) {
	p, _, perms, ok := a.canRepo(w, r, auth.RepoSettings)
	if !ok {
		return
	}
	var in struct {
		Members []string `json:"members"`
	}
	if !httpx.DecodeJSON(w, r, a.cfg().MaxJSONBytes, &in) {
		return
	}
	repo, err := a.repos.SetMembers(r.Context(), r.PathValue("name"), in.Members)
	if err != nil {
		fail(w, r, err)
		return
	}
	a.record(r, p, audit.RepoUpdate, repo, repo.Name, map[string]any{"fields": []string{"members"}, "members": in.Members})
	a.writeRepo(w, r, http.StatusOK, p, repo, perms)
}

// testProxy checks the remote of a proxy now (settings): 200 {ok: true}, or 502 {ok: false,
// error}. The result is the proxy's last answer or last error.
func (a *API) testProxy(w http.ResponseWriter, r *http.Request) {
	_, repo, _, ok := a.canRepo(w, r, auth.RepoSettings)
	if !ok {
		return
	}
	px, isProxy := a.repos.Proxy(repo.ID)
	if repo.Kind != repos.KindProxy || !isProxy {
		httpx.Error(w, http.StatusBadRequest, repo.Name+" is not a proxy")
		return
	}
	probe := ""
	if f, ok := format.Get(repo.Format).(format.Proxied); ok {
		probe = f.ProxyProbe(repo.Config)
	}
	if err := proxy.Check(r.Context(), px, probe); err != nil {
		a.repos.ProxyFailed(r.Context(), repo.ID, err.Error())
		httpx.WriteJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": "The remote repository did not answer the check: " + err.Error() + "."})
		return
	}
	a.repos.ProxyOK(r.Context(), repo.ID)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}
