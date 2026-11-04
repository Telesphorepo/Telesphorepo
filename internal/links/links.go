// Package links is the module links: short-lived signed download links. A person who reads a
// repo makes a link to one file (POST /api/repos/{name}/links) and pastes it into a tool that
// takes a plain URL only. The link is the file's format URL with an expiry and an HMAC-SHA256
// signature in its query; the route policy of a format read accepts it as the credential
// auth.CredLink, for that path of that repo only, with the current grants of its maker.
package links

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/access"
	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/module"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
)

// ActionCreate is the audit action of a new link: the repo, the path and the expiry, never the
// signature.
const ActionCreate = "file.link.create"

// maxTTLLimit is the longest links.max_ttl.
const maxTTLLimit = 30 * 24 * time.Hour

// minSecret is the shortest links.secret that a person may enter.
const minSecret = 32

// The settings of the group "links".
var (
	TTL = settings.Duration(&settings.Def{Key: "links.ttl", Default: "1h", Label: "Download link lifetime",
		Help: "A download link that does not name its lifetime works for this time, for example 1h. At most the longest lifetime."})
	MaxTTL = settings.Duration(&settings.Def{Key: "links.max_ttl", Default: "7d", Label: "Longest download link lifetime",
		Help: "No download link works longer than this, also one made before a change. The maximum is 30 days.",
		Validate: func(v any) error {
			if v.(time.Duration) > maxTTLLimit {
				return errors.New("must be at most 30 days")
			}
			return nil
		}})
	Secret = settings.String(&settings.Def{Key: "links.secret", Label: "Download link key", Secret: true,
		Help: "The key that signs download links. The server makes one at the first link. Enter a new value (at least 32 characters), or reset it to make a new one at the next link: every link made before stops working.",
		Validate: func(v any) error {
			if s := v.(string); s != "" && len(s) < minSecret {
				return fmt.Errorf("must be at least %d characters", minSecret)
			}
			return nil
		}})
)

// Module returns the module links: the settings group "links", the audit action
// file.link.create, the route that makes a link and the verifier of links (auth.CredLink).
func Module() module.Module {
	m := &mod{}
	return module.Module{
		ID: "links", Title: "Download links",
		Register: func(x *module.Extender) {
			x.Settings(&settings.Group{ID: "links", Label: "Download links", Perm: perm.SettingsGeneral,
				Defs: []*settings.Def{TTL.Def(), MaxTTL.Def(), Secret.Def()},
				Validate: func(d *settings.Draft, _ settings.Mode) error {
					if TTL.Get(d.Next) > MaxTTL.Get(d.Next) {
						d.Fail(TTL.Name(), "must be at most the longest lifetime ("+MaxTTL.Name()+")")
					}
					return nil
				}})
			x.AuditActions(audit.ActionDef{ID: ActionCreate, Label: "Download link created", Subsystem: "repo"})
			x.Init(func(_ context.Context, h *module.Host) error {
				m.h = h
				h.Auth.SetLinkVerifier(m.verify)
				return nil
			})
			x.Routes(func(r *route.Router) {
				r.Handle("POST /api/repos/{name}/links", route.Policy{Accept: auth.CredSession,
					Repo: &route.RepoRule{Op: access.Read, Perm: perm.RepoDownload}}, m.create)
			})
		},
	}
}

type mod struct {
	h  *module.Host
	mu sync.Mutex // makes the key once (key)
}

// linkRequest is the body of POST /api/repos/{name}/links: the file path and the lifetime in
// seconds (absent: links.ttl).
type linkRequest struct {
	Path string `json:"path"`
	TTL  *int64 `json:"ttl"`
}

// Link is the answer: the URL of the file with the signature, and when it stops working.
type Link struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

// create makes a link to one file of the repo for the caller. The file must exist (in a group:
// in a member the caller reads), and the format must serve its files under /<format>/{repo}/.
func (m *mod) create(w http.ResponseWriter, r *http.Request, c *route.Call) {
	var in linkRequest
	if !m.h.API.Decode(w, r, &in) {
		return
	}
	p, err := repopath.Parse(strings.TrimPrefix(in.Path, "/"))
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid path")
		return
	}
	snap := m.h.Settings.Current()
	ttl, longest := TTL.Get(snap), MaxTTL.Get(snap)
	if in.TTL != nil {
		if *in.TTL < 1 || *in.TTL > int64(longest/time.Second) {
			httpx.Error(w, r, http.StatusBadRequest, fmt.Sprintf("ttl must be 1 to %d seconds", int64(longest/time.Second)))
			return
		}
		ttl = time.Duration(*in.TTL) * time.Second
	}
	ttl = min(ttl, longest)
	repo := c.Repo
	if m.h.API.RepoRooted == nil || !m.h.API.RepoRooted(repo.Format) {
		httpx.Error(w, r, http.StatusConflict, "the files of a "+repo.Format+" repository have no download URL")
		return
	}
	found, err := m.exists(r, c, string(p))
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	if !found {
		httpx.Error(w, r, http.StatusNotFound, "file not found")
		return
	}
	key, err := m.key(r.Context(), c.P.UserID)
	if errors.Is(err, errNoKey) {
		httpx.Error(w, r, http.StatusServiceUnavailable, err.Error())
		return
	}
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	exp := time.Now().Add(ttl).Unix()
	server := "/" + repo.Format + "/" + repo.Name + "/" + string(p)
	segs := strings.Split(server, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	q := url.Values{}
	q.Set(auth.LinkExpParam, strconv.FormatInt(exp, 10))
	q.Set(auth.LinkSigParam, strconv.FormatInt(c.P.UserID, 10)+"."+strconv.FormatInt(repo.ID, 10)+"."+
		base64.RawURLEncoding.EncodeToString(sign(key, repo.ID, c.P.UserID, exp, server)))
	expires := time.Unix(exp, 0).UTC()
	if err := m.h.Audit.Record(r.Context(), audit.Event{Action: ActionCreate, RepoID: repo.ID, Repo: repo.Name,
		Target: string(p), Detail: map[string]any{"expires_at": expires.Format(time.RFC3339)}}); err != nil {
		route.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, Link{
		URL:       strings.TrimSuffix(m.h.API.BaseURL(r), "/") + strings.Join(segs, "/") + "?" + q.Encode(),
		ExpiresAt: expires,
	})
}

