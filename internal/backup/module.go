package backup

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/blobs"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/jobs"
	"github.com/Telesphorepo/Telesphorepo/internal/module"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
	"github.com/Telesphorepo/Telesphorepo/internal/storage"
)

// PermManage edits the backup settings, starts a backup and lists the backups.
const PermManage perm.ID = "backup.manage"

// The audit actions of the backup module.
const (
	ActionDone   = "backup.done"
	ActionFailed = "backup.failed"
)

// The settings of the group "backup".
var (
	Dir = settings.String(&settings.Def{Key: "backup.dir", Label: "Backup folder", Normalize: settings.Trim,
		Help: "The server writes its backups into this folder, best on another disk. Leave it empty to turn backups off.",
		Validate: func(v any) error {
			if s := v.(string); s != "" && !filepath.IsAbs(s) {
				return errors.New("must be an absolute path")
			}
			return nil
		}})
	Interval = settings.Duration(&settings.Def{Key: "backup.interval", Default: "24h", Label: "Backup interval",
		Help: "The server writes a backup when the newest one is this old.",
		Validate: func(v any) error {
			if v.(time.Duration) < time.Minute {
				return errors.New("must be at least 1 minute")
			}
			return nil
		}})
	Keep = settings.Int(&settings.Def{Key: "backup.keep", Default: "7", Label: "Backups to keep",
		Help: "The server keeps this many of the newest backups and deletes the older ones.",
		Validate: func(v any) error {
			if v.(int64) > 1000 {
				return errors.New("must be at most 1000")
			}
			return nil
		}})
)

// tickEvery is how often the schedule checks whether a backup is due.
const tickEvery = time.Minute

// retryAfter is the longest wait after a failed scheduled backup before the next try.
const retryAfter = time.Hour

// Module returns the backup module: the settings group "backup", the job that writes a backup
// when the newest one is older than the interval, and GET /api/backup and POST /api/backup/run.
func Module() module.Module {
	m := &mod{}
	return module.Module{
		ID: "backup", Title: "Backups",
		Register: func(x *module.Extender) {
			x.Permissions(perm.Subsystem{ID: "backup", Scope: perm.ScopeServer, Label: "Backups",
				Help: "Copies of the database, the master key and the stored files in a folder."},
				perm.Def{ID: PermManage, Label: "Manage backups", Help: "Choose the backup folder and schedule, back up now and see the backups.", SecondFactor: true})
			x.AuditActions(
				audit.ActionDef{ID: ActionDone, Label: "Backup written", Subsystem: "backup"},
				audit.ActionDef{ID: ActionFailed, Label: "Backup failed", Subsystem: "backup"},
			)
			x.Settings(&settings.Group{ID: "backup", Label: "Backups", Perm: PermManage, Page: "/settings/backups",
				Defs: []*settings.Def{Dir.Def(), Interval.Def(), Keep.Def()}, Test: test})
			x.Init(func(ctx context.Context, h *module.Host) error {
				m.h = h
				return nil
			})
			x.Job(jobs.Job{Name: "backup.schedule", Every: tickEvery, Tick: m.tick})
			x.Routes(func(r *route.Router) {
				r.Handle("GET /api/backup", route.UIPerm(PermManage), m.get)
				r.Handle("POST /api/backup/run", route.UIPerm(PermManage), m.start)
			})
		},
	}
}

// test checks that the server can write to the backup folder of the draft
// (POST /api/settings/backup/test).
func test(_ context.Context, d *settings.Draft) error {
	dir := Dir.Get(d.Next)
	if dir == "" {
		return &settings.HookError{Status: http.StatusBadRequest, Message: "no backup folder is set",
			Fields: map[string]string{Dir.Name(): "set a folder first"}}
	}
	fail := func(err error) error {
		msg := "the server cannot write to the folder: " + err.Error()
		return &settings.HookError{Status: http.StatusBadRequest, Message: msg, Fields: map[string]string{Dir.Name(): msg}}
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fail(err)
	}
	f, err := os.CreateTemp(dir, tmpPrefix+"probe-*")
	if err != nil {
		return fail(err)
	}
	_, err = f.WriteString("telesphorepo backup probe\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if rerr := os.Remove(f.Name()); err == nil {
		err = rerr
	}
	if err != nil {
		return fail(err)
	}
	return nil
}

