package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"

	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/netrules"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
)

// Network rules (spec §13.1, §13.3): the IP lists, the per-repo IP rules and the reverse proxy
// candidates (admin).

func (a *API) registerNetwork(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/repos/{name}/ip-rules", a.getIPRules)
	mux.HandleFunc("PUT /api/repos/{name}/ip-rules", a.putIPRules)

	mux.HandleFunc("GET /api/ip-lists", a.listIPLists)
	mux.HandleFunc("POST /api/ip-lists", a.createIPList)
	mux.HandleFunc("GET /api/ip-lists/{name}", a.getIPList)
	mux.HandleFunc("PUT /api/ip-lists/{name}", a.updateIPList)
	mux.HandleFunc("DELETE /api/ip-lists/{name}", a.deleteIPList)
	mux.HandleFunc("POST /api/ip-lists/{name}/refresh", a.refreshIPList)
	mux.HandleFunc("POST /api/ip-ranges/parse", a.parseIPRanges)

	mux.HandleFunc("GET /api/network/proxy-candidates", a.listProxyCandidates)
}

// getIPRules returns the IP rules of repo {name}: {read: [Rule], publish: [Rule]}.
func (a *API) getIPRules(w http.ResponseWriter, r *http.Request) {
	repo, ok := a.adminRepo(w, r)
	if !ok {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, a.rules.RepoRules(repo.ID))
}

// putIPRules replaces the IP rules of repo {name} and returns them in canonical form. 400 for
// an invalid CIDR, an IP list that does not exist, a duplicate or a long note.
func (a *API) putIPRules(w http.ResponseWriter, r *http.Request) {
	p, repo, ok := a.adminRepoP(w, r)
	if !ok {
		return
	}
	var in netrules.Rules
	if !httpx.DecodeJSON(w, r, a.cfg().MaxJSONBytes, &in) {
		return
	}
	out, err := a.rules.SetRules(r.Context(), repo.ID, in)
	if errors.Is(err, netrules.ErrInvalid) {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	a.record(r, p, audit.IPRulesUpdate, repo, repo.Name, map[string]any{"read": len(out.Read), "publish": len(out.Publish)})
	httpx.WriteJSON(w, http.StatusOK, out)
}

// ipListJSON is an IP list with what uses it.
type ipListJSON struct {
	netrules.List
	UsedBy netrules.Usage `json:"used_by"`
}

// ipListFail maps an IP list error to a response. An in-use list answers 409 {error, used_by}.
func ipListFail(w http.ResponseWriter, r *http.Request, err error) {
	var inv *netrules.InvalidError
	var used *netrules.InUseError
	switch {
	case errors.As(err, &inv):
		httpx.Error(w, http.StatusBadRequest, err.Error())
	case errors.As(err, &used):
		httpx.WriteJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "used_by": used.Usage})
	case errors.Is(err, netrules.ErrNotFound):
		httpx.Error(w, http.StatusNotFound, err.Error())
	case errors.Is(err, netrules.ErrExists):
		httpx.Error(w, http.StatusConflict, err.Error())
	case errors.Is(err, netrules.ErrReadOnly), errors.Is(err, netrules.ErrNotPreset):
		httpx.Error(w, http.StatusBadRequest, err.Error())
	default:
		fail(w, r, err)
	}
}

// ipListDetail writes list name with its ranges and what uses it.
func (a *API) ipListDetail(w http.ResponseWriter, r *http.Request, status int, name string) {
	l, ok := a.rules.Get(name)
	if !ok {
		httpx.Error(w, http.StatusNotFound, netrules.ErrNotFound.Error())
		return
	}
	u, err := a.rules.Usage(r.Context(), name)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, status, ipListJSON{l, u})
}

