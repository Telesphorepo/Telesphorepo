package nexus

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/nexus/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// Sync states (nexus_syncs.state) and sync repo states (nexus_sync_repos.state).
const (
	SyncPending   = "pending"
	SyncRunning   = "running"
	SyncDone      = "done"
	SyncFailed    = "failed"
	SyncCancelled = "cancelled"
)

// Sync triggers (nexus_syncs.trigger): what started a sync.
const (
	TriggerSave     = "save"     // the repos were saved
	TriggerManual   = "manual"   // an admin clicked "Sync now"
	TriggerSchedule = "schedule" // the scheduler
	TriggerCutover  = "cutover"  // the final sync of a cut-over
)

// pageRetries is how many times the runner retries a failed asset page before the source repo
// fails; retryDelay is the wait before the first retry, doubled for each next one.
const (
	pageRetries = 3
	retryDelay  = 2 * time.Second
)

// schedulerTick is the longest wait between two scheduler checks; a shorter
// nexus.sync_interval checks at that interval.
const schedulerTick = time.Minute

// dueBatch is the number of due sources the scheduler starts per check.
const dueBatch = 100

// keepSyncs is the number of syncs a source keeps; a new sync deletes the older ones, except
// the cut-over sync.
const keepSyncs = 100

// Sync is a sync as the API returns it. The counters are the sums over its repos. Repos is
// set only for a single sync.
type Sync struct {
	ID         int64      `json:"id"`
	SourceID   int64      `json:"source_id"`
	State      string     `json:"state"`
	Trigger    string     `json:"trigger"`
	StartedBy  string     `json:"started_by"` // user name; "" for the scheduler or a deleted user
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	Error      string     `json:"error"`
	Checked    int64      `json:"checked"`
	Imported   int64      `json:"imported"`
	Skipped    int64      `json:"skipped"`
	Ignored    int64      `json:"ignored"`
	Failed     int64      `json:"failed"`
	Bytes      int64      `json:"bytes"`
	// RepoCount is the number of hosted repos the sync copies, ReposDone the ended ones, and
	// CurrentRepo the one that copies now ("" when none).
	RepoCount   int        `json:"repo_count"`
	ReposDone   int        `json:"repos_done"`
	CurrentRepo string     `json:"current_repo"`
	Repos       []SyncRepo `json:"repos,omitempty"`
	// MergeID is the merge whose final sync this is (trigger cutover without a switch-over of its
	// own), 0 for none; MergeFrom and MergeInto name its repos at the start of the merge.
	MergeID   int64  `json:"merge_id"`
	MergeFrom string `json:"merge_from"`
	MergeInto string `json:"merge_into"`
}

// SyncRepo is one mapped hosted repo of a sync with its counters. Checked counts the files
// read from Nexus; PreviousChecked is the Checked of the newest earlier walk of the repo that
// reached the end (0 when there is none), so Checked/PreviousChecked estimates the progress.
type SyncRepo struct {
	SourceRepo      string     `json:"source_repo"`
	SourceFormat    string     `json:"source_format"`
	Target          string     `json:"target"`
	State           string     `json:"state"`
	Error           string     `json:"error"`
	Checked         int64      `json:"checked"`
	Imported        int64      `json:"imported"`
	Skipped         int64      `json:"skipped"`
	Ignored         int64      `json:"ignored"`
	Failed          int64      `json:"failed"`
	Bytes           int64      `json:"bytes"`
	ActiveAt        *time.Time `json:"active_at"`
	PreviousChecked int64      `json:"previous_checked"`
}

// Failure is one source file that a sync did not import.
type Failure struct {
	ID         int64  `json:"-"`
	SourceRepo string `json:"source_repo"`
	Path       string `json:"path"`
	Reason     string `json:"reason"`
}

// LogLine is one line of a sync's log.
type LogLine struct {
	ID         int64     `json:"id"`
	At         time.Time `json:"at"`
	SourceRepo string    `json:"source_repo"` // "" for a line about the whole sync
	Level      string    `json:"level"`       // "info", "warn" or "error"
	Message    string    `json:"message"`
}

// Log levels (nexus_sync_log.level).
const (
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
)

type runState struct {
	id          int64
	cancel      context.CancelFunc
	done        chan struct{}
	cancelled   bool   // set by Cancel before cancel() runs
	cancelledBy string // the admin who cancelled
}

// writeLog adds a line to the log of sync id in q. A line names a repo and counts, never a
// path, a URL, a password or a token.
func writeLog(ctx context.Context, q *dbq.Queries, id int64, repo, level, msg string) error {
	return q.InsertNexusSyncLog(ctx, dbq.InsertNexusSyncLogParams{SyncID: id, At: time.Now().Unix(),
		SourceRepo: repo, Level: level, Message: truncate(msg, 1000)})
}

// logLine writes a log line in its own statement; a failure goes to the server log only.
func (s *Service) logLine(ctx context.Context, id int64, repo, level, msg string) {
	if err := writeLog(context.WithoutCancel(ctx), dbq.New(s.db.W), id, repo, level, msg); err != nil {
		s.log.Error("nexus: cannot write the sync log", "sync", id, "error", err.Error())
	}
}