type mod struct {
	h *module.Host

	mu       sync.Mutex
	job      Job
	failedAt time.Time // the end of the last failed run, for the retry wait
}

// Job is the state of the backup job of this process.
type Job struct {
	Running   bool       `json:"running"`
	StartedAt *time.Time `json:"started_at"`
	Progress  *Progress  `json:"progress"`
	// LastAt and LastError are the end and the error ("" for none) of the last run of this process.
	LastAt    *time.Time `json:"last_at"`
	LastError string     `json:"last_error"`
}

// begin marks a run as started; false when one runs.
func (m *mod) begin() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.job.Running {
		return false
	}
	now := time.Now().UTC()
	m.job.Running, m.job.StartedAt, m.job.Progress = true, &now, &Progress{Step: "files"}
	return true
}

func (m *mod) state() Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.job
	if j.Progress != nil {
		p := *j.Progress
		j.Progress = &p
	}
	return j
}

// due reports when the next scheduled backup is due in dir: the newest complete backup plus the
// interval, now when there is none, and not before the retry wait after a failed run.
func (m *mod) due(dir string, interval time.Duration) (time.Time, error) {
	entries, err := List(dir)
	if err != nil {
		return time.Time{}, err
	}
	next := time.Now()
	for _, e := range entries {
		if e.Complete {
			next = e.CreatedAt.Add(interval)
			break
		}
	}
	m.mu.Lock()
	failed := m.failedAt
	m.mu.Unlock()
	if !failed.IsZero() {
		if retry := failed.Add(min(interval, retryAfter)); retry.After(next) {
			next = retry
		}
	}
	return next, nil
}

// tick writes a backup when one is due.
func (m *mod) tick(ctx context.Context) error {
	snap := m.h.Settings.Current()
	dir := Dir.Get(snap)
	if dir == "" {
		return nil
	}
	next, err := m.due(dir, Interval.Get(snap))
	if err != nil {
		return err
	}
	if time.Now().Before(next) || !m.begin() {
		return nil
	}
	m.run(ctx, dir)
	return nil
}

// run writes one backup into dir and applies the retention. It records the outcome in the audit
// log and the server log.
func (m *mod) run(ctx context.Context, dir string) {
	h := m.h
	snap := h.Settings.Current()
	start := time.Now()
	src := Source{
		DB: h.DB.R, DataDir: h.Boot.DataDir, MasterKeyEnv: h.Boot.MasterKey != "", Version: h.Version,
		Storage: storage.ConfigView.Get(snap),
		Get: func(ctx context.Context, key string) (io.ReadCloser, storage.ObjectInfo, error) {
			return h.Storage.Get(ctx, key)
		},
		Hold: func(ctx context.Context, sha256 string) (func(), bool, error) {
			release := h.Blobs.Lock([]string{sha256})
			_, err := h.Blobs.Get(ctx, sha256)
			if errors.Is(err, blobs.ErrUnknown) {
				return release, false, nil
			}
			if err != nil {
				release()
				return nil, false, err
			}
			return release, true, nil
		},
		Progress: func(p Progress) {
			m.mu.Lock()
			m.job.Progress = &p
			m.mu.Unlock()
		},
	}
	res, err := Run(ctx, dir, src)
	var pruned Pruned
	if err == nil {
		src.Progress(Progress{Step: "cleanup", Copied: res.Copied})
		pruned, err = Prune(ctx, dir, int(Keep.Get(snap)))
		if err != nil {
			err = errors.New("the backup " + res.Name + " is written, but the old backups could not be removed: " + err.Error())
		}
	}

	now := time.Now().UTC()
	m.mu.Lock()
	m.job = Job{LastAt: &now}
	if err != nil {
		m.job.LastError = err.Error()
		m.failedAt = now
	} else {
		m.failedAt = time.Time{}
	}
	m.mu.Unlock()

	actx := context.WithoutCancel(ctx)
	if err != nil {
		h.Log.Error("backup failed", "dir", dir, "error", err.Error())
		h.Audit.Record(actx, audit.Event{Action: ActionFailed, Target: dir, Detail: map[string]any{"error": err.Error()}})
		return
	}
	man := res.Manifest
	h.Log.Info("backup written", "backup", res.Name, "dir", dir, "storage", man.Storage, "blobs", man.Counts.Blobs,
		"copied", res.Copied, "size", man.Size(), "removed", len(pruned.Backups), "removed_blobs", pruned.Blobs,
		"duration_ms", time.Since(start).Milliseconds())
	h.Audit.Record(actx, audit.Event{Action: ActionDone, Target: res.Name, Detail: map[string]any{
		"dir": dir, "storage": man.Storage, "blobs": man.Counts.Blobs, "copied": res.Copied, "size": man.Size(),
		"master_key_included": man.MasterKeyIncluded, "removed": pruned.Backups, "removed_blobs": pruned.Blobs,
	}})
}

