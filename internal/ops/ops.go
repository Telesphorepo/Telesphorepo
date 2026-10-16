// Package ops keeps the operations on repos (repo_ops): a long task that owns a repo for a
// while, such as a repo delete or a Nexus mirror. A repo has at most one operation. While it
// has one, the access engine refuses writes, deletes and mints of the repo with 409 and the
// operation's message. An ended operation's row is deleted.
package ops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
)

// States of an operation.
const (
	Running = "running" // the server works on it
	Held    = "held"    // another system owns the repo's content (e.g. a Nexus source mirrors it)
	Failed  = "failed"  // it stopped with an error; it can be resumed or cleared
)

var (
	// ErrBusy is a repo that has an operation already.
	ErrBusy = errors.New("the repository has an operation already")
	// ErrNone is a repo without an operation.
	ErrNone = errors.New("the repository has no operation")
	// ErrNotOwner is an end of an operation by a module that does not own it.
	ErrNotOwner = errors.New("the operation belongs to another module")
)

// Op is an operation on a repo.
type Op struct {
	ID        int64
	RepoID    int64
	Type      string
	Owner     string // the module that began it
	Ref       string // the owner's reference, e.g. the Nexus source ID
	State     string
	Done      int64
	Total     int64
	Error     string
	StartedBy int64 // 0: none
	StartedAt time.Time
	UpdatedAt time.Time
}

// Type is a kind of operation that a module registers.
type Type struct {
	ID    string // e.g. "delete", "mirror"
	Label string
	// Message is the 409 text of a refused write and the line the UI shows, made from the state.
	Message func(op *Op) string
	// Resume continues a failed operation; nil: it cannot be resumed.
	Resume func(ctx context.Context, op *Op) error
	// Clear ends an operation without finishing it; nil: it cannot be cleared here.
	Clear func(ctx context.Context, op *Op) error
}

// Service keeps the operation types and the repo_ops rows.
type Service struct {
	db    *db.DB
	types map[string]*Type
}

// New returns the service.
func New(d *db.DB) *Service { return &Service{db: d, types: map[string]*Type{}} }

// Register adds an operation type. It runs before the server serves and panics on a repeated ID.
func (s *Service) Register(t Type) {
	if t.ID == "" || t.Message == nil {
		panic("ops: a type needs an ID and a Message")
	}
	if s.types[t.ID] != nil {
		panic(fmt.Sprintf("ops: type %q registered twice", t.ID))
	}
	s.types[t.ID] = &t
}

// Type returns the operation type id, or nil.
func (s *Service) Type(id string) *Type { return s.types[id] }

