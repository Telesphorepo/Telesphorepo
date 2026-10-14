package api

import (
	"errors"
	"net/http"

	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/netrules"
)

// Network rules (spec §13.1): the IP range presets and the per-repo IP rules (admin).

// getIPRules returns the IP rules of repo {name}: {read: [Rule], publish: [Rule]}.
func (a *API) getIPRules(w http.ResponseWriter, r *http.Request) {
	repo, ok := a.adminRepo(w, r)
	if !ok {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, a.rules.RepoRules(repo.ID))
}

// putIPRules replaces the IP rules of repo {name} and returns them in canonical form. 400 for
// an invalid CIDR, an unknown preset, a duplicate or a long note.
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

// listPresets returns the fetched IP range presets: [{name, title, count, fetched_at, error}].
func (a *API) listPresets(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.admin(w, r); !ok {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, a.rules.Presets())
}

// listProxyCandidates returns the untrusted peers that sent forwarded headers:
// [{address, last_seen, count, header}], most recent first.
func (a *API) listProxyCandidates(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.admin(w, r); !ok {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, a.rules.ProxyCandidates())
}

// refreshPreset fetches preset {name} now and returns it. 404 for an unknown preset; 502 with
// {"error"} when the fetch fails (the last good list stays).
func (a *API) refreshPreset(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.admin(w, r); !ok {
		return
	}
	name := r.PathValue("name")
	err := a.rules.Refresh(r.Context(), name)
	if errors.Is(err, netrules.ErrUnknownPreset) {
		httpx.Error(w, http.StatusNotFound, "unknown preset")
		return
	}
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	pr, _ := a.rules.Preset(name)
	httpx.WriteJSON(w, http.StatusOK, pr)
}