// exists reports whether file p is in the repo of c: in a group, in a member that the caller
// reads through the group.
func (m *mod) exists(r *http.Request, c *route.Call, p string) (bool, error) {
	in := []*repos.Repo{c.Repo}
	if c.Repo.Kind == repos.KindGroup {
		members, err := m.h.Access.GroupMembers(r.Context(), r, c.P, c.Repo)
		if err != nil {
			return false, err
		}
		in = members
	}
	for _, repo := range in {
		_, err := m.h.Assets.Get(r.Context(), repo.ID, p)
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, assets.ErrNotFound) {
			return false, err
		}
	}
	return false, nil
}

// errNoKey: links.secret is set empty by the environment.
var errNoKey = errors.New("download links need the setting links.secret")

// key returns links.secret, and makes it on the first link: 32 random bytes, saved like a change
// of the setting by the person userID.
func (m *mod) key(ctx context.Context, userID int64) ([]byte, error) {
	if s := Secret.Get(m.h.Settings.Current()); s != "" {
		return []byte(s), nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cur := m.h.Settings.Current()
	if s := Secret.Get(cur); s != "" {
		return []byte(s), nil
	}
	if cur.Source(Secret.Name()) == settings.SourceEnv {
		return nil, errNoKey
	}
	b := make([]byte, 32)
	rand.Read(b)
	raw, _ := json.Marshal(base64.RawURLEncoding.EncodeToString(b))
	var err error
	if cur.Source(Secret.Name()) == settings.SourceDefault {
		_, err = m.h.Settings.Fill(ctx, Secret.Name(), raw, userID, "download link")
	} else {
		_, err = m.h.Settings.Patch(ctx, map[string]json.RawMessage{Secret.Name(): raw}, userID)
	}
	if err != nil {
		return nil, err
	}
	if s := Secret.Get(m.h.Settings.Current()); s != "" {
		return []byte(s), nil
	}
	return nil, errNoKey
}

// sign is the MAC of a link: HMAC-SHA256 under key over the repo id, the user id, the expiry and
// the server path of the file ("/<format>/<repo>/<path>", not escaped).
func sign(key []byte, repoID, userID, exp int64, server string) []byte {
	mac := hmac.New(sha256.New, key)
	fmt.Fprintf(mac, "telesphorepo link v1\n%d\n%d\n%d\n%s", repoID, userID, exp, server)
	return mac.Sum(nil)
}

// verify is the auth.LinkVerifier: the link of r must carry a signature of the current key over
// the path of r, and its expiry must lie in the future and within links.max_ttl from now.
func (m *mod) verify(ctx context.Context, r *http.Request) (*auth.Principal, error) {
	q := r.URL.Query()
	parts := strings.Split(q.Get(auth.LinkSigParam), ".")
	if len(parts) != 3 {
		return nil, auth.ErrBadCredentials
	}
	userID, err1 := strconv.ParseInt(parts[0], 10, 64)
	repoID, err2 := strconv.ParseInt(parts[1], 10, 64)
	exp, err3 := strconv.ParseInt(q.Get(auth.LinkExpParam), 10, 64)
	got, err4 := base64.RawURLEncoding.DecodeString(parts[2])
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil || userID <= 0 || repoID <= 0 {
		return nil, auth.ErrBadCredentials
	}
	snap := m.h.Settings.Current()
	now := time.Now()
	if now.Unix() >= exp || time.Unix(exp, 0).After(now.Add(MaxTTL.Get(snap))) {
		return nil, auth.ErrBadCredentials
	}
	key := Secret.Get(snap)
	if key == "" || !hmac.Equal(got, sign([]byte(key), repoID, userID, exp, r.URL.Path)) {
		return nil, auth.ErrBadCredentials
	}
	return m.h.Auth.LinkPrincipal(ctx, userID, repoID, time.Unix(exp, 0))
}