// Resume resumes the syncs left running by the previous process and lets StartSync run syncs.
// A sync stopped by the end of the module's context stays running in the database and resumes
// at its last saved page on the next start.
func (s *Service) Resume(ctx context.Context) error {
	running, err := dbq.New(s.db.R).ListRunningNexusSyncs(ctx)
	if err != nil {
		return fmt.Errorf("nexus: look up running syncs: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.started = true
	for _, sy := range running {
		s.log.Info("nexus: resuming sync", "sync", sy.ID, "source_id", sy.SourceID)
		s.launch(sy.ID)
	}
	return nil
}

// Schedule starts a sync of every mirroring source whose last sync ended more than
// nexus.sync_interval ago, until ctx ends. It reads the interval from the live settings at each
// check.
func (s *Service) Schedule(ctx context.Context) {
	for {
		t := time.NewTimer(min(s.interval(), schedulerTick))
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		s.startDue(ctx)
	}
}

func (s *Service) interval() time.Duration {
	return SyncInterval.Get(s.settings.Current())
}

func (s *Service) startDue(ctx context.Context) {
	cutoff := time.Now().Add(-s.interval()).Unix()
	ids, err := dbq.New(s.db.R).ListDueNexusSources(ctx, dbq.ListDueNexusSourcesParams{Cutoff: cutoff, Limit: dueBatch})
	if err != nil {
		if ctx.Err() == nil {
			s.log.Error("nexus: cannot list due sources", "error", err.Error())
		}
		return
	}
	for _, id := range ids {
		_, err := s.startSync(ctx, id, 0, TriggerSchedule, nil)
		switch {
		case err == nil, ctx.Err() != nil:
		case errors.Is(err, ErrRunning), errors.Is(err, ErrInvalid), errors.Is(err, ErrCutOver), errors.Is(err, ErrNotFound):
			// Running already, no mapped repos, or changed since the list.
		default:
			s.log.Error("nexus: cannot start a scheduled sync", "source_id", id, "error", err.Error())
		}
	}
}

// launch starts the runner of sync id. The caller holds s.mu.
func (s *Service) launch(id int64) {
	ctx, cancel := context.WithCancel(s.base)
	st := &runState{id: id, cancel: cancel, done: make(chan struct{})}
	s.runs[id] = st
	s.spawn("sync", func(context.Context) {
		defer close(st.done)
		defer cancel()
		s.runSync(ctx, st)
		s.mu.Lock()
		if s.runs[id] == st {
			delete(s.runs, id)
		}
		s.mu.Unlock()
	})
}

// StartSync starts a sync of a mirroring source that has mapped repos ("Sync now"). userID 0
// means unknown.
func (s *Service) StartSync(ctx context.Context, sourceID, userID int64) (*Sync, error) {
	id, err := s.startSync(ctx, sourceID, userID, TriggerManual, nil)
	if err != nil {
		return nil, err
	}
	s.record(ctx, ActionSyncStart, strconv.FormatInt(sourceID, 10), map[string]any{"source_id": sourceID, "sync_id": id})
	return s.Sync(ctx, id)
}

// CutOver starts the final sync of every mapped hosted repo of a source that still copies
// ("Switch over everything"). When it walks every repo to the end, each of them switches over
// (see switchOver) and the source becomes cut_over with its credentials deleted; files that
// failed to import are counted in the switch-overs and the source's cutover_failed. When a repo
// fails or the sync is cancelled, nothing switches over and the source's last_error says why.
func (s *Service) CutOver(ctx context.Context, sourceID, userID int64) (*Sync, error) {
	id, err := s.startSync(ctx, sourceID, userID, TriggerCutover, nil)
	if err != nil {
		return nil, err
	}
	s.record(ctx, ActionCutover, strconv.FormatInt(sourceID, 10), map[string]any{"source_id": sourceID, "sync_id": id})
	return s.Sync(ctx, id)
}

// SwitchOverRepo starts the final sync of one mapped hosted repo of a source, sourceRepo, and of
// the other Nexus repos that copy into the same target. When the sync walks them to the end, the
// target takes uploads and the source stops syncing them; the other repos of the source keep
// copying. The last repo of a source to switch over cuts the source over. A proxy or a group
// needs no switch-over (ErrInvalid); a repo that switched over already is ErrSwitched.
func (s *Service) SwitchOverRepo(ctx context.Context, sourceID int64, sourceRepo string, userID int64) (*Sync, error) {
	q := dbq.New(s.db.R)
	src, err := sourceRow(q.GetNexusSource(ctx, sourceID))
	if err != nil {
		return nil, err
	}
	if src.State != StateMirroring {
		return nil, ErrCutOver
	}
	m, err := q.GetNexusSourceRepo(ctx, dbq.GetNexusSourceRepoParams{SourceID: sourceID, SourceRepo: sourceRepo})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if m.Kind != TypeHosted {
		return nil, fmt.Errorf("%w: a %s serves from this server already and needs no switch-over", ErrInvalid, m.Kind)
	}
	target, err := s.repos.ByID(ctx, m.TargetRepoID)
	if err != nil {
		return nil, err
	}
	if target.Op != nil && target.Op.Type == OpMerge {
		return nil, fmt.Errorf("%w: %s", ErrBusy, s.ops.Message(target.Op))
	}
	id, err := s.startSync(ctx, sourceID, userID, TriggerCutover, target)
	if err != nil {
		return nil, err
	}
	s.record(ctx, ActionSwitchOverRepo, target.Name, map[string]any{"source_id": sourceID, "source_repo": sourceRepo, "sync_id": id})
	return s.Sync(ctx, id)
}

// startLine is the first log line of a sync. target is the repo that a switch-over of one
// repository switches over, nil for any other sync.
func startLine(trigger, by string, target *repos.Repo) string {
	if by == "" {
		by = "an administrator"
	}
	switch trigger {
	case TriggerSave:
		return "Started after the repositories were saved."
	case TriggerSchedule:
		return "Scheduled sync started."
	case TriggerCutover:
		if target != nil {
			return "Final sync before the switch-over of " + target.Name + " started by " + by + "."
		}
		return "Final sync before the switch-over started by " + by + "."
	}
	return "Started by " + by + "."
}

// startSync starts a sync of the mapped hosted repos of a source that still copy, or, with a
// target, of those that copy into target (the final sync of one repository's switch-over).
func (s *Service) startSync(ctx context.Context, sourceID, userID int64, trigger string, target *repos.Repo) (int64, error) {
	return s.startSyncTx(ctx, sourceID, userID, trigger, target, "", nil)
}

// startSyncTx is startSync with the first log line line ("": startLine); bind, when set, runs in
// the transaction that creates the sync, with its id (a merge records the final sync it waits
// for before the sync can end).
func (s *Service) startSyncTx(ctx context.Context, sourceID, userID int64, trigger string, target *repos.Repo, line string, bind func(q *dbq.Queries, id int64) error) (int64, error) {
	s.mu.Lock()
	started := s.started
	s.mu.Unlock()
	if !started {
		return 0, errors.New("nexus service not started")
	}
	var id int64
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		row, err := sourceRow(q.GetNexusSource(ctx, sourceID))
		if err != nil {
			return err
		}
		if row.State != StateMirroring {
			return ErrCutOver
		}
		if _, err := q.GetRunningNexusSync(ctx, sourceID); err == nil {
			return ErrRunning
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		mapped, err := q.ListNexusSourceRepos(ctx, sourceID)
		if err != nil {
			return err
		}
		if len(mapped) == 0 {
			return fmt.Errorf("%w: the source has no mapped repos", ErrInvalid)
		}
		id, err = q.CreateNexusSync(ctx, dbq.CreateNexusSyncParams{SourceID: sourceID, Trigger: trigger,
			StartedBy: sql.NullInt64{Int64: userID, Valid: userID != 0}, StartedAt: time.Now().Unix()})
		if err != nil {
			return err
		}
		if target == nil {
			if _, err := q.InsertNexusSyncRepos(ctx, dbq.InsertNexusSyncReposParams{SyncID: id, SourceID: sourceID}); err != nil {
				return err
			}
		} else {
			n, err := q.InsertNexusSyncReposOfTarget(ctx, dbq.InsertNexusSyncReposOfTargetParams{SyncID: id, SourceID: sourceID, TargetRepoID: target.ID})
			if err != nil {
				return err
			}
			if n == 0 {
				return ErrSwitched
			}
		}
		// Retention: keep the newest keepSyncs syncs (this one included) and the cut-over sync.
		ids, err := q.ListNexusSyncIDs(ctx, dbq.ListNexusSyncIDsParams{SourceID: sourceID, Limit: keepSyncs + 1})
		if err != nil {
			return err
		}
		if len(ids) > keepSyncs {
			if _, err := q.DeleteOldNexusSyncs(ctx, dbq.DeleteOldNexusSyncsParams{SourceID: sourceID, Cutoff: ids[keepSyncs]}); err != nil {
				return err
			}
		}
		sy, err := q.GetNexusSync(ctx, id)
		if err != nil {
			return err
		}
		if line == "" {
			line = startLine(trigger, sy.StartedByName, target)
		}
		if err := writeLog(ctx, q, id, "", LevelInfo, line); err != nil {
			return err
		}
		if bind != nil {
			if err := bind(q, id); err != nil {
				return err
			}
		}
		if target != nil {
			return nil
		}
		var proxies int
		for _, m := range mapped {
			if m.Kind == TypeProxy {
				proxies++
			}
		}
		switch {
		case proxies == 1:
			return writeLog(ctx, q, id, "", LevelInfo, "Skipped 1 proxy: a proxy gets its files from its remote when a client asks for them.")
		case proxies > 1:
			return writeLog(ctx, q, id, "", LevelInfo, fmt.Sprintf("Skipped %d proxies: a proxy gets its files from its remote when a client asks for them.", proxies))
		}
		return nil
	})
	if isUnique(err) {
		return 0, ErrRunning
	}
	if err != nil {
		return 0, err
	}
	s.log.Info("nexus: sync started", "sync", id, "source_id", sourceID, "trigger", trigger)
	s.mu.Lock()
	s.launch(id)
	s.mu.Unlock()
	return id, nil
}

func syncRow(row dbq.GetNexusSyncRow, err error) (dbq.GetNexusSyncRow, error) {
	if errors.Is(err, sql.ErrNoRows) {
		return row, ErrNotFound
	}
	return row, err
}

// Sync returns sync id with its repos.
func (s *Service) Sync(ctx context.Context, id int64) (*Sync, error) {
	row, err := syncRow(dbq.New(s.db.R).GetNexusSync(ctx, id))
	if err != nil {
		return nil, err
	}
	return s.toSync(ctx, row, true)
}

// toSync converts a row and sums its repos' counters; withRepos also lists them with the
// checked count of each one's previous walk.
func (s *Service) toSync(ctx context.Context, row dbq.GetNexusSyncRow, withRepos bool) (*Sync, error) {
	sy := &Sync{ID: row.ID, SourceID: row.SourceID, State: row.State, Trigger: row.Trigger,
		StartedBy: row.StartedByName, StartedAt: time.Unix(row.StartedAt, 0).UTC(), Error: row.Error}
	if row.FinishedAt.Valid {
		t := time.Unix(row.FinishedAt.Int64, 0).UTC()
		sy.FinishedAt = &t
	}
	q := dbq.New(s.db.R)
	if row.Trigger == TriggerCutover {
		switch m, err := q.GetNexusMergeBySync(ctx, row.ID); {
		case err == nil:
			sy.MergeID, sy.MergeFrom, sy.MergeInto = m.ID, m.SourceName, m.TargetName
		case !errors.Is(err, sql.ErrNoRows):
			return nil, err
		}
	}
	rs, err := q.ListNexusSyncRepos(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	sy.RepoCount = len(rs)
	if withRepos {
		sy.Repos = make([]SyncRepo, 0, len(rs))
	}
	for _, r := range rs {
		sy.Checked += r.Checked
		sy.Imported += r.Imported
		sy.Skipped += r.Skipped
		sy.Ignored += r.Ignored
		sy.Failed += r.Failed
		sy.Bytes += r.Bytes
		switch r.State {
		case SyncRunning:
			sy.CurrentRepo = r.SourceRepo
		case SyncDone, SyncFailed, SyncCancelled:
			sy.ReposDone++
		}
		if !withRepos {
			continue
		}
		target := ""
		if t, err := s.repos.ByID(ctx, r.TargetRepoID); err == nil {
			target = t.Name
		} else if !errors.Is(err, repos.ErrNotFound) {
			return nil, err
		}
		out := SyncRepo{SourceRepo: r.SourceRepo, SourceFormat: r.SourceFormat, Target: target,
			State: r.State, Error: r.Error, Checked: r.Checked, Imported: r.Imported, Skipped: r.Skipped,
			Ignored: r.Ignored, Failed: r.Failed, Bytes: r.Bytes}
		if r.ActiveAt > 0 {
			t := time.Unix(r.ActiveAt, 0).UTC()
			out.ActiveAt = &t
		}
		prev, err := q.PreviousNexusChecked(ctx, dbq.PreviousNexusCheckedParams{SourceRepo: r.SourceRepo,
			SourceID: row.SourceID, Before: row.ID})
		switch {
		case err == nil:
			out.PreviousChecked = prev
		case !errors.Is(err, sql.ErrNoRows):
			return nil, err
		}
		sy.Repos = append(sy.Repos, out)
	}
	return sy, nil
}

// Syncs returns a keyset page of a source's syncs, newest first, with ids below before (0:
// from the newest). next is the cursor of the next page, 0 on the last page.
func (s *Service) Syncs(ctx context.Context, sourceID, before int64, limit int) (out []Sync, next int64, err error) {
	q := dbq.New(s.db.R)
	if _, err := sourceRow(q.GetNexusSource(ctx, sourceID)); err != nil {
		return nil, 0, err
	}
	if before <= 0 {
		before = 1<<63 - 1
	}
	rows, err := q.ListNexusSyncs(ctx, dbq.ListNexusSyncsParams{SourceID: sourceID, Before: before, Limit: int64(limit)})
	if err != nil {
		return nil, 0, err
	}
	out = make([]Sync, 0, len(rows))
	for _, row := range rows {
		sy, err := s.toSync(ctx, dbq.GetNexusSyncRow(row), false)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *sy)
	}
	if len(rows) == limit && limit > 0 {
		next = rows[len(rows)-1].ID
	}
	return out, next, nil
}

// Failures returns a keyset page of sync id's failures after failure id after. next is the
// cursor of the next page, 0 on the last page.
func (s *Service) Failures(ctx context.Context, id, after int64, limit int) (out []Failure, next int64, err error) {
	q := dbq.New(s.db.R)
	if _, err := syncRow(q.GetNexusSync(ctx, id)); err != nil {
		return nil, 0, err
	}
	rows, err := q.ListNexusSyncFailures(ctx, dbq.ListNexusSyncFailuresParams{SyncID: id, AfterID: after, Limit: int64(limit)})
	if err != nil {
		return nil, 0, err
	}
	out = make([]Failure, 0, len(rows))
	for _, r := range rows {
		out = append(out, Failure{ID: r.ID, SourceRepo: r.SourceRepo, Path: r.Path, Reason: r.Reason})
	}
	if len(out) == limit && limit > 0 {
		next = out[len(out)-1].ID
	}
	return out, next, nil
}

// logScan is the number of log lines Log reads per query while it filters by repo.
const logScan = 500

// Log returns a keyset page of sync id's log lines with ids above after, oldest first. A repo
// other than "" keeps only that repo's lines: Log reads the sync's lines through the index and
// filters them here. next is the cursor of the next page, 0 at the end.
func (s *Service) Log(ctx context.Context, id, after int64, repo string, limit int) (out []LogLine, next int64, err error) {
	q := dbq.New(s.db.R)
	if _, err := syncRow(q.GetNexusSync(ctx, id)); err != nil {
		return nil, 0, err
	}
	out = []LogLine{}
	scan := int64(limit)
	if repo != "" {
		scan = logScan
	}
	for {
		rows, err := q.ListNexusSyncLog(ctx, dbq.ListNexusSyncLogParams{SyncID: id, AfterID: after, Limit: scan})
		if err != nil {
			return nil, 0, err
		}
		for _, r := range rows {
			after = r.ID
			if repo != "" && r.SourceRepo != repo {
				continue
			}
			out = append(out, LogLine{ID: r.ID, At: time.Unix(r.At, 0).UTC(), SourceRepo: r.SourceRepo, Level: r.Level, Message: r.Message})
			if len(out) == limit {
				return out, r.ID, nil
			}
		}
		if int64(len(rows)) < scan {
			return out, 0, nil
		}
	}
}

// Cancel stops sync id, waits for its runner to stop, marks the sync and its unfinished repos
// cancelled, and returns it. A cancelled cut-over leaves the source mirroring. userID is the
// admin who cancels (0: unknown).
func (s *Service) Cancel(ctx context.Context, id, userID int64) (*Sync, error) {
	row, err := syncRow(dbq.New(s.db.R).GetNexusSync(ctx, id))
	if err != nil {
		return nil, err
	}
	if row.State != SyncRunning {
		return nil, ErrNotRunning
	}
	by := ""
	if userID != 0 {
		if name, err := s.username(ctx, userID); err == nil {
			by = name
		}
	}
	s.mu.Lock()
	st := s.runs[id]
	if st != nil {
		st.cancelled, st.cancelledBy = true, by
		st.cancel()
	}
	s.mu.Unlock()
	if st != nil {
		select {
		case <-st.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	// The runner marks the sync cancelled when it stops; this covers a sync without a runner.
	if err := s.finish(context.WithoutCancel(ctx), id, row.SourceID, SyncCancelled, "", cancelLine(by)); err != nil {
		return nil, err
	}
	s.record(ctx, ActionSyncCancel, strconv.FormatInt(row.SourceID, 10), map[string]any{"source_id": row.SourceID, "sync_id": id})
	return s.Sync(ctx, id)
}

func cancelLine(by string) string {
	if by == "" {
		return "Cancelled."
	}
	return "Cancelled by " + by + "."
}

// finish ends sync id and every repo of it that has not ended, records the end on the source
// and writes line to the log. It is a no-op for a sync that already ended.
func (s *Service) finish(ctx context.Context, id, sourceID int64, state, msg, line string) error {
	return s.db.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		now := time.Now().Unix()
		n, err := q.FinishNexusSync(ctx, dbq.FinishNexusSyncParams{State: state, Error: msg,
			FinishedAt: sql.NullInt64{Int64: now, Valid: true}, ID: id})
		if err != nil || n == 0 {
			return err
		}
		if err := endRepos(ctx, q, id, state, msg); err != nil {
			return err
		}
		level := LevelInfo
		if state == SyncFailed {
			level = LevelError
		}
		if err := writeLog(ctx, q, id, "", level, line); err != nil {
			return err
		}
		return q.SetNexusSourceSynced(ctx, dbq.SetNexusSourceSyncedParams{LastSyncAt: now, LastError: msg, ID: sourceID})
	})
}

// endRepos ends every repo of sync id that has not ended.
func endRepos(ctx context.Context, q *dbq.Queries, id int64, state, msg string) error {
	rs, err := q.ListNexusSyncRepos(ctx, id)
	if err != nil {
		return err
	}
	for _, r := range rs {
		if r.State == SyncPending || r.State == SyncRunning {
			rstate := state
			if rstate == SyncDone {
				rstate = SyncFailed // a repo the runner never ended
			}
			if err := q.SetNexusSyncRepoState(ctx, dbq.SetNexusSyncRepoStateParams{
				State: rstate, Error: msg, SyncID: id, SourceRepo: r.SourceRepo}); err != nil {
				return err
			}
		}
	}
	return nil
}

// switchOver ends the final sync id of a switch-over as done and switches its repos over in one
// transaction: each repo of the sync gets its switch-over (the sync and its failed files), its
// target takes uploads again and the source stops syncing it. When no mapped hosted repo of the
// source copies any more, the source becomes cut_over and its credentials are deleted; it keeps
// the sync id and the sync's failed file count. It returns whether the source cut over.
func (s *Service) switchOver(ctx context.Context, id, sourceID, failedFiles int64, line string) (bool, error) {
	cut := false
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		now := time.Now().Unix()
		n, err := q.FinishNexusSync(ctx, dbq.FinishNexusSyncParams{State: SyncDone,
			FinishedAt: sql.NullInt64{Int64: now, Valid: true}, ID: id})
		if err != nil || n == 0 {
			return err
		}
		if err := endRepos(ctx, q, id, SyncDone, ""); err != nil {
			return err
		}
		rs, err := q.ListNexusSyncRepos(ctx, id)
		if err != nil {
			return err
		}
		var names []string
		for _, r := range rs {
			if err := q.InsertNexusSwitchOver(ctx, dbq.InsertNexusSwitchOverParams{SourceID: sourceID, SourceRepo: r.SourceRepo,
				SyncID: id, Failed: r.Failed, SwitchedAt: now}); err != nil {
				return err
			}
			if err := s.release(ctx, tx, r.TargetRepoID, sourceID); err != nil {
				return err
			}
			names = append(names, r.SourceRepo)
		}
		copying, err := q.CountCopyingNexusRepos(ctx, sourceID)
		if err != nil {
			return err
		}
		msg := line + " Switched over " + strings.Join(names, ", ") + ": uploads to it go to this server."
		if len(names) != 1 {
			msg = line + " Switched over: uploads go to this server."
		}
		if copying == 0 {
			if _, err := q.CutOverNexusSource(ctx, dbq.CutOverNexusSourceParams{CutoverSyncID: id, CutoverFailed: failedFiles, ID: sourceID}); err != nil {
				return err
			}
			cut = true
			msg += " Every repository switched over, so the server deleted the saved Nexus password."
		}
		if err := writeLog(ctx, q, id, "", LevelInfo, msg); err != nil {
			return err
		}
		return q.SetNexusSourceSynced(ctx, dbq.SetNexusSourceSyncedParams{LastSyncAt: now, ID: sourceID})
	})
	return cut, err
}

