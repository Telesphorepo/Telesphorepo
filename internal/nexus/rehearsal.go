package nexus

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/nexus/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/ops"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// FieldRehearsalFor is the repo field of a rehearsal repo: the name of the repo whose publishing
// it rehearses. Only Rehearse sets it, when it creates the repo.
const FieldRehearsalFor = "rehearsal_for"

// OpEmpty is the repo operation that deletes every file of a rehearsal repo and keeps the repo.
const OpEmpty = "empty"

// rehearsalSuffix ends the name of the rehearsal repo of a repo.
const rehearsalSuffix = "-rehearsal"

// resumeEmpties is the most running empty operations that a start resumes.
const resumeEmpties = 100

// ErrBusy is a repo that has an operation already.
var ErrBusy = errors.New("the repository is busy")

// TakenError is a rehearsal or a moving repo (Move) whose name is taken. Rehearsal is true when
// that repo is the rehearsal of the same repo already.
type TakenError struct {
	Name      string
	Rehearsal bool
	Move      bool
}

func (e *TakenError) Error() string {
	switch {
	case e.Rehearsal:
		return "The rehearsal repository " + e.Name + " exists already."
	case e.Move:
		return "A repository named " + e.Name + " exists already. Choose another name."
	}
	return "A repository named " + e.Name + " exists already, and it rehearses nothing. Rename or delete it first."
}

// rehearsalKey is the context key of the id of the repo that a create rehearses: the field
// rehearsal_for takes a value only under it.
type rehearsalKey struct{}

// loadRehearsals reads every rehearsal repo into memory. It runs at Init.
func (s *Service) loadRehearsals(ctx context.Context) error {
	rows, err := dbq.New(s.db.R).ListRepoRehearsals(ctx)
	if err != nil {
		return fmt.Errorf("nexus: load rehearsal repos: %w", err)
	}
	s.rehMu.Lock()
	defer s.rehMu.Unlock()
	for _, r := range rows {
		s.reh[r.RepoID] = r.ForRepoID
	}
	return nil
}

// rehearsalFor returns the id of the repo that repo id rehearses, 0 for none.
func (s *Service) rehearsalFor(id int64) int64 {
	s.rehMu.RLock()
	defer s.rehMu.RUnlock()
	return s.reh[id]
}

// forgetRepo drops a deleted repo from memory: as a rehearsal or a moving repo, and as the repo
// of rehearsals or moving repos (their rows cascade with it).
func (s *Service) forgetRepo(id int64) {
	s.rehMu.Lock()
	defer s.rehMu.Unlock()
	for _, m := range []map[int64]int64{s.reh, s.moves} {
		delete(m, id)
		for r, f := range m {
			if f == id {
				delete(m, r)
			}
		}
	}
}

// rehearsalsOf returns the names of the rehearsal repos of repo id, by creation.
func (s *Service) rehearsalsOf(ctx context.Context, id int64) ([]string, error) {
	ids, err := dbq.New(s.db.R).ListRehearsalsFor(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ids))
	for _, rid := range ids {
		r, err := s.repos.ByID(ctx, rid)
		if errors.Is(err, repos.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, r.Name)
	}
	return out, nil
}

// rehearsalField is the repo field rehearsal_for of the hosted repos: the name of the repo that
// a rehearsal repo rehearses. Rehearse sets it when it creates the repo; a create or an update
// through the API cannot.
func (s *Service) rehearsalField() repos.Field {
	return repos.Field{
		Name:    FieldRehearsalFor,
		Applies: func(_, kind string) bool { return kind == repos.KindHosted },
		Validate: func(ctx context.Context, repo *repos.Repo, raw json.RawMessage) (json.RawMessage, error) {
			if id, _ := ctx.Value(rehearsalKey{}).(int64); id == 0 || repo.ID != 0 {
				return nil, errors.New("is set by Rehearse publishing on the page of a Nexus server")
			}
			var name string
			if err := json.Unmarshal(raw, &name); err != nil {
				return nil, errors.New("must be the name of a repository")
			}
			return json.Marshal(name)
		},
		Save: func(ctx context.Context, tx *sql.Tx, repo *repos.Repo, _ json.RawMessage) error {
			id, _ := ctx.Value(rehearsalKey{}).(int64)
			return dbq.New(tx).InsertRepoRehearsal(ctx, dbq.InsertRepoRehearsalParams{RepoID: repo.ID, ForRepoID: id, CreatedAt: time.Now().Unix()})
		},
		Load: func(repoID int64) json.RawMessage {
			id := s.rehearsalFor(repoID)
			if id == 0 {
				return nil
			}
			r, err := s.repos.ByID(context.Background(), id)
			if err != nil {
				return nil
			}
			raw, _ := json.Marshal(r.Name)
			return raw
		},
	}
}

