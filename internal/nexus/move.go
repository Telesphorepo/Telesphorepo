package nexus

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/nexus/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// FieldMovesInto is the repo field of a moving repo: the name of the repo that a Nexus source
// copies into and that it merges into when the last project has moved. Only Move sets it, when it
// creates the repo.
const FieldMovesInto = "moves_into"

// groupSuffix ends the name of the group that reads a moving repo and the repo it moves into.
const groupSuffix = "-all"

// moveKey is the context key of the id of the repo that a create moves into: the field
// moves_into takes a value only under it.
type moveKey struct{}

// MoveInput is the body of POST /api/nexus/sources/{id}/repos/{repo}/move: the name of the new
// repo, and whether a group <target>-all reads the new repo first, then the target.
type MoveInput struct {
	Name  string `json:"name"`
	Group bool   `json:"group"`
}

// Moved is what Move created: the new repo and the group ("" for none).
type Moved struct {
	Name  string `json:"name"`
	Group string `json:"group"`
}

// loadMoves reads every moving repo into memory. It runs at Init.
func (s *Service) loadMoves(ctx context.Context) error {
	rows, err := dbq.New(s.db.R).ListRepoMoves(ctx)
	if err != nil {
		return fmt.Errorf("nexus: load moving repos: %w", err)
	}
	s.rehMu.Lock()
	defer s.rehMu.Unlock()
	for _, r := range rows {
		s.moves[r.RepoID] = r.IntoRepoID
	}
	return nil
}

// movesInto returns the id of the repo that repo id moves into, 0 for none.
func (s *Service) movesInto(id int64) int64 {
	s.rehMu.RLock()
	defer s.rehMu.RUnlock()
	return s.moves[id]
}

// movingInto returns the names of the moving repos of repo id, by creation.
func (s *Service) movingInto(ctx context.Context, id int64) ([]string, error) {
	ids, err := dbq.New(s.db.R).ListMovesInto(ctx, id)
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

// moveField is the repo field moves_into of the hosted repos. Move sets it when it creates the
// repo; a create or an update through the API cannot.
func (s *Service) moveField() repos.Field {
	return repos.Field{
		Name:    FieldMovesInto,
		Applies: func(_, kind string) bool { return kind == repos.KindHosted },
		Validate: func(ctx context.Context, repo *repos.Repo, raw json.RawMessage) (json.RawMessage, error) {
			if id, _ := ctx.Value(moveKey{}).(int64); id == 0 || repo.ID != 0 {
				return nil, errors.New("is set by Move it project by project on the page of a Nexus server")
			}
			var name string
			if err := json.Unmarshal(raw, &name); err != nil {
				return nil, errors.New("must be the name of a repository")
			}
			return json.Marshal(name)
		},
		Save: func(ctx context.Context, tx *sql.Tx, repo *repos.Repo, _ json.RawMessage) error {
			id, _ := ctx.Value(moveKey{}).(int64)
			return dbq.New(tx).InsertRepoMove(ctx, dbq.InsertRepoMoveParams{RepoID: repo.ID, IntoRepoID: id, CreatedAt: time.Now().Unix()})
		},
		Load: func(repoID int64) json.RawMessage {
			id := s.movesInto(repoID)
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

// Move creates the repo that projects publish to while they move from the hosted repo that Nexus
// repo sourceRepo of a source copies into (the target), one project at a time: a hosted repo
// named in.Name of the target's format with its config, field values and visibility, the repo
// field moves_into, and download and publish for each person who can publish to the target. With
// in.Group it also creates the group <target>-all of the new repo first, then the target, with the
// target's visibility and download for each person with a grant on the target, so builds read
// both while projects move. A taken name is a *TakenError; nothing is created then. userID 0
// means unknown.
func (s *Service) Move(ctx context.Context, sourceID int64, sourceRepo string, in MoveInput, userID int64) (*Moved, error) {
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
	if !repos.NameRE.MatchString(in.Name) {
		return nil, fmt.Errorf("%w: a name has 1 to 64 lower-case letters, digits, '.', '_' or '-', and starts with a letter or a digit", ErrInvalid)
	}
	group := ""
	if in.Group {
		if f := s.formats.Get(target.Format); f == nil || !f.Takes(repos.KindGroup) {
			title := target.Format
			if f != nil {
				title = f.Title
			}
			return nil, fmt.Errorf("%w: %s repositories have no groups, so a move of %s cannot create the group %s", ErrInvalid, title, target.Name, target.Name+groupSuffix)
		}
		group = target.Name + groupSuffix
		if !repos.NameRE.MatchString(group) {
			return nil, fmt.Errorf("%w: %s is too long a name for a group", ErrInvalid, group)
		}
	}
	for _, name := range []string{in.Name, group} {
		if name == "" {
			continue
		}
		if _, err := s.repos.ByName(ctx, name); err == nil {
			return nil, &TakenError{Name: name, Move: true}
		} else if !errors.Is(err, repos.ErrNotFound) {
			return nil, err
		}
	}
	fields := maps.Clone(target.Fields)
	if fields == nil {
		fields = map[string]json.RawMessage{}
	}
	delete(fields, FieldRehearsalFor)
	fields[FieldMovesInto], _ = json.Marshal(target.Name)
	r, err := s.repos.Create(context.WithValue(ctx, moveKey{}, target.ID), repos.Create{Name: in.Name, Format: target.Format,
		AnonymousRead: target.AnonymousRead, Config: target.Config, Fields: fields,
		Description: "Projects publish here while they move from " + target.Name + ", one at a time. Merge it into " + target.Name + " when the last one has moved."})
	if errors.Is(err, repos.ErrExists) {
		return nil, &TakenError{Name: in.Name, Move: true}
	}
	if err != nil {
		return nil, err
	}
	s.rehMu.Lock()
	s.moves[r.ID] = target.ID
	s.rehMu.Unlock()

	grants, err := s.auth.ListPermissions(ctx, target.ID)
	if err != nil {
		return nil, err
	}
	var publishers, readers []string
	for _, g := range grants {
		readers = append(readers, g.Username)
		for _, p := range g.Perms {
			if perm.ID(p) == perm.RepoPublish {
				publishers = append(publishers, g.Username)
				break
			}
		}
	}
	if len(publishers) > 0 {
		if _, err := s.auth.SetPermissions(ctx, nil, publishers, []string{in.Name}, []string{string(perm.RepoDownload), string(perm.RepoPublish)}); err != nil {
			return nil, fmt.Errorf("give publish on %s: %w", in.Name, err)
		}
	}
	if group != "" {
		g, err := s.repos.Create(ctx, repos.Create{Name: group, Format: target.Format, Kind: repos.KindGroup,
			AnonymousRead: target.AnonymousRead, Members: []string{in.Name, target.Name},
			Description: "Reads " + in.Name + " first, then " + target.Name + ", while projects move."})
		if errors.Is(err, repos.ErrExists) {
			return nil, &TakenError{Name: group, Move: true}
		}
		if err != nil {
			return nil, fmt.Errorf("create the group %s: %w", group, err)
		}
		if len(readers) > 0 {
			if _, err := s.auth.SetPermissions(ctx, nil, readers, []string{g.Name}, []string{string(perm.RepoDownload)}); err != nil {
				return nil, fmt.Errorf("give download on %s: %w", g.Name, err)
			}
		}
	}
	s.log.Info("nexus: moving repo created", "repo", in.Name, "into", target.Name, "group", group, "people", len(publishers))
	s.record(ctx, ActionMoveCreate, in.Name, map[string]any{"into": target.Name, "group": group, "people": publishers})
	return &Moved{Name: in.Name, Group: group}, nil
}
