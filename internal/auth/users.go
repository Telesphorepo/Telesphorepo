package auth

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

const minPasswordLen = 8

func newUser(id int64, username, perms string, disabled bool, kind string, email sql.NullString, created int64) *User {
	p := perm.Joined(perms).Of(perm.ScopeServer)
	return &User{
		ID: id, Username: username, Permissions: p.Strings(), Perms: p, Disabled: disabled, Kind: kind,
		Email: email.String, CreatedAt: time.Unix(created, 0),
	}
}

// CreateUser creates a local account with the server permissions perms and records user.create.
func (s *Service) CreateUser(ctx context.Context, username, password string, perms perm.Set) (*User, error) {
	if err := perm.Current().CheckNeeds(perms); err != nil {
		return nil, err
	}
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
		if err := addUserPerms(ctx, q, id, perms); err != nil {
			return err
		}
		return s.audit.RecordTx(ctx, tx, audit.Event{Action: audit.UserCreate, Target: username, Detail: map[string]any{"permissions": perms.Strings()}})
	})
	if err != nil {
		return nil, err
	}
	s.changed(ctx, Change{Users: []int64{id}})
	return &User{ID: id, Username: username, Permissions: perms.Strings(), Perms: perms, Kind: KindLocal, CreatedAt: time.Unix(now, 0)}, nil
}

func addUserPerms(ctx context.Context, q *dbq.Queries, id int64, perms perm.Set) error {
	for _, n := range perms.Strings() {
		if err := q.AddUserPerm(ctx, dbq.AddUserPermParams{UserID: id, Perm: n}); err != nil {
			return err
		}
	}
	return nil
}

// ErrLastPeople refuses a change that leaves no enabled person with people.manage.
var ErrLastPeople = errors.New("at least one enabled person must keep the People permission")

// checkPeopleHolder returns ErrLastPeople when no enabled person holds people.manage.
func checkPeopleHolder(ctx context.Context, q *dbq.Queries) error {
	rows, err := q.ListPermHolders(ctx, string(perm.PeopleManage))
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return ErrLastPeople
	}
	return nil
}

// ErrLastAdmin refuses a change that leaves no enabled administrator (a person with every
// server permission) where there was one.
var ErrLastAdmin = errors.New("the only administrator must stay an administrator: make another person an administrator first")

// enabledAdmins returns the enabled people who hold every server permission: id to username.
func enabledAdmins(ctx context.Context, q *dbq.Queries) (map[int64]string, error) {
	var in map[int64]string
	for _, p := range perm.Current().AllServer().Strings() {
		rows, err := q.ListPermHolders(ctx, p)
		if err != nil {
			return nil, err
		}
		next := make(map[int64]string, len(rows))
		for _, r := range rows {
			if _, ok := in[r.ID]; in == nil || ok {
				next[r.ID] = r.Username
			}
		}
		if in = next; len(in) == 0 {
			break
		}
	}
	return in, nil
}

// keepAdmin runs change and returns ErrLastAdmin when it leaves no enabled administrator where
// there was one before. A server with none (a module added a permission that nobody holds yet)
// refuses nothing.
func keepAdmin(ctx context.Context, q *dbq.Queries, change func() error) error {
	before, err := enabledAdmins(ctx, q)
	if err != nil {
		return err
	}
	if err := change(); err != nil {
		return err
	}
	if len(before) == 0 {
		return nil
	}
	after, err := enabledAdmins(ctx, q)
	if err != nil {
		return err
	}
	if len(after) == 0 {
		return ErrLastAdmin
	}
	return nil
}

