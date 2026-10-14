// Package move implements the storage move (spec §11): when an admin points the server at a
// new blob store while assets exist, the server copies every blob to the new store in the
// background, with no downtime. While the copier runs, the live store is a storage.Mirror that
// reads from the source and writes to both stores. The source is never modified or deleted.
package move

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/config"
	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
	"github.com/Telesphorepo/Telesphorepo/internal/storage"
)

var (
	// ErrNotFound means no move has the given id (or no move exists yet).
	ErrNotFound = errors.New("storage move not found")
	// ErrBusy means another move is copying; only one runs at a time.
	ErrBusy = errors.New("a storage move is already running")
	// ErrNotRunning means the move has already ended.
	ErrNotRunning = errors.New("the storage move is not running")
)

// Move states.
const (
	StateCopying   = "copying"
	StateDone      = "done"
	StateFailed    = "failed"
	StateCancelled = "cancelled"
)

// Move is a storage move as the API shows it. It never holds secrets.
type Move struct {
	ID         int64      `json:"id"`
	State      string     `json:"state"`
	Total      int64      `json:"total"`
	Copied     int64      `json:"copied"`
	Skipped    int64      `json:"skipped"`
	Failed     int64      `json:"failed"`
	Error      string     `json:"error"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	Target     Target     `json:"target"`
}

// Target describes where a move copies to, without credentials.
type Target struct {
	Storage  string `json:"storage"`
	Endpoint string `json:"endpoint,omitempty"`
	Bucket   string `json:"bucket,omitempty"`
	Prefix   string `json:"prefix,omitempty"`
	FSRoot   string `json:"fs_root,omitempty"`
}

// Failure is one asset the copier could not copy.
type Failure struct {
	AssetID int64  `json:"asset_id"`
	Key     string `json:"key"`
	Reason  string `json:"reason"`
}

// stored is the target storage configuration sealed in storage_moves.target. It holds the S3
// secret, so it is only ever stored encrypted and never logged.
type stored struct {
	Storage           string `json:"storage"`
	FSRoot            string `json:"fs_root"`
	S3Endpoint        string `json:"s3_endpoint"`
	S3Region          string `json:"s3_region"`
	S3Bucket          string `json:"s3_bucket"`
	S3AccessKeyID     string `json:"s3_access_key_id"`
	S3SecretAccessKey string `json:"s3_secret_access_key"`
	S3Prefix          string `json:"s3_prefix"`
	S3PathStyle       bool   `json:"s3_path_style"`
	S3Redirect        bool   `json:"s3_redirect"`
}

func toStored(c *config.Config) stored {
	return stored{
		Storage: c.Storage, FSRoot: c.FSRoot,
		S3Endpoint: c.S3Endpoint, S3Region: c.S3Region, S3Bucket: c.S3Bucket,
		S3AccessKeyID: c.S3AccessKeyID, S3SecretAccessKey: c.S3SecretAccessKey, S3Prefix: c.S3Prefix,
		S3PathStyle: c.S3PathStyle, S3Redirect: c.S3Redirect,
	}
}

// apply returns a copy of base with the storage fields of st.
func (st stored) apply(base *config.Config) *config.Config {
	c := *base
	c.Storage, c.FSRoot = st.Storage, st.FSRoot
	c.S3Endpoint, c.S3Region, c.S3Bucket = st.S3Endpoint, st.S3Region, st.S3Bucket
	c.S3AccessKeyID, c.S3SecretAccessKey, c.S3Prefix = st.S3AccessKeyID, st.S3SecretAccessKey, st.S3Prefix
	c.S3PathStyle, c.S3Redirect = st.S3PathStyle, st.S3Redirect
	return &c
}

func (st stored) target() Target {
	if st.Storage == "s3" {
		return Target{Storage: st.Storage, Endpoint: st.S3Endpoint, Bucket: st.S3Bucket, Prefix: st.S3Prefix}
	}
	return Target{Storage: st.Storage, FSRoot: st.FSRoot}
}

// SameLocation reports whether a and b address the same blobs: the same directory, or the same
// bucket and prefix at the same endpoint. Credentials, region, path style and redirects do not
// change where blobs live, so a change of only those needs no move.
func SameLocation(a, b *config.Config) bool {
	if a.Storage != b.Storage {
		return false
	}
	if a.Storage != "s3" {
		return filepath.Clean(a.FSRoot) == filepath.Clean(b.FSRoot)
	}
	return a.S3Endpoint == b.S3Endpoint && a.S3Bucket == b.S3Bucket && a.S3Prefix == b.S3Prefix
}

// Service starts, runs, resumes and cancels storage moves.
type Service struct {
	db     *db.DB
	set    *settings.Service
	live   *storage.Live
	assets *assets.Service
	repos  *repos.Service
	log    *slog.Logger

	mu   sync.Mutex // serializes start, finish, cancel and resume
	base context.Context
	run  *run           // the copying move of this process, nil when none
	wg   sync.WaitGroup // the copier goroutines of this process
}

// Wait blocks until every copier of this process has returned. The server calls it after the
// Resume context ends and before it closes the database.
func (s *Service) Wait() { s.wg.Wait() }

// run is a move whose copier runs in this process.
type run struct {
	id        int64
	startedBy int64
	target    *config.Config
	mirror    *storage.Mirror
	cancel    context.CancelFunc
}

// New returns the move service. live is the server's store; the service swaps a Mirror into it
// while a move runs.
func New(d *db.DB, set *settings.Service, live *storage.Live, as *assets.Service, rs *repos.Service, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{db: d, set: set, live: live, assets: as, repos: rs, log: log, base: context.Background()}
}

// Running reports whether a move is copying in this process.
func (s *Service) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.run != nil
}

// HasAssets reports whether any asset exists (one index probe). Without assets a storage
// change needs no move.
func (s *Service) HasAssets(ctx context.Context) (bool, error) {
	_, err := dbq.New(s.db.R).AnyAsset(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// Start begins a move of every blob to target, the full effective configuration the storage
// settings change would produce (the caller has probed it). It saves no settings: the target
// is sealed in the move row, and the settings change when the copy is done. ErrBusy when a move
// is already copying.
func (s *Service) Start(ctx context.Context, target *config.Config, startedBy int64) (*Move, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run != nil {
		return nil, ErrBusy
	}
	dst, err := storage.Open(ctx, target)
	if err != nil {
		return nil, fmt.Errorf("open target storage: %w", err)
	}
	total, err := dbq.New(s.db.R).CountAssets(ctx)
	if err != nil {
		return nil, err
	}
	plain, err := json.Marshal(toStored(target))
	if err != nil {
		return nil, err
	}
	var by sql.NullInt64
	if startedBy > 0 {
		by = sql.NullInt64{Int64: startedBy, Valid: true}
	}
	var id int64
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		var err error
		id, err = q.CreateMove(ctx, dbq.CreateMoveParams{Total: total, StartedBy: by, StartedAt: time.Now().Unix()})
		if err != nil {
			return err
		}
		return q.SetMoveTarget(ctx, dbq.SetMoveTargetParams{Target: s.set.Encrypt(aad(id), plain), ID: id})
	})
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return nil, ErrBusy
		}
		return nil, fmt.Errorf("create storage move: %w", err)
	}
	r := &run{id: id, startedBy: startedBy, target: target, mirror: storage.NewMirror(s.live.Active(), dst)}
	s.live.Swap(r.mirror)
	s.launch(r, 0, 0, 0, 0)
	s.log.Info("storage move started", "move", id, "storage", target.Storage, "assets", total)
	return s.Get(ctx, id)
}

// Resume runs at startup, before the server accepts requests. ctx bounds every copier of this
// process. A move left copying continues from its cursor; if its target cannot be opened, the
// move fails with that error.
func (s *Service) Resume(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.base = ctx
	row, err := dbq.New(s.db.R).GetCopyingMove(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("find running storage move: %w", err)
	}
	st, err := s.open(row)
	if err != nil {
		return s.endRow(ctx, row.ID, StateFailed, "read move target: "+err.Error())
	}
	cur := s.set.Config()
	target := st.apply(cur)
	if SameLocation(cur, target) {
		// The process stopped after the settings were saved and before the move was marked
		// done: the server already runs on the target.
		s.log.Info("storage move finished before the restart", "move", row.ID)
		return s.endRow(ctx, row.ID, StateDone, "")
	}
	dst, err := storage.Open(ctx, target)
	if err == nil {
		err = storage.Probe(ctx, dst)
	}
	if err != nil {
		s.log.Error("storage move target unusable; the move failed", "move", row.ID, "error", err.Error())
		return s.endRow(ctx, row.ID, StateFailed, "target storage: "+err.Error())
	}
	var by int64
	if row.StartedBy.Valid {
		by = row.StartedBy.Int64
	}
	r := &run{id: row.ID, startedBy: by, target: target, mirror: storage.NewMirror(s.live.Active(), dst)}
	s.live.Swap(r.mirror)
	s.launch(r, row.Cursor, row.Copied, row.Skipped, row.Failed)
	s.log.Info("storage move resumed", "move", row.ID, "storage", target.Storage, "cursor", row.Cursor)
	return nil
}

// launch starts the copier of r. Caller holds s.mu.
func (s *Service) launch(r *run, cursor, copied, skipped, failed int64) {
	ctx, cancel := context.WithCancel(s.base)
	r.cancel = cancel
	s.run = r
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.copy(ctx, r, counters{cursor: cursor, copied: copied, skipped: skipped, failed: failed})
	}()
}

// Current returns the newest move, running or finished, or ErrNotFound when there is none.
func (s *Service) Current(ctx context.Context) (*Move, error) {
	row, err := dbq.New(s.db.R).GetLatestMove(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.toMove(row), nil
}

// Get returns move id.
func (s *Service) Get(ctx context.Context, id int64) (*Move, error) {
	row, err := dbq.New(s.db.R).GetMove(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.toMove(row), nil
}

// Failures returns a keyset page of move id's failures in asset id order, after the asset id
// after. next is the cursor of the following page, 0 when this page is the last.
func (s *Service) Failures(ctx context.Context, id, after int64, limit int) ([]Failure, int64, error) {
	if _, err := s.Get(ctx, id); err != nil {
		return nil, 0, err
	}
	rows, err := dbq.New(s.db.R).ListMoveFailures(ctx, dbq.ListMoveFailuresParams{MoveID: id, AfterID: after, Limit: int64(limit)})
	if err != nil {
		return nil, 0, err
	}
	out := make([]Failure, len(rows))
	for i, r := range rows {
		out[i] = Failure{AssetID: r.AssetID, Key: r.Key, Reason: r.Reason}
	}
	var next int64
	if len(out) == limit && limit > 0 {
		next = out[len(out)-1].AssetID
	}
	return out, next, nil
}

// Cancel stops move id: the copier stops, the live store drops the mirror and stays on the
// source, and the move becomes cancelled. ErrNotRunning when the move has already ended.
func (s *Service) Cancel(ctx context.Context, id int64) (*Move, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, err := dbq.New(s.db.R).GetMove(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if row.State != StateCopying {
		return nil, ErrNotRunning
	}
	n, err := s.finishRow(ctx, id, StateCancelled, "")
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, ErrNotRunning
	}
	if r := s.run; r != nil && r.id == id {
		r.cancel()
		s.run = nil
		s.live.Swap(r.mirror.Src)
	}
	s.log.Info("storage move cancelled", "move", id)
	return s.Get(ctx, id)
}

func aad(id int64) string { return "storage_moves:" + strconv.FormatInt(id, 10) }

// open decrypts the sealed target of row.
func (s *Service) open(row dbq.StorageMove) (stored, error) {
	var st stored
	plain, err := s.set.Decrypt(aad(row.ID), row.Target)
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(plain, &st); err != nil {
		return st, errors.New("malformed target")
	}
	return st, nil
}

func (s *Service) toMove(row dbq.StorageMove) *Move {
	m := &Move{
		ID: row.ID, State: row.State, Total: row.Total, Copied: row.Copied, Skipped: row.Skipped,
		Failed: row.Failed, Error: row.Error, StartedAt: time.Unix(row.StartedAt, 0).UTC(),
	}
	if row.FinishedAt.Valid {
		t := time.Unix(row.FinishedAt.Int64, 0).UTC()
		m.FinishedAt = &t
	}
	if st, err := s.open(row); err == nil {
		m.Target = st.target()
	}
	return m
}

// finishRow ends a copying move row; it returns the number of rows changed (0 when the move
// had already ended).
func (s *Service) finishRow(ctx context.Context, id int64, state, msg string) (int64, error) {
	return dbq.New(s.db.W).FinishMove(ctx, dbq.FinishMoveParams{
		State: state, Error: msg, FinishedAt: sql.NullInt64{Int64: time.Now().Unix(), Valid: true}, ID: id,
	})
}

// endRow is finishRow for a move without a copier in this process.
func (s *Service) endRow(ctx context.Context, id int64, state, msg string) error {
	if _, err := s.finishRow(ctx, id, state, msg); err != nil {
		return fmt.Errorf("end storage move %d: %w", id, err)
	}
	return nil
}

// storagePatch returns the settings write that makes t the active storage: every storage
// setting whose effective value differs between cur and t.
func storagePatch(cur, t *config.Config) map[string]json.RawMessage {
	p := map[string]json.RawMessage{}
	add := func(key string, old, new any) {
		if old != new {
			b, _ := json.Marshal(new)
			p[key] = b
		}
	}
	add(settings.KeyStorage, cur.Storage, t.Storage)
	add(settings.KeyFSRoot, cur.FSRoot, t.FSRoot)
	add(settings.KeyS3Endpoint, cur.S3Endpoint, t.S3Endpoint)
	add(settings.KeyS3Region, cur.S3Region, t.S3Region)
	add(settings.KeyS3Bucket, cur.S3Bucket, t.S3Bucket)
	add(settings.KeyS3AccessKeyID, cur.S3AccessKeyID, t.S3AccessKeyID)
	add(settings.KeyS3SecretAccessKey, cur.S3SecretAccessKey, t.S3SecretAccessKey)
	add(settings.KeyS3Prefix, cur.S3Prefix, t.S3Prefix)
	add(settings.KeyS3PathStyle, cur.S3PathStyle, t.S3PathStyle)
	add(settings.KeyS3Redirect, cur.S3Redirect, t.S3Redirect)
	return p
}
