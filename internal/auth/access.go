package auth

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/netrules"
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
func (s *Service) publishRequired(w http.ResponseWriter, r *http.Request) {
	base := strings.TrimSuffix(s.cfg().BaseURL, "/")
	unauthorized(w, r, "publishing needs a publish session: "+base+"/#/settings/tokens")
}

// RepoGate authenticates a format-route request and checks it may do need on repo name. A write
// (need >= Write) needs an open publish session for that repo; anything else gets 401 with the
// Basic challenge (none on the JSON API or for the web UI). A user who lost write permission
// gets 403, a locked repo 409.
// The repo's IP rules apply to every caller, admins included: the read rules to a read, the
// publish rules to a write. A refused address gets 403 and an ip.denied audit event.
// allowSession is true on the JSON API. A format route (allowSession false) also takes the UI
// session cookie for a GET or HEAD read, so a signed-in browser downloads without a Basic prompt.
func (s *Service) RepoGate(w http.ResponseWriter, r *http.Request, name, format string, need Level, allowSession bool) (*repos.Repo, *Principal, bool) {
	p, ok := s.identify(w, r, allowSession, allowSession || (need < Write && safeMethod(r.Method)))
	if !ok {
		return nil, nil, false
	}
	repo, p, ok := s.gate(w, r, p, name, format, need)
	if !ok || !s.ipAllowed(w, r, p, repo, need) {
		return nil, nil, false
	}
	return repo, p, true
}

// RepoMeta is RepoGate for a read of the repo record itself (GET /api/repos/{name}): it does not
// apply the IP rules, so an admin can still open the repo to change them.
func (s *Service) RepoMeta(w http.ResponseWriter, r *http.Request, name string) (*repos.Repo, *Principal, bool) {
	p, ok := s.Identify(w, r, true)
	if !ok {
		return nil, nil, false
	}
	return s.gate(w, r, p, name, "", Read)
}

// ipAllowed checks the repo's IP rules for need (a read or a publish) and answers 403 when they
// refuse the client address.
func (s *Service) ipAllowed(w http.ResponseWriter, r *http.Request, p *Principal, repo *repos.Repo, need Level) bool {
	if s.rules == nil {
		return true
	}
	scope := netrules.ScopeRead
	if need >= Write {
		scope = netrules.ScopePublish
	}
	if d := s.rules.CheckRequest(r, repo, scope, ActorOf(p)); d != nil {
		s.rules.Refuse(w, r, d)
		return false
	}
	return true
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
		repo, p, ok := s.gate(w, r, p, name, "", Write)
		if !ok || !s.ipAllowed(w, r, p, repo, Write) {
			return nil, nil, false
		}
		return repo, p, true
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
	if !s.ipAllowed(w, r, p, repo, Write) {
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
			s.publishRequired(w, r)
		case p == nil:
			unauthorized(w, r, "authentication required")
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
	// A proxy or a group takes no writes. Say so to a caller who can read it; anyone else gets
	// the usual refusal, so the answer does not tell that the repo exists.
	if need >= Write && repo.Kind != repos.KindHosted && lvl >= Read {
		w.Header().Set("Allow", "GET, HEAD")
		httpx.Error(w, http.StatusMethodNotAllowed, ReadOnly(repo))
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

// ReadOnly is the answer to a write to a proxy or a group repo.
func ReadOnly(repo *repos.Repo) string {
	if repo.Kind == repos.KindGroup {
		return "A group is read-only. Publish to one of its repositories."
	}
	return "A proxy is read-only."
}

// ReadsThrough reports whether the caller p of r may read repo as a member of a group: p's
// access to repo (as on its own route) and repo's read rules for the client address both count.
// It writes no answer and no audit event; the group skips a member that p cannot read.
func (s *Service) ReadsThrough(ctx context.Context, r *http.Request, p *Principal, repo *repos.Repo) (bool, error) {
	lvl, err := s.gateLevel(ctx, p, repo)
	if err != nil || lvl < Read {
		return false, err
	}
	if s.rules != nil && s.rules.CheckRequest(r, repo, netrules.ScopeRead, ActorOf(p)) != nil {
		return false, nil
	}
	return true, nil
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

// Grant is one pair of a user and a repo that SetPermissions or DeletePermissions changed.
type Grant struct {
	Repo     *repos.Repo
	Username string
}

// pairs reads every user and repo of a list change of permissions. An unknown name returns a
// *repos.MissingError. The repos come from the read pool; a repo deleted before the write
// transaction ends fails its foreign key, and the transaction changes nothing.
func (s *Service) pairs(ctx context.Context, q *dbq.Queries, users, repoNames []string) ([]int64, []*repos.Repo, error) {
	rs := make([]*repos.Repo, 0, len(repoNames))
	for _, name := range repoNames {
		r, err := s.repos.ByName(ctx, name)
		if errors.Is(err, repos.ErrNotFound) {
			return nil, nil, &repos.MissingError{Kind: "repository", Name: name}
		}
		if err != nil {
			return nil, nil, err
		}
		rs = append(rs, r)
	}
	ids := make([]int64, 0, len(users))
	for _, name := range users {
		row, err := q.GetUserIDKind(ctx, name)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, &repos.MissingError{Kind: "user", Name: name}
		}
		if err != nil {
			return nil, nil, err
		}
		ids = append(ids, row.ID)
	}
	return ids, rs, nil
}

// SetPermissions gives level on every repo of repoNames to every user of users, in one
// transaction. It reads every name first: an unknown name returns a *repos.MissingError and
// changes nothing. It returns the pairs whose level changed.
func (s *Service) SetPermissions(ctx context.Context, users, repoNames []string, level Level) ([]Grant, error) {
	if level != Read && level != Write {
		return nil, ErrInvalid
	}
	var changed []Grant
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		changed = changed[:0]
		q := s.wq.WithTx(tx)
		ids, rs, err := s.pairs(ctx, q, users, repoNames)
		if err != nil {
			return err
		}
		for i, uid := range ids {
			for _, r := range rs {
				old, err := q.GetPermissionLevel(ctx, dbq.GetPermissionLevelParams{RepoID: r.ID, UserID: uid})
				if err != nil && !errors.Is(err, sql.ErrNoRows) {
					return err
				}
				if old == level.String() {
					continue
				}
				if err := q.SetPermission(ctx, dbq.SetPermissionParams{RepoID: r.ID, UserID: uid, Level: level.String()}); err != nil {
					if strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
						return &repos.MissingError{Kind: "repository", Name: r.Name}
					}
					return err
				}
				changed = append(changed, Grant{Repo: r, Username: users[i]})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return changed, nil
}

// DeletePermissions removes the level of every pair of users and repoNames, in one transaction.
// It reads every name first: an unknown name returns a *repos.MissingError and changes nothing.
// It returns the pairs that had a level.
func (s *Service) DeletePermissions(ctx context.Context, users, repoNames []string) ([]Grant, error) {
	var changed []Grant
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		changed = changed[:0]
		q := s.wq.WithTx(tx)
		ids, rs, err := s.pairs(ctx, q, users, repoNames)
		if err != nil {
			return err
		}
		for i, uid := range ids {
			for _, r := range rs {
				n, err := q.DeletePermission(ctx, dbq.DeletePermissionParams{RepoID: r.ID, UserID: uid})
				if err != nil {
					return err
				}
				if n > 0 {
					changed = append(changed, Grant{Repo: r, Username: users[i]})
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return changed, nil
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
