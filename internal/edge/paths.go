package edge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/edge/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// The classes of the paths of a repo whose format classifies them per repo (EdgeSpec.Paths: the
// overwrite rules of raw). A rule change can make an immutable path mutable, and a mutable
// replace changes only the home pointer, so a regional pointer of the old bytes would stay for a
// Worker that still reads the region. The module keeps one invariant instead of a time bound:
// every regional pointer that is not pending holds the bytes of the asset now. It holds because
//
//   - every write of a regional pointer (the outbox, a region fill, the walk) first records its rule
//     list in edge_paths, under the path lock that it holds through the put;
//   - the pre-steps treat a path as immutable when the current rules or any recorded list say so,
//     so a replace or a remove makes every regional pointer that may exist pending or deletes it;
//   - a recorded list goes only at the end of a walk (job edge.reclass) over every asset of the
//     repo that deleted the regional pointers of the paths it no longer makes immutable, and only
//     when no write used it since the walk began (the walk epoch).
//
// So a Worker that holds any repo object, old or new, reads a pointer of the current bytes, a
// pending one, or none, and the last two pass to the server.

// classSet is one recorded rule list and the walk epoch of its last use.
type classSet struct {
	rules []format.EdgeRule
	epoch int64
}

// repoClasses is the state of one repo: its recorded lists and its walk.
type repoClasses struct {
	mu      sync.Mutex
	sets    map[string]classSet // encoded rules -> set
	epoch   int64
	filled  string // the encoded rules that the regional pointers follow; "" for the format's globs
	walking string // the encoded rules of the walk that runs; "" for none
	cursor  string // the last path that the walk did
}

// classes holds the repoClasses that were loaded, and the repos whose walk the job checks.
type classes struct {
	// reset keeps a walk step and resetClasses apart. record does not take it: it runs under a
	// path lock, which a walk step may wait for, and nothing records while edge delivery is not on.
	reset sync.RWMutex
	mu    sync.Mutex
	repos map[int64]*repoClasses
	queue map[int64]bool
	kick  chan struct{}
}

func newClasses() *classes {
	return &classes{repos: map[int64]*repoClasses{}, queue: map[int64]bool{}, kick: make(chan struct{}, 1)}
}

// encodeRules is the JSON of rules as the repo object carries them: [[glob, immutable], ...].
func encodeRules(rules []format.EdgeRule) string {
	out := make([][]any, len(rules))
	for i, r := range rules {
		out[i] = []any{r.Match, r.Immutable}
	}
	b, _ := json.Marshal(out)
	return string(b)
}

func decodeRules(s string) []format.EdgeRule {
	var in [][]json.RawMessage
	if json.Unmarshal([]byte(s), &in) != nil {
		return nil
	}
	out := make([]format.EdgeRule, 0, len(in))
	for _, r := range in {
		var g string
		var imm bool
		if len(r) != 2 || json.Unmarshal(r[0], &g) != nil || json.Unmarshal(r[1], &imm) != nil {
			return nil
		}
		out = append(out, format.EdgeRule{Match: g, Immutable: imm})
	}
	return out
}

// pathRules returns the current rules of repo; ok=false when its format has no EdgeSpec.Paths.
func (m *mod) pathRules(repo *repos.Repo) ([]format.EdgeRule, bool) {
	es := m.edgeOf(repo)
	if es == nil || es.Paths == nil {
		return nil, false
	}
	return es.Paths(repo), true
}

// classOf returns the classification of repo's paths now: true for an immutable path, whose
// pointer the outbox writes to every region. The Worker reads the same classes (config.json, or
// the repo object's paths).
func (m *mod) classOf(repo *repos.Repo) func(p string) bool {
	es := m.edgeOf(repo)
	if es == nil {
		return func(string) bool { return false }
	}
	if rules, ok := m.pathRules(repo); ok {
		return func(p string) bool { return format.ImmutableBy(rules, p) }
	}
	return es.IsImmutable
}

// immutable reports whether path of repo is immutable now.
func (m *mod) immutable(repo *repos.Repo, path string) bool { return m.classOf(repo)(path) }

