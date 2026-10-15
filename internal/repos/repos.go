// Package repos manages repository records (spec §5.3). The format of a repo and its config
// are checked by a Validator that the server builds from the format registry.
package repos

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
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
	// LockedBy is set while another system owns the repo's content, e.g. a Nexus source that
	// the repo mirrors. The write gate (auth.RepoGate) answers uploads with 409 and this text.
	// In-process writers such as importers do not pass the gate. "" means not locked.
	LockedBy string
	// FileCount and TotalSize count every asset row of the repo (artifacts, metadata, checksums
	// and index files). LastPublishedAt is the newest write of a non-index asset; zero for
	// never. Triggers on the assets table keep them (internal/db/migrations).
	FileCount       int64
	TotalSize       int64
	LastPublishedAt time.Time
	// MemberCount is the number of members of a group (from memory); 0 for another kind.
	MemberCount int
}

// Validator validates the config of a repo of the given format and returns its normalized
// form; an empty config selects the format's default. It rejects unknown formats.
type Validator func(format string, config json.RawMessage) (json.RawMessage, error)

// KindCheck returns an error when format does not take repos of kind (the format registry
// knows which formats serve proxies and groups).
type KindCheck func(format, kind string) error

type Service struct {
	db       *db.DB
	validate Validator
	kinds    KindCheck
	seal     Sealer

	// The in-memory copy of the group members and the proxy remotes (see kinds.go). Request
	// paths read it; every change writes the database first and then this copy.
	mu      sync.RWMutex
	groups  map[int64][]int64 // group id -> member ids in read order
	proxies map[int64]*Proxy  // repo id -> remote
}

// New returns the repo service. validate checks formats and configs in Create and Update, and
// kinds the kind of a new proxy or group; with a nil validate (offline commands that only read
// repos) both reject every format. Load reads the members and remotes into memory.
func New(d *db.DB, validate Validator, kinds KindCheck) *Service {
	return &Service{db: d, validate: validate, kinds: kinds, groups: map[int64][]int64{}, proxies: map[int64]*Proxy{}}
}

func fromRow(r dbq.Repo, err error) (*Repo, error) {
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
		LockedBy:      r.LockedBy.String,
		Description:   r.Description,
		FileCount:     r.FileCount,
		TotalSize:     r.TotalSize,
	}
	if r.LastPublishedAt > 0 {
		out.LastPublishedAt = time.Unix(r.LastPublishedAt, 0)
	}
	return out, nil
}

// ByName is an indexed point lookup (used on every request).
func (s *Service) ByName(ctx context.Context, name string) (*Repo, error) {
	return s.counted(fromRow(dbq.New(s.db.R).GetRepoByName(ctx, name)))
}

func (s *Service) ByID(ctx context.Context, id int64) (*Repo, error) {
	return s.counted(fromRow(dbq.New(s.db.R).GetRepoByID(ctx, id)))
}

// listPage is the page size List reads with.
const listPage = 500

// List returns all repos ordered by name (admin/UI only; repo count is small). It reads
// keyset pages of listPage repos.
func (s *Service) List(ctx context.Context) ([]*Repo, error) {
	q := dbq.New(s.db.R)
	var out []*Repo
	after := ""
	for {
		rows, err := q.ListRepos(ctx, dbq.ListReposParams{After: after, Limit: listPage})
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			r, _ := s.counted(fromRow(row, nil))
			out = append(out, r)
		}
		if len(rows) < listPage {
			return out, nil
		}
		after = rows[len(rows)-1].Name
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
	for _, row := range rows {
		r, _ := s.counted(fromRow(row, nil))
		out = append(out, r)
	}
	next := ""
	if limit > 0 && len(rows) == limit {
		next = rows[len(rows)-1].Name
	}
	return out, next, nil
}

// normalizeConfig checks that cfg is empty or a JSON object, then runs the format's validation,
// which must return a JSON object.
func (s *Service) normalizeConfig(format string, cfg json.RawMessage) (json.RawMessage, error) {
	if t := bytes.TrimSpace(cfg); len(t) > 0 && string(t) != "null" && !isObject(t) {
		return nil, fmt.Errorf("%w: config must be a JSON object", ErrInvalid)
	}
	if s.validate == nil {
		return nil, fmt.Errorf("%w: unknown format %q", ErrInvalid, format)
	}
	out, err := s.validate(format, cfg)
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

// Create validates the name, the format, the description and the config (empty: the format's
// default) and inserts a hosted repo.
func (s *Service) Create(ctx context.Context, name, format string, anonymousRead bool, description string, config json.RawMessage) (*Repo, error) {
	var repo *Repo
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		repo, err = s.insert(ctx, tx, name, format, KindHosted, anonymousRead, description, config)
		return err
	})
	return repo, err
}

