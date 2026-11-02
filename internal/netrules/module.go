package netrules

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"

	"github.com/Telesphorepo/Telesphorepo/internal/access"
	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/jobs"
	"github.com/Telesphorepo/Telesphorepo/internal/module"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
)

// PermManage edits the IP lists, the trusted proxies and the proxy settings.
const PermManage perm.ID = "network.manage"

// The audit actions of the network module.
const (
	ActionRulesUpdate   = "network.rules.update"
	ActionListCreate    = "network.list.create"
	ActionListUpdate    = "network.list.update"
	ActionListDelete    = "network.list.delete"
	ActionTrustedUpdate = "network.trusted.update"
)

// Module returns the network module: the client address and scheme of every request, the IP lists
// and their refresh job, the allowed networks of each repo (an address in them downloads from the
// repo: the access engine asks the module through access.Networks), and their API. It provides
// *Service to the modules that require it.
func Module() module.Module {
	m := &mod{}
	return module.Module{
		ID: "network", Title: "Networks and proxy",
		Register: func(x *module.Extender) {
			x.Schema(component())
			x.Permissions(perm.Subsystem{ID: "network", Scope: perm.ScopeServer, Label: "Networks and proxy",
				Help: "The IP lists that the allowed networks of repositories use, and the reverse proxies whose client addresses the server trusts."},
				perm.Def{ID: PermManage, Label: "Manage networks and proxy", Help: "Create, edit and delete IP lists, and set the trusted reverse proxies.", SecondFactor: true})
			x.AuditActions(
				audit.ActionDef{ID: ActionRulesUpdate, Label: "Repository allowed networks changed", Subsystem: "network"},
				audit.ActionDef{ID: ActionListCreate, Label: "IP list created", Subsystem: "network"},
				audit.ActionDef{ID: ActionListUpdate, Label: "IP list changed", Subsystem: "network"},
				audit.ActionDef{ID: ActionListDelete, Label: "IP list deleted", Subsystem: "network"},
				audit.ActionDef{ID: ActionTrustedUpdate, Label: "Trusted proxies changed", Subsystem: "network"},
			)
			x.Settings(&settings.Group{ID: "network", Label: "Networks and proxy", Perm: PermManage, Page: "/settings/proxy",
				Defs: []*settings.Def{TrustedProxies.Def(), ProbeHosts.Def()}, Validate: m.validate})
			x.Init(func(ctx context.Context, h *module.Host) error {
				m.h = h
				m.rules = New(h.DB, h.Settings.Current, h.Audit, h.Log)
				if err := m.rules.Load(ctx); err != nil {
					return err
				}
				if err := m.rules.resolveEnvLists(); err != nil {
					return err
				}
				h.Repos.OnDelete(m.rules.Forget) // the rows cascade with the repo; an id can be used again
				h.Repos.CheckChange(m.rules.checkPublic)
				h.Access.SetNetworks(m.rules)
				h.Provide(m.rules)
				return nil
			})
			x.ResolveClient(func(r *http.Request) httpx.Client { return m.rules.Client(r) }) // m.rules is set in Init
			// The probe hosts answer as the main host (whoami).
			for i := range 2 {
				x.Host(func(s *settings.Snapshot) string { return probeHost(s, i) })
			}
			x.Job(jobs.Job{Name: "network.presets", Every: checkEvery, Tick: func(ctx context.Context) error {
				return m.rules.RefreshStale(ctx)
			}})
			x.Job(jobs.Job{Name: "network.restrict", Start: m.restrictPublic})
			x.Routes(m.routes)
		},
	}
}

type mod struct {
	h     *module.Host
	rules *Service
}

