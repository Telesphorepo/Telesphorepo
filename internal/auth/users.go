package auth

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"telesphorepo/internal/repos"
)

const minPasswordLen = 8

const userCols = `id, username, is_admin, disabled, created_at`

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	u := &User{}
	var created int64
	if err := row.Scan(&u.ID, &u.Username, &u.IsAdmin, &u.Disabled, &created); err != nil {
		return nil, err
	}
	u.CreatedAt = time.Unix(created, 0)
	return u, nil
}

func (s *Service) CreateUser(ctx context.Context, username, password string, admin bool) (*User, error) {
	if !repos.NameRE.MatchString(username) || len(password) < minPasswordLen {
		return nil, ErrInvalid
	}
	hash, err := s.hashPassword(ctx, password)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	res, err := s.db.W.ExecContext(ctx,
		`INSERT INTO users (username, password_hash, is_admin, disabled, password_changed_at, created_at) VALUES (?, ?, ?, 0, ?, ?)`,
		username, hash, admin, now, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return nil, ErrExists
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &User{ID: id, Username: username, IsAdmin: admin, CreatedAt: time.Unix(now, 0)}, nil
}

func (s *Service) GetUser(ctx context.Context, username string) (*User, error) {
	u, err := scanUser(s.db.R.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE username = ?`, username))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

func (s *Service) ListUsers(ctx context.Context, after string, limit int) ([]*User, string, error) {
	limit = min(max(limit, 1), 200)
	rows, err := s.db.R.QueryContext(ctx,
		`SELECT `+userCols+` FROM users WHERE username > ? ORDER BY username LIMIT ?`, after, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := []*User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = out[limit-1].Username
	}
	return out, next, nil
}

func (s *Service) UpdateUser(ctx context.Context, username string, password *string, admin, disabled *bool) (*User, error) {
	var hash string
	if password != nil {
		if len(*password) < minPasswordLen {
			return nil, ErrInvalid
		}
		var err error
		if hash, err = s.hashPassword(ctx, *password); err != nil {
			return nil, err
		}
	}
	revoke := password != nil || (disabled != nil && *disabled)
	var id int64
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE username = ?`, username).Scan(&id); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if password != nil {
			if _, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ?, password_changed_at = ? WHERE id = ?`,
				hash, time.Now().Unix(), id); err != nil {
				return err
			}
		}
		if admin != nil {
			if _, err := tx.ExecContext(ctx, `UPDATE users SET is_admin = ? WHERE id = ?`, *admin, id); err != nil {
				return err
			}
		}
		if disabled != nil {
			if _, err := tx.ExecContext(ctx, `UPDATE users SET disabled = ? WHERE id = ?`, *disabled, id); err != nil {
				return err
			}
		}
		if revoke {
			if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if revoke {
		s.cache.invalidate(id)
	}
	return scanUser(s.db.W.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

func (s *Service) HasUsers(ctx context.Context) (bool, error) {
	var ok bool
	err := s.db.R.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM users)`).Scan(&ok)
	return ok, err
}
