package auth

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"telesphorepo/internal/httpx"
	"telesphorepo/internal/repos"
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
	var l string
	err := s.db.R.QueryRowContext(ctx,
		`SELECT level FROM repo_permissions WHERE repo_id = ? AND user_id = ?`, repo.ID, p.UserID).Scan(&l)
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

func (s *Service) ReadableRepoIDs(ctx context.Context, p *Principal) ([]int64, bool, error) {
	if p != nil && p.IsAdmin {
		return nil, true, nil
	}
	q, args := `SELECT id FROM repos WHERE anonymous_read = 1`, []any{}
	if p != nil {
		q = `SELECT repo_id FROM repo_permissions WHERE user_id = ? UNION ` + q
		args = append(args, p.UserID)
	}
	rows, err := s.db.R.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, false, err
		}
		ids = append(ids, id)
	}
	return ids, false, rows.Err()
}

func (s *Service) SetPermission(ctx context.Context, repoID int64, username string, level Level) error {
	if level != Read && level != Write {
		return ErrInvalid
	}
	res, err := s.db.W.ExecContext(ctx, `
		INSERT INTO repo_permissions (repo_id, user_id, level)
		SELECT ?, id, ? FROM users WHERE username = ?
		ON CONFLICT (repo_id, user_id) DO UPDATE SET level = excluded.level`, repoID, level.String(), username)
	if err != nil {
		if strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
			return ErrNotFound
		}
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Service) DeletePermission(ctx context.Context, repoID int64, username string) error {
	res, err := s.db.W.ExecContext(ctx, `
		DELETE FROM repo_permissions WHERE repo_id = ? AND user_id = (SELECT id FROM users WHERE username = ?)`,
		repoID, username)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Service) ListPermissions(ctx context.Context, repoID int64) ([]Permission, error) {
	rows, err := s.db.R.QueryContext(ctx, `
		SELECT u.username, p.level FROM repo_permissions p JOIN users u ON u.id = p.user_id
		WHERE p.repo_id = ? ORDER BY u.username`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Permission{}
	for rows.Next() {
		var p Permission
		if err := rows.Scan(&p.Username, &p.Level); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
