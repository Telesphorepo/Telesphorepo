package nexus

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
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

// Start resumes the syncs left running by the previous process, starts the scheduler, and lets
// StartSync run syncs until ctx ends. It returns at once. A sync stopped by the end of ctx
// stays running in the database and resumes at its last saved page on the next Start.
func (s *Service) Start(ctx context.Context) {
	s.mu.Lock()
	s.root = ctx
	s.mu.Unlock()
	running, err := dbq.New(s.db.R).ListRunningNexusSyncs(ctx)
	if err != nil {
		s.log.Error("nexus: cannot look up running syncs", "error", err.Error())
	}
	s.mu.Lock()
	for _, sy := range running {
		s.log.Info("nexus: resuming sync", "sync", sy.ID, "source_id", sy.SourceID)
		s.launch(sy.ID)
	}
	s.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.schedule(ctx)
	}()
}

// schedule starts a sync of every mirroring source whose last sync ended more than
// nexus.sync_interval ago. It reads the interval from the live settings at each check.
func (s *Service) schedule(ctx context.Context) {
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
	return s.settings.Current().Duration(settings.KeyNexusSyncInterval)
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
		_, err := s.startSync(ctx, id, 0, TriggerSchedule)
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
	ctx, cancel := context.WithCancel(s.root)
	st := &runState{id: id, cancel: cancel, done: make(chan struct{})}
	s.runs[id] = st
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer close(st.done)
		defer cancel()
		s.runSync(ctx, st)
		s.mu.Lock()
		if s.runs[id] == st {
			delete(s.runs, id)
		}
		s.mu.Unlock()
	}()
}

// StartSync starts a sync of a mirroring source that has mapped repos ("Sync now"). userID 0
// means unknown.
func (s *Service) StartSync(ctx context.Context, sourceID, userID int64) (*Sync, error) {
	id, err := s.startSync(ctx, sourceID, userID, TriggerManual)
	if err != nil {
		return nil, err
	}
	return s.Sync(ctx, id)
}

// CutOver starts the final sync of a source. When it walks every repo to the end, the source's
// repos take uploads again, the source becomes cut_over and its credentials are deleted; files
// that failed to import are counted in the source's cutover_failed. When a repo fails or the
// sync is cancelled, the source stays mirroring and its last_error says why.
func (s *Service) CutOver(ctx context.Context, sourceID, userID int64) (*Sync, error) {
	id, err := s.startSync(ctx, sourceID, userID, TriggerCutover)
	if err != nil {
		return nil, err
	}
	return s.Sync(ctx, id)
}

// startLine is the first log line of a sync.
func startLine(trigger, by string) string {
	if by == "" {
		by = "an administrator"
	}
	switch trigger {
	case TriggerSave:
		return "Started after the repositories were saved."
	case TriggerSchedule:
		return "Scheduled sync started."
	case TriggerCutover:
		return "Final sync before the switch-over started by " + by + "."
	}
	return "Started by " + by + "."
}