// finishedLine is the log line of a sync that walked every repo.
func finishedLine(started time.Time, imported, bytes int64) string {
	took := duration(time.Since(started))
	if imported == 0 {
		return "Finished in " + took + ". No new files."
	}
	return fmt.Sprintf("Finished in %s. %s (%s).", took, files(imported, "new file"), size(bytes))
}

// runSync syncs every unfinished repo of the sync, then ends it: done when every repo finished
// (item failures are listed, not fatal), failed when a repo failed, cancelled on Cancel. A
// switch-over sync (trigger cutover) switches its repos over when every repo was walked to the
// end, even when some files failed; each switch-over keeps its failed file count as a warning
// (see switchOver). When the server
// context ends first, the sync stays running for the next Start.
func (s *Service) runSync(ctx context.Context, st *runState) {
	id := st.id
	log := s.log.With("sync", id)
	q := dbq.New(s.db.R)
	sy, err := q.GetNexusSync(ctx, id)
	if err != nil {
		if ctx.Err() == nil {
			log.Error("nexus: cannot load sync", "error", err.Error())
		}
		return
	}
	log = log.With("source_id", sy.SourceID)
	end := func(state, msg, line string) {
		if err := s.finish(context.WithoutCancel(ctx), id, sy.SourceID, state, msg, line); err != nil {
			log.Error("nexus: cannot end sync", "error", err.Error())
			return
		}
		log.Info("nexus: sync ended", "state", state, "error", msg)
	}
	fail := func(msg string) { end(SyncFailed, msg, "Failed: "+msg) }
	stopped := func() bool {
		if ctx.Err() == nil {
			return false
		}
		s.mu.Lock()
		c, by := st.cancelled, st.cancelledBy
		s.mu.Unlock()
		if c {
			end(SyncCancelled, "", cancelLine(by))
		} else {
			log.Info("nexus: sync paused by shutdown; resumes on next start")
		}
		return true
	}

	src, err := q.GetNexusSource(ctx, sy.SourceID)
	if err != nil {
		if !stopped() {
			fail("cannot load the source: " + err.Error())
		}
		return
	}
	cl, err := s.client(src)
	if err != nil {
		fail(err.Error())
		return
	}
	rs, err := q.ListNexusSyncRepos(ctx, id)
	if err != nil {
		if !stopped() {
			fail("cannot load the sync repos: " + err.Error())
		}
		return
	}
	// A repo that Nexus marks offline cannot download its files, so the sync skips it.
	online := map[string]bool{}
	if listed, err := cl.Repositories(ctx); err == nil {
		for _, r := range listed {
			online[r.Name] = r.Online
		}
	} else if ctx.Err() == nil {
		s.logLine(ctx, id, "", LevelWarn, "Nexus did not list its repositories, so the sync cannot check which ones are offline: "+err.Error())
	}
	var failedRepos int
	var failedFiles, imported, bytes int64
	for _, r := range rs {
		imported += r.Imported
		bytes += r.Bytes
		switch r.State {
		case SyncFailed:
			failedRepos++
			failedFiles += r.Failed
			continue
		case SyncDone:
			failedFiles += r.Failed
			continue
		case SyncCancelled:
			continue
		}
		if on, ok := online[r.SourceRepo]; ok && !on {
			// An offline repo is frozen: Nexus takes no uploads to it, so the files copied so far are
			// all of it. The sync skips it, and a switch-over or a merge goes on without it.
			if err := s.db.Tx(context.WithoutCancel(ctx), func(tx *sql.Tx) error {
				q := dbq.New(tx)
				if err := q.SetNexusSyncRepoState(ctx, dbq.SetNexusSyncRepoStateParams{
					State: SyncDone, Error: "", SyncID: id, SourceRepo: r.SourceRepo}); err != nil {
					return err
				}
				return writeLog(ctx, q, id, r.SourceRepo, LevelInfo, offlineSkipped)
			}); err != nil {
				log.Error("nexus: cannot save repo state", "source_repo", r.SourceRepo, "error", err.Error())
			}
			continue
		}
		c, msg := s.syncRepo(ctx, cl, log, r)
		if stopped() {
			return
		}
		failedFiles += c.Failed
		imported += c.Imported - r.Imported
		bytes += c.Bytes - r.Bytes
		if msg != "" {
			failedRepos++
			if err := s.db.Tx(context.WithoutCancel(ctx), func(tx *sql.Tx) error {
				q := dbq.New(tx)
				if err := q.SetNexusSyncRepoState(ctx, dbq.SetNexusSyncRepoStateParams{
					State: SyncFailed, Error: msg, SyncID: id, SourceRepo: r.SourceRepo}); err != nil {
					return err
				}
				return writeLog(ctx, q, id, r.SourceRepo, LevelError, "Failed. "+msg)
			}); err != nil {
				log.Error("nexus: cannot save repo state", "source_repo", r.SourceRepo, "error", err.Error())
			}
			log.Warn("nexus: source repo failed", "source_repo", r.SourceRepo, "error", msg)
		}
	}
	started := time.Unix(sy.StartedAt, 0)
	failed := fmt.Sprintf("%d of %d repositories failed", failedRepos, len(rs))
	if len(rs) == 1 {
		failed = "the repository failed"
	}
	switch {
	case failedRepos > 0 && sy.Trigger == TriggerCutover:
		fail("the switch-over stopped: " + failed + "; nothing switched over, and the copy from Nexus goes on")
	case failedRepos > 0:
		fail(failed)
	case sy.Trigger == TriggerCutover && s.mergeWaits(context.WithoutCancel(ctx), id):
		// The final sync of a merge: the merge copies its files, then switches the repo over.
		end(SyncDone, "", finishedLine(started, imported, bytes)+" The merge goes on.")
	case sy.Trigger == TriggerCutover:
		cut, err := s.switchOver(context.WithoutCancel(ctx), id, sy.SourceID, failedFiles, finishedLine(started, imported, bytes))
		if err != nil {
			log.Error("nexus: cannot switch over", "error", err.Error())
			fail("cannot switch over: " + err.Error())
			return
		}
		log.Info("nexus: repos switched over", "source", src.Name, "repos", len(rs), "failed_files", failedFiles, "source_cut_over", cut)
	default:
		end(SyncDone, "", finishedLine(started, imported, bytes))
	}
}