// insert validates and inserts a repo of kind in tx.
func (s *Service) insert(ctx context.Context, tx *sql.Tx, name, format, kind string, anonymousRead bool, description string, config json.RawMessage) (*Repo, error) {
	if !NameRE.MatchString(name) {
		return nil, fmt.Errorf("%w: bad name", ErrInvalid)
	}
	description, err := checkDescription(description)
	if err != nil {
		return nil, err
	}
	cfg, err := s.normalizeConfig(format, config)
	if err != nil {
		return nil, err
	}
	if kind != KindHosted {
		if s.kinds == nil {
			return nil, fmt.Errorf("%w: format %q takes no %s repositories", ErrInvalid, format, kind)
		}
		if err := s.kinds(format, kind); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
	}
	now := time.Now().Unix()
	id, err := dbq.New(tx).CreateRepo(ctx, dbq.CreateRepoParams{
		Name: name, Format: format, Kind: kind, AnonymousRead: anonymousRead, Config: string(cfg), Description: description, CreatedAt: now})
	if isUnique(err) {
		return nil, ErrExists
	}
	if err != nil {
		return nil, err
	}
	return &Repo{ID: id, Name: name, Format: format, Kind: kind, AnonymousRead: anonymousRead, Config: cfg, CreatedAt: time.Unix(now, 0), Description: description}, nil
}

// MissingError names an item of a list that does not exist: a repo, or a user (Kind). It
// matches ErrNotFound.
type MissingError struct {
	Kind string // "repository" or "user"
	Name string
}

func (e *MissingError) Error() string { return e.Kind + " " + e.Name + " does not exist" }

func (e *MissingError) Is(target error) bool { return target == ErrNotFound }

// SetAnonymousRead sets anonymous read on every repo of names in one transaction. It reads every
// name first: an unknown name returns a *MissingError and changes nothing. It returns the repos
// whose value changed, after the change.
func (s *Service) SetAnonymousRead(ctx context.Context, names []string, on bool) ([]*Repo, error) {
	var changed []*Repo
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		changed = changed[:0]
		q := dbq.New(tx)
		var all []*Repo
		for _, name := range names {
			r, err := fromRow(q.GetRepoByName(ctx, name))
			if errors.Is(err, ErrNotFound) {
				return &MissingError{Kind: "repository", Name: name}
			}
			if err != nil {
				return err
			}
			all = append(all, r)
		}
		for _, r := range all {
			if r.AnonymousRead == on {
				continue
			}
			if err := q.SetRepoAnonymousRead(ctx, dbq.SetRepoAnonymousReadParams{AnonymousRead: on, ID: r.ID}); err != nil {
				return err
			}
			r.AnonymousRead = on
			changed = append(changed, r)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return changed, nil
}

// Update changes only the non-nil fields and returns the repo before and after the change.
func (s *Service) Update(ctx context.Context, name string, description *string, config json.RawMessage) (old, updated *Repo, err error) {
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		r, err := s.counted(fromRow(q.GetRepoByName(ctx, name)))
		if err != nil {
			return err
		}
		prev := *r
		if description != nil {
			if r.Description, err = checkDescription(*description); err != nil {
				return err
			}
		}
		if config != nil {
			if r.Config, err = s.normalizeConfig(r.Format, config); err != nil {
				return err
			}
		}
		if err := q.UpdateRepo(ctx, dbq.UpdateRepoParams{
			Config: string(r.Config), Description: r.Description, ID: r.ID}); err != nil {
			return err
		}
		old, updated = &prev, r
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return old, updated, nil
}

// SetLockedBy sets the LockedBy text of repo id; "" clears it. With a non-nil tx it writes in
// that transaction.
func (s *Service) SetLockedBy(ctx context.Context, tx *sql.Tx, id int64, by string) error {
	q := dbq.New(s.db.W)
	if tx != nil {
		q = dbq.New(tx)
	}
	return q.SetRepoLockedBy(ctx, dbq.SetRepoLockedByParams{LockedBy: sql.NullString{String: by, Valid: by != ""}, ID: id})
}

// DeleteRow deletes the repo row of id; the rows that reference it cascade, so a deleted member
// leaves its groups. ErrNotFound when it does not exist.
func (s *Service) DeleteRow(ctx context.Context, id int64) error {
	n, err := dbq.New(s.db.W).DeleteRepo(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return s.forget(ctx, id)
}
