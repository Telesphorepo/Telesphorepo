package auth

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

const minPasswordLen = 8

func newUser(id int64, username string, admin, disabled bool, kind string, email sql.NullString, created int64) *User {
	return &User{
		ID: id, Username: username, IsAdmin: admin, Disabled: disabled, Kind: kind,
		Email: email.String, CreatedAt: time.Unix(created, 0),
	}
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
	id, err := s.wq.CreateUser(ctx, dbq.CreateUserParams{
		Username: username, PasswordHash: hash, IsAdmin: admin, PasswordChangedAt: now, CreatedAt: now})
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return nil, ErrExists
		}
		return nil, err
	}
	return &User{ID: id, Username: username, IsAdmin: admin, Kind: KindLocal, CreatedAt: time.Unix(now, 0)}, nil
}

func (s *Service) GetUser(ctx context.Context, username string) (*User, error) {
	r, err := s.rq.GetUserByUsername(ctx, username)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return newUser(r.ID, r.Username, r.IsAdmin, r.Disabled, r.Kind, r.Email, r.CreatedAt), nil
}

func getUserByID(ctx context.Context, q *dbq.Queries, id int64) (*User, error) {
	r, err := q.GetUserByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return newUser(r.ID, r.Username, r.IsAdmin, r.Disabled, r.Kind, r.Email, r.CreatedAt), nil
}

func (s *Service) ListUsers(ctx context.Context, after string, limit int) ([]*User, string, error) {
	limit = min(max(limit, 1), 200)
	rows, err := s.rq.ListUsers(ctx, dbq.ListUsersParams{After: after, Limit: int64(limit + 1)})
	if err != nil {
		return nil, "", err
	}
	out := make([]*User, len(rows))
	for i, r := range rows {
		out[i] = newUser(r.ID, r.Username, r.IsAdmin, r.Disabled, r.Kind, r.Email, r.CreatedAt)
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
		q := s.wq.WithTx(tx)
		row, err := q.GetUserIDKind(ctx, username)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		id = row.ID
		if password != nil && row.Kind != KindLocal {
			return ErrSSOAccount // SSO accounts keep the unusable "!" hash
		}
		if password != nil {
			if err := q.SetUserPassword(ctx, dbq.SetUserPasswordParams{
				PasswordHash: hash, PasswordChangedAt: time.Now().Unix(), ID: id}); err != nil {
				return err
			}
		}
		if admin != nil {
			if err := q.SetUserAdmin(ctx, dbq.SetUserAdminParams{IsAdmin: *admin, ID: id}); err != nil {
				return err
			}
		}
		if disabled != nil {
			if err := q.SetUserDisabled(ctx, dbq.SetUserDisabledParams{Disabled: *disabled, ID: id}); err != nil {
				return err
			}
		}
		if revoke {
			if err := q.DeleteUserSessions(ctx, id); err != nil {
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
	return getUserByID(ctx, s.wq, id)
}

func (s *Service) HasUsers(ctx context.Context) (bool, error) {
	return s.rq.HasUsers(ctx)
}

// UpsertOIDCUser creates or updates the SSO admin identified by (issuer, subject): is_admin is
// set and the email updated. A new account gets username (already normalized); if that name
// belongs to another account the result is ErrUsernameConflict (accounts are never merged by
// name). A disabled account returns ErrDisabled.
func (s *Service) UpsertOIDCUser(ctx context.Context, issuer, subject, username, email string) (*User, error) {
	mail := sql.NullString{String: email, Valid: email != ""}
	oidcID := dbq.GetOIDCUserParams{
		OidcIssuer:  sql.NullString{String: issuer, Valid: true},
		OidcSubject: sql.NullString{String: subject, Valid: true},
	}
	var id int64
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		q := s.wq.WithTx(tx)
		row, err := q.GetOIDCUser(ctx, oidcID)
		switch {
		case err == nil:
			id = row.ID
			if row.Disabled {
				return ErrDisabled
			}
			return q.UpdateOIDCUser(ctx, dbq.UpdateOIDCUserParams{Email: mail, ID: id})
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		if !repos.NameRE.MatchString(username) {
			return ErrInvalid
		}
		now := time.Now().Unix()
		id, err = q.CreateOIDCUser(ctx, dbq.CreateOIDCUserParams{
			Username: username, PasswordHash: noPassword, PasswordChangedAt: now, CreatedAt: now,
			Kind: KindOIDC, OidcIssuer: oidcID.OidcIssuer, OidcSubject: oidcID.OidcSubject, Email: mail})
		if err != nil {
			if strings.Contains(err.Error(), "users.username") {
				return ErrUsernameConflict
			}
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return getUserByID(ctx, s.wq, id)
}

// RevokeOIDCUser deletes the sessions of the SSO account (issuer, subject), if it exists. Used
// when a login no longer qualifies as admin.
func (s *Service) RevokeOIDCUser(ctx context.Context, issuer, subject string) error {
	return s.wq.DeleteOIDCUserSessions(ctx, dbq.DeleteOIDCUserSessionsParams{
		OidcIssuer:  sql.NullString{String: issuer, Valid: true},
		OidcSubject: sql.NullString{String: subject, Valid: true},
	})
}