// SetUserPerms replaces the server permissions of username with ids and returns the old set. It
// checks ids against the permission registry (an unknown ID or a broken need is an error). A
// user who needed no second factor before and needs one now loses their sessions, so a session
// that a password alone started never holds the new permissions. It returns ErrLastPeople when
// the change leaves no enabled person with people.manage, ErrLastAdmin when it takes the last
// enabled administrator's role, and records user.permissions.
func (s *Service) SetUserPerms(ctx context.Context, username string, ids []string) (old, perms perm.Set, err error) {
	if perms, err = perm.Current().Parse(perm.ScopeServer, ids); err != nil {
		return perm.Set{}, perm.Set{}, err
	}
	var changedID int64
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		changedID = 0
		q := s.wq.WithTx(tx)
		r, err := q.GetUserByUsername(ctx, username)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if s.Managed(r.Kind) {
			return ErrManaged
		}
		old = perm.Joined(r.Perms).Of(perm.ScopeServer)
		if old == perms {
			return nil
		}
		changedID = r.ID
		before, err := userNeedsFactor(ctx, q, r.ID)
		if err != nil {
			return err
		}
		if err := keepAdmin(ctx, q, func() error {
			if err := q.DeleteUserPerms(ctx, r.ID); err != nil {
				return err
			}
			return addUserPerms(ctx, q, r.ID, perms)
		}); err != nil {
			return err
		}
		if err := checkPeopleHolder(ctx, q); err != nil {
			return err
		}
		if err := s.audit.RecordTx(ctx, tx, audit.Event{Action: audit.UserPermissions, Target: username,
			Detail: map[string]any{"old": old.Strings(), "new": perms.Strings()}}); err != nil {
			return err
		}
		after, err := userNeedsFactor(ctx, q, r.ID)
		if err != nil {
			return err
		}
		if !before && after {
			return q.DeleteUserSessions(ctx, r.ID)
		}
		return nil
	})
	if err == nil && changedID != 0 {
		s.changed(ctx, Change{Users: []int64{changedID}})
	}
	return old, perms, err
}

func (s *Service) GetUser(ctx context.Context, username string) (*User, error) {
	r, err := s.rq.GetUserByUsername(ctx, username)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.mark(newUser(r.ID, r.Username, r.Perms, r.Disabled, r.Kind, r.Email, r.CreatedAt)), nil
}

// mark sets PermissionsManaged of u.
func (s *Service) mark(u *User) *User {
	u.PermissionsManaged = s.Managed(u.Kind)
	return u
}

// Admins returns the usernames of the enabled administrators, sorted.
func (s *Service) Admins(ctx context.Context) ([]string, error) {
	admins, err := enabledAdmins(ctx, s.rq)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(admins))
	for _, name := range admins {
		out = append(out, name)
	}
	slices.Sort(out)
	return out, nil
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
		out[i] = s.mark(newUser(r.ID, r.Username, r.Perms, r.Disabled, r.Kind, r.Email, r.CreatedAt))
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
// sessions of every user in the list. It records user.update for each user whose value changed,
// and returns them.
func (s *Service) SetDisabled(ctx context.Context, names []string, disabled bool) ([]*User, error) {
	var changed []*User
	var ids []int64
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		changed, ids = changed[:0], ids[:0]
		q := s.wq.WithTx(tx)
		before, err := enabledAdmins(ctx, q)
		if err != nil {
			return err
		}
		var all []*User
		for _, name := range names {
			r, err := q.GetUserByUsername(ctx, name)
			if errors.Is(err, sql.ErrNoRows) {
				return &repos.MissingError{Kind: "user", Name: name}
			}
			if err != nil {
				return err
			}
			all = append(all, s.mark(newUser(r.ID, r.Username, r.Perms, r.Disabled, r.Kind, r.Email, r.CreatedAt)))
		}
		for _, u := range all {
			if u.Disabled != disabled {
				if err := q.SetUserDisabled(ctx, dbq.SetUserDisabledParams{Disabled: disabled, ID: u.ID}); err != nil {
					return err
				}
				u.Disabled = disabled
				changed = append(changed, u)
				if err := s.audit.RecordTx(ctx, tx, audit.Event{Action: audit.UserUpdate, Target: u.Username, Detail: map[string]any{"disabled": disabled}}); err != nil {
					return err
				}
			}
			if disabled {
				if err := q.DeleteUserSessions(ctx, u.ID); err != nil {
					return err
				}
				ids = append(ids, u.ID)
			}
		}
		if !disabled {
			return nil
		}
		if len(before) > 0 {
			if after, err := enabledAdmins(ctx, q); err != nil {
				return err
			} else if len(after) == 0 {
				return ErrLastAdmin
			}
		}
		return checkPeopleHolder(ctx, q)
	})
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		s.cache.invalidate(id)
	}
	c := Change{}
	for _, u := range changed {
		c.Users = append(c.Users, u.ID)
	}
	s.changed(ctx, c)
	return changed, nil
}

