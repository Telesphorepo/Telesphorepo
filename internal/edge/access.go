package edge

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/edge/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// object returns the JSON of the access object at key, computed from the database now;
// ok=false when its row is gone (the object must not exist). config.json is putConfig's.
func (m *mod) object(ctx context.Context, key string) ([]byte, bool, error) {
	var obj any
	var err error
	ok := true
	switch {
	case strings.HasPrefix(key, repoPrefix):
		obj, ok, err = m.repoObject(ctx, strings.TrimPrefix(key, repoPrefix))
	case strings.HasPrefix(key, userPrefix):
		id, perr := strconv.ParseInt(strings.TrimPrefix(key, userPrefix), 10, 64)
		if perr != nil {
			return nil, false, nil
		}
		obj, ok, err = m.userObject(ctx, id)
	case strings.HasPrefix(key, tokenPrefix):
		obj, ok, err = m.tokenObject(ctx, strings.TrimPrefix(key, tokenPrefix))
	case strings.HasPrefix(key, devicePrefix):
		id, perr := strconv.ParseInt(strings.TrimPrefix(key, devicePrefix), 10, 64)
		if perr != nil {
			return nil, false, nil
		}
		obj, ok, err = m.deviceObject(ctx, id)
	default:
		return nil, false, nil
	}
	if err != nil || !ok {
		return nil, false, err
	}
	b, err := json.Marshal(obj)
	return b, true, err
}

