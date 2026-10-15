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

func newUser(id int64, username, perms string, disabled bool, kind string, email sql.NullString, created int64) *User {
	p := ServerPermsOf(perms)
	return &User{
		ID: id, Username: username, Permissions: p.Names(), Perms: p, Disabled: disabled, Kind: kind,
		Email: email.String, CreatedAt: time.Unix(created, 0),
	}
}

// CreateUser creates a local account with the server permissions perms.
func (s *Service) CreateUser(ctx context.Context, username, password string, perms ServerPerm) (*User, error) {
	if !repos.NameRE.MatchString(username) || len(password) < minPasswordLen {
		return nil, ErrInvalid
	}
	hash, err := s.hashPassword(ctx, password)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	var id int64
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		q := s.wq.WithTx(tx)
		var err error
		id, err = q.CreateUser(ctx, dbq.CreateUserParams{
			Username: username, PasswordHash: hash, PasswordChangedAt: now, CreatedAt: now})
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed") {
				return ErrExists
			}
			return err
		}
		return addUserPerms(ctx, q, id, perms)
	})
	if err != nil {
		return nil, err
	}
	return &User{ID: id, Username: username, Permissions: perms.Names(), Perms: perms, Kind: KindLocal, CreatedAt: time.Unix(now, 0)}, nil
}

func addUserPerms(ctx context.Context, q *dbq.Queries, id int64, perms ServerPerm) error {
	for _, n := range perms.Names() {
		if err := q.AddUserPerm(ctx, dbq.AddUserPermParams{UserID: id, Perm: n}); err != nil {
			return err
		}
	}
	return nil
}

// ErrLastPeople refuses a change that leaves no enabled person with the People permission.
var ErrLastPeople = errors.New("at least one enabled person must keep the People permission")

// checkPeopleHolder returns ErrLastPeople when no enabled person holds PermPeople.
func checkPeopleHolder(ctx context.Context, q *dbq.Queries) error {
	rows, err := q.ListPermHolders(ctx, "people")
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return ErrLastPeople
	}
	return nil
}

// SetUserPerms replaces the server permissions of username and returns the old set. A user who
// needed no second factor before and needs one now loses their sessions, so a session that a
// password alone started never holds the new permissions. It returns ErrLastPeople when the
// change leaves no enabled person with PermPeople.
func (s *Service) SetUserPerms(ctx context.Context, username string, perms ServerPerm) (ServerPerm, error) {
	var old ServerPerm
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		q := s.wq.WithTx(tx)
		r, err := q.GetUserByUsername(ctx, username)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		old = ServerPermsOf(r.Perms)
		if old == perms {
			return nil
		}
		before, err := q.UserElevated(ctx, r.ID)
		if err != nil {
			return err
		}
		if err := q.DeleteUserPerms(ctx, r.ID); err != nil {
			return err
		}
		if err := addUserPerms(ctx, q, r.ID, perms); err != nil {
			return err
		}
		if err := checkPeopleHolder(ctx, q); err != nil {
			return err
		}
		if !before.Bool && perms != 0 {
			return q.DeleteUserSessions(ctx, r.ID)
		}
		return nil
	})
	return old, err
}

func (s *Service) GetUser(ctx context.Context, username string) (*User, error) {
	r, err := s.rq.GetUserByUsername(ctx, username)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return newUser(r.ID, r.Username, r.Perms, r.Disabled, r.Kind, r.Email, r.CreatedAt), nil
}

func getUserByID(ctx context.Context, q *dbq.Queries, id int64) (*User, error) {
	r, err := q.GetUserByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return newUser(r.ID, r.Username, r.Perms, r.Disabled, r.Kind, r.Email, r.CreatedAt), nil
}

func (s *Service) ListUsers(ctx context.Context, after string, limit int) ([]*User, string, error) {
	limit = min(max(limit, 1), 200)
	rows, err := s.rq.ListUsers(ctx, dbq.ListUsersParams{After: after, Limit: int64(limit + 1)})
	if err != nil {
		return nil, "", err
	}
	out := make([]*User, len(rows))
	for i, r := range rows {
		out[i] = newUser(r.ID, r.Username, r.Perms, r.Disabled, r.Kind, r.Email, r.CreatedAt)
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = out[limit-1].Username
	}
	return out, next, nil
}

