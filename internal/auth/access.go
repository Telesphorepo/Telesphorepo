package auth

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

func parseLevel(s string) Level {
	switch s {
	case "read":
		return Read
	case "write":
		return Write
	}
	return None
}

func (s *Service) Access(ctx context.Context, p *Principal, repo *repos.Repo) (Level, error) {
	lvl := None
	if repo.AnonymousRead {
		lvl = Read
	}
	if p == nil {
		return lvl, nil
	}
	if p.IsAdmin {
		return Write, nil
	}
	l, err := s.rq.GetPermissionLevel(ctx, dbq.GetPermissionLevelParams{RepoID: repo.ID, UserID: p.UserID})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return None, err
	}
	return max(lvl, parseLevel(l)), nil
}

func (s *Service) RepoGate(w http.ResponseWriter, r *http.Request, name, format string, need Level, allowSession bool) (*repos.Repo, *Principal, bool) {
	p, ok := s.Identify(w, r, allowSession)
	if !ok {
		return nil, nil, false
	}
	denied := func() (*repos.Repo, *Principal, bool) {
		if p == nil {
			unauthorized(w, "authentication required")
		} else {
			httpx.Error(w, http.StatusForbidden, "forbidden")
		}
		return nil, nil, false
	}
	repo, err := s.repos.ByName(r.Context(), name)
	if errors.Is(err, repos.ErrNotFound) {
		if p == nil {
			return denied()
		}
		httpx.Error(w, http.StatusNotFound, "repo not found")
		return nil, nil, false
	}
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "internal error")
		return nil, nil, false
	}
	lvl, err := s.Access(r.Context(), p, repo)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "internal error")
		return nil, nil, false
	}
	if format != "" && repo.Format != format {
		if lvl == None && p == nil {
			return denied() // don't reveal that a repo of another format exists
		}
		httpx.Error(w, http.StatusNotFound, "repo not found")
		return nil, nil, false
	}
	if lvl < need {
		return denied()
	}
	return repo, p, true
}

// ReadableRepoIDs returns the ids of the repos p can read, or all=true for an admin. It merges
// the user's permission rows with the anonymous repos (served by the partial index
// repos_anonymous).
func (s *Service) ReadableRepoIDs(ctx context.Context, p *Principal) ([]int64, bool, error) {
	if p != nil && p.IsAdmin {
		return nil, true, nil
	}
	anon, err := s.rq.ListAnonymousRepoIDs(ctx)
	if err != nil {
		return nil, false, err
	}
	if p == nil {
		return anon, false, nil
	}
	permitted, err := s.rq.ListPermittedRepoIDs(ctx, p.UserID)
	if err != nil {
		return nil, false, err
	}
	seen := make(map[int64]struct{}, len(anon)+len(permitted))
	ids := make([]int64, 0, len(anon)+len(permitted))
	for _, id := range append(permitted, anon...) {
		if _, dup := seen[id]; !dup {
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	return ids, false, nil
}

func (s *Service) SetPermission(ctx context.Context, repoID int64, username string, level Level) error {
	if level != Read && level != Write {
		return ErrInvalid
	}
	n, err := s.wq.SetPermission(ctx, dbq.SetPermissionParams{RepoID: repoID, Level: level.String(), Username: username})
	if err != nil {
		if strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
			return ErrNotFound
		}
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Service) DeletePermission(ctx context.Context, repoID int64, username string) error {
	n, err := s.wq.DeletePermission(ctx, dbq.DeletePermissionParams{RepoID: repoID, Username: username})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Service) ListPermissions(ctx context.Context, repoID int64) ([]Permission, error) {
	rows, err := s.rq.ListPermissions(ctx, repoID)
	if err != nil {
		return nil, err
	}
	out := make([]Permission, len(rows))
	for i, r := range rows {
		out[i] = Permission{Username: r.Username, Level: r.Level}
	}
	return out, nil
}