// Types returns the registered types in ID order.
func (s *Service) Types() []*Type {
	out := make([]*Type, 0, len(s.types))
	for _, t := range s.types {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Message is the text of op: the 409 answer of a refused write and the UI line.
func (s *Service) Message(op *Op) string {
	if t := s.types[op.Type]; t != nil {
		return t.Message(op)
	}
	return "the repository has an operation (" + op.Type + ")"
}

// FromRow builds an Op from the columns of a repo row joined with repo_ops (nil when the repo
// has none).
func FromRow(id sql.NullInt64, repoID int64, typ, owner, ref, state sql.NullString, done, total sql.NullInt64,
	errText sql.NullString, startedBy, startedAt, updatedAt sql.NullInt64) *Op {
	if !id.Valid {
		return nil
	}
	return &Op{ID: id.Int64, RepoID: repoID, Type: typ.String, Owner: owner.String, Ref: ref.String, State: state.String,
		Done: done.Int64, Total: total.Int64, Error: errText.String, StartedBy: startedBy.Int64,
		StartedAt: time.Unix(startedAt.Int64, 0).UTC(), UpdatedAt: time.Unix(updatedAt.Int64, 0).UTC()}
}

func fromRow(r dbq.RepoOp) *Op {
	return &Op{ID: r.ID, RepoID: r.RepoID, Type: r.Type, Owner: r.Owner, Ref: r.Ref, State: r.State, Done: r.Done,
		Total: r.Total, Error: r.Error, StartedBy: r.StartedBy.Int64, StartedAt: time.Unix(r.StartedAt, 0).UTC(),
		UpdatedAt: time.Unix(r.UpdatedAt, 0).UTC()}
}

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// Begin begins an operation of type typ on repo repoID in tx, owned by module owner, in state
// (Running or Held). It returns ErrBusy when the repo has an operation already.
func (s *Service) Begin(ctx context.Context, tx *sql.Tx, repoID int64, typ, owner, ref, state string, startedBy int64) (*Op, error) {
	if s.types[typ] == nil {
		return nil, fmt.Errorf("ops: unknown type %q", typ)
	}
	now := time.Now().Unix()
	id, err := dbq.New(tx).InsertRepoOp(ctx, dbq.InsertRepoOpParams{RepoID: repoID, Type: typ, Owner: owner, Ref: ref,
		State: state, StartedBy: sql.NullInt64{Int64: startedBy, Valid: startedBy != 0}, StartedAt: now, UpdatedAt: now})
	if isUnique(err) {
		return nil, ErrBusy
	}
	if err != nil {
		return nil, err
	}
	return &Op{ID: id, RepoID: repoID, Type: typ, Owner: owner, Ref: ref, State: state, StartedBy: startedBy,
		StartedAt: time.Unix(now, 0).UTC(), UpdatedAt: time.Unix(now, 0).UTC()}, nil
}

// Get returns the operation of repo repoID, or ErrNone. A non-nil tx reads in that transaction.
func (s *Service) Get(ctx context.Context, tx *sql.Tx, repoID int64) (*Op, error) {
	q := dbq.New(s.db.R)
	if tx != nil {
		q = dbq.New(tx)
	}
	r, err := q.GetRepoOp(ctx, repoID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNone
	}
	if err != nil {
		return nil, err
	}
	return fromRow(r), nil
}

// Progress saves how much of operation id is done.
func (s *Service) Progress(ctx context.Context, id, done, total int64) error {
	return dbq.New(s.db.W).SetRepoOpProgress(ctx, dbq.SetRepoOpProgressParams{Done: done, Total: total, UpdatedAt: time.Now().Unix(), ID: id})
}

// SetState sets the state of operation id, with the error text of a failure.
func (s *Service) SetState(ctx context.Context, id int64, state string, cause error) error {
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	return dbq.New(s.db.W).SetRepoOpState(ctx, dbq.SetRepoOpStateParams{State: state, Error: msg, UpdatedAt: time.Now().Unix(), ID: id})
}

// Fail sets operation id to failed with err.
func (s *Service) Fail(ctx context.Context, id int64, err error) error {
	return s.SetState(ctx, id, Failed, err)
}

// End ends operation id of owner: its row is deleted. With a non-nil tx it writes in that
// transaction. ErrNotOwner when another module owns it (or it ended already).
func (s *Service) End(ctx context.Context, tx *sql.Tx, id int64, owner string) error {
	q := dbq.New(s.db.W)
	if tx != nil {
		q = dbq.New(tx)
	}
	n, err := q.DeleteRepoOp(ctx, dbq.DeleteRepoOpParams{ID: id, Owner: owner})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotOwner
	}
	return nil
}

// Running returns up to limit running operations of type typ.
func (s *Service) Running(ctx context.Context, typ string, limit int) ([]*Op, error) {
	rows, err := dbq.New(s.db.R).ListRunningRepoOps(ctx, dbq.ListRunningRepoOpsParams{Type: typ, Limit: int64(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]*Op, len(rows))
	for i, r := range rows {
		out[i] = fromRow(r)
	}
	return out, nil
}

// Deleting reports whether op is an active repo delete: the asset pipeline then calls no format
// hook and the rebuild queue skips the repo.
func Deleting(op *Op) bool { return op != nil && op.Type == TypeDelete }

// TypeDelete is the operation type of a repo delete (registered by the core module).
const TypeDelete = "delete"
