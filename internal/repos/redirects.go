package repos

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
)

// A redirect is a repository name that answers for another repo: every lookup by name
// (ByName) finds the repo of the name first, then the repo that a redirect of the name points
// at, both by index. A request through a redirect is served by its repo exactly as if it named
// the repo. A rename leaves the old name as a redirect; AddRedirect adds one without a rename. A
// redirect holds the id of its repo, so it follows the repo through later renames and never
// points at another redirect. A name is a repo
// or a redirect, never both: Create and Rename refuse the name of a redirect of another repo.

// Redirect is a name that answers for the repo RepoID.
type Redirect struct {
	Name      string
	RepoID    int64
	CreatedAt time.Time
	// LastUsedAt is the last use that the server wrote (zero for never); the uses of the last
	// minute are still in memory (stats.Counter).
	LastUsedAt time.Time
}

func redirectOf(name string, repoID, created, used int64) Redirect {
	rd := Redirect{Name: name, RepoID: repoID, CreatedAt: time.Unix(created, 0)}
	if used > 0 {
		rd.LastUsedAt = time.Unix(used, 0)
	}
	return rd
}

// RedirectedError refuses a name that is a redirect of another repo. It matches ErrExists and
// answers 409.
type RedirectedError struct {
	Name   string
	Target string // the name of the repo it answers for
}

func (e *RedirectedError) Error() string {
	return e.Name + " is a redirect to " + e.Target + "; remove the redirect on " + e.Target + " to use the name"
}

func (e *RedirectedError) Is(target error) bool { return target == ErrExists }

func (e *RedirectedError) HTTPStatus() int { return http.StatusConflict }

// OnRedirectUse sets fn, which ByName calls with the redirect name of each lookup through a
// redirect (the stats counter keeps the time and writes it once a minute). It runs before the
// server serves.
func (s *Service) OnRedirectUse(fn func(name string)) { s.onRedirect = fn }