// countingReader counts the bytes read through it. The downloads of one item (a Docker manifest
// and its blobs) may run at the same time.
type countingReader struct {
	io.ReadCloser
	n *atomic.Int64
}

func (c countingReader) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	c.n.Add(int64(n))
	return n, err
}

// syncWorkers is the number of assets of one repo that import at once. pagesAhead is the most
// asset pages that the runner reads ahead of the oldest page that is not saved yet.
const (
	syncWorkers = 16
	pagesAhead  = 8
)

// itemResult is what the import of one asset did, with the bytes its downloads read.
type itemResult struct {
	out  format.ImportOutcome
	err  error
	read int64
}

// pageRun is an asset page whose items import: results[i] gets the result of item i. err is set
// for the page that Nexus did not give.
type pageRun struct {
	page    AssetPage
	results []chan itemResult
	err     error
}

// syncRepo syncs source repo r from its saved continuation token. A reader fetches the asset
// pages one after the other, up to pagesAhead pages ahead, and hands each asset to syncWorkers
// workers; this goroutine takes the results in page and item order and saves each page when it
// and every page before it are done, so the saved token and counters cover a prefix of the walk
// and a resume redoes only the pages after it. It returns the repo's counters, and "" when the
// repo is done or ctx ended, else the reason the repo failed. It writes a log line when the repo
// starts, for each page that imported a file, and when the repo is done; a page's line goes in
// the transaction that saves the page.
func (s *Service) syncRepo(ctx context.Context, cl *Client, log *slog.Logger, r dbq.NexusSyncRepo) (dbq.NexusSyncRepo, string) {
	counts := r
	target, err := s.repos.ByID(ctx, r.TargetRepoID)
	if err != nil {
		if ctx.Err() != nil {
			return counts, ""
		}
		return counts, "cannot load the target repo: " + err.Error()
	}
	im, ok := s.accepts(target.Format, r.SourceFormat)
	if !ok {
		return counts, fmt.Sprintf("target repo %q (%s) cannot import %s content", target.Name, target.Format, r.SourceFormat)
	}
	w := dbq.New(s.db.W)
	if r.State != SyncRunning {
		if err := w.SetNexusSyncRepoState(ctx, dbq.SetNexusSyncRepoStateParams{
			State: SyncRunning, Error: "", SyncID: r.SyncID, SourceRepo: r.SourceRepo}); err != nil {
			if ctx.Err() != nil {
				return counts, ""
			}
			return counts, "cannot save state: " + err.Error()
		}
		s.logLine(ctx, r.SyncID, r.SourceRepo, LevelInfo, "Started.")
	} else {
		s.logLine(ctx, r.SyncID, r.SourceRepo, LevelInfo, "Resumed after a restart.")
	}
	log = log.With("source_repo", r.SourceRepo, "target", target.Name)
	log.Info("nexus: syncing repo", "continuation_saved", r.Continuation != "")

	// The reader and the workers stop when syncRepo returns; it waits for them, so no import of
	// this repo runs after it.
	runCtx, stop := context.WithCancel(ctx)
	var wg sync.WaitGroup
	defer func() {
		stop()
		wg.Wait()
	}()
	type job struct {
		a   Asset
		out chan<- itemResult
	}
	jobs := make(chan job)
	pages := make(chan pageRun, pagesAhead)
	cache := format.NewRunCache()
	for range syncWorkers {
		wg.Go(func() {
			for j := range jobs {
				var read atomic.Int64
				out, err := s.importAsset(runCtx, cl, im, target, r.SourceRepo, cache, j.a, &read)
				j.out <- itemResult{out: out, err: err, read: read.Load()}
			}
		})
	}
	wg.Go(func() {
		defer close(jobs)
		defer close(pages)
		token := r.Continuation
		for {
			page, err := s.page(runCtx, cl, r.SyncID, r.SourceRepo, token)
			pr := pageRun{page: page, err: err, results: make([]chan itemResult, len(page.Items))}
			for i := range pr.results {
				pr.results[i] = make(chan itemResult, 1)
			}
			select {
			case pages <- pr:
			case <-runCtx.Done():
				return
			}
			if err != nil {
				return
			}
			for i, a := range page.Items {
				select {
				case jobs <- job{a: a, out: pr.results[i]}:
				case <-runCtx.Done():
					return
				}
			}
			if page.Next == "" {
				return
			}
			token = page.Next
		}
	})

	for {
		var pr pageRun
		var ok bool
		select {
		case pr, ok = <-pages:
		case <-ctx.Done():
			return counts, ""
		}
		if !ok {
			return counts, "" // the reader stops early only when ctx ended
		}
		if pr.err != nil {
			if ctx.Err() != nil {
				return counts, ""
			}
			return counts, pr.err.Error()
		}
		page := pr.page
		var fails []dbq.InsertNexusSyncFailureParams
		var pageImported, pageBytes int64
		for i, a := range page.Items {
			var res itemResult
			select {
			case res = <-pr.results[i]:
			case <-ctx.Done():
				return counts, "" // the page is redone on resume
			}
			if res.err != nil {
				if ctx.Err() != nil {
					return counts, ""
				}
				counts.Failed++
				fails = append(fails, dbq.InsertNexusSyncFailureParams{
					SyncID: r.SyncID, SourceRepo: r.SourceRepo, Path: a.Path, Reason: truncate(res.err.Error(), 1000)})
				continue
			}
			switch res.out {
			case format.ImportImported:
				pageImported++
				pageBytes += res.read
			case format.ImportSkipped:
				counts.Skipped++
			default:
				counts.Ignored++
			}
		}
		counts.Imported += pageImported
		counts.Bytes += pageBytes
		counts.Checked += int64(len(page.Items))
		// The page is saved even when ctx ends now, so its imports are not redone on resume.
		last := page.Next == ""
		wctx := context.WithoutCancel(ctx)
		err = s.db.Tx(wctx, func(tx *sql.Tx) error {
			q := dbq.New(tx)
			for _, f := range fails {
				if err := q.InsertNexusSyncFailure(wctx, f); err != nil {
					return err
				}
			}
			if err := q.UpdateNexusSyncRepoProgress(wctx, dbq.UpdateNexusSyncRepoProgressParams{
				Continuation: page.Next, Checked: counts.Checked, Imported: counts.Imported, Skipped: counts.Skipped,
				Ignored: counts.Ignored, Failed: counts.Failed, Bytes: counts.Bytes, ActiveAt: time.Now().Unix(),
				SyncID: r.SyncID, SourceRepo: r.SourceRepo}); err != nil {
				return err
			}
			if pageImported > 0 {
				if err := writeLog(wctx, q, r.SyncID, r.SourceRepo, LevelInfo, fmt.Sprintf("Checked %s, copied %s (%s).",
					count(int64(len(page.Items))), count(pageImported), size(pageBytes))); err != nil {
					return err
				}
			}
			if !last {
				return nil
			}
			if err := writeLog(wctx, q, r.SyncID, r.SourceRepo, LevelInfo, doneLine(counts)); err != nil {
				return err
			}
			return q.SetNexusSyncRepoState(wctx, dbq.SetNexusSyncRepoStateParams{
				State: SyncDone, Error: "", SyncID: r.SyncID, SourceRepo: r.SourceRepo})
		})
		if err != nil {
			if ctx.Err() != nil {
				return counts, ""
			}
			return counts, "cannot save progress: " + err.Error()
		}
		if last {
			log.Info("nexus: repo synced", "imported", counts.Imported, "skipped", counts.Skipped,
				"ignored", counts.Ignored, "failed", counts.Failed)
			return counts, ""
		}
	}
}

