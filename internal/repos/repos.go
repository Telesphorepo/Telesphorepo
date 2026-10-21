// Package repos manages repository records. The format of a repo and its config are checked by a
// Validator that the server builds from the format registry. Create and Update are the only ways
// a repo record changes: each validates the whole new state first, then saves it in one
// transaction with the format's change hook and one audit event.
package repos

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/ops"
)

var (
	ErrNotFound = errors.New("repo not found")
	ErrExists   = errors.New("repo already exists")
	ErrInvalid  = errors.New("invalid repo")
)

// NameRE validates repo, distribution, component and user names.
var NameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

type Repo struct {
	ID            int64
	Name          string
	Format        string
	Kind          string // KindHosted, KindProxy or KindGroup
	AnonymousRead bool
	Config        json.RawMessage // always a JSON object
	CreatedAt     time.Time
	Description   string // free text shown in the UI; "" when unset
	// Op is the active operation of the repo (a delete, a Nexus mirror), nil for none. While it
	// is set, the access engine answers writes, deletes and mints with 409.
	Op *ops.Op
	// FileCount and TotalSize count every asset row of the repo (artifacts, metadata, checksums
	// and index files). LastPublishedAt is the newest write of a non-index asset; zero for
	// never. Triggers on the assets table keep them (internal/db/migrations).
	FileCount       int64
	TotalSize       int64
	LastPublishedAt time.Time
	// MemberCount is the number of members of a group (from memory); 0 for another kind.
	MemberCount int
	// Fields are the values of the module fields that apply to the format (see Field), by name.
	Fields map[string]json.RawMessage
}

// Validator validates the name and the config of a repo of the given format and returns the
// normalized config; an empty config selects the format's default. It rejects unknown formats.
type Validator func(format, name string, config json.RawMessage) (json.RawMessage, error)

// KindCheck returns an error when format does not take repos of kind (the format registry
// knows which formats serve proxies and groups).
type KindCheck func(format, kind string) error

// ChangeHook runs inside the transaction of every repo update, after the rows changed: the
// format's OnRepoChange.
type ChangeHook func(ctx context.Context, tx *sql.Tx, old, new *Repo) error

type Service struct {
	db       *db.DB
	validate Validator
	kinds    KindCheck
	seal     Sealer
	audit    *audit.Log
	onChange ChangeHook

	// The in-memory copy of the group members and the proxy remotes (see kinds.go). Request
	// paths read it; every change writes the database first and then this copy.
	mu      sync.RWMutex
	groups  map[int64][]int64 // group id -> member ids in read order
	proxies map[int64]*Proxy  // repo id -> remote

	onDelete []func(id int64)                      // set before the server serves
	purges   []Purge                               // set before the server serves (OnPurge)
	after    []func(ctx context.Context, n Notice) // set before the server serves (AfterChange)
	fields   []Field                               // set before the server serves
}

// OnDelete registers fn to run after a repo row is deleted, with the repo's id: a module drops
// what it keeps in memory about the repo, because the id can be used again. It runs before the
// server serves.
func (s *Service) OnDelete(fn func(id int64)) { s.onDelete = append(s.onDelete, fn) }

// PurgeBatch is how many rows one call of a Purge deletes at most, in each of its statements.
const PurgeBatch = 1000

// Purge deletes one batch of a module's rows of repo id: at most PurgeBatch rows per statement,
// each statement its own short write on the writer. It returns how many rows it deleted; 0 when
// the module has no row of the repo left. Delete children before their parents, so a statement
// cascades to no more rows than its batch holds.
type Purge func(ctx context.Context, repoID int64) (int64, error)

// OnPurge registers fn to empty the module's tables of a repo before its row is deleted, so the
// final DELETE FROM repos cascades to almost nothing and never holds the writer for long. It runs
// before the server serves.
func (s *Service) OnPurge(fn Purge) { s.purges = append(s.purges, fn) }

// purge empties the rows of repo id that its row delete would cascade to, batch by batch: it
// nulls the repo of its audit events, then runs each module's Purge until it deletes no more.
func (s *Service) purge(ctx context.Context, id int64) error {
	q := dbq.New(s.db.W)
	for {
		n, err := q.ClearAuditRepo(ctx, dbq.ClearAuditRepoParams{RepoID: sql.NullInt64{Int64: id, Valid: true}, Limit: PurgeBatch})
		if err != nil {
			return err
		}
		if n == 0 {
			break
		}
	}
	for _, fn := range s.purges {
		for {
			n, err := fn(ctx, id)
			if err != nil {
				return err
			}
			if n == 0 {
				break
			}
		}
	}
	return nil
}

