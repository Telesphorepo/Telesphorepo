// Package audit writes and lists the audit log (spec §13): who did what, to which repo, from
// which address. The audit log is the only place that shows client IP addresses.
package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"math"
	"net/http"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Telesphorepo/Telesphorepo/internal/config"
	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
)

// Actions.
const (
	SignIn           = "signin"
	SignInFailed     = "signin.failed"
	SessionCreate    = "session.create" // a publish session was minted
	SessionClose     = "session.close"
	Upload           = "upload"
	Delete           = "delete"
	RepoCreate       = "repo.create"
	RepoUpdate       = "repo.update"
	RepoDelete       = "repo.delete"
	PermissionSet    = "permission.set"
	PermissionDelete = "permission.delete"
	UserCreate       = "user.create"
	UserUpdate       = "user.update"
	SettingsUpdate   = "settings.update"
)

// Actor is who did something: a user (UserID set), a CI identity (Name is the token's
// subject) or the attempted username of a failed sign-in.
type Actor struct {
	UserID int64
	Name   string
}

// Event is one audit row. Detail must never hold a secret value.
type Event struct {
	Actor  Actor
	Action string
	RepoID int64  // 0: no repo
	Repo   string // the repo name, kept in the detail so it survives a repo delete
	Target string // a path, a prefix, a username, ...
	Detail map[string]any
}

// Entry is an audit row as the API shows it.
type Entry struct {
	ID          int64          `json:"id"`
	At          time.Time      `json:"at"`
	Actor       string         `json:"actor"`
	ActorUserID *int64         `json:"actor_user_id"`
	Action      string         `json:"action"`
	Repo        string         `json:"repo"`
	Target      string         `json:"target"`
	Detail      map[string]any `json:"detail"`
	IP          string         `json:"ip"`
}

// Service writes audit rows and deletes them after the retention.
type Service struct {
	db        *db.DB
	rq, wq    *dbq.Queries
	cfg       config.Provider
	retention func() time.Duration
	log       *slog.Logger
	wg        sync.WaitGroup
}

// New returns the audit service. retention returns the current audit.retention.
func New(d *db.DB, cfg config.Provider, retention func() time.Duration, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{db: d, rq: dbq.New(d.R), wq: dbq.New(d.W), cfg: cfg, retention: retention, log: log}
}

// clip shortens s to at most n bytes on a rune boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// Record writes e. r (may be nil) gives the client IP. A failed write is logged and does not
// fail the caller's action. Record runs detached from the request's cancellation.
func (s *Service) Record(ctx context.Context, r *http.Request, e Event) {
	if s == nil {
		return
	}
	ip := ""
	if r != nil {
		if a := httpx.ClientIP(r, s.cfg().TrustedProxies); a.IsValid() {
			ip = a.String()
		}
	}
	detail := map[string]any{}
	for k, v := range e.Detail {
		detail[k] = v
	}
	if e.Repo != "" {
		detail["repo"] = e.Repo
	}
	js, err := json.Marshal(detail)
	if err != nil {
		js = []byte("{}")
	}
	err = s.wq.InsertAuditEvent(context.WithoutCancel(ctx), dbq.InsertAuditEventParams{
		At:          time.Now().Unix(),
		ActorUserID: sql.NullInt64{Int64: e.Actor.UserID, Valid: e.Actor.UserID != 0},
		Actor:       clip(e.Actor.Name, 200),
		Action:      e.Action,
		RepoID:      sql.NullInt64{Int64: e.RepoID, Valid: e.RepoID != 0},
		Target:      clip(e.Target, 1100),
		Detail:      string(js),
		Ip:          ip,
	})
	if err != nil {
		s.log.ErrorContext(ctx, "write audit event", "action", e.Action, "err", err.Error())
	}
}

// Filter narrows List. RepoID 0 and Action "" match everything.
type Filter struct {
	Action string
	RepoID int64
}

// List returns a newest-first keyset page: ids below after (0: first page). next is the cursor
// of the next page, 0 at the end.
func (s *Service) List(ctx context.Context, f Filter, after int64, limit int) ([]Entry, int64, error) {
	before := after
	if before <= 0 {
		before = math.MaxInt64
	}
	n := int64(limit)
	repo := sql.NullInt64{Int64: f.RepoID, Valid: f.RepoID != 0}
	var rows []dbq.AuditEvent
	var err error
	switch {
	case f.RepoID != 0 && f.Action != "":
		rows, err = s.rq.ListAuditByRepoAction(ctx, dbq.ListAuditByRepoActionParams{RepoID: repo, Before: before, Action: f.Action, Limit: n})
	case f.RepoID != 0:
		rows, err = s.rq.ListAuditByRepo(ctx, dbq.ListAuditByRepoParams{RepoID: repo, Before: before, Limit: n})
	case f.Action != "":
		rows, err = s.rq.ListAuditByAction(ctx, dbq.ListAuditByActionParams{Action: f.Action, Before: before, Limit: n})
	default:
		rows, err = s.rq.ListAudit(ctx, dbq.ListAuditParams{Before: before, Limit: n})
	}
	if err != nil {
		return nil, 0, err
	}
	out := make([]Entry, 0, len(rows))
	for _, r := range rows {
		e := Entry{
			ID: r.ID, At: time.Unix(r.At, 0).UTC(), Actor: r.Actor, Action: r.Action,
			Target: r.Target, IP: r.Ip, Detail: map[string]any{},
		}
		if r.ActorUserID.Valid {
			id := r.ActorUserID.Int64
			e.ActorUserID = &id
		}
		if err := json.Unmarshal([]byte(r.Detail), &e.Detail); err != nil || e.Detail == nil {
			e.Detail = map[string]any{}
		}
		if name, ok := e.Detail["repo"].(string); ok {
			e.Repo = name
		}
		out = append(out, e)
	}
	var next int64
	if len(rows) > 0 && len(rows) == limit {
		next = rows[len(rows)-1].ID
	}
	return out, next, nil
}

const (
	cleanupEvery = time.Hour
	cleanupBatch = 1000
)

// Start runs the retention cleanup at once and then every hour, until ctx ends.
func (s *Service) Start(ctx context.Context) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		t := time.NewTicker(cleanupEvery)
		defer t.Stop()
		for {
			if err := s.cleanup(ctx); err != nil && ctx.Err() == nil {
				s.log.Error("audit log cleanup", "err", err.Error())
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

// Wait waits for the cleanup job to stop.
func (s *Service) Wait() { s.wg.Wait() }

// cleanup deletes the events older than the retention in batches.
func (s *Service) cleanup(ctx context.Context) error {
	cut := time.Now().Add(-s.retention()).Unix()
	for ctx.Err() == nil {
		n, err := s.wq.DeleteOldAudit(ctx, dbq.DeleteOldAuditParams{Before: cut, Limit: cleanupBatch})
		if err != nil {
			return err
		}
		if n < cleanupBatch {
			return nil
		}
	}
	return ctx.Err()
}
