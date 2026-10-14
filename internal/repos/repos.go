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
}

// Validator validates the config of a repo of the given format and returns its normalized
// form; an empty config selects the format's default. It rejects unknown formats.
type Validator func(format string, config json.RawMessage) (json.RawMessage, error)

type Service struct {
	db       *db.DB
	validate Validator
}

// New returns the repo service. validate checks formats and configs in Create and Update; with
// a nil validate (offline commands that only read repos) both reject every format.
func New(d *db.DB, validate Validator) *Service { return &Service{db: d, validate: validate} }

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
	return fromRow(dbq.New(s.db.R).GetRepoByName(ctx, name))
}

func (s *Service) ByID(ctx context.Context, id int64) (*Repo, error) {
	return fromRow(dbq.New(s.db.R).GetRepoByID(ctx, id))
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
			r, _ := fromRow(row, nil)
			out = append(out, r)
		}
		if len(rows) < listPage {
			return out, nil
		}
		after = rows[len(rows)-1].Name
	}
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
// default) and inserts the repo.
func (s *Service) Create(ctx context.Context, name, format string, anonymousRead bool, description string, config json.RawMessage) (*Repo, error) {
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
	now := time.Now().Unix()
	id, err := dbq.New(s.db.W).CreateRepo(ctx, dbq.CreateRepoParams{
		Name: name, Format: format, AnonymousRead: anonymousRead, Config: string(cfg), Description: description, CreatedAt: now})
	if isUnique(err) {
		return nil, ErrExists
	}
	if err != nil {
		return nil, err
	}
	return &Repo{ID: id, Name: name, Format: format, AnonymousRead: anonymousRead, Config: cfg, CreatedAt: time.Unix(now, 0), Description: description}, nil
}

// Update changes only the non-nil fields and returns the repo before and after the change.
func (s *Service) Update(ctx context.Context, name string, anonymousRead *bool, description *string, config json.RawMessage) (old, updated *Repo, err error) {
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		r, err := fromRow(q.GetRepoByName(ctx, name))
		if err != nil {
			return err
		}
		prev := *r
		if anonymousRead != nil {
			r.AnonymousRead = *anonymousRead
		}
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
			AnonymousRead: r.AnonymousRead, Config: string(r.Config), Description: r.Description, ID: r.ID}); err != nil {
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

// DeleteRow deletes the repo row of id; the rows that reference it cascade. ErrNotFound when
// it does not exist.
func (s *Service) DeleteRow(ctx context.Context, id int64) error {
	n, err := dbq.New(s.db.W).DeleteRepo(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