// UpdateUser changes the password of username when password is set; the user's sessions end and
// user.update is recorded (never the value).
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
			if err := s.audit.RecordTx(ctx, tx, audit.Event{Action: audit.UserUpdate, Target: username, Detail: map[string]any{"password": "changed"}}); err != nil {
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

// UpsertOIDCUser creates or updates the SSO account identified by (issuer, subject) and sets its
// email. A new account gets username (already normalized); if that name belongs to another
// account the result is ErrUsernameConflict (accounts are never merged by name). A disabled
// account returns ErrDisabled. With managed set, single sign-on manages the server permissions:
// they become exactly *managed, and a change records user.permissions with the actor kind sso.
// A change that would leave no enabled person with people.manage keeps the old permissions and
// is logged. With managed nil the permissions stay as they are; a new account has none.
func (s *Service) UpsertOIDCUser(ctx context.Context, issuer, subject, username, email string, managed *perm.Set) (*User, error) {
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
		case !errors.Is(err, sql.ErrNoRows):
			return err
		default:
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
		}
		if managed == nil {
			return nil
		}
		return s.syncManaged(ctx, tx, q, id, *managed)
	})
	if err != nil {
		return nil, err
	}
	// A new account, or permissions that single sign-on changed.
	s.changed(ctx, Change{Users: []int64{id}})
	u, err := getUserByID(ctx, s.wq, id)
	if err != nil {
		return nil, err
	}
	return s.mark(u), nil
}

// syncManaged sets the server permissions of SSO account id to perms in tx, through a savepoint,
// and records user.permissions as the actor sso when they change. A change that leaves no enabled
// person with people.manage is rolled back to the savepoint and logged.
func (s *Service) syncManaged(ctx context.Context, tx *sql.Tx, q *dbq.Queries, id int64, perms perm.Set) error {
	u, err := getUserByID(ctx, q, id)
	if err != nil {
		return err
	}
	old := u.Perms.Of(perm.ScopeServer)
	if old == perms {
		return nil
	}
	if _, err := tx.ExecContext(ctx, "SAVEPOINT sso_permissions"); err != nil {
		return err
	}
	err = keepAdmin(ctx, q, func() error {
		if err := q.DeleteUserPerms(ctx, id); err != nil {
			return err
		}
		return addUserPerms(ctx, q, id, perms)
	})
	if err == nil {
		err = checkPeopleHolder(ctx, q)
	}
	if errors.Is(err, ErrLastPeople) || errors.Is(err, ErrLastAdmin) {
		slog.WarnContext(ctx, "single sign-on: the permissions from the provider would leave no administrator or nobody with people.manage; the old permissions stay",
			"username", u.Username)
		_, err := tx.ExecContext(ctx, "ROLLBACK TO sso_permissions")
		return err
	} else if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "RELEASE sso_permissions"); err != nil {
		return err
	}
	actx := audit.WithActor(ctx, audit.Actor{Kind: audit.ActorSSO, UserID: id, Name: u.Username, IP: audit.ActorOf(ctx).IP})
	return s.audit.RecordTx(actx, tx, audit.Event{Action: audit.UserPermissions, Target: u.Username,
		Detail: map[string]any{"old": old.Strings(), "new": perms.Strings()}})
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

// Covers reports whether by holds every permission that target holds: each server permission,
// and on each repository each repository permission (All repositories covers them all). A
// person may reset the password of, or disable, only a person they cover, so neither action
// gives them more than they hold.
func (s *Service) Covers(ctx context.Context, by *Principal, target *User) (bool, error) {
	if !by.Perms.Contains(target.Perms) {
		return false, nil
	}
	if perm.Current().AllRepos(by.Perms) {
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
		if !mine[id].Contains(set) {
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
