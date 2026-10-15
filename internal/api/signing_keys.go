package api

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/signing"
)

// Repository signing keys (spec §13.3, keys).

func (a *API) registerSigningKeys(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/signing-keys", a.listSigningKeys)
	mux.HandleFunc("POST /api/signing-keys", a.createSigningKey)
	mux.HandleFunc("GET /api/signing-keys/{name}", a.getSigningKey)
	mux.HandleFunc("DELETE /api/signing-keys/{name}", a.deleteSigningKey)
	mux.HandleFunc("GET /api/signing-keys/{name}/public.asc", a.signingKeyPublic)
}

// signingKeyJSON is a key with the names of the repos that use it.
type signingKeyJSON struct {
	signing.Key
	Repos []string `json:"repos"`
}

func (a *API) signingKeyOut(r *http.Request, k signing.Key) (signingKeyJSON, error) {
	used, err := a.keys.UsedBy(r.Context(), k.Name)
	return signingKeyJSON{k, used}, err
}

// signingKeyFail maps a key error to a response. A key in use answers 409 {error, repos}.
func signingKeyFail(w http.ResponseWriter, r *http.Request, err error) {
	var used *signing.InUseError
	switch {
	case errors.As(err, &used):
		httpx.WriteJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "repos": used.Repos})
	case errors.Is(err, signing.ErrInvalid):
		httpx.Error(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, signing.ErrExists):
		httpx.Error(w, http.StatusConflict, err.Error())
	case errors.Is(err, signing.ErrNotFound):
		httpx.Error(w, http.StatusNotFound, err.Error())
	default:
		fail(w, r, err)
	}
}

// listSigningKeys returns every key by name:
// [{name, fingerprint, public_key, created_at, error?, repos}]. It needs keys, or settings on a
// repository (the Signing tab of a Debian repository picks a key from this list).
func (a *API) listSigningKeys(w http.ResponseWriter, r *http.Request) {
	p, ok := a.session(w, r)
	if !ok {
		return
	}
	if !p.Perms.Has(auth.PermKeys) {
		ids, all, err := a.auth.ManagedRepoIDs(r.Context(), p, auth.RepoSettings)
		if err != nil {
			fail(w, r, err)
			return
		}
		if !all && len(ids) == 0 {
			httpx.Error(w, http.StatusForbidden, "permission required: keys, or settings on a repository")
			return
		}
	}
	keys := a.keys.List()
	out := make([]signingKeyJSON, 0, len(keys))
	for _, k := range keys {
		x, err := a.signingKeyOut(r, k)
		if err != nil {
			fail(w, r, err)
			return
		}
		out = append(out, x)
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// createSigningKey creates a key: {name} generates a new Ed25519 key; {name, armored,
// passphrase?} imports an armored private key (encrypted ones need the passphrase). 201 with
// the key; 400 for a bad name or key, 409 for a name that is taken.
func (a *API) createSigningKey(w http.ResponseWriter, r *http.Request) {
	p, ok := a.can(w, r, auth.PermKeys)
	if !ok {
		return
	}
	var in struct {
		Name       string `json:"name"`
		Armored    string `json:"armored"`
		Passphrase string `json:"passphrase"`
	}
	if !httpx.DecodeJSON(w, r, a.cfg().MaxJSONBytes, &in) {
		return
	}
	var k signing.Key
	var err error
	imported := in.Armored != ""
	if imported {
		k, err = a.keys.Import(r.Context(), in.Name, in.Armored, in.Passphrase)
	} else {
		k, err = a.keys.Create(r.Context(), in.Name)
	}
	if err != nil {
		signingKeyFail(w, r, err)
		return
	}
	a.record(r, p, audit.SigningKeyCreate, nil, k.Name, map[string]any{"fingerprint": k.Fingerprint, "imported": imported})
	httpx.WriteJSON(w, http.StatusCreated, signingKeyJSON{k, []string{}})
}

// getSigningKey returns key {name} with the repos that use it.
func (a *API) getSigningKey(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.can(w, r, auth.PermKeys); !ok {
		return
	}
	k, ok := a.keys.Get(r.PathValue("name"))
	if !ok {
		signingKeyFail(w, r, signing.ErrNotFound)
		return
	}
	out, err := a.signingKeyOut(r, k)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// deleteSigningKey deletes key {name}: 204, or 409 {error, repos} while repos use it.
func (a *API) deleteSigningKey(w http.ResponseWriter, r *http.Request) {
	p, ok := a.can(w, r, auth.PermKeys)
	if !ok {
		return
	}
	name := r.PathValue("name")
	k, ok := a.keys.Get(name)
	if !ok {
		signingKeyFail(w, r, signing.ErrNotFound)
		return
	}
	if err := a.keys.Delete(r.Context(), name); err != nil {
		signingKeyFail(w, r, err)
		return
	}
	a.record(r, p, audit.SigningKeyDelete, nil, name, map[string]any{"fingerprint": k.Fingerprint})
	w.WriteHeader(http.StatusNoContent)
}

// signingKeyPublic downloads the armored public key of {name} as <name>.asc.
func (a *API) signingKeyPublic(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.can(w, r, auth.PermKeys); !ok {
		return
	}
	k, ok := a.keys.Get(r.PathValue("name"))
	if !ok {
		signingKeyFail(w, r, signing.ErrNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/pgp-keys")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", k.Name+".asc"))
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(k.PublicKey))
}