// restrictPublic makes restricted every public repo with allowed networks, through
// repos.Service, so the edge objects and the counters follow. Such a repo comes from the time a
// public repo could also have network rules; a public repo takes none now.
func (m *mod) restrictPublic(ctx context.Context) error {
	m.rules.rulesMu.RLock()
	ids := make([]int64, 0, len(m.rules.rules))
	for id := range m.rules.rules {
		ids = append(ids, id)
	}
	m.rules.rulesMu.RUnlock()
	off := false
	for _, id := range ids {
		repo, err := m.h.Repos.ByID(ctx, id)
		if errors.Is(err, repos.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if !repo.AnonymousRead {
			continue
		}
		if _, err := m.h.Repos.Update(audit.System(ctx, "network"), repo.Name, repos.Patch{AnonymousRead: &off}); err != nil {
			return fmt.Errorf("make %s restricted: %w", repo.Name, err)
		}
	}
	return nil
}
func (m *mod) routes(r *route.Router) {
	r.Handle("GET /api/repos/{name}/ip-rules", route.UIRepo(access.Manage, perm.RepoSettings), m.getRules)
	r.Handle("PUT /api/repos/{name}/ip-rules", route.UIRepo(access.Manage, perm.RepoSettings), m.putRules)

	r.Handle("GET /api/ip-lists", route.Policy{Accept: auth.CredSession, Perm: PermManage, SomeRepo: perm.RepoSettings}, m.listLists)
	r.Handle("POST /api/ip-lists", route.UIPerm(PermManage), m.createList)
	r.Handle("GET /api/ip-lists/{name}", route.UIPerm(PermManage), m.getList)
	r.Handle("PUT /api/ip-lists/{name}", route.UIPerm(PermManage), m.updateList)
	r.Handle("DELETE /api/ip-lists/{name}", route.UIPerm(PermManage), m.deleteList)
	r.Handle("POST /api/ip-lists/{name}/refresh", route.UIPerm(PermManage), m.refreshList)
	r.Handle("POST /api/ip-ranges/parse", route.Policy{Accept: auth.CredSession, Perm: PermManage, SomeRepo: perm.RepoSettings}, m.parseRanges)

	r.Handle("GET /api/network/whoami", route.Public, m.whoami)
	r.Handle("GET /api/network/proxy-candidates", route.UIPerm(PermManage), m.proxyCandidates)
	r.Handle("GET /api/network/trusted-proxies", route.UIPerm(PermManage), m.getTrusted)
	r.Handle("PUT /api/network/trusted-proxies", route.UIPerm(PermManage), m.putTrusted)
}

// getTrusted returns the IP lists that the trusted proxies include: {lists: [{id, name}],
// env_lists: [name]}.
func (m *mod) getTrusted(w http.ResponseWriter, r *http.Request, c *route.Call) {
	httpx.WriteJSON(w, http.StatusOK, m.rules.TrustedLists())
}

// putTrusted replaces the IP lists that the trusted proxies include: {lists: [id]}. 400 for an
// unknown id.
func (m *mod) putTrusted(w http.ResponseWriter, r *http.Request, c *route.Call) {
	var in struct {
		Lists []int64 `json:"lists"`
	}
	if !m.h.API.Decode(w, r, &in) {
		return
	}
	out, err := m.rules.SetTrustedLists(r.Context(), in.Lists)
	if err != nil {
		listFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// getRules returns the allowed networks of repo {name} (settings): {read: [Rule]}.
func (m *mod) getRules(w http.ResponseWriter, r *http.Request, c *route.Call) {
	repo := c.Repo
	httpx.WriteJSON(w, http.StatusOK, m.rules.RepoRules(repo.ID))
}

// putRules replaces the allowed networks of repo {name} (settings) and returns them in canonical
// form. 400 for an invalid CIDR, an IP list that does not exist, a duplicate, a long note, or a
// network on a public repo.
func (m *mod) putRules(w http.ResponseWriter, r *http.Request, c *route.Call) {
	repo := c.Repo
	var in Rules
	if !m.h.API.Decode(w, r, &in) {
		return
	}
	out, err := m.rules.SetRules(r.Context(), repo, in)
	if errors.Is(err, ErrInvalid) || errors.Is(err, ErrPublic) {
		httpx.Error(w, r, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// listJSON is an IP list with what uses it.
type listJSON struct {
	List
	UsedBy Usage `json:"used_by"`
}

// listFail maps an IP list error to a response. An in-use list answers 409 with the extension member used_by.
func listFail(w http.ResponseWriter, r *http.Request, err error) {
	var inv *InvalidError
	var used *InUseError
	switch {
	case errors.As(err, &inv):
		httpx.Error(w, r, http.StatusBadRequest, err.Error())
	case errors.As(err, &used):
		httpx.WriteProblem(w, r, httpx.Blank(http.StatusConflict, err.Error()).With(map[string]any{"used_by": used.Usage}))
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, r, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrExists), errors.Is(err, ErrEnvNamed):
		httpx.Error(w, r, http.StatusConflict, err.Error())
	case errors.Is(err, ErrReadOnly), errors.Is(err, ErrNotPreset):
		httpx.Error(w, r, http.StatusBadRequest, err.Error())
	default:
		route.Fail(w, r, err)
	}
}

// listDetail writes list name with its ranges and what uses it.
func (m *mod) listDetail(w http.ResponseWriter, r *http.Request, status int, name string) {
	l, ok := m.rules.Get(name)
	if !ok {
		httpx.Error(w, r, http.StatusNotFound, ErrNotFound.Error())
		return
	}
	u, err := m.rules.Usage(r.Context(), name)
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, status, listJSON{l, u})
}

// listLists returns every IP list without its ranges, presets first:
// [{name, kind, title, description, count, source, fetched_at, error, created_at, used_by}].
// It needs network.manage, or repo.settings on a repository (the Access tab picks the lists of the
// repository's allowed networks from it).
func (m *mod) listLists(w http.ResponseWriter, r *http.Request, c *route.Call) {
	lists := m.rules.Lists()
	out := make([]listJSON, 0, len(lists))
	for _, l := range lists {
		u, err := m.rules.Usage(r.Context(), l.Name)
		if err != nil {
			route.Fail(w, r, err)
			return
		}
		out = append(out, listJSON{l, u})
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// parseRanges pulls the addresses and CIDRs out of pasted text {text} and answers
// {ranges, invalid: [{line, text, reason}], duplicates}. It stores nothing. It needs
// network.manage, or repo.settings on a repository (the Access tab adds addresses with it). 400
// for text over 1 MiB or with more than 100000 ranges.
func (m *mod) parseRanges(w http.ResponseWriter, r *http.Request, c *route.Call) {
	var in struct {
		Text string `json:"text"`
	}
	// JSON escapes can make the body longer than the text; MaxParseText bounds the text below.
	if !httpx.DecodeJSON(w, r, 4*MaxParseText, &in) {
		return
	}
	out, err := ParseRanges(in.Text)
	if err != nil {
		listFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// getList returns list {name} with its ranges and used_by.
func (m *mod) getList(w http.ResponseWriter, r *http.Request, c *route.Call) {
	m.listDetail(w, r, http.StatusOK, r.PathValue("name"))
}

// createList creates a custom list from {name, title?, description?, ranges?} and answers 201
// with it.
func (m *mod) createList(w http.ResponseWriter, r *http.Request, c *route.Call) {
	var in ListInput
	if !m.h.API.Decode(w, r, &in) {
		return
	}
	l, err := m.rules.Create(r.Context(), in)
	if err != nil {
		listFail(w, r, err)
		return
	}
	m.listDetail(w, r, http.StatusCreated, l.Name)
}

// updateList changes custom list {name} from {name?, title?, description?, ranges?}. Allowed networks
// and trusted proxies name the list by its id, so a rename keeps them. 400 for a preset list,
// 409 for the rename of a list that TR_TRUSTED_PROXIES names.
func (m *mod) updateList(w http.ResponseWriter, r *http.Request, c *route.Call) {
	var in ListInput
	if !m.h.API.Decode(w, r, &in) {
		return
	}
	l, err := m.rules.Update(r.Context(), r.PathValue("name"), in)
	if err != nil {
		listFail(w, r, err)
		return
	}
	m.listDetail(w, r, http.StatusOK, l.Name)
}

// deleteList deletes custom list {name}: 204, or 409 {error, used_by} while allowed networks or the
// trusted proxies name it, 400 for a preset list.
func (m *mod) deleteList(w http.ResponseWriter, r *http.Request, c *route.Call) {
	name := r.PathValue("name")
	if err := m.rules.Delete(r.Context(), name); err != nil {
		listFail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// refreshList fetches preset list {name} now and returns it. 404 for an unknown list, 400 for a
// custom list, 502 {error} when the fetch fails (the last good list stays).
func (m *mod) refreshList(w http.ResponseWriter, r *http.Request, c *route.Call) {
	name := r.PathValue("name")
	err := m.rules.Refresh(r.Context(), name)
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrNotPreset) {
		listFail(w, r, err)
		return
	}
	if err != nil {
		httpx.Error(w, r, http.StatusBadGateway, err.Error())
		return
	}
	m.listDetail(w, r, http.StatusOK, name)
}

// proxyCandidates returns the untrusted peers that sent forwarded headers:
// [{address, last_seen, count, header}], most recent first.
func (m *mod) proxyCandidates(w http.ResponseWriter, r *http.Request, c *route.Call) {
	httpx.WriteJSON(w, http.StatusOK, m.rules.ProxyCandidates())
}

// whoami answers {ip, cidr, probes}: the client address of the request as the server reads it,
// the range to allow for it (an IPv4 address as its /32, an IPv6 address as its /64: one network
// whose devices change their addresses), and the whoami URLs on the probe hosts
// (network.probe_hosts), which a browser asks to learn its address of the other family. Any
// caller; it echoes the caller's own address only. A cross-origin GET from the base URL's origin
// may read it (the probe hosts are other origins).
func (m *mod) whoami(w http.ResponseWriter, r *http.Request, c *route.Call) {
	snap := m.h.Settings.Current()
	base, err := url.Parse(settings.BaseURL.Get(snap))
	scheme := "https"
	if err == nil && base.Host != "" {
		w.Header().Set("Access-Control-Allow-Origin", base.Scheme+"://"+base.Host)
		scheme = base.Scheme
	}
	w.Header().Set("Cache-Control", "no-store")
	probes := []string{}
	for _, h := range ProbeHosts.Get(snap) {
		probes = append(probes, scheme+"://"+h+"/api/network/whoami")
	}
	ip, cidr := "", ""
	if a := httpx.ClientIP(r).Unmap(); a.IsValid() {
		bits := 32
		if a.Is6() {
			bits = 64
		}
		ip, cidr = a.WithZone("").String(), netip.PrefixFrom(a.WithZone(""), bits).Masked().String()
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ip": ip, "cidr": cidr, "probes": probes})
}
