// Package repos manages repository records (spec §5.3).
package repos

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"telesphorepo/internal/db"
)

var (
	ErrNotFound = errors.New("repo not found")
	ErrExists   = errors.New("repo already exists")
	ErrInvalid  = errors.New("invalid repo")
)

// NameRE validates repo, distribution, component and user names.
var NameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

const (
	FormatMaven  = "maven"
	FormatDebian = "debian"
)

type Repo struct {
	ID            int64
	Name          string
	Format        string
	AnonymousRead bool
	Config        json.RawMessage // always a JSON object
	CreatedAt     time.Time
}

// DebianConfig is the repos.config shape for debian repos.
type DebianConfig struct {
	Architectures []string `json:"architectures"` // default ["amd64","arm64"]
}

// Debian returns the parsed debian config with defaults applied.
func (r *Repo) Debian() DebianConfig { panic("TODO") }

type Service struct{ db *db.DB }

func New(d *db.DB) *Service { return &Service{db: d} }

// ByName is an indexed point lookup (used on every request).
func (s *Service) ByName(ctx context.Context, name string) (*Repo, error) { panic("TODO") }
func (s *Service) ByID(ctx context.Context, id int64) (*Repo, error)      { panic("TODO") }

// List returns all repos ordered by name (admin/UI only; repo count is small).
func (s *Service) List(ctx context.Context) ([]*Repo, error) { panic("TODO") }

// Create validates name/format/config (debian config: arch names ^[a-z0-9-]+$, non-empty) and inserts.
func (s *Service) Create(ctx context.Context, name, format string, anonymousRead bool, config json.RawMessage) (*Repo, error) {
	panic("TODO")
}

// Update changes only the non-nil fields.
func (s *Service) Update(ctx context.Context, name string, anonymousRead *bool, config json.RawMessage) (*Repo, error) {
	panic("TODO")
}