// byName reads the repo of name: the repo whose own name it is, else the repo that the redirect
// name answers for.
func byName(ctx context.Context, q *dbq.Queries, name string) (*Repo, error) {
	r, err := byOwnName(ctx, q, name)
	if !errors.Is(err, ErrNotFound) {
		return r, err
	}
	rd, err := q.GetRepoRedirect(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return byID(ctx, q, rd.RepoID)
}

// checkNotRedirect refuses name when it is a redirect of a repo other than id (0: of any repo).
func checkNotRedirect(ctx context.Context, q *dbq.Queries, name string, id int64) error {
	rd, err := q.GetRepoRedirect(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if rd.RepoID == id {
		return nil
	}
	target, err := byID(ctx, q, rd.RepoID)
	if err != nil {
		return err
	}
	return &RedirectedError{Name: name, Target: target.Name}
}

// RedirectTarget is the repo that the redirect name answers for; ErrNotFound when name is no
// redirect. It notes no use.
func (s *Service) RedirectTarget(ctx context.Context, name string) (*Repo, error) {
	q := dbq.New(s.db.R)
	rd, err := q.GetRepoRedirect(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.counted(byID(ctx, q, rd.RepoID))
}

// Redirects returns the redirects of repo id by name.
func (s *Service) Redirects(ctx context.Context, id int64) ([]Redirect, error) {
	rows, err := dbq.New(s.db.R).ListRepoRedirectsOf(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]Redirect, len(rows))
	for i, r := range rows {
		out[i] = redirectOf(r.Name, r.RepoID, r.CreatedAt, r.LastUsedAt)
	}
	return out, nil
}

// RedirectPage returns a keyset page of every redirect by name: names after after ("" for the
// first page), with their repo ids only. next is the cursor of the next page, "" at the end.
func (s *Service) RedirectPage(ctx context.Context, after string, limit int) ([]Redirect, string, error) {
	rows, err := dbq.New(s.db.R).ListRepoRedirects(ctx, dbq.ListRepoRedirectsParams{After: after, Limit: int64(limit)})
	if err != nil {
		return nil, "", err
	}
	out := make([]Redirect, len(rows))
	for i, r := range rows {
		out[i] = Redirect{Name: r.Name, RepoID: r.RepoID}
	}
	next := ""
	if limit > 0 && len(rows) == limit {
		next = rows[len(rows)-1].Name
	}
	return out, next, nil
}

// Rename gives the repo of name (its own name or a redirect of it) the name to, in one
// transaction: the repo row, a redirect from the old name, the format's OnRepoChange (with the
// old and the new name) and one repo.rename event. A redirect of the repo named to goes, so a
// repo can take an old name back; the name of another repo or of a redirect of another repo is
// refused (ErrExists, *RedirectedError). The repo's other redirects keep answering for it. It
// returns the repo after the rename; a rename to the current name changes nothing.
func (s *Service) Rename(ctx context.Context, name, to string) (*Repo, error) {
	if !NameRE.MatchString(to) {
		return nil, invalid("a name has 1 to 64 lower-case letters, digits, '.', '_' or '-', and starts with a letter or a digit")
	}
	var cur, next *Repo
	var notice []string
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		var err error
		if cur, err = s.counted(byName(ctx, q, name)); err != nil {
			return err
		}
		next = cur
		if to == cur.Name {
			return nil
		}
		// The format checks the name (some formats take fewer names than NameRE).
		if _, err := s.normalizeConfig(cur.Format, to, cur.Config); err != nil {
			return err
		}
		if err := checkNotRedirect(ctx, q, to, cur.ID); err != nil {
			return err
		}
		rds, err := q.ListRepoRedirectsOf(ctx, cur.ID)
		if err != nil {
			return err
		}
		notice = notice[:0]
		for _, rd := range rds {
			if rd.Name == to {
				if _, err := q.DeleteRepoRedirect(ctx, dbq.DeleteRepoRedirectParams{Name: to, RepoID: cur.ID}); err != nil {
					return err
				}
			}
			notice = append(notice, rd.Name)
		}
		if err := q.RenameRepo(ctx, dbq.RenameRepoParams{Name: to, ID: cur.ID}); isUnique(err) {
			return ErrExists
		} else if err != nil {
			return err
		}
		if err := q.InsertRepoRedirect(ctx, dbq.InsertRepoRedirectParams{Name: cur.Name, RepoID: cur.ID, CreatedAt: time.Now().Unix()}); err != nil {
			return err
		}
		notice = append(notice, cur.Name)
		n := *cur
		n.Name = to
		next = &n
		if s.onChange != nil {
			if err := s.onChange(ctx, tx, cur, next); err != nil {
				return err
			}
		}
		return s.record(ctx, tx, audit.RepoRename, next, map[string]any{"renamed_from": cur.Name})
	})
	if err != nil {
		return nil, err
	}
	if next != cur {
		s.notify(ctx, Notice{Changed: []*Repo{next}, Redirects: notice})
	}
	return next, nil
}

// Retire gives the names of repo from to repo into, in one transaction: from takes the name
// tomb, which no repo or redirect holds, and its own name and each of its redirects become
// redirects of into, so every request that names from is served by into from then on. The
// format's OnRepoChange runs for the rename of from, from records one repo.rename event (detail
// redirect_to names into), and inTx runs last in the same transaction (the caller's rows, e.g.
// the delete operation of from). It returns from after the change. A merge retires its source
// before the source's delete.
func (s *Service) Retire(ctx context.Context, fromID, intoID int64, tomb string, inTx func(tx *sql.Tx) error) (*Repo, error) {
	if !NameRE.MatchString(tomb) {
		return nil, invalid("a name has 1 to 64 lower-case letters, digits, '.', '_' or '-', and starts with a letter or a digit")
	}
	var cur, next, into *Repo
	var notice []string
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		var err error
		if cur, err = s.counted(byID(ctx, q, fromID)); err != nil {
			return err
		}
		if into, err = byID(ctx, q, intoID); err != nil {
			return err
		}
		if cur.ID == into.ID {
			return invalid("a repository cannot retire into itself")
		}
		if _, err := s.normalizeConfig(cur.Format, tomb, cur.Config); err != nil {
			return err
		}
		if err := checkNotRedirect(ctx, q, tomb, 0); err != nil {
			return err
		}
		rds, err := q.ListRepoRedirectsOf(ctx, cur.ID)
		if err != nil {
			return err
		}
		notice = notice[:0]
		for _, rd := range rds {
			notice = append(notice, rd.Name)
		}
		if _, err := q.MoveRepoRedirects(ctx, dbq.MoveRepoRedirectsParams{ToID: into.ID, FromID: cur.ID}); err != nil {
			return err
		}
		if err := q.RenameRepo(ctx, dbq.RenameRepoParams{Name: tomb, ID: cur.ID}); isUnique(err) {
			return ErrExists
		} else if err != nil {
			return err
		}
		if err := q.InsertRepoRedirect(ctx, dbq.InsertRepoRedirectParams{Name: cur.Name, RepoID: into.ID, CreatedAt: time.Now().Unix()}); err != nil {
			return err
		}
		notice = append(notice, cur.Name)
		n := *cur
		n.Name = tomb
		next = &n
		if s.onChange != nil {
			if err := s.onChange(ctx, tx, cur, next); err != nil {
				return err
			}
		}
		if err := s.record(ctx, tx, audit.RepoRename, next, map[string]any{"renamed_from": cur.Name, "redirect_to": into.Name}); err != nil {
			return err
		}
		return inTx(tx)
	})
	if err != nil {
		return nil, err
	}
	s.notify(ctx, Notice{Changed: []*Repo{next}, Redirects: notice})
	return next, nil
}