// Rehearse creates the rehearsal repo of the hosted repo that Nexus repo sourceRepo of a source
// copies into: <name>-rehearsal, a private hosted repo of the same format with the same format
// settings and field values, where the people who can publish to the repo can download and
// publish. A taken name is a *TakenError. userID 0 means unknown.
func (s *Service) Rehearse(ctx context.Context, sourceID int64, sourceRepo string, userID int64) (*repos.Repo, error) {
	m, err := dbq.New(s.db.R).GetNexusSourceRepo(ctx, dbq.GetNexusSourceRepoParams{SourceID: sourceID, SourceRepo: sourceRepo})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if m.Kind != TypeHosted {
		return nil, fmt.Errorf("%w: only a repository takes publishes; a %s does not", ErrInvalid, m.Kind)
	}
	target, err := s.repos.ByID(ctx, m.TargetRepoID)
	if err != nil {
		return nil, err
	}
	name := target.Name + rehearsalSuffix
	if !repos.NameRE.MatchString(name) {
		return nil, fmt.Errorf("%w: %s is too long a name for a repository", ErrInvalid, name)
	}
	if r, err := s.repos.ByName(ctx, name); err == nil {
		return nil, &TakenError{Name: name, Rehearsal: s.rehearsalFor(r.ID) == target.ID}
	} else if !errors.Is(err, repos.ErrNotFound) {
		return nil, err
	}
	fields := maps.Clone(target.Fields)
	if fields == nil {
		fields = map[string]json.RawMessage{}
	}
	fields[FieldRehearsalFor], _ = json.Marshal(target.Name)
	r, err := s.repos.Create(context.WithValue(ctx, rehearsalKey{}, target.ID), repos.Create{Name: name, Format: target.Format,
		Description: "Publish here first, to try the builds before the switch-over.", Config: target.Config, Fields: fields})
	if errors.Is(err, repos.ErrExists) {
		return nil, &TakenError{Name: name}
	}
	if err != nil {
		return nil, err
	}
	s.rehMu.Lock()
	s.reh[r.ID] = target.ID
	s.rehMu.Unlock()

	// The people who can publish to the repo can download and publish here.
	grants, err := s.auth.ListPermissions(ctx, target.ID)
	if err != nil {
		return nil, err
	}
	var people []string
	for _, g := range grants {
		for _, p := range g.Perms {
			if perm.ID(p) == perm.RepoPublish {
				people = append(people, g.Username)
				break
			}
		}
	}
	if len(people) > 0 {
		if _, err := s.auth.SetPermissions(ctx, nil, people, []string{name}, []string{string(perm.RepoDownload), string(perm.RepoPublish)}); err != nil {
			return nil, fmt.Errorf("give publish on %s: %w", name, err)
		}
	}
	s.log.Info("nexus: rehearsal repo created", "repo", name, "for", target.Name, "people", len(people))
	s.record(ctx, ActionRehearsalCreate, name, map[string]any{"for": target.Name, "people": people})
	return s.repos.ByName(ctx, name)
}

// Empty starts to delete every file of rehearsal repo name under its empty operation; the repo
// keeps its settings, its CI pipelines and who can do what. It returns the repo as it was. A repo
// that rehearses nothing is ErrInvalid, a repo with an operation ErrBusy. userID 0 means unknown.
func (s *Service) Empty(ctx context.Context, name string, userID int64) (*repos.Repo, error) {
	r, err := s.repos.ByName(ctx, name)
	if errors.Is(err, repos.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if s.rehearsalFor(r.ID) == 0 {
		return nil, fmt.Errorf("%w: %s is not a rehearsal repository", ErrInvalid, name)
	}
	if r.Op != nil {
		return nil, fmt.Errorf("%w: You cannot empty %s now. %s", ErrBusy, name, s.ops.Message(r.Op))
	}
	var op *ops.Op
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		op, err = s.ops.Begin(ctx, tx, r.ID, OpEmpty, "nexus", "", ops.Running, userID)
		return err
	})
	if errors.Is(err, ops.ErrBusy) {
		return nil, fmt.Errorf("%w: You cannot empty %s now: %v", ErrBusy, name, err)
	}
	if err != nil {
		return nil, err
	}
	s.startEmpty(r, op)
	return r, nil
}

// startEmpty runs the empty operation op of repo as a task of the module.
func (s *Service) startEmpty(repo *repos.Repo, op *ops.Op) {
	s.spawn("empty", func(ctx context.Context) { s.runEmpty(ctx, repo, op) })
}

