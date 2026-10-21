// Package edge is the edge module: it keeps the R2 objects that the edge Worker (edge/) reads to
// serve downloads from Cloudflare. It writes a pointer per stored file (p/), the access objects
// of repos, users, read tokens and devices (a/), and config.json, under edge/v1/ of the home
// bucket (the S3 bucket of the server) and, for the files that cannot change, of every regional
// bucket. A pipeline watcher changes or deletes a pointer before a replacement or a remove, and
// queues the final state in the outbox (edge_outbox) in the same transaction as the commit.
package edge

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/edge/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/jobs"
	"github.com/Telesphorepo/Telesphorepo/internal/module"
	"github.com/Telesphorepo/Telesphorepo/internal/move"
	"github.com/Telesphorepo/Telesphorepo/internal/netrules"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/publish"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
	"github.com/Telesphorepo/Telesphorepo/internal/storage"
)

// PermManage edits the edge settings and reads the edge status and the Worker configuration.
const PermManage perm.ID = "edge.manage"

// ProblemNotConfigured answers a request for the Worker configuration while the settings cannot
// run edge delivery (storage fs, or a base URL with a path).
var ProblemNotConfigured = httpx.DeclareProblem(httpx.Problem{Name: "edge-not-configured", Status: http.StatusConflict,
	Title: "Edge delivery is not configured"})

// ProblemNoWorker answers a download of the edge Worker on a server whose build holds no Worker of
// its own version.
var ProblemNoWorker = httpx.DeclareProblem(httpx.Problem{Name: "no-edge-worker", Status: http.StatusNotFound,
	Title: "This build has no edge Worker download"})

// Module returns the edge module: the settings group edge, the permission edge.manage, the
// pipeline watcher, the change notices of auth, repos, network, publish and storage, the jobs
// edge.outbox, edge.backfill, edge.reconcile and edge.seen, and the routes of /api/edge/.
func Module() module.Module {
	m := &mod{kick: make(chan struct{}, 1), akick: make(chan struct{}, 1), ctl: make(chan struct{}, 1),
		countKick: make(chan struct{}, 1), sp: newSpacer(), errs: map[string]string{}, removedKeys: map[string]*view{},
		marked: map[string]bool{}}
	m.cur.Store(&view{})
	counted := make(chan struct{})
	m.counted.Store(&counted)
	return module.Module{
		ID: "edge", Title: "Edge delivery", Requires: []string{"storage", "network", "publish"},
		Register: func(x *module.Extender) {
			x.Schema(component())
			x.Permissions(perm.Subsystem{ID: "edge", Scope: perm.ScopeServer, Label: "Edge delivery",
				Help: "Downloads from Cloudflare's edge: the regions, the Worker configuration and its status."},
				perm.Def{ID: PermManage, Label: "Manage edge delivery", Help: "Turn edge delivery on or off, choose its regions, and read its status and the Worker configuration.", SecondFactor: true})
			x.Settings(&settings.Group{ID: "edge", Label: "Edge delivery", Perm: PermManage, Page: "/settings/edge",
				Defs: defs(), Validate: m.validate, Test: m.test, Apply: m.apply})
			x.Init(m.init)
			x.Job(jobs.Job{Name: "edge.outbox", Run: m.runOutbox})
			x.Job(jobs.Job{Name: "edge.backfill", Run: m.runControl})
			x.Job(jobs.Job{Name: "edge.reconcile", Every: 5 * time.Minute, Tick: m.reconcile})
			x.Job(jobs.Job{Name: "edge.seen", Every: time.Minute, Tick: m.seen})
			x.Routes(m.routes)
		},
	}
}

type mod struct {
	h     *module.Host
	q     *dbq.Queries // the write pool
	rq    *dbq.Queries // the read pool
	moves *move.Service

	cur   atomic.Pointer[view] // the buckets that the watcher and the outbox write now
	kick  chan struct{}        // wakes the outbox
	akick chan struct{}        // an access object was marked: the outbox reads them before its next pointer
	ctl   chan struct{}        // wakes the controller (edge.backfill)

	ack        atomic.Bool                   // the edge settings were saved since the last "deploy needed"
	locChanged atomic.Bool                   // a location key of the storage settings changed
	suspend    atomic.Value                  // string: why a storage move suspends edge delivery, "" for none
	counts     atomic.Value                  // outboxCounts
	countAsked atomic.Int64                  // unix ms of the last status request that read the counts
	countKick  chan struct{}                 // a status request asks for a count now
	counted    atomic.Pointer[chan struct{}] // closed by the next count
	gap        atomic.Value                  // *gapState: the last read of gap/state.json
	sp         *spacer

	errMu sync.Mutex
	errs  map[string]string // bucket label ("home" or a hint) -> the last write error, "" after a success

	clearMu     sync.Mutex
	removedKeys map[string]*view // pointer key -> the view whose buckets Removing deleted it in, until Removed

	stMu    sync.Mutex // guards home and regions, which the controller owns
	home    homeState
	regions map[string]*regionState
	marked  map[string]bool // markerID of each bucket whose bucket.json this process wrote
	pub     atomic.Value    // *published: a copy of home and regions

	cfgMu sync.Mutex // one write of config.json at a time (putConfig)

	ttlMu sync.Mutex // guards ttl
	ttl   ttlState
}