// takenError refuses a new redirect whose name a repo or a redirect of the same repo holds. It
// matches ErrExists and answers 409.
type takenError struct{ msg string }

func (e *takenError) Error() string        { return e.msg }
func (e *takenError) Is(target error) bool { return target == ErrExists }
func (e *takenError) HTTPStatus() int      { return http.StatusConflict }

// AddRedirect makes redirect a name that answers for the repo of name (its own name or a redirect
// of it), with one repo.redirect.add event. The name follows the rule of a repo name and the
// format's; the name of a repo or of a redirect is refused (ErrExists, *RedirectedError).
func (s *Service) AddRedirect(ctx context.Context, name, redirect string) (Redirect, error) {
	if !NameRE.MatchString(redirect) {
		return Redirect{}, invalid("a name has 1 to 64 lower-case letters, digits, '.', '_' or '-', and starts with a letter or a digit")
	}
	var out Redirect
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		repo, err := byName(ctx, q, name)
		if err != nil {
			return err
		}
		if _, err := s.normalizeConfig(repo.Format, redirect, repo.Config); err != nil {
			return err
		}
		if _, err := byOwnName(ctx, q, redirect); err == nil {
			return &takenError{redirect + " is a repository"}
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		if rd, err := q.GetRepoRedirect(ctx, redirect); err == nil && rd.RepoID == repo.ID {
			return &takenError{redirect + " is already a redirect to " + repo.Name}
		} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := checkNotRedirect(ctx, q, redirect, repo.ID); err != nil {
			return err
		}
		now := time.Now().Unix()
		if err := q.InsertRepoRedirect(ctx, dbq.InsertRepoRedirectParams{Name: redirect, RepoID: repo.ID, CreatedAt: now}); isUnique(err) {
			return ErrExists
		} else if err != nil {
			return err
		}
		out = redirectOf(redirect, repo.ID, now, 0)
		return s.record(ctx, tx, audit.RedirectAdd, repo, map[string]any{"redirect": redirect})
	})
	if err != nil {
		return Redirect{}, err
	}
	s.notify(ctx, Notice{Redirects: []string{redirect}})
	return out, nil
}

// RemoveRedirect deletes the redirect redirect of the repo of name, with one repo.redirect.remove
// event: the name is free again, and requests to it answer as for any unknown repo.
func (s *Service) RemoveRedirect(ctx context.Context, name, redirect string) error {
	var repo *Repo
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		var err error
		if repo, err = byName(ctx, q, name); err != nil {
			return err
		}
		n, err := q.DeleteRepoRedirect(ctx, dbq.DeleteRepoRedirectParams{Name: redirect, RepoID: repo.ID})
		if err != nil {
			return err
		}
		if n == 0 {
			return &MissingError{Kind: "redirect", Name: redirect}
		}
		return s.record(ctx, tx, audit.RedirectRemove, repo, map[string]any{"redirect": redirect})
	})
	if err != nil {
		return err
	}
	s.notify(ctx, Notice{Redirects: []string{redirect}})
	return nil
}