// Notice is one committed change of repos: the repos that were created or updated (after the
// change), or the repo whose row was deleted with the groups it was a member of.
type Notice struct {
	Changed []*Repo
	Deleted *Repo
	Groups  []int64 // the groups that Deleted left
}

// AfterChange registers fn to run after each committed Create, Update, UpdateAll and DeleteRow
// that changed a repo. fn runs on the request that made the change and must be quick (the edge
// module marks its access objects). It runs before the server serves.
func (s *Service) AfterChange(fn func(ctx context.Context, n Notice)) { s.after = append(s.after, fn) }

func (s *Service) notify(ctx context.Context, n Notice) {
	for _, fn := range s.after {
		fn(ctx, n)
	}
}

// SetAudit gives the service the audit log of repo.create and repo.update.
func (s *Service) SetAudit(a *audit.Log) { s.audit = a }

// SetChangeHook sets the hook that every update runs in its transaction (the format's
// OnRepoChange).
func (s *Service) SetChangeHook(fn ChangeHook) { s.onChange = fn }

// New returns the repo service. validate checks formats and configs in Create and Update, and
// kinds the kind of a new proxy or group; with a nil validate (offline commands that only read
// repos) both reject every format. Load reads the members and remotes into memory.
func New(d *db.DB, validate Validator, kinds KindCheck) *Service {
	return &Service{db: d, validate: validate, kinds: kinds, groups: map[int64][]int64{}, proxies: map[int64]*Proxy{}}
}

// row is a repo row with its operation; the three repo queries return the same columns.
type row = dbq.GetRepoByNameRow

func fromRow(r row, err error) (*Repo, error) {
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	out := &Repo{
		ID:            r.ID,
		Name:          r.Name,
		Format:        r.Format,
		Kind:          r.Kind,
		AnonymousRead: r.AnonymousRead,
		Config:        json.RawMessage(r.Config),
		CreatedAt:     time.Unix(r.CreatedAt, 0),
		Description:   r.Description,
		FileCount:     r.FileCount,
		TotalSize:     r.TotalSize,
		Op: ops.FromRow(r.OpID, r.ID, r.OpType, r.OpOwner, r.OpRef, r.OpState, r.OpDone, r.OpTotal, r.OpError,
			r.OpStartedBy, r.OpStartedAt, r.OpUpdatedAt),
	}
	if r.LastPublishedAt > 0 {
		out.LastPublishedAt = time.Unix(r.LastPublishedAt, 0)
	}
	return out, nil
}

func byName(ctx context.Context, q *dbq.Queries, name string) (*Repo, error) {
	return fromRow(q.GetRepoByName(ctx, name))
}

func byID(ctx context.Context, q *dbq.Queries, id int64) (*Repo, error) {
	r, err := q.GetRepoByID(ctx, id)
	return fromRow(row(r), err)
}

// ByName is an indexed point lookup (used on every request).
func (s *Service) ByName(ctx context.Context, name string) (*Repo, error) {
	return s.counted(byName(ctx, dbq.New(s.db.R), name))
}

func (s *Service) ByID(ctx context.Context, id int64) (*Repo, error) {
	return s.counted(byID(ctx, dbq.New(s.db.R), id))
}

// listPage is the page size List reads with.
const listPage = 500

// List returns all repos ordered by name (admin/UI only; repo count is small). It reads
// keyset pages of listPage repos.
func (s *Service) List(ctx context.Context) ([]*Repo, error) {
	var out []*Repo
	after := ""
	for {
		page, next, err := s.Page(ctx, after, listPage)
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if next == "" {
			return out, nil
		}
		after = next
	}
}

// Page returns a keyset page of repos by name: names after after ("" for the first page). next
// is the cursor of the next page, "" at the end.
func (s *Service) Page(ctx context.Context, after string, limit int) ([]*Repo, string, error) {
	rows, err := dbq.New(s.db.R).ListRepos(ctx, dbq.ListReposParams{After: after, Limit: int64(limit)})
	if err != nil {
		return nil, "", err
	}
	out := make([]*Repo, 0, len(rows))
	for _, r := range rows {
		repo, _ := s.counted(fromRow(row(r), nil))
		out = append(out, repo)
	}
	next := ""
	if limit > 0 && len(rows) == limit {
		next = rows[len(rows)-1].Name
	}
	return out, next, nil
}

