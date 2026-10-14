// Package repos manages repository records (spec §5.3).
package repos

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
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
func (r *Repo) Debian() DebianConfig {
	var c DebianConfig
	_ = json.Unmarshal(r.Config, &c)
	if len(c.Architectures) == 0 {
		c.Architectures = []string{"amd64", "arm64"}
	}
	return c
}

type Service struct{ db *db.DB }

func New(d *db.DB) *Service { return &Service{db: d} }

const cols = `id, name, format, anonymous_read, config, created_at`

type scanner interface{ Scan(...any) error }

func scan(s scanner) (*Repo, error) {
	var (
		r    Repo
		anon int
		cfg  string
		ts   int64
	)
	if err := s.Scan(&r.ID, &r.Name, &r.Format, &anon, &cfg, &ts); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	r.AnonymousRead = anon != 0
	r.Config = json.RawMessage(cfg)
	r.CreatedAt = time.Unix(ts, 0)
	return &r, nil
}

// ByName is an indexed point lookup (used on every request).
func (s *Service) ByName(ctx context.Context, name string) (*Repo, error) {
	return scan(s.db.R.QueryRowContext(ctx, `SELECT `+cols+` FROM repos WHERE name = ?`, name))
}

func (s *Service) ByID(ctx context.Context, id int64) (*Repo, error) {
	return scan(s.db.R.QueryRowContext(ctx, `SELECT `+cols+` FROM repos WHERE id = ?`, id))
}

// List returns all repos ordered by name (admin/UI only; repo count is small).
func (s *Service) List(ctx context.Context) ([]*Repo, error) {
	rows, err := s.db.R.QueryContext(ctx, `SELECT `+cols+` FROM repos ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Repo
	for rows.Next() {
		r, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

var archRE = regexp.MustCompile(`^[a-z0-9-]+$`)

// normalizeConfig checks cfg is a JSON object and, for debian, validates and re-encodes it.
func normalizeConfig(format string, cfg json.RawMessage) (json.RawMessage, error) {
	if len(strings.TrimSpace(string(cfg))) == 0 {
		cfg = json.RawMessage("{}")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(cfg, &obj); err != nil || obj == nil {
		return nil, fmt.Errorf("%w: config must be a JSON object", ErrInvalid)
	}
	if format != FormatDebian {
		return cfg, nil
	}
	if raw, ok := obj["architectures"]; ok {
		var archs []string
		if err := json.Unmarshal(raw, &archs); err != nil || len(archs) == 0 {
			return nil, fmt.Errorf("%w: architectures must be a non-empty list of strings", ErrInvalid)
		}
		for _, a := range archs {
			if !archRE.MatchString(a) || a == "all" {
				return nil, fmt.Errorf("%w: invalid architecture %q", ErrInvalid, a)
			}
		}
		obj["architectures"], _ = json.Marshal(archs)
	} else {
		obj["architectures"] = json.RawMessage(`["amd64","arm64"]`)
	}
	out, err := json.Marshal(obj)
	return out, err
}

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// Create validates name/format/config (debian config: arch names ^[a-z0-9-]+$, non-empty) and inserts.
func (s *Service) Create(ctx context.Context, name, format string, anonymousRead bool, config json.RawMessage) (*Repo, error) {
	if !NameRE.MatchString(name) {
		return nil, fmt.Errorf("%w: bad name", ErrInvalid)
	}
	if format != FormatMaven && format != FormatDebian {
		return nil, fmt.Errorf("%w: unknown format %q", ErrInvalid, format)
	}
	cfg, err := normalizeConfig(format, config)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	res, err := s.db.W.ExecContext(ctx,
		`INSERT INTO repos (name, format, anonymous_read, config, created_at) VALUES (?, ?, ?, ?, ?)`,
		name, format, anonymousRead, string(cfg), now)
	if isUnique(err) {
		return nil, ErrExists
	}
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return &Repo{ID: id, Name: name, Format: format, AnonymousRead: anonymousRead, Config: cfg, CreatedAt: time.Unix(now, 0)}, nil
}

// Update changes only the non-nil fields.
func (s *Service) Update(ctx context.Context, name string, anonymousRead *bool, config json.RawMessage) (*Repo, error) {
	var out *Repo
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		r, err := scan(tx.QueryRowContext(ctx, `SELECT `+cols+` FROM repos WHERE name = ?`, name))
		if err != nil {
			return err
		}
		if anonymousRead != nil {
			r.AnonymousRead = *anonymousRead
		}
		if config != nil {
			if r.Config, err = normalizeConfig(r.Format, config); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE repos SET anonymous_read = ?, config = ? WHERE id = ?`,
			r.AnonymousRead, string(r.Config), r.ID); err != nil {
			return err
		}
		out = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
