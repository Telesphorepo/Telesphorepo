package auth

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/perm"
)

// SessionView is the signed-in person as the web UI sees them: the answer of GET /api/me and of
// a finished sign-in. PermissionsManaged is true when single sign-on manages the server
// permissions. RepoPermissions holds every repo with a grant, by name ({} for none, and
// for a holder of All repositories, whose AllRepos is true). SecondFactorRequired is true for a
// setup-only session.
type SessionView struct {
	ID                   int64               `json:"id"`
	Username             string              `json:"username"`
	Kind                 string              `json:"kind"`
	Email                string              `json:"email"`
	Disabled             bool                `json:"disabled"`
	CreatedAt            time.Time           `json:"created_at"`
	Permissions          []string            `json:"permissions"`
	PermissionsManaged   bool                `json:"permissions_managed"`
	AllRepos             bool                `json:"all_repos"`
	RepoPermissions      map[string][]string `json:"repo_permissions"`
	SecondFactorRequired bool                `json:"second_factor_required"`
}

// View returns the session view of user userID. setupOnly marks a setup-only session.
func (s *Service) View(ctx context.Context, userID int64, setupOnly bool) (*SessionView, error) {
	u, err := getUserByID(ctx, s.rq, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	v := &SessionView{
		ID: u.ID, Username: u.Username, Kind: u.Kind, Email: u.Email, Disabled: u.Disabled, CreatedAt: u.CreatedAt,
		Permissions: u.Permissions, PermissionsManaged: s.Managed(u.Kind), AllRepos: perm.Current().AllRepos(u.Perms), RepoPermissions: map[string][]string{},
		SecondFactorRequired: setupOnly,
	}
	if v.AllRepos {
		return v, nil
	}
	rows, err := s.rq.ListUserGrantsNamed(ctx, userID)
	if err != nil {
		return nil, err
	}
	sets := map[string]perm.Set{}
	for _, g := range rows {
		sets[g.Name] = sets[g.Name].Union(perm.SetOf(perm.ID(g.Perm)).Of(perm.ScopeRepo))
	}
	for name, set := range sets {
		if !set.Empty() {
			v.RepoPermissions[name] = set.Strings()
		}
	}
	return v, nil
}