// normalizeConfig checks that cfg is empty or a JSON object, then runs the format's validation,
// which must return a JSON object.
func (s *Service) normalizeConfig(format, name string, cfg json.RawMessage) (json.RawMessage, error) {
	if t := bytes.TrimSpace(cfg); len(t) > 0 && string(t) != "null" && !isObject(t) {
		return nil, fmt.Errorf("%w: config must be a JSON object", ErrInvalid)
	}
	if s.validate == nil {
		return nil, fmt.Errorf("%w: unknown format %q", ErrInvalid, format)
	}
	out, err := s.validate(format, name, cfg)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if !isObject(out) {
		return nil, fmt.Errorf("format %q returned a config that is not a JSON object", format)
	}
	return out, nil
}

func isObject(raw json.RawMessage) bool {
	var obj map[string]json.RawMessage
	return json.Unmarshal(raw, &obj) == nil && obj != nil
}

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// maxDescription is the longest repo description in characters.
const maxDescription = 500

// checkDescription trims d and checks it: at most maxDescription characters, valid UTF-8, no
// control characters other than newlines and tabs.
func checkDescription(d string) (string, error) {
	d = strings.TrimSpace(d)
	if !utf8.ValidString(d) || utf8.RuneCountInString(d) > maxDescription {
		return "", fmt.Errorf("%w: description must be at most %d characters", ErrInvalid, maxDescription)
	}
	for _, r := range d {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return "", fmt.Errorf("%w: description must not contain control characters", ErrInvalid)
		}
	}
	return d, nil
}

// Create is a new repo. Kind "" is a hosted repo. A proxy needs Proxy (see ProxyInput.Apply), a
// group its Members in read order.
type Create struct {
	Name          string
	Format        string
	Kind          string
	AnonymousRead bool
	Description   string
	Config        json.RawMessage // empty: the format's default
	Proxy         *Proxy
	Members       []string
	Fields        map[string]json.RawMessage // module field values; a field without one gets its default
}

// Create validates in and inserts the repo, its remote or its members, its field values and the
// repo.create event in one transaction.
func (s *Service) Create(ctx context.Context, in Create) (*Repo, error) {
	if in.Kind == "" {
		in.Kind = KindHosted
	}
	if !NameRE.MatchString(in.Name) {
		return nil, fmt.Errorf("%w: bad name", ErrInvalid)
	}
	description, err := checkDescription(in.Description)
	if err != nil {
		return nil, err
	}
	switch in.Kind {
	case KindHosted:
	case KindProxy, KindGroup:
		if s.kinds == nil {
			return nil, fmt.Errorf("%w: format %q takes no %s repositories", ErrInvalid, in.Format, in.Kind)
		}
		if err := s.kinds(in.Format, in.Kind); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
	default:
		return nil, invalid(`kind must be "hosted", "proxy" or "group"`)
	}
	if in.Kind == KindProxy && in.Proxy == nil {
		return nil, invalid("a proxy needs the URL of its remote repository")
	}
	cfg := in.Config
	if in.Kind == KindGroup {
		cfg = nil // a group keeps no files: the format's default config
	}
	if cfg, err = s.normalizeConfig(in.Format, in.Name, cfg); err != nil {
		return nil, err
	}
	planned := &Repo{Name: in.Name, Format: in.Format, Kind: in.Kind, AnonymousRead: in.AnonymousRead, Config: cfg, Description: description}
	fields, err := s.planFields(ctx, planned, in.Fields, true)
	if err != nil {
		return nil, err
	}
	var repo *Repo
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		now := time.Now().Unix()
		id, err := q.CreateRepo(ctx, dbq.CreateRepoParams{Name: in.Name, Format: in.Format, Kind: in.Kind,
			AnonymousRead: in.AnonymousRead, Config: string(cfg), Description: description, CreatedAt: now})
		if isUnique(err) {
			return ErrExists
		}
		if err != nil {
			return err
		}
		repo = &Repo{ID: id, Name: in.Name, Format: in.Format, Kind: in.Kind, AnonymousRead: in.AnonymousRead,
			Config: cfg, CreatedAt: time.Unix(now, 0), Description: description, Fields: planned.Fields}
		if err := saveFields(ctx, tx, repo, fields); err != nil {
			return err
		}
		detail := map[string]any{"format": in.Format, "anonymous_read": in.AnonymousRead, "kind": in.Kind}
		switch in.Kind {
		case KindGroup:
			ids, err := resolveMembers(ctx, q, in.Format, in.Members)
			if err != nil {
				return err
			}
			if err := writeMembers(ctx, q, id, ids); err != nil {
				return err
			}
			detail["members"] = in.Members
		case KindProxy:
			if err := s.insertProxy(ctx, q, id, in.Proxy); err != nil {
				return err
			}
			detail["remote_url"] = in.Proxy.RemoteURL
		}
		return s.record(ctx, tx, audit.RepoCreate, repo, detail)
	})
	if err != nil {
		return nil, err
	}
	committedFields(repo.ID, fields)
	switch in.Kind {
	case KindGroup:
		if err := s.reloadGroup(ctx, repo.ID); err != nil {
			return nil, err
		}
	case KindProxy:
		p := *in.Proxy
		p.LastOK, p.LastError, p.LastErrorAt = time.Time{}, "", time.Time{}
		s.mu.Lock()
		s.proxies[repo.ID] = &p
		s.mu.Unlock()
	}
	out, err := s.counted(repo, nil)
	s.notify(ctx, Notice{Changed: []*Repo{out}})
	return out, err
}