// guardOf returns the classification of the pre-steps: a path is immutable when the current
// rules or any recorded list of the repo say so, since a regional pointer may then exist.
func (m *mod) guardOf(ctx context.Context, repo *repos.Repo) (func(p string) bool, error) {
	cur := m.classOf(repo)
	if _, ok := m.pathRules(repo); !ok {
		return cur, nil
	}
	rc, err := m.loadClasses(ctx, repo.ID)
	if err != nil {
		return nil, err
	}
	rc.mu.Lock()
	sets := make([][]format.EdgeRule, 0, len(rc.sets))
	for _, s := range rc.sets {
		sets = append(sets, s.rules)
	}
	rc.mu.Unlock()
	return func(p string) bool {
		if cur(p) {
			return true
		}
		for _, s := range sets {
			if format.ImmutableBy(s, p) {
				return true
			}
		}
		return false
	}, nil
}

// oldImmutable reports whether a recorded list of repo makes path immutable while the current
// rules do not: the path may have regional pointers that no write keeps any more.
func (m *mod) oldImmutable(ctx context.Context, repo *repos.Repo, path string) (bool, error) {
	g, err := m.guardOf(ctx, repo)
	if err != nil {
		return false, err
	}
	return g(path) && !m.immutable(repo, path), nil
}

// loadClasses returns the state of repo, read from the database on first use.
func (m *mod) loadClasses(ctx context.Context, repoID int64) (*repoClasses, error) {
	c := m.cls
	c.mu.Lock()
	defer c.mu.Unlock()
	if rc := c.repos[repoID]; rc != nil {
		return rc, nil
	}
	rows, err := m.q.ListEdgePaths(ctx, repoID)
	if err != nil {
		return nil, err
	}
	rc := &repoClasses{sets: map[string]classSet{}}
	for _, r := range rows {
		rc.sets[r.Rules] = classSet{rules: decodeRules(r.Rules), epoch: r.Epoch}
	}
	st, err := m.q.GetReclass(ctx, repoID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	rc.epoch, rc.filled, rc.walking, rc.cursor = st.Epoch, st.Filled, st.Walking, st.Cursor
	if rc.walking != "" {
		// The walk that runs writes under its rules: they count as used in its epoch.
		if s, ok := rc.sets[rc.walking]; !ok || s.epoch < rc.epoch {
			rc.sets[rc.walking] = classSet{rules: decodeRules(rc.walking), epoch: rc.epoch}
		}
	}
	c.repos[repoID] = rc
	return rc, nil
}

// record notes that a regional pointer of repo is written under rules. The caller holds the path
// lock of the pointer through its put, and calls it before the put.
func (m *mod) record(ctx context.Context, repoID int64, rules []format.EdgeRule) error {
	rc, err := m.loadClasses(ctx, repoID)
	if err != nil {
		return err
	}
	key := encodeRules(rules)
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if s, ok := rc.sets[key]; ok && s.epoch >= rc.epoch {
		return nil
	}
	if err := m.q.UpsertEdgePaths(ctx, dbq.UpsertEdgePathsParams{RepoID: repoID, Rules: key, Epoch: rc.epoch}); err != nil {
		return err
	}
	rc.sets[key] = classSet{rules: rules, epoch: rc.epoch}
	return nil
}

// saveReclass writes the walk state of rc; the caller holds rc.mu.
func (m *mod) saveReclass(ctx context.Context, repoID int64, rc *repoClasses) error {
	return m.q.PutReclass(ctx, dbq.PutReclassParams{RepoID: repoID, Epoch: rc.epoch, Filled: rc.filled, Walking: rc.walking, Cursor: rc.cursor})
}

// resetClasses runs at a first start, after the wipe of every bucket: no regional pointer is
// left, and the backfill of every pointer writes those of the current rules (recording them), so
// each repo starts filled with its current rules. The caller holds stMu; no pre-step runs.
func (m *mod) resetClasses(ctx context.Context) error {
	m.cls.reset.Lock()
	defer m.cls.reset.Unlock()
	if err := m.q.ClearEdgePaths(ctx); err != nil {
		return err
	}
	if err := m.q.ClearReclass(ctx); err != nil {
		return err
	}
	m.cls.mu.Lock()
	clear(m.cls.repos)
	m.cls.mu.Unlock()
	all, err := m.h.Repos.List(ctx)
	if err != nil {
		return err
	}
	for _, r := range all {
		if rules, ok := m.pathRules(r); ok && r.Kind != repos.KindGroup {
			if err := m.q.PutReclass(ctx, dbq.PutReclassParams{RepoID: r.ID, Filled: encodeRules(rules)}); err != nil {
				return err
			}
		}
	}
	return nil
}

// sameClasses reports whether the encoded rules a and b (or "" for the format's globs) make the
// same paths immutable, as far as the walk can tell without the paths: equal lists, or two that
// make no path immutable.
func sameClasses(es *format.EdgeSpec, a, b string) bool {
	if a == b {
		return true
	}
	none := func(k string) bool {
		if k == "" {
			return len(es.Immutable) == 0
		}
		for _, r := range decodeRules(k) {
			if r.Immutable {
				return false
			}
		}
		return true
	}
	return none(a) && none(b)
}

// --- the walk (job edge.reclass) ---

// reclassPage is how many assets one page of a walk reads.
const reclassPage = 500

// queueClasses asks the job to check the walk of each repo of ids.
func (m *mod) queueClasses(ids ...int64) {
	if len(ids) == 0 {
		return
	}
	m.cls.mu.Lock()
	for _, id := range ids {
		m.cls.queue[id] = true
	}
	m.cls.mu.Unlock()
	select {
	case m.cls.kick <- struct{}{}:
	default:
	}
}

// queueAllClasses queues every repo of a format with EdgeSpec.Paths: at the job's start (the first
// start of a server version that classifies a format per repo walks each repo whose rules make a
// path immutable) and when edge delivery turns on.
func (m *mod) queueAllClasses(ctx context.Context) error {
	all, err := m.h.Repos.List(ctx)
	if err != nil {
		return err
	}
	var ids []int64
	for _, r := range all {
		if _, ok := m.pathRules(r); ok && r.Kind != repos.KindGroup {
			ids = append(ids, r.ID)
		}
	}
	m.queueClasses(ids...)
	return nil
}

func (m *mod) nextClass() (int64, bool) {
	m.cls.mu.Lock()
	defer m.cls.mu.Unlock()
	for id := range m.cls.queue {
		delete(m.cls.queue, id)
		return id, true
	}
	return 0, false
}

// runReclass is the job edge.reclass: while edge delivery is on, it walks each queued repo whose
// regional pointers do not follow its current rules, a page at a time.
func (m *mod) runReclass(ctx context.Context) {
	queued := false
	wait := func(d time.Duration) {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
		case <-m.cls.kick:
		case <-t.C:
		}
	}
	for ctx.Err() == nil {
		if m.cur.Load().mode != modeOn {
			queued = false
			wait(10 * time.Second)
			continue
		}
		if !queued {
			if err := m.queueAllClasses(ctx); err != nil {
				if ctx.Err() == nil {
					m.h.Log.Error("edge reclass: listing the repositories failed", "error", err.Error())
				}
				wait(10 * time.Second)
				continue
			}
			queued = true
		}
		id, ok := m.nextClass()
		if !ok {
			wait(time.Minute)
			continue
		}
		more, err := m.reclassStep(ctx, id)
		if err != nil {
			if ctx.Err() == nil {
				m.h.Log.Error("edge reclass failed", "repo", id, "error", err.Error())
			}
			wait(10 * time.Second)
			m.queueClasses(id)
			continue
		}
		if more {
			m.queueClasses(id)
		}
	}
}