// outboxCounts are the rows of the outbox at its last count (At).
type outboxCounts struct {
	Total, Pointers int64
	LastError       string
	At              time.Time
}

func (m *mod) init(ctx context.Context, h *module.Host) error {
	m.h = h
	m.q, m.rq = dbq.New(h.DB.W), dbq.New(h.DB.R)
	m.moves = module.Use[*move.Service](h)
	m.suspend.Store("")
	m.counts.Store(outboxCounts{})
	m.gap.Store((*gapState)(nil))
	if err := m.loadState(ctx); err != nil {
		return err
	}
	if err := m.startView(ctx); err != nil {
		return err
	}
	h.Assets.Watch(watcher{m})
	h.Settings.Subscribe(m.settingsChanged)
	h.Auth.AfterChange(m.authChanged)
	h.Repos.AfterChange(m.reposChanged)
	module.Use[*netrules.Service](h).AfterRulesChange(m.rulesChanged)
	module.Use[*publish.Service](h).AfterDeviceChange(m.deviceChanged)
	m.moves.OnStart(m.moveStarted)
	h.Access.AddDownloadNote(m.downloadNote)
	return nil
}

// signal wakes the controller.
func (m *mod) signal() {
	select {
	case m.ctl <- struct{}{}:
	default:
	}
}

// wake wakes the outbox.
func (m *mod) wake() {
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

// settingsChanged runs in the settings writer: it notes a change of where the blobs are and
// wakes the controller, which does the rest.
func (m *mod) settingsChanged(old, new *settings.Snapshot) {
	if old != nil && location(storage.ConfigView.Get(old)) != location(storage.ConfigView.Get(new)) {
		m.locChanged.Store(true)
	}
	m.signal()
}

// moveStarted runs when a storage move starts copying: the home bucket changes, so edge
// delivery stops until the Worker of the new home is deployed.
func (m *mod) moveStarted(ctx context.Context, moveID int64) {
	m.suspend.Store("storage move")
	m.signal()
}

// downloadNote is the sentence of the access report: whether the edge serves the downloads of
// repo.
func (m *mod) downloadNote(ctx context.Context, repo *repos.Repo) (string, error) {
	if m.cur.Load().mode != modeOn {
		return "", nil
	}
	s := m.h.Formats.Get(repo.Format)
	if s == nil || s.Edge == nil {
		return "", nil
	}
	guarded := m.h.Access.Guarded(repo)
	if !guarded && repo.Kind == repos.KindGroup {
		members, err := m.h.Repos.MemberRepos(ctx, repo)
		if err != nil {
			return "", err
		}
		for _, mb := range members {
			guarded = guarded || m.h.Access.Guarded(mb)
		}
	}
	if guarded {
		return "Downloads go to the server: read rules apply.", nil
	}
	return "Downloads come from the edge.", nil
}

// --- buckets ---

// The modes of edge delivery.
const (
	modeOff   = iota // nothing is written
	modeOn           // the watcher and the outbox write every bucket
	modeDrain        // stopped: the pre-steps still change the old buckets until the Workers' cached config.json says off
)

// bucket is one bucket that the module writes: the home bucket or a region.
type bucket struct {
	label string // "home" or the hint
	name  string
	st    storage.Store
	cfg   storage.Config // the home bucket: its location (in memory only, it holds the secret)
}

// view is what the module writes now. It never changes: the controller swaps a new one in.
type view struct {
	mode     int
	location string // the fingerprint of the home location
	home     *bucket
	regions  []*bucket // the filling and ready regions
	gone     []*bucket // the removed regions whose drain runs: the pre-steps only
	until    time.Time // modeDrain: the pre-steps run until then; zero while the off switch is not written
	ttl      time.Duration
}

// all returns the home bucket and the regions: the buckets that the outbox writes.
func (v *view) all() []*bucket {
	if v.home == nil {
		return nil
	}
	return append([]*bucket{v.home}, v.regions...)
}

// pre returns the buckets of the pre-steps: the home bucket, the regions and the gone regions
// (each bucket once; a region added again during its drain is in both lists).
func (v *view) pre() []*bucket {
	out := v.all()
	for _, g := range v.gone {
		if !slices.ContainsFunc(out, func(b *bucket) bool { return b.label == g.label && b.name == g.name }) {
			out = append(out, g)
		}
	}
	return out
}

// writing reports whether the pre-steps change the buckets: on, or draining (until the end of
// the drain; before the off switch is written, without an end).
func (v *view) writing() bool {
	return v.mode == modeOn || (v.mode == modeDrain && (v.until.IsZero() || time.Now().Before(v.until)))
}

// opTimeout bounds one R2 call; a put that times out has an unknown outcome.
const opTimeout = 30 * time.Second

// throttled reports whether err is an R2 429 (more than one write per second to one key).
func throttled(err error) bool {
	var he interface{ HTTPStatusCode() int }
	return errors.As(err, &he) && he.HTTPStatusCode() == http.StatusTooManyRequests
}

// retry429 runs fn and tries again on a 429 for up to 2 s.
func retry429(ctx context.Context, fn func() error) error {
	deadline := time.Now().Add(2 * time.Second)
	for d := 100 * time.Millisecond; ; d *= 2 {
		err := fn()
		if err == nil || !throttled(err) || time.Now().Add(d).After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(d):
		}
	}
}