// SetDisabled disables or enables every user of names in one transaction. It reads every name
// first: an unknown name returns a *repos.MissingError and changes nothing. Disabling ends the
// sessions of every user in the list. It returns the users whose value changed.
func (s *Service) SetDisabled(ctx context.Context, names []string, disabled bool) ([]*User, error) {
	var changed []*User
	var ids []int64
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		changed, ids = changed[:0], ids[:0]
		q := s.wq.WithTx(tx)
		var all []*User
		for _, name := range names {
			r, err := q.GetUserByUsername(ctx, name)
			if errors.Is(err, sql.ErrNoRows) {
				return &repos.MissingError{Kind: "user", Name: name}
			}
			if err != nil {
				return err
			}
			all = append(all, newUser(r.ID, r.Username, r.Perms, r.Disabled, r.Kind, r.Email, r.CreatedAt))
		}
		for _, u := range all {
			if u.Disabled != disabled {
				if err := q.SetUserDisabled(ctx, dbq.SetUserDisabledParams{Disabled: disabled, ID: u.ID}); err != nil {
					return err
				}
				u.Disabled = disabled
				changed = append(changed, u)
			}
			if disabled {
				if err := q.DeleteUserSessions(ctx, u.ID); err != nil {
					return err
				}
				ids = append(ids, u.ID)
			}
		}
		if disabled {
			return checkPeopleHolder(ctx, q)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		s.cache.invalidate(id)
	}
	return changed, nil
}

// UpdateUser changes the password of username when password is set; the user's sessions end.
func (s *Service) UpdateUser(ctx context.Context, username string, password *string) (*User, error) {
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
	revoke := password != nil
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

// UpsertOIDCUser creates or updates the SSO administrator identified by (issuer, subject): it
// holds every server permission and the email is updated. A new account gets username (already normalized); if that name
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
			if err := q.UpdateOIDCUser(ctx, dbq.UpdateOIDCUserParams{Email: mail, ID: id}); err != nil {
				return err
			}
			return addUserPerms(ctx, q, id, AllServerPerms)
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
		return addUserPerms(ctx, q, id, AllServerPerms)
	})
	if err != nil {
		return nil, err
	}
	return getUserByID(ctx, s.wq, id)
}

// OIDCUser returns the id of the SSO account (issuer, subject) and whether it is disabled. The id
// is 0 when no account has this identity.
func (s *Service) OIDCUser(ctx context.Context, issuer, subject string) (id int64, disabled bool, err error) {
	row, err := s.rq.GetOIDCUser(ctx, dbq.GetOIDCUserParams{
		OidcIssuer:  sql.NullString{String: issuer, Valid: true},
		OidcSubject: sql.NullString{String: subject, Valid: true},
	})
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return row.ID, row.Disabled, nil
}

// RevokeOIDCUser deletes the sessions of the SSO account (issuer, subject), if it exists. Used
// when a login no longer qualifies as admin.
func (s *Service) RevokeOIDCUser(ctx context.Context, issuer, subject string) error {
	return s.wq.DeleteOIDCUserSessions(ctx, dbq.DeleteOIDCUserSessionsParams{
		OidcIssuer:  sql.NullString{String: issuer, Valid: true},
		OidcSubject: sql.NullString{String: subject, Valid: true},
	})
}

// Covers reports whether by holds every permission that target holds: each server permission,
// and on each repository each repository permission (All repositories covers them all). A
// person may reset the password of, or disable, only a person they cover, so neither action
// gives them more than they hold.
func (s *Service) Covers(ctx context.Context, by *Principal, target *User) (bool, error) {
	if target.Perms&^by.Perms != 0 {
		return false, nil
	}
	if by.Perms.Has(PermRepos) {
		return true, nil
	}
	theirs, err := s.UserRepoPerms(ctx, target.ID)
	if err != nil {
		return false, err
	}
	if len(theirs) == 0 {
		return true, nil
	}
	mine, err := s.UserRepoPerms(ctx, by.UserID)
	if err != nil {
		return false, err
	}
	for id, set := range theirs {
		if set&^mine[id] != 0 {
			return false, nil
		}
	}
	return true, nil
}

// People returns a keyset page of the enabled usernames that start with prefix, after the name
// after ("" for the first page). next is "" at the end. The range has one lower bound: the
// tighter of the prefix and the cursor.
func (s *Service) People(ctx context.Context, prefix, after string, limit int) ([]string, string, error) {
	limit = min(max(limit, 1), 20)
	lower := prefix
	if c := after + "\x00"; after != "" && c > lower {
		lower = c
	}
	upper := "\x7f" // above every username (they are ASCII)
	if prefix != "" {
		b := []byte(prefix)
		b[len(b)-1]++
		upper = string(b)
	}
	rows, err := s.rq.ListPeople(ctx, dbq.ListPeopleParams{Lower: lower, Upper: upper, Limit: int64(limit + 1)})
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = rows[limit-1]
	}
	return rows, next, nil
}