// runEmpty deletes every file of repo through the byte pipeline (the format's hooks run as for a
// delete of folders), counts the progress on op, then ends op and records the event. A failure
// sets op to failed: it can be resumed or cleared. When the server stops first, op stays
// running and the next start resumes it.
func (s *Service) runEmpty(ctx context.Context, repo *repos.Repo, op *ops.Op) {
	work := context.WithoutCancel(ctx)
	total := repo.FileCount
	_ = s.ops.Progress(work, op.ID, 0, total)
	n, err := s.assets.Remove(ctx, assets.Removal{Repo: repo, Prefixes: []repopath.Path{""},
		Origin: assets.Origin{Source: assets.SourceUI, UserID: op.StartedBy},
		Progress: func(done int) {
			_ = s.ops.Progress(work, op.ID, int64(done), max(total, int64(done)))
		}})
	if err != nil {
		if ctx.Err() != nil {
			s.log.Info("nexus: empty of a rehearsal repo stopped with the server; it resumes at the next start", "repo", repo.Name, "files", n)
			return
		}
		s.log.Error("nexus: empty a rehearsal repo", "repo", repo.Name, "error", err.Error())
		_ = s.ops.Fail(work, op.ID, err)
		return
	}
	if err := s.ops.End(work, nil, op.ID, "nexus"); err != nil {
		s.log.Error("nexus: end the empty of a rehearsal repo", "repo", repo.Name, "error", err.Error())
		return
	}
	s.log.Info("nexus: rehearsal repo emptied", "repo", repo.Name, "files", n)
	_ = s.audit.Record(work, audit.Event{Action: ActionRehearsalEmpty, RepoID: repo.ID, Repo: repo.Name, Target: repo.Name,
		Detail: map[string]any{"count": n}})
}

// resumeEmpty sets a failed empty operation running again (ops.Type Resume).
func (s *Service) resumeEmpty(ctx context.Context, op *ops.Op) error {
	repo, err := s.repos.ByID(ctx, op.RepoID)
	if err != nil {
		return err
	}
	if err := s.ops.SetState(ctx, op.ID, ops.Running, nil); err != nil {
		return err
	}
	s.startEmpty(repo, op)
	return nil
}

// clearEmpty ends a failed empty operation: the repo keeps the files that are left (ops.Type
// Clear).
func (s *Service) clearEmpty(ctx context.Context, op *ops.Op) error {
	return s.ops.End(ctx, nil, op.ID, "nexus")
}

// resumeEmpties continues the empty operations that a stop interrupted.
func (s *Service) resumeEmpties(ctx context.Context) error {
	running, err := s.ops.Running(ctx, OpEmpty, resumeEmpties)
	if err != nil {
		return fmt.Errorf("nexus: look up running empty operations: %w", err)
	}
	for _, op := range running {
		repo, err := s.repos.ByID(ctx, op.RepoID)
		if err != nil {
			return err
		}
		s.startEmpty(repo, op)
	}
	return nil
}

// emptyMessage is the text of an empty operation, in full sentences.
func emptyMessage(op *ops.Op) string {
	if op.State == ops.Failed {
		return "Emptying the repository failed (" + op.Error + "). Resume it, or stop it and keep the files, on the repository's page."
	}
	return "The rehearsal repository is being emptied."
}

// switchOvers returns the switch-overs of source id by Nexus repo name.
func (s *Service) switchOvers(ctx context.Context, id int64) (map[string]*SwitchOver, error) {
	rows, err := dbq.New(s.db.R).ListNexusSwitchOvers(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make(map[string]*SwitchOver, len(rows))
	for _, r := range rows {
		out[r.SourceRepo] = &SwitchOver{SyncID: r.SyncID, Failed: r.Failed, SwitchedAt: time.Unix(r.SwitchedAt, 0).UTC()}
	}
	return out, nil
}

// switchedMappings returns the mapping rows of the repos of source id that switched over, by
// Nexus repo name.
func (s *Service) switchedMappings(ctx context.Context, id int64) (map[string]dbq.NexusSourceRepo, error) {
	q := dbq.New(s.db.R)
	rows, err := q.ListNexusSwitchOvers(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make(map[string]dbq.NexusSourceRepo, len(rows))
	for _, r := range rows {
		m, err := q.GetNexusSourceRepo(ctx, dbq.GetNexusSourceRepoParams{SourceID: id, SourceRepo: r.SourceRepo})
		if err != nil {
			return nil, err
		}
		out[r.SourceRepo] = m
	}
	return out, nil
}
