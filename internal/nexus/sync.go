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

// Sync is a sync as the API returns it. The counters are the sums over its repos. Repos is
// set only for a single sync.
type Sync struct {
	ID         int64      `json:"id"`
	SourceID   int64      `json:"source_id"`
	State      string     `json:"state"`
	Cutover    bool       `json:"cutover"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	Error      string     `json:"error"`
	Imported   int64      `json:"imported"`
	Skipped    int64      `json:"skipped"`
	Ignored    int64      `json:"ignored"`
	Failed     int64      `json:"failed"`
	Repos      []SyncRepo `json:"repos,omitempty"`
}

// SyncRepo is one mapped repo of a sync with its counters.
type SyncRepo struct {
	SourceRepo   string `json:"source_repo"`
	SourceFormat string `json:"source_format"`
	Target       string `json:"target"`
	State        string `json:"state"`
	Error        string `json:"error"`
	Imported     int64  `json:"imported"`
	Skipped      int64  `json:"skipped"`
	Ignored      int64  `json:"ignored"`
	Failed       int64  `json:"failed"`
}

// Failure is one source file that a sync did not import.
type Failure struct {
	ID         int64  `json:"-"`
	SourceRepo string `json:"source_repo"`
	Path       string `json:"path"`
	Reason     string `json:"reason"`
}

type runState struct {
	id        int64
	cancel    context.CancelFunc
	done      chan struct{}
	cancelled bool // set by Cancel before cancel() runs
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
		_, err := s.startSync(ctx, id, 0, false)
		switch {
		case err == nil, ctx.Err() != nil:
		case errors.Is(err, ErrRunning), errors.Is(err, ErrInvalid), errors.Is(err, ErrCutOver), errors.Is(err, ErrNotFound):
			// Running already, no mapped repos yet, or changed since the list.
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

// StartSync starts a sync of a mirroring source that has mapped repos. userID 0 means
// unknown.
func (s *Service) StartSync(ctx context.Context, sourceID, userID int64) (*Sync, error) {
	id, err := s.startSync(ctx, sourceID, userID, false)
	if err != nil {
		return nil, err
	}
	return s.Sync(ctx, id)
}

// CutOver starts the final sync of a source. When it ends without a failure, the source's
// repos take uploads again, the source becomes cut_over and its credentials are deleted. When
// it fails or is cancelled, the source stays mirroring and its last_error says why.
func (s *Service) CutOver(ctx context.Context, sourceID, userID int64) (*Sync, error) {
	id, err := s.startSync(ctx, sourceID, userID, true)
	if err != nil {
		return nil, err
	}
	return s.Sync(ctx, id)
}

func (s *Service) startSync(ctx context.Context, sourceID, userID int64, cutover bool) (int64, error) {
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
		var co int64
		if cutover {
			co = 1
		}
		id, err = q.CreateNexusSync(ctx, dbq.CreateNexusSyncParams{SourceID: sourceID, Cutover: co,
			StartedBy: sql.NullInt64{Int64: userID, Valid: userID != 0}, StartedAt: time.Now().Unix()})
		if err != nil {
			return err
		}
		n, err := q.InsertNexusSyncRepos(ctx, dbq.InsertNexusSyncReposParams{SyncID: id, SourceID: sourceID})
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("%w: the source has no mapped repos", ErrInvalid)
		}
		return nil
	})
	if isUnique(err) {
		return 0, ErrRunning
	}
	if err != nil {
		return 0, err
	}
	s.log.Info("nexus: sync started", "sync", id, "source_id", sourceID, "cutover", cutover)
	s.mu.Lock()
	s.launch(id)
	s.mu.Unlock()
	return id, nil
}

func syncRow(row dbq.NexusSync, err error) (dbq.NexusSync, error) {
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

// toSync converts a row and sums its repos' counters; withRepos also lists them.
func (s *Service) toSync(ctx context.Context, row dbq.NexusSync, withRepos bool) (*Sync, error) {
	sy := &Sync{ID: row.ID, SourceID: row.SourceID, State: row.State, Cutover: row.Cutover != 0,
		StartedAt: time.Unix(row.StartedAt, 0).UTC(), Error: row.Error}
	if row.FinishedAt.Valid {
		t := time.Unix(row.FinishedAt.Int64, 0).UTC()
		sy.FinishedAt = &t
	}
	rs, err := dbq.New(s.db.R).ListNexusSyncRepos(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	if withRepos {
		sy.Repos = make([]SyncRepo, 0, len(rs))
	}
	for _, r := range rs {
		sy.Imported += r.Imported
		sy.Skipped += r.Skipped
		sy.Ignored += r.Ignored
		sy.Failed += r.Failed
		if !withRepos {
			continue
		}
		target := ""
		if t, err := s.repos.ByID(ctx, r.TargetRepoID); err == nil {
			target = t.Name
		} else if !errors.Is(err, repos.ErrNotFound) {
			return nil, err
		}
		sy.Repos = append(sy.Repos, SyncRepo{SourceRepo: r.SourceRepo, SourceFormat: r.SourceFormat, Target: target,
			State: r.State, Error: r.Error, Imported: r.Imported, Skipped: r.Skipped, Ignored: r.Ignored, Failed: r.Failed})
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
		sy, err := s.toSync(ctx, row, false)
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

// Cancel stops sync id, waits for its runner to stop, marks the sync and its unfinished repos
// cancelled, and returns it. A cancelled cut-over leaves the source mirroring.
func (s *Service) Cancel(ctx context.Context, id int64) (*Sync, error) {
	row, err := syncRow(dbq.New(s.db.R).GetNexusSync(ctx, id))
	if err != nil {
		return nil, err
	}
	if row.State != SyncRunning {
		return nil, ErrNotRunning
	}
	s.mu.Lock()
	st := s.runs[id]
	if st != nil {
		st.cancelled = true
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
	if err := s.finish(context.WithoutCancel(ctx), id, row.SourceID, SyncCancelled, ""); err != nil {
		return nil, err
	}
	return s.Sync(ctx, id)
}

// finish ends sync id and every repo of it that has not ended, and records the end on the
// source. It is a no-op for a sync that already ended.
func (s *Service) finish(ctx context.Context, id, sourceID int64, state, msg string) error {
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
// deleted.
func (s *Service) cutOver(ctx context.Context, id, sourceID int64) error {
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
		if _, err := q.CutOverNexusSource(ctx, sourceID); err != nil {
			return err
		}
		mapped, err := q.ListNexusSourceRepos(ctx, sourceID)
		if err != nil {
			return err
		}
		for _, m := range mapped {
			if err := s.repos.SetLockedBy(ctx, tx, m.TargetRepoID, ""); err != nil {
				return err
			}
		}
		return q.SetNexusSourceSynced(ctx, dbq.SetNexusSourceSyncedParams{LastSyncAt: now, ID: sourceID})
	})
}

// runSync syncs every unfinished repo of the sync, then ends it: done when every repo finished
// (item failures are listed, not fatal), failed when a repo failed, cancelled on Cancel. A
// cut-over sync cuts the source over only when no repo and no file failed. When the server
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
	end := func(state, msg string) {
		if err := s.finish(context.WithoutCancel(ctx), id, sy.SourceID, state, msg); err != nil {
			log.Error("nexus: cannot end sync", "error", err.Error())
			return
		}
		log.Info("nexus: sync ended", "state", state, "error", msg)
	}
	stopped := func() bool {
		if ctx.Err() == nil {
			return false
		}
		s.mu.Lock()
		c := st.cancelled
		s.mu.Unlock()
		if c {
			end(SyncCancelled, "")
		} else {
			log.Info("nexus: sync paused by shutdown; resumes on next start")
		}
		return true
	}

	src, err := q.GetNexusSource(ctx, sy.SourceID)
	if err != nil {
		if !stopped() {
			end(SyncFailed, "cannot load the source: "+err.Error())
		}
		return
	}
	cl, err := s.client(src)
	if err != nil {
		end(SyncFailed, err.Error())
		return
	}
	rs, err := q.ListNexusSyncRepos(ctx, id)
	if err != nil {
		if !stopped() {
			end(SyncFailed, "cannot load the sync repos: "+err.Error())
		}
		return
	}
	var failedRepos int
	var failedFiles int64
	for _, r := range rs {
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
		failed, msg := s.syncRepo(ctx, cl, log, r)
		if stopped() {
			return
		}
		failedFiles += failed
		if msg != "" {
			failedRepos++
			if err := dbq.New(s.db.W).SetNexusSyncRepoState(ctx, dbq.SetNexusSyncRepoStateParams{
				State: SyncFailed, Error: msg, SyncID: id, SourceRepo: r.SourceRepo}); err != nil {
				log.Error("nexus: cannot save repo state", "source_repo", r.SourceRepo, "error", err.Error())
			}
			log.Warn("nexus: source repo failed", "source_repo", r.SourceRepo, "error", msg)
		}
	}
	switch {
	case failedRepos > 0 && sy.Cutover != 0:
		end(SyncFailed, fmt.Sprintf("cut-over stopped: %d of %d repos failed; the source stays mirroring", failedRepos, len(rs)))
	case failedRepos > 0:
		end(SyncFailed, fmt.Sprintf("%d of %d repos failed", failedRepos, len(rs)))
	case sy.Cutover != 0 && failedFiles > 0:
		end(SyncFailed, fmt.Sprintf("cut-over stopped: %d files failed to import; the source stays mirroring", failedFiles))
	case sy.Cutover != 0:
		if err := s.cutOver(context.WithoutCancel(ctx), id, sy.SourceID); err != nil {
			log.Error("nexus: cannot cut over", "error", err.Error())
			end(SyncFailed, "cannot cut over: "+err.Error())
			return
		}
		log.Info("nexus: source cut over", "source", src.Name)
	default:
		end(SyncDone, "")
	}
}

// syncRepo syncs source repo r page by page from its saved continuation token. It returns the
// repo's failed file count, and "" when the repo is done or ctx ended, else the reason the
// repo failed.
func (s *Service) syncRepo(ctx context.Context, cl *Client, log *slog.Logger, r dbq.NexusSyncRepo) (int64, string) {
	target, err := s.repos.ByID(ctx, r.TargetRepoID)
	if err != nil {
		if ctx.Err() != nil {
			return r.Failed, ""
		}
		return r.Failed, "cannot load the target repo: " + err.Error()
	}
	im, ok := accepts(target.Format, r.SourceFormat)
	if !ok {
		return r.Failed, fmt.Sprintf("target repo %q (%s) cannot import %s content", target.Name, target.Format, r.SourceFormat)
	}
	w := dbq.New(s.db.W)
	if r.State != SyncRunning {
		if err := w.SetNexusSyncRepoState(ctx, dbq.SetNexusSyncRepoStateParams{
			State: SyncRunning, Error: "", SyncID: r.SyncID, SourceRepo: r.SourceRepo}); err != nil {
			if ctx.Err() != nil {
				return r.Failed, ""
			}
			return r.Failed, "cannot save state: " + err.Error()
		}
	}
	log = log.With("source_repo", r.SourceRepo, "target", target.Name)
	log.Info("nexus: syncing repo", "continuation_saved", r.Continuation != "")

	counts := r
	token := r.Continuation
	for {
		page, err := s.page(ctx, cl, r.SourceRepo, token)
		if err != nil {
			if ctx.Err() != nil {
				return counts.Failed, ""
			}
			return counts.Failed, err.Error()
		}
		var fails []dbq.InsertNexusSyncFailureParams
		for _, a := range page.Items {
			if ctx.Err() != nil {
				return counts.Failed, "" // the page is redone on resume
			}
			out, err := importAsset(ctx, cl, im, target, r.SourceRepo, a)
			if err != nil {
				if ctx.Err() != nil {
					return counts.Failed, ""
				}
				counts.Failed++
				fails = append(fails, dbq.InsertNexusSyncFailureParams{
					SyncID: r.SyncID, SourceRepo: r.SourceRepo, Path: a.Path, Reason: truncate(err.Error(), 1000)})
				continue
			}
			switch out {
			case format.ImportImported:
				counts.Imported++
			case format.ImportSkipped:
				counts.Skipped++
			default:
				counts.Ignored++
			}
		}
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
				Continuation: page.Next, Imported: counts.Imported, Skipped: counts.Skipped,
				Ignored: counts.Ignored, Failed: counts.Failed, SyncID: r.SyncID, SourceRepo: r.SourceRepo}); err != nil {
				return err
			}
			if last {
				return q.SetNexusSyncRepoState(wctx, dbq.SetNexusSyncRepoStateParams{
					State: SyncDone, Error: "", SyncID: r.SyncID, SourceRepo: r.SourceRepo})
			}
			return nil
		})
		if err != nil {
			if ctx.Err() != nil {
				return counts.Failed, ""
			}
			return counts.Failed, "cannot save progress: " + err.Error()
		}
		if last {
			log.Info("nexus: repo synced", "imported", counts.Imported, "skipped", counts.Skipped,
				"ignored", counts.Ignored, "failed", counts.Failed)
			return counts.Failed, ""
		}
		token = page.Next
	}
}

// page fetches one asset page, retrying a failure that is not an authorization error.
func (s *Service) page(ctx context.Context, cl *Client, source, token string) (AssetPage, error) {
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
		select {
		case <-ctx.Done():
			return AssetPage{}, ctx.Err()
		case <-time.After(delay):
		}
		delay *= 2
	}
}

// importAsset offers one Nexus asset to the target plugin. The plugin skips an asset whose
// path and SHA-256 already match without downloading it.
func importAsset(ctx context.Context, cl *Client, im format.Importer, target *repos.Repo, source string, a Asset) (format.ImportOutcome, error) {
	if !validPath(a.Path) {
		return "", errors.New("invalid path")
	}
	item := format.ImportItem{
		Path:   a.Path,
		Size:   a.Size,
		SHA256: a.SHA256,
		Open: func(ctx context.Context) (io.ReadCloser, error) {
			return cl.Open(ctx, source, a)
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