// reclassStep starts the walk of repo id when its regional pointers do not follow its current
// rules, or does the next page of the walk that runs, or ends it. more: the repo has work left.
func (m *mod) reclassStep(ctx context.Context, id int64) (bool, error) {
	m.cls.reset.RLock()
	defer m.cls.reset.RUnlock()
	repo, err := m.h.Repos.ByID(ctx, id)
	if errors.Is(err, repos.ErrNotFound) {
		return false, m.dropClasses(ctx, id)
	}
	if err != nil {
		return false, err
	}
	cur, ok := m.pathRules(repo)
	if !ok || repo.Kind == repos.KindGroup {
		return false, nil
	}
	es := m.edgeOf(repo)
	rc, err := m.loadClasses(ctx, id)
	if err != nil {
		return false, err
	}
	curKey := encodeRules(cur)

	rc.mu.Lock()
	if rc.walking == "" {
		stale := false
		for k := range rc.sets {
			stale = stale || k != curKey
		}
		if !stale && sameClasses(es, rc.filled, curKey) {
			rc.mu.Unlock()
			return false, nil
		}
		// The walk state and the record of its rules commit together, and rc changes only after:
		// a walk never runs under rules that are not recorded.
		next := rc.epoch + 1
		err := m.h.DB.Tx(ctx, func(tx *sql.Tx) error {
			q := dbq.New(tx)
			if err := q.PutReclass(ctx, dbq.PutReclassParams{RepoID: id, Epoch: next, Filled: rc.filled, Walking: curKey}); err != nil {
				return err
			}
			return q.UpsertEdgePaths(ctx, dbq.UpsertEdgePathsParams{RepoID: id, Rules: curKey, Epoch: next})
		})
		if err != nil {
			rc.mu.Unlock()
			return false, err
		}
		rc.epoch, rc.walking, rc.cursor = next, curKey, ""
		rc.sets[curKey] = classSet{rules: cur, epoch: next}
		m.h.Log.Info("edge reclass: the regional pointers of a repository follow its new rules", "repo", repo.Name)
	}
	walking, epoch, cursor := rc.walking, rc.epoch, rc.cursor
	want := decodeRules(walking)
	had := es.IsImmutable // the format's globs
	if rc.filled != "" {
		filled := decodeRules(rc.filled)
		had = func(p string) bool { return format.ImmutableBy(filled, p) }
	}
	var old [][]format.EdgeRule
	for k, s := range rc.sets {
		if k != walking {
			old = append(old, s.rules)
		}
	}
	rc.mu.Unlock()

	v := m.cur.Load()
	if v.mode != modeOn {
		return false, nil // the job queues every repo again when edge delivery is on
	}
	list, next, err := m.h.Assets.List(ctx, id, "", cursor, reclassPage)
	if err != nil {
		return false, err
	}
	type job struct {
		path string
		put  bool
	}
	var jobs []job
	for _, a := range list {
		if !m.eligible(repo, a.Path) {
			continue
		}
		w, h := format.ImmutableBy(want, a.Path), had(a.Path)
		maybe := h
		for _, s := range old {
			maybe = maybe || format.ImmutableBy(s, a.Path)
		}
		switch {
		case w && !h:
			jobs = append(jobs, job{a.Path, true})
		case !w && maybe:
			jobs = append(jobs, job{a.Path, false})
		}
	}
	var far []*bucket // the regions and the gone regions: every bucket but home
	for _, b := range v.pre() {
		if b != v.home {
			far = append(far, b)
		}
	}
	if err := parallel(16, jobs, func(j job) error {
		key := pointerKey(id, j.path)
		unlock := m.h.Assets.Lock(id, j.path)
		defer unlock()
		if !j.put {
			return parallel(len(far)+1, far, func(b *bucket) error { return m.del(ctx, b, key) })
		}
		a, err := m.h.Assets.Get(ctx, id, j.path)
		if errors.Is(err, assets.ErrNotFound) {
			return nil // removed: its pointers went with it
		}
		if err != nil {
			return err
		}
		body := pointerOf(a)
		return parallel(len(v.regions)+1, v.regions, func(b *bucket) error {
			return m.putUnknown(ctx, id, j.path, m.put(ctx, b, key, body))
		})
	}); err != nil {
		return false, err
	}

	rc.mu.Lock()
	defer rc.mu.Unlock()
	if next != "" {
		rc.cursor = next
		return true, m.saveReclass(ctx, id, rc)
	}
	if len(list) > 0 {
		rc.cursor = list[len(list)-1].Path
	}
	// A put of unknown outcome may still land with a list that the end would drop; its row applies
	// the state again a minute later, which deletes such a pointer, so the walk ends after it.
	timedOut, err := m.q.RepoTimedOut(ctx, id)
	if err != nil {
		return false, err
	}
	if timedOut {
		if err := m.saveReclass(ctx, id, rc); err != nil {
			return false, err
		}
		time.AfterFunc(time.Minute, func() { m.queueClasses(id) })
		return false, nil
	}
	// The prune and the end of the walk commit together; rc follows only after.
	if err := m.h.DB.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		if err := q.PruneEdgePaths(ctx, dbq.PruneEdgePathsParams{RepoID: id, Epoch: epoch}); err != nil {
			return err
		}
		return q.PutReclass(ctx, dbq.PutReclassParams{RepoID: id, Epoch: epoch, Filled: walking})
	}); err != nil {
		return false, err
	}
	for k, s := range rc.sets {
		if s.epoch < epoch {
			delete(rc.sets, k)
		}
	}
	rc.filled, rc.walking, rc.cursor = walking, "", ""
	m.h.Log.Info("edge reclass done", "repo", repo.Name)
	return true, nil // the rules may have changed during the walk
}

// dropClasses forgets a deleted repo: its files went before its row, and its id never comes back.
func (m *mod) dropClasses(ctx context.Context, id int64) error {
	m.cls.mu.Lock()
	delete(m.cls.repos, id)
	m.cls.mu.Unlock()
	if err := m.q.DeleteEdgePaths(ctx, id); err != nil {
		return err
	}
	return m.q.DeleteReclass(ctx, id)
}
