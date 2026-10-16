package signing

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/module"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
)

// PermManage creates, imports and deletes signing keys.
const PermManage perm.ID = "signing.manage"

// The audit actions of the signing module.
const (
	ActionKeyCreate = "signing.key.create"
	ActionKeyDelete = "signing.key.delete"
	ActionKeyBind   = "signing.key.bind"
)

// Field is the repo field that names the signing key of a hosted repo whose format binds one
// (format.Spec.SigningKey).
const Field = "signing_key"

// Module returns the signing module: the repository signing keys (the key "default" is
// generated on the first start), their API, and the repo field signing_key that binds a hosted repo
// of a format with format.Spec.SigningKey to a key. It provides *Keys to the modules that require
// it.
func Module() module.Module {
	m := &mod{}
	return module.Module{
		ID: "signing", Title: "Signing keys",
		Register: func(x *module.Extender) {
			x.Schema(component())
			x.Permissions(perm.Subsystem{ID: "signing", Scope: perm.ScopeServer, Label: "Signing keys",
				Help: "The OpenPGP keys that sign repository indexes, such as the Release files of Debian repositories."},
				perm.Def{ID: PermManage, Label: "Manage signing keys", Help: "Create, import and delete signing keys, and download their public keys.", SecondFactor: true})
			x.AuditActions(
				audit.ActionDef{ID: ActionKeyCreate, Label: "Signing key created", Subsystem: "signing"},
				audit.ActionDef{ID: ActionKeyDelete, Label: "Signing key deleted", Subsystem: "signing"},
				audit.ActionDef{ID: ActionKeyBind, Label: "Repository signing key set", Subsystem: "signing"},
			)
			x.Init(func(ctx context.Context, h *module.Host) error {
				m.h = h
				m.keys = NewKeys(h.DB, h.Settings, h.Audit, h.Log)
				if err := m.keys.Load(ctx); err != nil {
					return err
				}
				h.Repos.OnDelete(m.keys.forget) // the binding cascades with the repo; an id can be used again
				h.Provide(m.keys)
				return nil
			})
			x.RepoField(m.field())
			x.Routes(m.routes)
		},
	}
}

type mod struct {
	h    *module.Host
	keys *Keys
}

// field is the repo field signing_key of the hosted repos: the name of the repo's key, "default"
// for a new repo that names none. A proxy and a group sign nothing, so they have no key.
func (m *mod) field() repos.Field {
	return repos.Field{
		Name: Field,
		Applies: func(format, kind string) bool {
			s := m.h.Formats.Get(format)
			return kind == repos.KindHosted && s != nil && s.SigningKey
		},
		Validate: func(ctx context.Context, repo *repos.Repo, raw json.RawMessage) (json.RawMessage, error) {
			var name string
			if err := json.Unmarshal(raw, &name); err != nil {
				return nil, errors.New("must be the name of a signing key")
			}
			if !m.keys.Exists(name) {
				return nil, fmt.Errorf("signing key %q does not exist", name)
			}
			return json.Marshal(name)
		},
		Default: func(ctx context.Context, format string) json.RawMessage {
			raw, _ := json.Marshal(DefaultKey)
			return raw
		},
		Save: func(ctx context.Context, tx *sql.Tx, repo *repos.Repo, v json.RawMessage) error {
			var name string
			if err := json.Unmarshal(v, &name); err != nil {
				return err
			}
			return m.keys.bind(ctx, tx, repo.ID, repo.Name, name)
		},
		Committed: func(repoID int64, v json.RawMessage) {
			var name string
			if json.Unmarshal(v, &name) == nil {
				m.keys.setBound(repoID, name)
			}
		},
		Load: func(repoID int64) json.RawMessage {
			name := m.keys.RepoKey(repoID)
			if name == "" {
				return nil
			}
			raw, _ := json.Marshal(name)
			return raw
		},
	}
}

func (m *mod) routes(r *route.Router) {
	r.Handle("GET /api/signing-keys", route.Policy{Accept: auth.CredSession, Perm: PermManage, SomeRepo: perm.RepoSettings}, m.list)
	r.Handle("POST /api/signing-keys", route.UIPerm(PermManage), m.create)
	r.Handle("GET /api/signing-keys/{name}", route.UIPerm(PermManage), m.get)
	r.Handle("DELETE /api/signing-keys/{name}", route.UIPerm(PermManage), m.delete)
	r.Handle("GET /api/signing-keys/{name}/public.asc", route.UIPerm(PermManage), m.public)
}

// keyJSON is a key with the names of the repos that use it.
type keyJSON struct {
	Key
	Repos []string `json:"repos"`
}

func (m *mod) out(r *http.Request, k Key) (keyJSON, error) {
	used, err := m.keys.UsedBy(r.Context(), k.Name)
	return keyJSON{k, used}, err
}

// fail maps a key error to a response. A key in use answers 409 with the extension member repos.
func fail(w http.ResponseWriter, r *http.Request, err error) {
	var used *InUseError
	switch {
	case errors.As(err, &used):
		httpx.WriteProblem(w, r, httpx.Blank(http.StatusConflict, err.Error()).With(map[string]any{"repos": used.Repos}))
	case errors.Is(err, ErrInvalid):
		httpx.Error(w, r, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrExists):
		httpx.Error(w, r, http.StatusConflict, err.Error())
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, r, http.StatusNotFound, err.Error())
	default:
		route.Fail(w, r, err)
	}
}

// list returns every key by name: [{name, fingerprint, public_key, created_at, error?, repos}].
// It needs signing.manage, or repo.settings on a repository (the Signing tab of a Debian repository picks a key
// from this list).
func (m *mod) list(w http.ResponseWriter, r *http.Request, c *route.Call) {
	keys := m.keys.List()
	out := make([]keyJSON, 0, len(keys))
	for _, k := range keys {
		x, err := m.out(r, k)
		if err != nil {
			route.Fail(w, r, err)
			return
		}
		out = append(out, x)
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// create creates a key: {name} generates a new Ed25519 key; {name, armored, passphrase?} imports
// an armored private key (encrypted ones need the passphrase). 201 with the key; 400 for a bad
// name or key, 409 for a name that is taken.
func (m *mod) create(w http.ResponseWriter, r *http.Request, c *route.Call) {
	var in struct {
		Name       string `json:"name"`
		Armored    string `json:"armored"`
		Passphrase string `json:"passphrase"`
	}
	if !m.h.API.Decode(w, r, &in) {
		return
	}
	var k Key
	var err error
	imported := in.Armored != ""
	if imported {
		k, err = m.keys.Import(r.Context(), in.Name, in.Armored, in.Passphrase)
	} else {
		k, err = m.keys.Create(r.Context(), in.Name)
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, keyJSON{k, []string{}})
}

// get returns key {name} with the repos that use it.
func (m *mod) get(w http.ResponseWriter, r *http.Request, c *route.Call) {
	k, ok := m.keys.Get(r.PathValue("name"))
	if !ok {
		fail(w, r, ErrNotFound)
		return
	}
	out, err := m.out(r, k)
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// delete deletes key {name}: 204, or 409 {error, repos} while repos use it.
func (m *mod) delete(w http.ResponseWriter, r *http.Request, c *route.Call) {
	name := r.PathValue("name")
	if err := m.keys.Delete(r.Context(), name); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// public downloads the armored public key of {name} as <name>.asc.
func (m *mod) public(w http.ResponseWriter, r *http.Request, c *route.Call) {
	k, ok := m.keys.Get(r.PathValue("name"))
	if !ok {
		fail(w, r, ErrNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/pgp-keys")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", k.Name+".asc"))
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(k.PublicKey))
}