func (s *Service) record(ctx context.Context, tx *sql.Tx, action string, repo *Repo, detail map[string]any) error {
	if s.audit == nil {
		return nil
	}
	return s.audit.RecordTx(ctx, tx, audit.Event{Action: action, RepoID: repo.ID, Repo: repo.Name, Target: repo.Name, Detail: detail})
}

// MissingError names an item of a list that does not exist: a repo, or a user (Kind). It
// matches ErrNotFound.
type MissingError struct {
	Kind string // "repository" or "user"
	Name string
}

func (e *MissingError) Error() string { return e.Kind + " " + e.Name + " does not exist" }

func (e *MissingError) Is(target error) bool { return target == ErrNotFound }

// Patch is a change of a repo. A nil field (or a nil Config or Members) keeps the value.
type Patch struct {
	Description   *string
	AnonymousRead *bool
	Config        json.RawMessage
	Proxy         *ProxyInput                // a proxy's remote; the password is write-only
	Members       []string                   // a group's members in read order
	Fields        map[string]json.RawMessage // module field values to change
}

// changes is what one update does to one repo.
type changes struct {
	old, new *Repo
	proxy    *Proxy // the new remote; nil: unchanged
	members  []int64
	values   []fieldChange
	fields   []string
	detail   map[string]any
}

// plan validates p against repo cur and returns the whole new state, or an error. It reads in q.
func (s *Service) plan(ctx context.Context, q *dbq.Queries, cur *Repo, p Patch) (*changes, error) {
	next := *cur
	c := &changes{old: cur, new: &next, detail: map[string]any{}}
	var err error
	if p.Description != nil {
		if next.Description, err = checkDescription(*p.Description); err != nil {
			return nil, err
		}
		if next.Description != cur.Description {
			c.fields = append(c.fields, "description")
		}
	}
	if p.AnonymousRead != nil && *p.AnonymousRead != cur.AnonymousRead {
		next.AnonymousRead = *p.AnonymousRead
		c.fields = append(c.fields, "anonymous_read")
		c.detail["anonymous_read"] = next.AnonymousRead
	}
	if p.Config != nil {
		if next.Config, err = s.normalizeConfig(cur.Format, cur.Name, p.Config); err != nil {
			return nil, err
		}
		if string(next.Config) != string(cur.Config) {
			c.fields = append(c.fields, "config")
		}
	}
	if p.Proxy != nil {
		old, ok := s.Proxy(cur.ID)
		if cur.Kind != KindProxy || !ok {
			return nil, invalid("%s is not a proxy", cur.Name)
		}
		px, err := p.Proxy.Apply(old)
		if err != nil {
			return nil, err
		}
		if changed := ProxyChanges(old, px); len(changed) > 0 {
			c.proxy = px
			c.fields = append(c.fields, "proxy")
			c.detail["proxy"] = changed
		}
	}
	if p.Members != nil {
		if cur.Kind != KindGroup {
			return nil, invalid("%s is not a group", cur.Name)
		}
		ids, err := resolveMembers(ctx, q, cur.Format, p.Members)
		if err != nil {
			return nil, err
		}
		if !slices.Equal(ids, s.Members(cur.ID)) {
			c.members = ids
			c.fields = append(c.fields, "members")
			c.detail["members"] = p.Members
		}
	}
	if p.Fields != nil {
		if c.values, err = s.planFields(ctx, &next, p.Fields, false); err != nil {
			return nil, err
		}
		for _, v := range c.values {
			c.fields = append(c.fields, v.f.Name)
		}
	}
	return c, nil
}

