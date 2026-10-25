package repos

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
)

// Repo kinds (repos.kind). A hosted repo stores what is published to it. A proxy repo fetches
// files from its remote on the first request and keeps them. A group repo serves the files of
// its members and has no files of its own.
const (
	KindHosted = "hosted"
	KindProxy  = "proxy"
	KindGroup  = "group"
)

// MaxMembers is the most members a group holds.
const MaxMembers = 50

// Sealer encrypts the proxy credentials with the master key (settings.Service).
type Sealer interface {
	Encrypt(aad string, plaintext []byte) []byte
	Decrypt(aad string, sealed []byte) ([]byte, error)
}

// SetSealer gives the service the key that seals proxy credentials. Without one, a proxy with
// credentials cannot be created or loaded.
func (s *Service) SetSealer(seal Sealer) { s.seal = seal }

func proxyAAD(id int64) string { return "repo_proxy:" + strconv.FormatInt(id, 10) }

// Proxy is the remote of a proxy repo. Username and Password are "" when the remote needs no
// credentials. The value is immutable: a change replaces it.
type Proxy struct {
	RemoteURL   string // http(s), ends with "/"
	Username    string
	Password    string
	MetadataTTL time.Duration // a changing file is used this long before a check
	NotFoundTTL time.Duration // a remote 404 is remembered this long
	LastOK      time.Time     // zero: never
	LastError   string
	LastErrorAt time.Time
}

// HasCredentials reports whether the remote gets a username and password.
func (p *Proxy) HasCredentials() bool { return p.Username != "" }

// Defaults and limits of the proxy times.
const (
	DefaultMetadataTTL = 30 * time.Minute
	DefaultNotFoundTTL = 5 * time.Minute
	maxProxyTTL        = 30 * 24 * time.Hour
)

// ProxyInput is a new proxy remote or a change of one; a nil field keeps the current value.
// Username and Password set together replace the credentials; Username "" removes them;
// Password alone replaces the password and keeps the username.
type ProxyInput struct {
	RemoteURL   *string `json:"remote_url"`
	Username    *string `json:"username"`
	Password    *string `json:"password"`
	MetadataTTL *int64  `json:"metadata_ttl"`  // seconds
	NotFoundTTL *int64  `json:"not_found_ttl"` // seconds
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// normalizeRemote checks a remote URL: http or https, a host, no user, query or fragment. It
// returns it with a trailing "/".
func normalizeRemote(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", invalid("the remote URL must be an http or https address, e.g. https://repo.maven.apache.org/maven2/")
	}
	if u.User != nil {
		return "", invalid("put the remote's username and password in their own fields, not in the URL")
	}
	if u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return "", invalid("the remote URL must not have a query or a fragment")
	}
	if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
		if u.RawPath != "" {
			u.RawPath += "/"
		}
	}
	return u.String(), nil
}

func checkTTL(name string, sec int64) (time.Duration, error) {
	d := time.Duration(sec) * time.Second
	if sec < 0 || d > maxProxyTTL {
		return 0, invalid("%s must be between 0 and %d seconds", name, int64(maxProxyTTL/time.Second))
	}
	return d, nil
}

func checkCredential(name, v string, max int) error {
	if len(v) > max {
		return invalid("the %s has at most %d characters", name, max)
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return invalid("the %s must not contain control characters", name)
		}
	}
	return nil
}