// listIPLists returns every IP list without its ranges, presets first:
// [{name, kind, title, description, count, source, fetched_at, error, created_at, used_by}].
func (a *API) listIPLists(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.admin(w, r); !ok {
		return
	}
	lists := a.rules.Lists()
	out := make([]ipListJSON, 0, len(lists))
	for _, l := range lists {
		u, err := a.rules.Usage(r.Context(), l.Name)
		if err != nil {
			fail(w, r, err)
			return
		}
		out = append(out, ipListJSON{l, u})
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// parseIPRanges pulls the addresses and CIDRs out of pasted text {text} and answers
// {ranges, invalid: [{line, text, reason}], duplicates}. It stores nothing. 400 for text over
// 1 MiB or with more than 100000 ranges.
func (a *API) parseIPRanges(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.admin(w, r); !ok {
		return
	}
	var in struct {
		Text string `json:"text"`
	}
	// JSON escapes can make the body longer than the text; MaxParseText bounds the text below.
	if !httpx.DecodeJSON(w, r, 4*netrules.MaxParseText, &in) {
		return
	}
	out, err := netrules.ParseRanges(in.Text)
	if err != nil {
		ipListFail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// getIPList returns list {name} with its ranges and used_by.
func (a *API) getIPList(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.admin(w, r); !ok {
		return
	}
	a.ipListDetail(w, r, http.StatusOK, r.PathValue("name"))
}

// createIPList creates a custom list from {name, title?, description?, ranges?} and answers 201
// with it.
func (a *API) createIPList(w http.ResponseWriter, r *http.Request) {
	p, ok := a.admin(w, r)
	if !ok {
		return
	}
	var in netrules.ListInput
	if !httpx.DecodeJSON(w, r, a.cfg().MaxJSONBytes, &in) {
		return
	}
	l, err := a.rules.Create(r.Context(), in)
	if err != nil {
		ipListFail(w, r, err)
		return
	}
	a.record(r, p, audit.IPListCreate, nil, l.Name, map[string]any{"ranges": l.Count})
	a.ipListDetail(w, r, http.StatusCreated, l.Name)
}

// updateIPList changes custom list {name} from {name?, title?, description?, ranges?}. A rename
// carries the repo rules and trusted_proxies along. 400 for a preset list.
func (a *API) updateIPList(w http.ResponseWriter, r *http.Request) {
	p, ok := a.admin(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	var in netrules.ListInput
	if !httpx.DecodeJSON(w, r, a.cfg().MaxJSONBytes, &in) {
		return
	}
	ref := settings.TrustedProxyList + name
	proxies := a.cfg().TrustedProxies
	if in.Name != nil && *in.Name != name && slices.Contains(proxies, ref) &&
		a.settings.Current().Source(settings.KeyTrustedProxies) == settings.SourceEnv {
		httpx.Error(w, http.StatusConflict, "the trusted proxies name this list and are set by "+
			settings.Lookup(settings.KeyTrustedProxies).Env()+"; change them there before the rename")
		return
	}
	l, old, err := a.rules.Update(r.Context(), name, in)
	if err != nil {
		ipListFail(w, r, err)
		return
	}
	if old != "" && slices.Contains(proxies, ref) {
		next := slices.Clone(proxies)
		for i, e := range next {
			if e == ref {
				next[i] = settings.TrustedProxyList + l.Name
			}
		}
		raw, _ := json.Marshal(next)
		if err := a.settings.Update(r.Context(), map[string]json.RawMessage{settings.KeyTrustedProxies: raw}, p.UserID); err != nil {
			fail(w, r, err)
			return
		}
		a.recordSettings(r, p, map[string]json.RawMessage{settings.KeyTrustedProxies: raw})
	}
	detail := map[string]any{"ranges": l.Count}
	if old != "" {
		detail["renamed_from"] = old
	}
	a.record(r, p, audit.IPListUpdate, nil, l.Name, detail)
	a.ipListDetail(w, r, http.StatusOK, l.Name)
}

// deleteIPList deletes custom list {name}: 204, or 409 {error, used_by} while repo rules or
// trusted_proxies name it, 400 for a preset list.
func (a *API) deleteIPList(w http.ResponseWriter, r *http.Request) {
	p, ok := a.admin(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if err := a.rules.Delete(r.Context(), name); err != nil {
		ipListFail(w, r, err)
		return
	}
	a.record(r, p, audit.IPListDelete, nil, name, nil)
	w.WriteHeader(http.StatusNoContent)
}

// refreshIPList fetches preset list {name} now and returns it. 404 for an unknown list, 400 for
// a custom list, 502 {error} when the fetch fails (the last good list stays).
func (a *API) refreshIPList(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.admin(w, r); !ok {
		return
	}
	name := r.PathValue("name")
	err := a.rules.Refresh(r.Context(), name)
	if errors.Is(err, netrules.ErrNotFound) || errors.Is(err, netrules.ErrNotPreset) {
		ipListFail(w, r, err)
		return
	}
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	a.ipListDetail(w, r, http.StatusOK, name)
}

// listProxyCandidates returns the untrusted peers that sent forwarded headers:
// [{address, last_seen, count, header}], most recent first.
func (a *API) listProxyCandidates(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.admin(w, r); !ok {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, a.rules.ProxyCandidates())
}