// save writes c in tx: the repo row, the remote, the members, the change hook and the
// repo.update event.
func (s *Service) save(ctx context.Context, tx *sql.Tx, c *changes) error {
	q := dbq.New(tx)
	n := c.new
	if err := q.UpdateRepo(ctx, dbq.UpdateRepoParams{Config: string(n.Config), Description: n.Description,
		AnonymousRead: n.AnonymousRead, ID: n.ID}); err != nil {
		return err
	}
	if c.proxy != nil {
		creds, err := s.sealCredentials(n.ID, c.proxy)
		if err != nil {
			return err
		}
		if err := q.UpdateProxy(ctx, dbq.UpdateProxyParams{RemoteUrl: c.proxy.RemoteURL, Credentials: creds,
			MetadataTtl: int64(c.proxy.MetadataTTL / time.Second), NotFoundTtl: int64(c.proxy.NotFoundTTL / time.Second), RepoID: n.ID}); err != nil {
			return err
		}
	}
	if c.members != nil {
		if err := writeMembers(ctx, q, n.ID, c.members); err != nil {
			return err
		}
	}
	if err := saveFields(ctx, tx, n, c.values); err != nil {
		return err
	}
	if s.onChange != nil {
		if err := s.onChange(ctx, tx, c.old, c.new); err != nil {
			return err
		}
	}
	c.detail["fields"] = c.fields
	return s.record(ctx, tx, audit.RepoUpdate, n, c.detail)
}

// applied refreshes the in-memory copies after the commit of c.
func (s *Service) applied(ctx context.Context, c *changes) error {
	committedFields(c.new.ID, c.values)
	if c.proxy != nil {
		s.mu.Lock()
		if cur, ok := s.proxies[c.new.ID]; ok {
			c.proxy.LastOK, c.proxy.LastError, c.proxy.LastErrorAt = cur.LastOK, cur.LastError, cur.LastErrorAt
		}
		s.proxies[c.new.ID] = c.proxy
		s.mu.Unlock()
	}
	if c.members != nil {
		return s.reloadGroup(ctx, c.new.ID)
	}
	return nil
}

// Update applies p to repo name: it validates the whole new state first (the config with the
// format, the proxy remote, the members, the field values), then saves the row, the remote, the
// members, the field values, the format's OnRepoChange and one repo.update event in one
// transaction. A patch that changes
// nothing writes nothing. It returns the repo after the change.
func (s *Service) Update(ctx context.Context, name string, p Patch) (*Repo, error) {
	out, _, err := s.UpdateAll(ctx, []string{name}, p)
	if err != nil {
		return nil, err
	}
	return out[0], nil
}

// UpdateAll applies p to every repo of names in one transaction. It checks every repo first: an
// unknown name returns a *MissingError and changes nothing. It returns the repos after the
// change, in the order of names, and how many of them changed.
func (s *Service) UpdateAll(ctx context.Context, names []string, p Patch) ([]*Repo, int, error) {
	var all []*changes
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		all = all[:0]
		q := dbq.New(tx)
		for _, name := range names {
			cur, err := s.counted(byName(ctx, q, name))
			if errors.Is(err, ErrNotFound) {
				return &MissingError{Kind: "repository", Name: name}
			}
			if err != nil {
				return err
			}
			c, err := s.plan(ctx, q, cur, p)
			if err != nil {
				return err
			}
			all = append(all, c)
		}
		for _, c := range all {
			if len(c.fields) == 0 {
				continue
			}
			if err := s.save(ctx, tx, c); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	out := make([]*Repo, len(all))
	changed := 0
	var n Notice
	for i, c := range all {
		if len(c.fields) > 0 {
			changed++
			if err := s.applied(ctx, c); err != nil {
				return nil, 0, err
			}
		}
		out[i], _ = s.counted(c.new, nil)
		if len(c.fields) > 0 {
			n.Changed = append(n.Changed, out[i])
		}
	}
	if len(n.Changed) > 0 {
		s.notify(ctx, n)
	}
	return out, changed, nil
}

// DeleteRow deletes the repo row of id. It first empties the large tables that reference it in
// batches (see purge); the rest cascades, so a deleted member leaves its groups. ErrNotFound when
// it does not exist.
func (s *Service) DeleteRow(ctx context.Context, id int64) error {
	var gone *Repo
	var groups []int64
	r, err := byID(ctx, dbq.New(s.db.W), id)
	if err != nil {
		return err
	}
	if len(s.after) > 0 {
		gone, groups = r, s.GroupsOf(id)
	}
	if err := s.purge(ctx, id); err != nil {
		return err
	}
	n, err := dbq.New(s.db.W).DeleteRepo(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	if err := s.forget(ctx, id); err != nil {
		return err
	}
	for _, fn := range s.onDelete {
		fn(id)
	}
	if gone != nil {
		s.notify(ctx, Notice{Deleted: gone, Groups: groups})
	}
	return nil
}