// Apply returns base (nil: a new remote with the defaults) with the fields of in, checked. A new
// remote needs RemoteURL.
func (in ProxyInput) Apply(base *Proxy) (*Proxy, error) {
	p := &Proxy{MetadataTTL: DefaultMetadataTTL, NotFoundTTL: DefaultNotFoundTTL}
	if base != nil {
		*p = *base
	}
	if in.RemoteURL != nil {
		u, err := normalizeRemote(*in.RemoteURL)
		if err != nil {
			return nil, err
		}
		p.RemoteURL = u
	}
	if p.RemoteURL == "" {
		return nil, invalid("a proxy needs the URL of its remote repository")
	}
	var err error
	if in.MetadataTTL != nil {
		if p.MetadataTTL, err = checkTTL("metadata_ttl", *in.MetadataTTL); err != nil {
			return nil, err
		}
	}
	if in.NotFoundTTL != nil {
		if p.NotFoundTTL, err = checkTTL("not_found_ttl", *in.NotFoundTTL); err != nil {
			return nil, err
		}
	}
	switch {
	case in.Username != nil && strings.TrimSpace(*in.Username) == "":
		p.Username, p.Password = "", ""
	case in.Username != nil:
		p.Username = strings.TrimSpace(*in.Username)
		if strings.Contains(p.Username, ":") {
			return nil, invalid("the username must not contain a colon")
		}
		if in.Password == nil && (base == nil || base.Username == "") {
			return nil, invalid("give the password of the remote's username")
		}
	}
	if in.Password != nil && p.Username == "" && *in.Password != "" {
		return nil, invalid("give the username of the remote's password")
	}
	if in.Password != nil && p.Username != "" {
		p.Password = *in.Password
	}
	if err := checkCredential("username", p.Username, 256); err != nil {
		return nil, err
	}
	if err := checkCredential("password", p.Password, 1024); err != nil {
		return nil, err
	}
	return p, nil
}

// sealCredentials returns the sealed JSON {username, password} of p for repo id, or nil when p
// has none.
func (s *Service) sealCredentials(id int64, p *Proxy) ([]byte, error) {
	if !p.HasCredentials() {
		return []byte{}, nil
	}
	if s.seal == nil {
		return nil, errors.New("repos: no sealer for proxy credentials")
	}
	plain, err := json.Marshal(struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}{p.Username, p.Password})
	if err != nil {
		return nil, err
	}
	return s.seal.Encrypt(proxyAAD(id), plain), nil
}

func (s *Service) proxyFromRow(r dbq.RepoProxy) (*Proxy, error) {
	p := &Proxy{
		RemoteURL:   r.RemoteUrl,
		MetadataTTL: time.Duration(r.MetadataTtl) * time.Second,
		NotFoundTTL: time.Duration(r.NotFoundTtl) * time.Second,
		LastError:   r.LastError,
	}
	if r.LastOkAt > 0 {
		p.LastOK = time.Unix(r.LastOkAt, 0)
	}
	if r.LastErrorAt > 0 {
		p.LastErrorAt = time.Unix(r.LastErrorAt, 0)
	}
	if len(r.Credentials) > 0 {
		if s.seal == nil {
			return nil, errors.New("no sealer for proxy credentials")
		}
		plain, err := s.seal.Decrypt(proxyAAD(r.RepoID), r.Credentials)
		if err != nil {
			return nil, err
		}
		var c struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := json.Unmarshal(plain, &c); err != nil {
			return nil, err
		}
		p.Username, p.Password = c.Username, c.Password
	}
	return p, nil
}

// proxyPage is the page size Load reads the remotes with.
const proxyPage = 500

// Load reads the group members and the proxy remotes into memory. A remote whose credentials do
// not decrypt (a changed master key) loads without them; undecryptable lists those repo ids.
func (s *Service) Load(ctx context.Context) (undecryptable []int64, err error) {
	q := dbq.New(s.db.R)
	groups := map[int64][]int64{}
	var after int64
	for {
		id, err := q.NextGroupWithMembers(ctx, after)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("load group members: %w", err)
		}
		members, err := q.ListGroupMembers(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("load group members: %w", err)
		}
		groups[id] = members
		after = id
	}
	proxies := map[int64]*Proxy{}
	after = 0
	for {
		rows, err := q.ListProxies(ctx, dbq.ListProxiesParams{After: after, Limit: proxyPage})
		if err != nil {
			return nil, fmt.Errorf("load proxies: %w", err)
		}
		for _, r := range rows {
			p, err := s.proxyFromRow(r)
			if err != nil {
				undecryptable = append(undecryptable, r.RepoID)
				r.Credentials = nil
				p, _ = s.proxyFromRow(r)
			}
			proxies[r.RepoID] = p
		}
		if len(rows) < proxyPage {
			break
		}
		after = rows[len(rows)-1].RepoID
	}
	s.mu.Lock()
	s.groups, s.proxies = groups, proxies
	s.mu.Unlock()
	return undecryptable, nil
}