// repoObject is the object of the repo name: none for a repo whose format the edge does not serve.
func (m *mod) repoObject(ctx context.Context, name string) (any, bool, error) {
	repo, err := m.h.Repos.ByName(ctx, name)
	if errors.Is(err, repos.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return m.repoObjectOf(ctx, repo)
}

func (m *mod) repoObjectOf(ctx context.Context, repo *repos.Repo) (any, bool, error) {
	if m.edgeOf(repo) == nil {
		return nil, false, nil
	}
	private, err := m.h.Access.Private(ctx, repo)
	if err != nil {
		return nil, false, err
	}
	o := repoObject{ID: repo.ID, Format: repo.Format, Kind: repo.Kind, Anon: repo.AnonymousRead,
		Private: private, Guarded: m.h.Access.Guarded(repo), Members: []string{}}
	if repo.Kind == repos.KindGroup {
		members, err := m.h.Repos.MemberRepos(ctx, repo)
		if err != nil {
			return nil, false, err
		}
		for _, mb := range members {
			o.Members = append(o.Members, mb.Name)
		}
	}
	return o, true, nil
}

func (m *mod) userObject(ctx context.Context, id int64) (any, bool, error) {
	u, err := m.rq.GetEdgeUser(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	o, err := m.userObjectOf(ctx, u.ID, u.Disabled, u.Kind)
	return o, err == nil, err
}

func (m *mod) userObjectOf(ctx context.Context, id int64, disabled bool, kind string) (userObject, error) {
	perms, err := m.rq.ListEdgeUserPerms(ctx, id)
	if err != nil {
		return userObject{}, err
	}
	read, err := m.rq.ListEdgeUserGrants(ctx, dbq.ListEdgeUserGrantsParams{UserID: id, Perm: string(perm.RepoDownload)})
	if err != nil {
		return userObject{}, err
	}
	slices.Sort(read)
	if read == nil {
		read = []int64{}
	}
	return userObject{Enabled: !disabled, Local: kind == auth.KindLocal,
		All: perm.Current().AllRepos(perm.SetOfStrings(perms)), Read: slices.Compact(read)}, nil
}

func (m *mod) tokenObject(ctx context.Context, publicID string) (any, bool, error) {
	t, err := m.rq.GetEdgeToken(ctx, publicID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	o, ok := tokenObjectOf(t.ID, t.UserID, t.SecretHash, t.ExpiresAt)
	return o, ok, nil
}

// tokenObjectOf is the object of a read token; none for an expired one.
func tokenObjectOf(id, user int64, hash []byte, exp sql.NullInt64) (tokenObject, bool) {
	o := tokenObject{ID: id, User: user, Hash: hex.EncodeToString(hash)}
	if exp.Valid {
		if exp.Int64 <= time.Now().Unix() {
			return o, false
		}
		o.Exp = exp.Int64
	}
	return o, true
}

func (m *mod) deviceObject(ctx context.Context, id int64) (any, bool, error) {
	d, err := m.rq.GetEdgeDevice(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	o, ok := deviceObjectOf(d.UserID, d.ExpiresAt, d.RevokedAt)
	return o, ok, nil
}

// deviceObjectOf is the object of a device; none for a revoked or expired one.
func deviceObjectOf(user, exp int64, revoked sql.NullInt64) (deviceObject, bool) {
	if revoked.Valid || exp <= time.Now().Unix() {
		return deviceObject{}, false
	}
	return deviceObject{User: user, Exp: exp}, true
}

// --- change notices ---

func (m *mod) authChanged(ctx context.Context, c auth.Change) {
	keys := make([]string, 0, len(c.Users)+len(c.Tokens))
	for _, id := range c.Users {
		keys = append(keys, userPrefix+strconv.FormatInt(id, 10))
	}
	for _, t := range c.Tokens {
		keys = append(keys, tokenPrefix+t)
	}
	m.mark(ctx, keys...)
}

// reposChanged marks the objects of the changed repos and of the groups they are in (a group is
// private when a member is). A deleted repo also marks every user: its grants went with the row,
// and its id can come back for a new repo.
func (m *mod) reposChanged(ctx context.Context, n repos.Notice) {
	var keys []string
	add := func(id int64) {
		if g, err := m.h.Repos.ByID(ctx, id); err == nil {
			keys = append(keys, repoPrefix+g.Name)
		}
	}
	for _, r := range n.Changed {
		keys = append(keys, repoPrefix+r.Name)
		for _, g := range m.h.Repos.GroupsOf(r.ID) {
			add(g)
		}
	}
	if n.Deleted != nil {
		keys = append(keys, repoPrefix+n.Deleted.Name)
		for _, g := range n.Groups {
			add(g)
		}
		users, err := m.writtenKeys(ctx, userPrefix)
		if err != nil {
			m.h.Log.Error("edge mark failed", "error", err.Error())
		}
		keys = append(keys, users...)
	}
	m.mark(ctx, keys...)
}

// rulesChanged marks the repo whose IP rules changed and its groups.
func (m *mod) rulesChanged(ctx context.Context, repoID int64) {
	r, err := m.h.Repos.ByID(ctx, repoID)
	if err != nil {
		return
	}
	m.reposChanged(ctx, repos.Notice{Changed: []*repos.Repo{r}})
}

func (m *mod) deviceChanged(ctx context.Context, deviceID int64) {
	m.mark(ctx, devicePrefix+strconv.FormatInt(deviceID, 10))
}

// writtenKeys returns the keys of edge_written under prefix.
func (m *mod) writtenKeys(ctx context.Context, prefix string) ([]string, error) {
	var out []string
	err := m.eachWritten(ctx, prefix, func(key, _ string) error {
		out = append(out, key)
		return nil
	})
	return out, err
}

// eachWritten calls fn for each row of edge_written under prefix, in key order.
func (m *mod) eachWritten(ctx context.Context, prefix string, fn func(key, hash string) error) error {
	after, upper := prefix, prefixEnd(prefix)
	if prefix == "" {
		upper = "\U0010FFFF"
	}
	for {
		rows, err := m.rq.ListWritten(ctx, dbq.ListWrittenParams{After: after, Upper: upper, Limit: 1000})
		if err != nil {
			return err
		}
		for _, r := range rows {
			if err := fn(r.Key, r.Hash); err != nil {
				return err
			}
		}
		if len(rows) < 1000 {
			return nil
		}
		after = rows[len(rows)-1].Key
	}
}

// prefixEnd is the least string above every string that starts with prefix ("" for none).
func prefixEnd(prefix string) string {
	b := []byte(prefix)
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] < 0xff {
			b[i]++
			return string(b[:i+1])
		}
	}
	return ""
}

// --- reconcile ---

// reconcile is the job edge.reconcile: every 5 minutes it computes every access object and
// config.json from the database and queues in the outbox each key whose hash differs from
// edge_written, is missing there, or whose row is gone. It writes nothing itself: the outbox
// computes each body again under its normal path, so a reconcile never puts a body older than
// a write of the outbox. It covers a change whose notice was lost.
func (m *mod) reconcile(ctx context.Context) error {
	if m.cur.Load().mode != modeOn {
		return nil
	}
	want, err := m.allObjects(ctx)
	if err != nil {
		return err
	}
	var dirty []string
	err = m.eachWritten(ctx, "", func(key, hash string) error {
		if h, ok := want[key]; !ok || h != hash {
			dirty = append(dirty, key)
		}
		delete(want, key)
		return nil
	})
	if err != nil {
		return err
	}
	for key := range want {
		dirty = append(dirty, key) // never written
	}
	for len(dirty) > 0 {
		n := min(len(dirty), 500)
		m.mark(ctx, dirty[:n]...)
		dirty = dirty[n:]
	}
	return nil
}

// allObjects computes the hash of every access object and of config.json, by key.
func (m *mod) allObjects(ctx context.Context) (map[string]string, error) {
	out := map[string]string{configKey: hashOf(m.config(true))}
	add := func(key string, obj any) error {
		b, err := json.Marshal(obj)
		out[key] = hashOf(b)
		return err
	}
	all, err := m.h.Repos.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range all {
		o, ok, err := m.repoObjectOf(ctx, r)
		if err != nil {
			return nil, err
		}
		if ok {
			if err := add(repoPrefix+r.Name, o); err != nil {
				return nil, err
			}
		}
	}
	for after := int64(0); ; {
		rows, err := m.rq.ListEdgeUsers(ctx, dbq.ListEdgeUsersParams{ID: after, Limit: 500})
		if err != nil {
			return nil, err
		}
		for _, u := range rows {
			o, err := m.userObjectOf(ctx, u.ID, u.Disabled, u.Kind)
			if err != nil {
				return nil, err
			}
			if err := add(userPrefix+strconv.FormatInt(u.ID, 10), o); err != nil {
				return nil, err
			}
			after = u.ID
		}
		if len(rows) < 500 {
			break
		}
	}
	for after := int64(0); ; {
		rows, err := m.rq.ListEdgeTokens(ctx, dbq.ListEdgeTokensParams{ID: after, Limit: 500})
		if err != nil {
			return nil, err
		}
		for _, t := range rows {
			if o, ok := tokenObjectOf(t.ID, t.UserID, t.SecretHash, t.ExpiresAt); ok {
				if err := add(tokenPrefix+t.PublicID, o); err != nil {
					return nil, err
				}
			}
			after = t.ID
		}
		if len(rows) < 500 {
			break
		}
	}
	for after := int64(0); ; {
		rows, err := m.rq.ListEdgeDevices(ctx, dbq.ListEdgeDevicesParams{ID: after, Limit: 500})
		if err != nil {
			return nil, err
		}
		for _, d := range rows {
			if o, ok := deviceObjectOf(d.UserID, d.ExpiresAt, d.RevokedAt); ok {
				if err := add(devicePrefix+strconv.FormatInt(d.ID, 10), o); err != nil {
					return nil, err
				}
			}
			after = d.ID
		}
		if len(rows) < 500 {
			break
		}
	}
	return out, nil
}