// put writes body to key under root of b.
func (m *mod) put(ctx context.Context, b *bucket, key string, body []byte) error {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	m.sp.wrote(key)
	err := b.st.Put(ctx, root+key, bytes.NewReader(body), int64(len(body)), "application/json")
	m.noteErr(b, err)
	return err
}

// del deletes keys under root of b, in one call per 1000 keys.
func (m *mod) del(ctx context.Context, b *bucket, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	full := make([]string, len(keys))
	for i, k := range keys {
		m.sp.wrote(k)
		full[i] = root + k
	}
	var err error
	if len(full) == 1 {
		err = b.st.Delete(ctx, full[0])
	} else {
		err = b.st.DeleteMany(ctx, full)
	}
	m.noteErr(b, err)
	return err
}

// noteErr keeps the last write error of b for the status page.
func (m *mod) noteErr(b *bucket, err error) {
	m.errMu.Lock()
	defer m.errMu.Unlock()
	if err == nil {
		m.errs[b.label] = ""
	} else {
		m.errs[b.label] = errText(err)
	}
}

func (m *mod) lastErr(label string) string {
	m.errMu.Lock()
	defer m.errMu.Unlock()
	return m.errs[label]
}

// errText is an error for the status page and the outbox row, at most 500 bytes.
func errText(err error) string {
	s := err.Error()
	if len(s) > 500 {
		s = s[:500]
	}
	return s
}

// spacer spaces the writes of one key by a second: R2 takes one write per second per key.
type spacer struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func newSpacer() *spacer { return &spacer{last: map[string]time.Time{}} }

func (s *spacer) wrote(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if len(s.last) > 10000 {
		for k, t := range s.last {
			if now.Sub(t) > 2*time.Second {
				delete(s.last, k)
			}
		}
	}
	s.last[key] = now
}

// wait returns how long a write of key must wait.
func (s *spacer) wait(key string) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.last[key]
	if !ok {
		return 0
	}
	return max(0, time.Second-time.Since(t))
}

// parallel runs fn on each item, at most n at a time, and returns the first error.
func parallel[T any](n int, items []T, fn func(T) error) error {
	var mu sync.Mutex
	var first error
	sem := make(chan struct{}, n)
	var wg sync.WaitGroup
	for _, it := range items {
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			if err := fn(it); err != nil {
				mu.Lock()
				if first == nil {
					first = err
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return first
}

// routes are the API of the Edge delivery page.
func (m *mod) routes(r *route.Router) {
	r.Handle("GET /api/edge/status", route.UIPerm(PermManage), m.status)
	r.Handle("GET /api/edge/wrangler.jsonc", route.UIPerm(PermManage), m.wranglerConfig)
	r.Handle("GET /api/edge/worker.zip", route.UIPerm(PermManage), m.workerZip)
}