// forget drops the in-memory rows of a deleted repo: its remote, its members, and its place in
// the groups it was in (the database rows cascaded).
func (s *Service) forget(ctx context.Context, id int64) error {
	s.mu.Lock()
	delete(s.proxies, id)
	delete(s.groups, id)
	var touched []int64
	for g, ms := range s.groups {
		if slices.Contains(ms, id) {
			touched = append(touched, g)
		}
	}
	s.mu.Unlock()
	for _, g := range touched {
		if err := s.reloadGroup(ctx, g); err != nil {
			return err
		}
	}
	return nil
}

// reloadGroup reads the members of group id into memory.
func (s *Service) reloadGroup(ctx context.Context, id int64) error {
	members, err := dbq.New(s.db.W).ListGroupMembers(ctx, id)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if len(members) == 0 {
		delete(s.groups, id)
	} else {
		s.groups[id] = members
	}
	s.mu.Unlock()
	return nil
}

// counted sets the field values of a repo and the MemberCount of a group repo from memory.
func (s *Service) counted(r *Repo, err error) (*Repo, error) {
	if err == nil {
		s.loadFields(r)
	}
	if err == nil && r.Kind == KindGroup {
		s.mu.RLock()
		r.MemberCount = len(s.groups[r.ID])
		s.mu.RUnlock()
	}
	return r, err
}

// Members returns the member ids of group id in read order (in memory).
func (s *Service) Members(id int64) []int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.groups[id])
}

// GroupsOf returns the ids of the groups that repo id is a member of (in memory), in id order.
func (s *Service) GroupsOf(id int64) []int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []int64
	for g, ms := range s.groups {
		if slices.Contains(ms, id) {
			out = append(out, g)
		}
	}
	slices.Sort(out)
	return out
}

// memoKey is the context key of a request's memo (Remember).
type memoKey struct{}

// memo holds the member repos of the groups that one request read.
type memo struct {
	mu      sync.Mutex
	members map[int64][]*Repo
}

// Remember returns ctx with a memo for one request: MemberRepos reads the members of a group once
// per request, although the route policy and the read path both ask for them. The repos stay as
// they were when the request first read them.
func Remember(ctx context.Context) context.Context {
	if _, ok := ctx.Value(memoKey{}).(*memo); ok {
		return ctx
	}
	return context.WithValue(ctx, memoKey{}, &memo{members: map[int64][]*Repo{}})
}

// MemberRepos returns the member repos of group in read order: the members from memory, their
// repos in one query of their ids, or the memo of the request (Remember).
func (s *Service) MemberRepos(ctx context.Context, group *Repo) ([]*Repo, error) {
	m, _ := ctx.Value(memoKey{}).(*memo)
	if m != nil {
		m.mu.Lock()
		out, ok := m.members[group.ID]
		m.mu.Unlock()
		if ok {
			return slices.Clone(out), nil
		}
	}
	ids := s.Members(group.ID)
	found, err := s.rows(dbq.New(s.db.R).ListReposByIDs(ctx, ids))
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]*Repo, len(found))
	for _, r := range found {
		byID[r.ID] = r
	}
	out := make([]*Repo, 0, len(ids))
	for _, id := range ids {
		if r, ok := byID[id]; ok { // a member deleted after the copy was read is gone
			out = append(out, r)
		}
	}
	if m != nil {
		m.mu.Lock()
		m.members[group.ID] = slices.Clone(out)
		m.mu.Unlock()
	}
	return out, nil
}