func (s *Service) startSync(ctx context.Context, sourceID, userID int64, trigger string) (int64, error) {
	s.mu.Lock()
	started := s.root != nil
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
		if _, err := q.InsertNexusSyncRepos(ctx, dbq.InsertNexusSyncReposParams{SyncID: id, SourceID: sourceID}); err != nil {
			return err
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
		if err := writeLog(ctx, q, id, "", LevelInfo, startLine(trigger, sy.StartedByName)); err != nil {
			return err
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
		if u, err := dbq.New(s.db.R).GetUserByID(ctx, userID); err == nil {
			by = u.Username
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

// cutOver ends cut-over sync id as done and cuts the source over in one transaction: the
// mapped repos take uploads again, the source becomes cut_over and its credentials are
// deleted. The source keeps the sync id and the failed file count as a warning.
func (s *Service) cutOver(ctx context.Context, id, sourceID, failedFiles int64, line string) error {
	return s.db.Tx(ctx, func(tx *sql.Tx) error {
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
		if _, err := q.CutOverNexusSource(ctx, dbq.CutOverNexusSourceParams{CutoverSyncID: id, CutoverFailed: failedFiles, ID: sourceID}); err != nil {
			return err
		}
		mapped, err := q.ListNexusSourceRepos(ctx, sourceID)
		if err != nil {
			return err
		}
		for _, m := range mapped {
			if m.Kind != TypeHosted {
				continue
			}
			if err := s.repos.SetLockedBy(ctx, tx, m.TargetRepoID, ""); err != nil {
				return err
			}
		}
		if err := writeLog(ctx, q, id, "", LevelInfo, line+" Switched over: uploads go to this server."); err != nil {
			return err
		}
		return q.SetNexusSourceSynced(ctx, dbq.SetNexusSourceSyncedParams{LastSyncAt: now, ID: sourceID})
	})
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
// cut-over sync cuts the source over when every repo was walked to the end, even when some
// files failed; the source keeps the failed file count as a warning. When the server
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
	// A repo that Nexus marks offline cannot download its files, so it fails at once.
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
		var msg string
		var c dbq.NexusSyncRepo
		if on, ok := online[r.SourceRepo]; ok && !on {
			c, msg = r, offlineReason
		} else {
			c, msg = s.syncRepo(ctx, cl, log, r)
		}
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
				return writeLog(ctx, q, id, r.SourceRepo, LevelError, r.SourceRepo+": failed. "+msg)
			}); err != nil {
				log.Error("nexus: cannot save repo state", "source_repo", r.SourceRepo, "error", err.Error())
			}
			log.Warn("nexus: source repo failed", "source_repo", r.SourceRepo, "error", msg)
		}
	}
	started := time.Unix(sy.StartedAt, 0)
	switch {
	case failedRepos > 0 && sy.Trigger == TriggerCutover:
		fail(fmt.Sprintf("the switch-over stopped: %d of %d repositories failed; the source stays mirroring", failedRepos, len(rs)))
	case failedRepos > 0:
		fail(fmt.Sprintf("%d of %d repositories failed", failedRepos, len(rs)))
	case sy.Trigger == TriggerCutover:
		if err := s.cutOver(context.WithoutCancel(ctx), id, sy.SourceID, failedFiles, finishedLine(started, imported, bytes)); err != nil {
			log.Error("nexus: cannot cut over", "error", err.Error())
			fail("cannot cut over: " + err.Error())
			return
		}
		log.Info("nexus: source cut over", "source", src.Name, "failed_files", failedFiles)
	default:
		end(SyncDone, "", finishedLine(started, imported, bytes))
	}
}

// countingReader counts the bytes read through it.
type countingReader struct {
	io.ReadCloser
	n *int64
}

func (c countingReader) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	*c.n += int64(n)
	return n, err
}

