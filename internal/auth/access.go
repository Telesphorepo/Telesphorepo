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

// gateLevel is what p may do on repo through a format route (spec §7.6). Only a publish session
// writes, and only to its own repo: an OIDC session writes there outright, a device or manual
// session as far as its user's current permission allows. A session reads no other repo beyond
// anonymous read. Every other credential reads at most.
func (s *Service) gateLevel(ctx context.Context, p *Principal, repo *repos.Repo) (Level, error) {
	if p != nil && p.Publish != nil {
		if p.Publish.RepoID != repo.ID {
			return s.Access(ctx, nil, repo)
		}
		if p.Publish.Via == "oidc" {
			return Write, nil
		}
		return s.Access(ctx, p, repo)
	}
	lvl, err := s.Access(ctx, p, repo)
	return min(lvl, Read), err
}

// publishRequired answers a write without an open publish session for the repo.
func (s *Service) publishRequired(w http.ResponseWriter) {
	base := strings.TrimSuffix(s.cfg().BaseURL, "/")
	unauthorized(w, "publishing needs a publish session: "+base+"/#/publishing")
}

// RepoGate authenticates a format-route request and checks it may do need on repo name. A write
// (need >= Write) needs an open publish session for that repo; anything else gets 401 with the
// Basic challenge. A user who lost write permission gets 403, a locked repo 409.
func (s *Service) RepoGate(w http.ResponseWriter, r *http.Request, name, format string, need Level, allowSession bool) (*repos.Repo, *Principal, bool) {
	p, ok := s.Identify(w, r, allowSession)
	if !ok {
		return nil, nil, false
	}
	return s.gate(w, r, p, name, format, need)
}

// DeleteGate authorizes a delete of files through the JSON API (spec §13): a publish session
// for the repo (the write gate of RepoGate), or the UI session of an admin. An admin's UI
// session needs a second factor (outside dev mode), so both paths had a fresh second factor or
// a device or CI proof. A locked repo answers 409.
func (s *Service) DeleteGate(w http.ResponseWriter, r *http.Request, name string) (*repos.Repo, *Principal, bool) {
	p, ok := s.Identify(w, r, true)
	if !ok {
		return nil, nil, false
	}
	if p == nil || p.Publish != nil || !p.Session || !p.IsAdmin {
		return s.gate(w, r, p, name, "", Write)
	}
	repo, err := s.repos.ByName(r.Context(), name)
	if errors.Is(err, repos.ErrNotFound) {
		httpx.Error(w, http.StatusNotFound, "repo not found")
		return nil, nil, false
	}
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "internal error")
		return nil, nil, false
	}
	if repo.LockedBy != "" {
		httpx.Error(w, http.StatusConflict, repo.LockedBy)
		return nil, nil, false
	}
	return repo, p, true
}

// gate checks that the identified caller p may do need on repo name (see RepoGate).
func (s *Service) gate(w http.ResponseWriter, r *http.Request, p *Principal, name, format string, need Level) (*repos.Repo, *Principal, bool) {
	var repo *repos.Repo
	denied := func() (*repos.Repo, *Principal, bool) {
		switch {
		case need >= Write && (p == nil || p.Publish == nil || (repo != nil && p.Publish.RepoID != repo.ID)):
			s.publishRequired(w)
		case p == nil:
			unauthorized(w, "authentication required")
		default:
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
	lvl, err := s.gateLevel(r.Context(), p, repo)
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
	// A locked repo takes no writes through HTTP; its text names the owner, e.g. the Nexus
	// source that the repo mirrors.
	if need >= Write && repo.LockedBy != "" {
		httpx.Error(w, http.StatusConflict, repo.LockedBy)
		return nil, nil, false
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