// Listed is one backup as GET /api/backup shows it.
type Listed struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	Complete  bool      `json:"complete"`
	Version   string    `json:"version"`
	Storage   string    `json:"storage"`
	// Size is the database and the files it uses: what a restore brings back.
	Size              int64 `json:"size"`
	Files             int64 `json:"files"`
	BlobsIncluded     bool  `json:"blobs_included"`
	MasterKeyIncluded bool  `json:"master_key_included"`
}

// Overview is the answer of GET /api/backup.
type Overview struct {
	Job     Job        `json:"job"`
	NextAt  *time.Time `json:"next_at"` // nil while backups are off
	Backups []Listed   `json:"backups"` // newest first
	// Error says why the folder could not be read ("" for none).
	Error string `json:"error"`
}

// get lists the backups of the backup folder and the state of the job.
func (m *mod) get(w http.ResponseWriter, r *http.Request, _ *route.Call) {
	snap := m.h.Settings.Current()
	out := Overview{Job: m.state(), Backups: []Listed{}}
	if dir := Dir.Get(snap); dir != "" {
		entries, err := List(dir)
		if err != nil {
			out.Error = "the server cannot read the backup folder: " + err.Error()
		}
		for _, e := range entries {
			l := Listed{Name: e.Name, CreatedAt: e.CreatedAt, Complete: e.Complete}
			if mf := e.Manifest; mf != nil {
				l.Version, l.Storage, l.Size, l.Files = mf.Version, mf.Storage, mf.Size(), mf.Counts.Files
				l.BlobsIncluded, l.MasterKeyIncluded = mf.BlobsIncluded, mf.MasterKeyIncluded
			}
			out.Backups = append(out.Backups, l)
		}
		if err == nil {
			if next, err := m.due(dir, Interval.Get(snap)); err == nil {
				next = next.UTC()
				out.NextAt = &next
			}
		}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// start begins a backup now: 202 with the job, 409 while one runs, 400 without a backup folder.
func (m *mod) start(w http.ResponseWriter, r *http.Request, _ *route.Call) {
	dir := Dir.Get(m.h.Settings.Current())
	if dir == "" {
		httpx.Error(w, r, http.StatusBadRequest, "set the backup folder first")
		return
	}
	if !m.begin() {
		httpx.Error(w, r, http.StatusConflict, "a backup is running; wait for it to end")
		return
	}
	actor := audit.ActorOf(r.Context())
	m.h.Go("backup.run", func(ctx context.Context) { m.run(audit.WithActor(ctx, actor), dir) })
	httpx.WriteJSON(w, http.StatusAccepted, m.state())
}