// syncRepo syncs source repo r page by page from its saved continuation token. It returns the
// repo's counters, and "" when the repo is done or ctx ended, else the reason the repo failed.
// It writes a log line when the repo starts, for each page that imported a file, and when the
// repo is done; a page's line goes in the transaction that saves the page.
func (s *Service) syncRepo(ctx context.Context, cl *Client, log *slog.Logger, r dbq.NexusSyncRepo) (dbq.NexusSyncRepo, string) {
	counts := r
	target, err := s.repos.ByID(ctx, r.TargetRepoID)
	if err != nil {
		if ctx.Err() != nil {
			return counts, ""
		}
		return counts, "cannot load the target repo: " + err.Error()
	}
	im, ok := accepts(target.Format, r.SourceFormat)
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
		s.logLine(ctx, r.SyncID, r.SourceRepo, LevelInfo, r.SourceRepo+": started.")
	} else {
		s.logLine(ctx, r.SyncID, r.SourceRepo, LevelInfo, r.SourceRepo+": resumed after a restart.")
	}
	log = log.With("source_repo", r.SourceRepo, "target", target.Name)
	log.Info("nexus: syncing repo", "continuation_saved", r.Continuation != "")

	token := r.Continuation
	for {
		page, err := s.page(ctx, cl, r.SyncID, r.SourceRepo, token)
		if err != nil {
			if ctx.Err() != nil {
				return counts, ""
			}
			return counts, err.Error()
		}
		var fails []dbq.InsertNexusSyncFailureParams
		var pageImported, pageBytes int64
		for _, a := range page.Items {
			if ctx.Err() != nil {
				return counts, "" // the page is redone on resume
			}
			var read int64
			out, err := importAsset(ctx, cl, im, target, r.SourceRepo, a, &read)
			if err != nil {
				if ctx.Err() != nil {
					return counts, ""
				}
				counts.Failed++
				fails = append(fails, dbq.InsertNexusSyncFailureParams{
					SyncID: r.SyncID, SourceRepo: r.SourceRepo, Path: a.Path, Reason: truncate(err.Error(), 1000)})
				continue
			}
			switch out {
			case format.ImportImported:
				pageImported++
				pageBytes += read
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
				if err := writeLog(wctx, q, r.SyncID, r.SourceRepo, LevelInfo, fmt.Sprintf("%s: checked %s, copied %s (%s).",
					r.SourceRepo, count(int64(len(page.Items))), count(pageImported), size(pageBytes))); err != nil {
					return err
				}
			}
			if !last {
				return nil
			}
			if err := writeLog(wctx, q, r.SyncID, r.SourceRepo, LevelInfo, doneLine(r.SourceRepo, counts)); err != nil {
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
		token = page.Next
	}
}

// doneLine is the log line of a repo that was walked to the end.
func doneLine(repo string, c dbq.NexusSyncRepo) string {
	if c.Imported == 0 {
		return fmt.Sprintf("%s: no new files. Skipped %s, failed %s.", repo, count(c.Skipped), count(c.Failed))
	}
	return fmt.Sprintf("%s: done. Copied %s (%s), skipped %s, failed %s.", repo, count(c.Imported), size(c.Bytes),
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
		s.logLine(ctx, id, source, LevelWarn, fmt.Sprintf("%s: Nexus did not give a page of files (%s). Try %d of %d in %s.",
			source, err.Error(), attempt+2, pageRetries+1, duration(delay)))
		select {
		case <-ctx.Done():
			return AssetPage{}, ctx.Err()
		case <-time.After(delay):
		}
		delay *= 2
	}
}

// importAsset offers one Nexus asset to the target plugin and adds the bytes the plugin read to
// read. The plugin skips an asset whose path and SHA-256 already match without downloading it.
func importAsset(ctx context.Context, cl *Client, im format.Importer, target *repos.Repo, source string, a Asset, read *int64) (format.ImportOutcome, error) {
	if !validPath(a.Path) {
		return "", errors.New("invalid path")
	}
	item := format.ImportItem{
		Path:      a.Path,
		Size:      a.Size,
		SHA256:    a.SHA256,
		CreatedAt: a.CreatedAt,
		UpdatedAt: a.UpdatedAt,
		Open: func(ctx context.Context) (io.ReadCloser, error) {
			rc, err := cl.Open(ctx, source, a)
			if err != nil {
				return nil, err
			}
			return countingReader{rc, read}, nil
		},
	}
	return im.Import(ctx, target, item)
}

// validPath applies the path rules of spec §6 to a source path: non-empty segments other than
// "." and "..", at most 255 bytes each and 1024 in all, valid UTF-8, no control characters or
// backslashes.
func validPath(p string) bool {
	if p == "" || len(p) > 1024 || !utf8.ValidString(p) {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." || len(seg) > 255 {
			return false
		}
		for i := 0; i < len(seg); i++ {
			if c := seg[i]; c < 0x20 || c == 0x7F || c == '\\' {
				return false
			}
		}
	}
	return true
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