// doneLine is the log line of a repo that was walked to the end.
func doneLine(c dbq.NexusSyncRepo) string {
	if c.Imported == 0 {
		return fmt.Sprintf("No new files. Skipped %s, failed %s.", count(c.Skipped), count(c.Failed))
	}
	return fmt.Sprintf("Done. Copied %s (%s), skipped %s, failed %s.", count(c.Imported), size(c.Bytes),
		count(c.Skipped), count(c.Failed))
}

// page fetches one asset page, retrying a failure that is not an authorization error. Each
// retry writes a warning to the log of sync id.
func (s *Service) page(ctx context.Context, cl *Client, id int64, source, token string) (AssetPage, error) {
	delay := retryDelay
	for attempt := 0; ; attempt++ {
		p, err := cl.Assets(ctx, source, token)
		if err == nil {
			return p, nil
		}
		var se *SourceError
		if ctx.Err() != nil || attempt == pageRetries || (errors.As(err, &se) && se.Unauthorized()) {
			return AssetPage{}, err
		}
		s.log.Warn("nexus: asset page failed; retrying", "source_repo", source, "error", err.Error())
		s.logLine(ctx, id, source, LevelWarn, fmt.Sprintf("Nexus did not give a page of files (%s). Try %d of %d in %s.",
			err.Error(), attempt+2, pageRetries+1, duration(delay)))
		select {
		case <-ctx.Done():
			return AssetPage{}, ctx.Err()
		case <-time.After(delay):
		}
		delay *= 2
	}
}