// resolveMembers checks the member names of a group of format in q: 1 to MaxMembers names,
// each once, each a hosted or proxy repo of format.
func resolveMembers(ctx context.Context, q *dbq.Queries, format string, names []string) ([]int64, error) {
	if len(names) == 0 || len(names) > MaxMembers {
		return nil, invalid("a group holds 1 to %d repositories", MaxMembers)
	}
	ids := make([]int64, 0, len(names))
	for _, n := range names {
		r, err := byName(ctx, q, n)
		if errors.Is(err, ErrNotFound) {
			return nil, invalid("there is no repository %q", clip(n))
		}
		if err != nil {
			return nil, err
		}
		switch {
		case r.Kind == KindGroup:
			return nil, invalid("%s is a group; a group cannot hold a group", r.Name)
		case r.Format != format:
			return nil, invalid("%s is a %s repository; this group takes %s repositories", r.Name, r.Format, format)
		case slices.Contains(ids, r.ID):
			return nil, invalid("%s is in the list twice", r.Name)
		}
		ids = append(ids, r.ID)
	}
	return ids, nil
}

func clip(s string) string {
	if len(s) > 64 {
		return s[:64]
	}
	return s
}

func writeMembers(ctx context.Context, q *dbq.Queries, group int64, ids []int64) error {
	if err := q.DeleteGroupMembers(ctx, group); err != nil {
		return err
	}
	for i, id := range ids {
		if err := q.InsertGroupMember(ctx, dbq.InsertGroupMemberParams{GroupID: group, MemberID: id, Position: int64(i)}); err != nil {
			return err
		}
	}
	return nil
}

// Proxy returns the remote of proxy repo id (in memory).
func (s *Service) Proxy(id int64) (*Proxy, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.proxies[id]
	return p, ok
}

// insertProxy writes the remote p of new proxy repo id.
func (s *Service) insertProxy(ctx context.Context, q *dbq.Queries, id int64, p *Proxy) error {
	creds, err := s.sealCredentials(id, p)
	if err != nil {
		return err
	}
	return q.InsertProxy(ctx, dbq.InsertProxyParams{RepoID: id, RemoteUrl: p.RemoteURL, Credentials: creds,
		MetadataTtl: int64(p.MetadataTTL / time.Second), NotFoundTtl: int64(p.NotFoundTTL / time.Second)})
}

// ProxyChanges names the fields of a proxy remote that changed, for the audit log (never a
// credential value).
func ProxyChanges(old, new *Proxy) []string {
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

// okWriteEvery is how often a stream of successful remote requests writes last_ok_at.
const okWriteEvery = time.Minute

// ProxyOK records a successful remote request of proxy repo id.
func (s *Service) ProxyOK(ctx context.Context, id int64) {
	now := time.Now()
	s.mu.Lock()
	cur, ok := s.proxies[id]
	if !ok {
		s.mu.Unlock()
		return
	}
	write := now.Sub(cur.LastOK) >= okWriteEvery
	p := *cur
	p.LastOK = now
	s.proxies[id] = &p
	s.mu.Unlock()
	if write {
		_ = dbq.New(s.db.W).SetProxyOK(ctx, dbq.SetProxyOKParams{LastOkAt: now.Unix(), RepoID: id})
	}
}

// ProxyFailed records a failed remote request of proxy repo id with its reason.
func (s *Service) ProxyFailed(ctx context.Context, id int64, reason string) {
	now := time.Now()
	s.mu.Lock()
	cur, ok := s.proxies[id]
	if !ok {
		s.mu.Unlock()
		return
	}
	p := *cur
	p.LastError, p.LastErrorAt = reason, now
	s.proxies[id] = &p
	s.mu.Unlock()
	_ = dbq.New(s.db.W).SetProxyError(ctx, dbq.SetProxyErrorParams{LastError: reason, LastErrorAt: now.Unix(), RepoID: id})
}