// importAsset offers one Nexus asset to the target format through its importer (format.Importer
// Run) and adds the bytes the downloads read to read. An asset whose target path already has its
// SHA-256 is skipped without a download. cache is the cache of the repo's import run.
func (s *Service) importAsset(ctx context.Context, cl *Client, im *format.Importer, target *repos.Repo, source string, cache *format.RunCache, a Asset, read *atomic.Int64) (format.ImportOutcome, error) {
	item := format.ImportItem{
		Cache:       cache,
		Path:        a.Path,
		Size:        a.Size,
		SHA256:      a.SHA256,
		ContentType: a.ContentType,
		CreatedAt:   a.CreatedAt,
		UpdatedAt:   a.UpdatedAt,
		Open: func(ctx context.Context) (io.ReadCloser, error) {
			rc, err := cl.Open(ctx, source, a, im.Header)
			if err != nil {
				return nil, err
			}
			return countingReader{rc, read}, nil
		},
		Fetch: func(ctx context.Context, p string, header http.Header) (io.ReadCloser, string, error) {
			h := im.Header.Clone()
			if h == nil {
				h = http.Header{}
			}
			for k, vs := range header {
				h[k] = vs
			}
			rc, ct, err := cl.Fetch(ctx, source, p, h)
			if err != nil {
				return nil, "", err
			}
			return countingReader{rc, read}, ct, nil
		},
	}
	return im.Run(ctx, s.assets, target, item)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
