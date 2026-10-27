package edge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/edge/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
	"github.com/Telesphorepo/Telesphorepo/internal/storage"
)

// homeState is the row "home" of edge_state.
type homeState struct {
	// Location is the fingerprint of the home location that the objects under edge/v1/ were
	// written for; Started: they were wiped there and the backfill of every pointer began.
	Location string `json:"location"`
	Started  bool   `json:"started"`
	// Seen is the fingerprint of the home location at the last check: a change while edge
	// delivery is on or waits for a deploy needs a new deploy.
	Seen string `json:"seen"`
	// The backfill of every pointer: the last asset id queued, done (every pointer queued), and
	// the counts. Ready: the outbox wrote every row of the backfill; config.json says home ready
	// from then on, and the Worker answers nothing before (a missing pointer means "not written
	// yet" until then, not "no file").
	Cursor int64 `json:"cursor"`
	Done   bool  `json:"done"`
	Ready  bool  `json:"ready"`
	Queued int64 `json:"queued"`
	Total  int64 `json:"total"`
	// Gone are the removed ready regions (a hint taken out of edge.regions, or the old bucket of a
	// hint that got a new one) whose drain runs: config.json no longer lists them, and the pre-steps
	// still change their buckets until every Worker's cached config.json is one without them.
	Gone []goneRegion `json:"gone,omitempty"`
	// Drain is the drain that runs (edge off, a location change or a storage move), nil for none.
	Drain *drainRecord `json:"drain,omitempty"`
	// DeployNeeded: the home bucket changed (a storage change or move); edge delivery stays off
	// until the edge settings are saved again after the Worker of the new home is deployed.
	DeployNeeded bool   `json:"deploy_needed"`
	Reason       string `json:"reason,omitempty"`
}

// regionState is the row "region:<hint>" of edge_state.
type regionState struct {
	Hint   string `json:"hint"`
	Bucket string `json:"bucket"`
	Ready  bool   `json:"ready"`
	// Cleared: the stale pointers of the bucket are deleted. ViaHome: the region was there at a
	// first start, so the backfill of every pointer writes its pointers; else its own backfill
	// (Cursor, Done, Written, Total) does.
	Cleared bool  `json:"cleared"`
	ViaHome bool  `json:"via_home"`
	Cursor  int64 `json:"cursor"`
	Done    bool  `json:"done"`
	Written int64 `json:"written"`
	Total   int64 `json:"total"`
	// The blob check: the keys of the home blobs/ up to Verified are in the region. Pass is the
	// last key of the running pass ("" between passes) and PassMissing what it found missing so
	// far; Missing is the count of the last whole pass (nil before one), and MissingSince (unix
	// seconds) the end of the pass that last changed it.
	Verified     string `json:"verified"`
	Pass         string `json:"pass"`
	PassMissing  int64  `json:"pass_missing"`
	Missing      *int64 `json:"missing"`
	MissingSince int64  `json:"missing_since"`
	CheckedAt    int64  `json:"checked_at"`
	GapAt        int64  `json:"gap_at"` // the last gap/request.json for this region
}

// goneRegion is a removed region while its drain runs. Until is unix milliseconds, 0 while the
// config.json without it is not written yet.
type goneRegion struct {
	Hint   string `json:"hint"`
	Bucket string `json:"bucket"`
	Until  int64  `json:"until"`
}

func regionKey(hint string) string { return "region:" + hint }

// pointersDone reports whether every pointer of the region is written: its own backfill wrote
// them, or the outbox wrote every row of the backfill of every pointer.
func (r *regionState) pointersDone(home *homeState) bool {
	return r.Done || (r.ViaHome && home.Ready)
}

// draining reports whether bucket of hint is a gone region whose drain runs.
func (h *homeState) draining(hint, bucket string) bool {
	return slices.ContainsFunc(h.Gone, func(g goneRegion) bool { return g.Hint == hint && g.Bucket == bucket })
}

// ttlState is the row "ttl" of edge_state: Pub is the access_ttl (seconds) of the last
// config.json written, and HoldUntil (unix milliseconds) the latest time at which a Worker can
// still hold an earlier config.json: each write supersedes the one before, which a Worker holds
// for at most its own access_ttl from then.
type ttlState struct {
	Pub       int64 `json:"pub"`
	HoldUntil int64 `json:"hold_until"`
}

// notePublished records a put of config.json with access_ttl ttl (seconds). After a failed put
// the bucket holds the old or the new object, so Pub is the larger of the two.
func (m *mod) notePublished(ctx context.Context, ttl int64, ok bool) {
	m.ttlMu.Lock()
	defer m.ttlMu.Unlock()
	m.ttl.HoldUntil = max(m.ttl.HoldUntil, nowMS()+m.ttl.Pub*1000)
	if ok {
		m.ttl.Pub = ttl
	} else {
		m.ttl.Pub = max(m.ttl.Pub, ttl)
	}
	b, _ := json.Marshal(m.ttl)
	if err := m.q.PutState(ctx, dbq.PutStateParams{Key: "ttl", Value: string(b)}); err != nil {
		m.h.Log.Warn("edge: saving the cache time state failed", "error", err.Error())
	}
}

// drainEnd is when no Worker holds a config.json written before now any more, with a margin:
// the end of a drain that starts with a config.json written just before.
func (m *mod) drainEnd() time.Time {
	m.ttlMu.Lock()
	defer m.ttlMu.Unlock()
	return time.UnixMilli(max(m.ttl.HoldUntil, nowMS())).Add(5 * time.Second)
}

// loadState reads edge_state.
func (m *mod) loadState(ctx context.Context) error {
	m.regions = map[string]*regionState{}
	raw, err := m.rq.GetState(ctx, "home")
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		if err := json.Unmarshal([]byte(raw), &m.home); err != nil {
			return fmt.Errorf("edge state: %w", err)
		}
	}
	for _, h := range Hints {
		raw, err := m.rq.GetState(ctx, regionKey(h))
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		var r regionState
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			return fmt.Errorf("edge state of %s: %w", h, err)
		}
		m.regions[h] = &r
	}
	raw, err = m.rq.GetState(ctx, "ttl")
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		if err := json.Unmarshal([]byte(raw), &m.ttl); err != nil {
			return fmt.Errorf("edge cache time state: %w", err)
		}
	}
	if err := m.loadPointers(ctx); err != nil {
		return err
	}
	m.publish()
	return nil
}

// saveHome writes the row "home"; the caller holds stMu.
func (m *mod) saveHome(ctx context.Context) error {
	m.publish()
	b, _ := json.Marshal(m.home)
	return m.q.PutState(ctx, dbq.PutStateParams{Key: "home", Value: string(b)})
}

// saveRegion writes the row of r; the caller holds stMu.
func (m *mod) saveRegion(ctx context.Context, r *regionState) error {
	m.publish()
	b, _ := json.Marshal(r)
	return m.q.PutState(ctx, dbq.PutStateParams{Key: regionKey(r.Hint), Value: string(b)})
}

// dropRegion deletes the row of the region hint; the caller holds stMu.
func (m *mod) dropRegion(ctx context.Context, hint string) error {
	delete(m.regions, hint)
	m.publish()
	return m.q.DeleteState(ctx, regionKey(hint))
}

// published is a copy of the state for the readers outside the controller: config.json, the
// outbox and the status route.
type published struct {
	home    homeState
	regions []regionState // in the order of Hints
}

// publish copies the state; the caller holds stMu.
func (m *mod) publish() {
	p := &published{home: m.home}
	for _, h := range Hints {
		if r := m.regions[h]; r != nil {
			p.regions = append(p.regions, *r)
		}
	}
	m.pub.Store(p)
}

func (m *mod) published() *published {
	if p, _ := m.pub.Load().(*published); p != nil {
		return p
	}
	return &published{}
}

// location is the whole location of a store config, the secret included, to compare two
// snapshots in memory. It is never stored or logged.
func location(c storage.Config) string {
	return strings.Join([]string{c.Backend, c.FSRoot, c.S3Endpoint, c.S3Region, c.S3Bucket, c.S3AccessKeyID,
		c.S3SecretAccessKey, c.S3Prefix, fmt.Sprint(c.S3PathStyle)}, "\x00")
}

// fingerprint is the stored fingerprint of the home location: no secret.
func fingerprint(c storage.Config) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{c.Backend, c.S3Endpoint, c.S3Region, c.S3Bucket, c.S3AccessKeyID,
		c.S3Prefix, fmt.Sprint(c.S3PathStyle)}, "\x00")))
	return hex.EncodeToString(sum[:16])
}

// runControl is the job edge.backfill: it follows the settings (on, off, the home location and
// the regions), runs the backfill of the pointers, clears and fills new regions and marks them
// ready. It wakes on a settings change, a storage move, and every 10 seconds.
func (m *mod) runControl(ctx context.Context) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		more, err := m.step(ctx)
		if err != nil && ctx.Err() == nil {
			m.h.Log.Error("edge delivery step failed", "error", err.Error())
		}
		if more && err == nil {
			m.signal()
		}
		select {
		case <-ctx.Done():
			m.closeView()
			return
		case <-m.ctl:
		case <-t.C:
		}
	}
}

// closeView drops the view at shutdown.
func (m *mod) closeView() { m.cur.Store(&view{}) }

// step brings the module to the state of the settings and does one bounded unit of the backfill.
// more: work is left.
func (m *mod) step(ctx context.Context) (bool, error) {
	m.stMu.Lock()
	defer m.stMu.Unlock()
	m.checkToken(ctx)
	if err := m.sync(ctx); err != nil {
		return false, err
	}
	v := m.cur.Load()
	if v.mode != modeOn {
		// The page turns edge delivery on only after the Worker has reported, so the
		// Worker's state is read while off too, once the home location is chosen.
		if HomeHint.Get(m.h.Settings.Current()) != "" {
			if hb, err := m.gapHome(ctx); err == nil {
				m.readGap(ctx, hb, false)
			}
		}
		return false, nil
	}
	m.readGap(ctx, v.home, false)
	m.checkPurge(ctx)
	m.writeMarkers(ctx, v)
	more, err := m.backfillHome(ctx, v)
	if err != nil {
		return false, err
	}
	if err := m.homeReady(ctx); err != nil {
		return false, err
	}
	for _, b := range v.regions {
		r := m.regions[b.label]
		if r == nil || !m.marked[markerID(b)] {
			continue // a region becomes ready only with its bucket.json
		}
		rm, err := m.fillRegion(ctx, v, b, r)
		if err != nil {
			m.noteErr(b, err)
			continue
		}
		more = more || rm
	}
	return more, nil
}

// sync applies the settings. The caller holds stMu.
func (m *mod) sync(ctx context.Context) error {
	snap := m.h.Settings.Current()
	cfg := storage.ConfigView.Get(snap)
	loc := fingerprint(cfg)
	v := m.cur.Load()
	active := Enabled.Get(snap) && unfit(snap) == ""

	// The home location changed: a deploy for the new home is needed.
	moved := m.locChanged.Swap(false) || (m.home.Seen != "" && m.home.Seen != loc)
	reason, _ := m.suspend.Swap("").(string)
	if moved && reason == "" {
		reason = "storage change"
	}
	if reason != "" && (v.mode != modeOff || m.home.Started || m.home.DeployNeeded) {
		m.home.DeployNeeded, m.home.Reason = true, reason
		m.ack.Store(false)
		if v.mode == modeOn {
			m.drain(ctx, v)
		}
	}
	if reason != "" || m.home.Seen != loc {
		m.home.Seen = loc
		if err := m.saveHome(ctx); err != nil {
			return err
		}
	}

	v = m.cur.Load()
	switch v.mode {
	case modeOn:
		if !active || v.location != loc {
			m.drain(ctx, v)
			return m.saveHome(ctx)
		}
		return m.syncRegions(ctx, v, snap)
	case modeDrain:
		if v.until.IsZero() {
			// The off switch is not written yet.
			m.drainStart(ctx, v)
			return nil
		}
		if time.Now().Before(v.until) {
			return nil
		}
		// Every cached config.json says off now: nothing is written any more.
		if err := m.q.ClearOutbox(ctx); err != nil {
			return err
		}
		m.cur.Store(&view{})
		m.home.Gone, m.home.Drain = nil, nil
		if err := m.saveHome(ctx); err != nil {
			return err
		}
		m.h.Log.Info("edge delivery is off")
	}

	// Off.
	if !active {
		if m.home.Started && m.home.Location == loc && cfg.Backend == "s3" {
			// Turned off while the server was down: write the off switch to the home bucket.
			if hb, err := m.openHome(ctx, cfg); err == nil {
				_ = m.write(ctx, &view{home: hb}, configKey, m.configJSON(false, AccessTTL.Get(snap)), true)
			}
		}
		if m.home.Started {
			m.home.Started = false
			return m.saveHome(ctx)
		}
		return nil
	}
	if m.home.DeployNeeded && (!m.ack.Load() || m.moves.Running()) {
		return nil
	}
	if m.home.Started && m.home.Location == loc && !m.home.DeployNeeded {
		return m.resume(ctx, snap, cfg, loc)
	}
	return m.firstStart(ctx, snap, cfg, loc)
}

// drain stops edge delivery in v: it writes config.json with enabled false to the home bucket,
// and the pre-steps keep changing the buckets of v (the gone regions too) until every Worker's
// cached config.json says off: until no Worker holds a config.json written before the off switch
// (the largest access_ttl that one of them carried, see ttlState), and a margin. The drain
// starts when the off switch is written; the controller writes it again until a put succeeds.
// Then the outbox is dropped.
func (m *mod) drain(ctx context.Context, v *view) {
	d := &view{mode: modeDrain, location: v.location, home: v.home, regions: v.regions, gone: v.gone,
		ttl: AccessTTL.Get(m.h.Settings.Current())}
	m.cur.Store(d)
	m.home.Started = false
	rec := &drainRecord{Location: d.location, Home: refOf(d.home.cfg), TTL: int64(d.ttl / time.Second)}
	for _, b := range append(append([]*bucket{}, d.regions...), d.gone...) {
		rec.Regions = append(rec.Regions, goneRegion{Hint: b.label, Bucket: b.name})
	}
	m.home.Drain = rec
	m.saveDrain(ctx)
	m.drainStart(ctx, d)
}

// drainStart writes the off switch of the draining view d and, after a put that succeeded,
// swaps in d with the end of the drain and saves it. The caller holds stMu.
func (m *mod) drainStart(ctx context.Context, d *view) {
	if err := m.putConfig(ctx, true); err != nil {
		m.h.Log.Warn("edge: writing config.json (off) failed", "error", err.Error())
		return
	}
	nd := *d
	nd.until = m.drainEnd()
	m.cur.Store(&nd)
	if m.home.Drain != nil {
		m.home.Drain.Until = nd.until.UnixMilli()
		m.saveDrain(ctx)
	}
	m.h.Log.Info("edge delivery stops", "until", nd.until.Format(time.RFC3339))
}

// saveDrain saves the row "home" with the drain record; a failure is logged (the drain runs in
// memory, and the next save of the row writes it). The caller holds stMu.
func (m *mod) saveDrain(ctx context.Context) {
	if err := m.saveHome(ctx); err != nil {
		m.h.Log.Warn("edge: saving the drain failed", "error", err.Error())
	}
}

// drainRecord is a drain in edge_state, so a restart during it keeps the pre-steps running
// until its end: the old home location without its secret, the regional buckets, the cache time
// of the off switch (seconds), and the end (unix milliseconds, 0 while the off switch is not
// written yet).
type drainRecord struct {
	Until    int64        `json:"until"`
	Location string       `json:"location"`
	Home     storeRef     `json:"home"`
	Regions  []goneRegion `json:"regions"`
	TTL      int64        `json:"ttl"`
}

// storeRef is an S3 location without its secret.
type storeRef struct {
	Endpoint  string `json:"endpoint"`
	Region    string `json:"region"`
	Bucket    string `json:"bucket"`
	KeyID     string `json:"key_id"`
	Prefix    string `json:"prefix"`
	PathStyle bool   `json:"path_style"`
}

func refOf(c storage.Config) storeRef {
	return storeRef{Endpoint: c.S3Endpoint, Region: c.S3Region, Bucket: c.S3Bucket, KeyID: c.S3AccessKeyID,
		Prefix: c.S3Prefix, PathStyle: c.S3PathStyle}
}

// configOf rebuilds the location of ref from the storage settings cur, which hold the secret: it
// needs the same access key ID in cur (the secret of another key is not known).
func configOf(cur storage.Config, ref storeRef) (storage.Config, bool) {
	if cur.Backend != "s3" || cur.S3AccessKeyID != ref.KeyID {
		return storage.Config{}, false
	}
	c := cur
	c.S3Endpoint, c.S3Region, c.S3Bucket, c.S3Prefix, c.S3PathStyle = ref.Endpoint, ref.Region, ref.Bucket, ref.Prefix, ref.PathStyle
	return c, true
}

// drainView is the view of the saved drain at init: the pre-steps run in its buckets until its
// end, and the controller then drops the outbox. ok=false when the old home cannot be opened.
func (m *mod) drainView(ctx context.Context) (*view, bool, error) {
	rec := m.home.Drain
	cfg, ok := configOf(storage.ConfigView.Get(m.h.Settings.Current()), rec.Home)
	if !ok {
		return nil, false, nil
	}
	hb, err := m.openHome(ctx, cfg)
	if err != nil {
		return nil, false, err
	}
	d := &view{mode: modeDrain, location: rec.Location, home: hb, ttl: time.Duration(rec.TTL) * time.Second}
	if rec.Until != 0 {
		d.until = time.UnixMilli(rec.Until)
	}
	for _, g := range rec.Regions {
		b, err := m.openRegion(ctx, cfg, regionCfg{Hint: g.Hint, Bucket: g.Bucket})
		if err != nil {
			return nil, false, err
		}
		d.regions = append(d.regions, b)
	}
	return d, true, nil
}

// openHome opens the home bucket of cfg.
func (m *mod) openHome(ctx context.Context, cfg storage.Config) (*bucket, error) {
	c := cfg
	c.S3Redirect = false
	st, err := storage.Open(ctx, c)
	if err != nil {
		return nil, err
	}
	return &bucket{label: "home", name: cfg.S3Bucket, st: st, cfg: cfg}, nil
}

func (m *mod) openRegion(ctx context.Context, cfg storage.Config, r regionCfg) (*bucket, error) {
	st, err := storage.Open(ctx, regionConfig(cfg, r.Bucket))
	if err != nil {
		return nil, err
	}
	return &bucket{label: r.Hint, name: r.Bucket, st: st}, nil
}

// resume turns edge delivery on where the objects were written already (a restart).
func (m *mod) resume(ctx context.Context, snap *settings.Snapshot, cfg storage.Config, loc string) error {
	v, err := m.stateView(ctx, snap, cfg, loc)
	if err != nil {
		return err
	}
	m.cur.Store(v)
	m.mark(ctx, configKey) // the server version may be new
	return m.syncRegions(ctx, v, snap)
}

// stateView is the view of edge delivery on in the buckets of the saved state: the home bucket,
// the regions of edge_state and the gone regions. syncRegions then brings it to the settings.
func (m *mod) stateView(ctx context.Context, snap *settings.Snapshot, cfg storage.Config, loc string) (*view, error) {
	hb, err := m.openHome(ctx, cfg)
	if err != nil {
		return nil, err
	}
	v := &view{mode: modeOn, location: loc, home: hb, ttl: AccessTTL.Get(snap), domains: m.domainsKey(snap)}
	for _, h := range Hints {
		if r := m.regions[h]; r != nil {
			b, err := m.openRegion(ctx, cfg, regionCfg{Hint: h, Bucket: r.Bucket})
			if err != nil {
				return nil, err
			}
			v.regions = append(v.regions, b)
		}
	}
	for _, g := range m.home.Gone {
		b, err := m.openRegion(ctx, cfg, regionCfg{Hint: g.Hint, Bucket: g.Bucket})
		if err != nil {
			return nil, err
		}
		v.gone = append(v.gone, b)
	}
	return v, nil
}

// startView turns edge delivery on at init, before any commit, when the saved state is on for
// this home and the settings run it: a commit or a remove before the controller's first step then
// runs the pre-steps and queues its outbox rows. The controller's first step syncs the regions.
func (m *mod) startView(ctx context.Context) error {
	if m.home.Drain != nil {
		// A drain ran at the restart: its pre-steps go on until its saved end.
		d, ok, err := m.drainView(ctx)
		if err != nil {
			return err
		}
		if ok {
			m.cur.Store(d)
			return nil
		}
		m.h.Log.Warn("edge: the drain of the old home bucket cannot go on: the storage settings no longer hold its access key")
		m.home.Drain = nil
		return m.saveHome(ctx)
	}
	snap := m.h.Settings.Current()
	cfg := storage.ConfigView.Get(snap)
	loc := fingerprint(cfg)
	if !m.home.Started || m.home.DeployNeeded || m.home.Location != loc || m.home.Seen != loc ||
		!Enabled.Get(snap) || unfit(snap) != "" {
		return nil
	}
	v, err := m.stateView(ctx, snap, cfg, loc)
	if err != nil {
		return err
	}
	m.cur.Store(v)
	m.mark(ctx, configKey) // the server version may be new
	return nil
}

// firstStart turns edge delivery on in a home bucket that may hold stale objects: it deletes
// edge/v1/ in the home bucket and in every region, then writes config.json and starts the
// backfill of every pointer. The regions start filling; their blobs stay (content-addressed).
func (m *mod) firstStart(ctx context.Context, snap *settings.Snapshot, cfg storage.Config, loc string) error {
	hb, err := m.openHome(ctx, cfg)
	if err != nil {
		return err
	}
	buckets := []*bucket{hb}
	for _, rc := range regionsOf(snap) {
		b, err := m.openRegion(ctx, cfg, rc)
		if err != nil {
			return err
		}
		buckets = append(buckets, b)
	}
	m.h.Log.Info("edge delivery starts: deleting old edge objects", "buckets", len(buckets))
	for _, b := range buckets {
		if err := m.wipe(ctx, b, root); err != nil {
			return fmt.Errorf("delete the edge objects of bucket %s: %w", b.name, err)
		}
	}
	m.marked = map[string]bool{} // the wipe deleted every bucket.json
	if err := m.q.ClearWritten(ctx); err != nil {
		return err
	}
	if err := m.q.ClearOutbox(ctx); err != nil {
		return err
	}
	if err := m.resetClasses(ctx); err != nil {
		return err
	}
	total, err := m.rq.SumFileCount(ctx)
	if err != nil {
		return err
	}
	for _, h := range Hints {
		if m.regions[h] != nil {
			if err := m.dropRegion(ctx, h); err != nil {
				return err
			}
		}
	}
	v := &view{mode: modeOn, location: loc, home: hb, regions: buckets[1:], ttl: AccessTTL.Get(snap), domains: m.domainsKey(snap)}
	for _, b := range v.regions {
		r := &regionState{Hint: b.label, Bucket: b.name, Cleared: true, ViaHome: true}
		m.regions[b.label] = r
		if err := m.saveRegion(ctx, r); err != nil {
			return err
		}
	}
	m.home = homeState{Location: loc, Started: true, Seen: loc, Total: total}
	m.ack.Store(false)
	if err := m.saveHome(ctx); err != nil {
		return err
	}
	m.cur.Store(v)
	if err := m.putConfig(ctx, true); err != nil {
		return err
	}
	if len(v.regions) > 0 {
		m.requestGap(ctx, v, nil)
	}
	m.h.Go("edge.reconcile", func(ctx context.Context) {
		if err := m.reconcile(ctx); err != nil && ctx.Err() == nil {
			m.h.Log.Error("edge reconcile failed", "error", err.Error())
		}
	})
	m.h.Log.Info("edge delivery is on", "home", hb.name, "regions", len(v.regions))
	return nil
}

// wipe deletes every object under prefix of b.
func (m *mod) wipe(ctx context.Context, b *bucket, prefix string) error {
	for {
		objs, err := b.st.List(ctx, prefix, "", 1000)
		if err != nil {
			return err
		}
		if len(objs) == 0 {
			return nil
		}
		keys := make([]string, len(objs))
		for i, o := range objs {
			keys[i] = o.Key
		}
		if err := b.st.DeleteMany(ctx, keys); err != nil {
			return err
		}
	}
}

// syncRegions follows edge.regions while edge delivery is on: a new region (or a new bucket of
// a hint) starts filling, and its bucket is written at once. A removed ready region (a hint
// taken out, or the old bucket of a hint) leaves config.json first and then drains: the
// pre-steps keep changing its bucket until no Worker holds a config.json that lists it as ready
// (see drainEnd); then nothing writes it. A removed region that was never ready needs no drain.
// The caller holds stMu.
func (m *mod) syncRegions(ctx context.Context, v *view, snap *settings.Snapshot) error {
	want := regionsOf(snap)
	cfg := storage.ConfigView.Get(snap)
	changed := v.ttl != AccessTTL.Get(snap) || v.domains != m.domainsKey(snap)
	homeChanged := false
	retire := func(r *regionState) {
		if r.Ready {
			m.home.Gone = append(m.home.Gone, goneRegion{Hint: r.Hint, Bucket: r.Bucket})
			homeChanged = true
		}
	}
	var next []*bucket
	var added []string
	for _, rc := range want {
		r := m.regions[rc.Hint]
		var b *bucket
		if i := slices.IndexFunc(v.regions, func(x *bucket) bool { return x.label == rc.Hint && x.name == rc.Bucket }); i >= 0 {
			b = v.regions[i]
		} else {
			var err error
			if b, err = m.openRegion(ctx, cfg, rc); err != nil {
				return err
			}
			changed = true
		}
		if r == nil || r.Bucket != rc.Bucket {
			if r != nil {
				retire(r)
				m.h.Log.Info("edge region moved to a new bucket: the old one drains", "region", rc.Hint, "bucket", r.Bucket)
			}
			total, err := m.rq.SumFileCount(ctx)
			if err != nil {
				return err
			}
			r = &regionState{Hint: rc.Hint, Bucket: rc.Bucket, Total: total}
			m.regions[rc.Hint] = r
			if err := m.saveRegion(ctx, r); err != nil {
				return err
			}
			added = append(added, rc.Hint)
			changed = true
		}
		next = append(next, b)
	}
	for _, h := range Hints {
		if r := m.regions[h]; r != nil && !slices.ContainsFunc(want, func(rc regionCfg) bool { return rc.Hint == h }) {
			retire(r)
			if err := m.dropRegion(ctx, h); err != nil {
				return err
			}
			changed = true
			m.h.Log.Info("edge region removed: its bucket drains, then the server no longer writes it", "region", h)
		}
	}
	// The gone regions: drop those whose drain ended; a pending one (Until 0) needs config.json.
	now := nowMS()
	pending := false
	gone := m.home.Gone[:0:0]
	for _, g := range m.home.Gone {
		if g.Until != 0 && now >= g.Until {
			homeChanged, changed = true, true
			m.h.Log.Info("edge region drained: the server no longer writes its bucket", "region", g.Hint, "bucket", g.Bucket)
			continue
		}
		pending = pending || g.Until == 0
		gone = append(gone, g)
	}
	m.home.Gone = gone
	if homeChanged {
		if err := m.saveHome(ctx); err != nil {
			return err
		}
	}
	if !changed && !pending {
		return nil
	}
	var goneBuckets []*bucket
	for _, g := range m.home.Gone {
		match := func(x *bucket) bool { return x.label == g.Hint && x.name == g.Bucket }
		var b *bucket
		if i := slices.IndexFunc(v.gone, match); i >= 0 {
			b = v.gone[i]
		} else if i := slices.IndexFunc(v.regions, match); i >= 0 {
			b = v.regions[i]
		} else {
			var err error
			if b, err = m.openRegion(ctx, cfg, regionCfg{Hint: g.Hint, Bucket: g.Bucket}); err != nil {
				return err
			}
		}
		goneBuckets = append(goneBuckets, b)
	}
	// The gone buckets are in the pre-steps from now, before config.json drops them.
	nv := &view{mode: modeOn, location: v.location, home: v.home, regions: next, gone: goneBuckets, ttl: AccessTTL.Get(snap), domains: m.domainsKey(snap)}
	m.cur.Store(nv)
	if err := m.putConfig(ctx, pending); err != nil {
		return err
	}
	if pending {
		// config.json without them is written: the drain ends when no Worker holds an older one.
		until := m.drainEnd().UnixMilli()
		for i := range m.home.Gone {
			if m.home.Gone[i].Until == 0 {
				m.home.Gone[i].Until = until
			}
		}
		if err := m.saveHome(ctx); err != nil {
			return err
		}
	}
	if len(added) > 0 {
		m.requestGap(ctx, nv, added)
	}
	return nil
}

// putConfig writes config.json for the current view under cfgMu, so a write of the outbox
// (enabled) never lands after the off switch of a drain: on, it is enabled with the cache time
// of the settings; draining, the off switch; off, nothing. A failed put while on queues
// config.json in the outbox, which tries again.
func (m *mod) putConfig(ctx context.Context, force bool) error {
	err := m.writeConfig(ctx, force)
	if err != nil {
		m.mark(ctx, configKey) // only while on
	}
	return err
}

// writeConfig is putConfig without the outbox row: the outbox's own write of config.json.
func (m *mod) writeConfig(ctx context.Context, force bool) error {
	m.cfgMu.Lock()
	defer m.cfgMu.Unlock()
	v := m.cur.Load()
	switch v.mode {
	case modeOn:
		return m.write(ctx, v, configKey, m.config(true), force)
	case modeDrain:
		return m.write(ctx, v, configKey, m.configJSON(false, v.ttl), force)
	}
	return nil
}

// config is config.json of the current state, with the cache time of the settings; enabled
// false is the off switch.
func (m *mod) config(enabled bool) []byte {
	return m.configJSON(enabled, AccessTTL.Get(m.h.Settings.Current()))
}

// configJSON is config.json. home.bucket is the home bucket of the current view (of the settings
// while off): the Worker serves only when the bucket.json of its HOME binding names it.
func (m *mod) configJSON(enabled bool, ttl time.Duration) []byte {
	snap := m.h.Settings.Current()
	p := m.published()
	hb := storage.S3Bucket.Get(snap)
	if v := m.cur.Load(); v.home != nil {
		hb = v.home.name
	}
	o := configObject{ServerVersion: m.h.Version, Requires: featureNames(m.requires()), Enabled: enabled, AccessTTL: ttlSeconds(ttl),
		EdgeKey: encodeKey(m.h.Auth.EdgePublicKey()), Formats: formatsObject(m.h.Formats),
		Home: homeObject{Hint: HomeHint.Get(snap), Bucket: hb, Ready: p.home.Ready}, Regions: []regionObject{},
		Domains: m.h.RepoDomains(snap)}
	for _, r := range p.regions {
		o.Regions = append(o.Regions, regionObject{Hint: r.Hint, Binding: binding(r.Hint), Bucket: r.Bucket, Ready: r.Ready})
	}
	if enabled {
		o.PointerCache = m.pointerCacheOf(&o, m.cur.Load().home)
	}
	b, _ := json.Marshal(o)
	return b
}

// requestGap writes gap/request.json: the Worker's gap check runs a pass at its next cron run.
func (m *mod) requestGap(ctx context.Context, v *view, hints []string) {
	if hints == nil {
		hints = []string{}
	}
	b, _ := json.Marshal(map[string]any{"at": time.Now().Unix(), "regions": hints})
	if err := m.put(ctx, v.home, gapRequestKey, b); err != nil {
		m.h.Log.Warn("edge: writing gap/request.json failed", "error", err.Error())
		return
	}
	now := time.Now().Unix()
	for _, h := range hints {
		if r := m.regions[h]; r != nil {
			r.GapAt = now
			_ = m.saveRegion(ctx, r)
		}
	}
}

// --- backfill ---

// backfillPage is how many assets one backfill page reads.
const backfillPage = 500

// stepBudget bounds the work of one step, so a settings change is seen soon.
const stepBudget = 5 * time.Second

// repoCache reads each repo of a backfill page once.
type repoCache map[int64]*repos.Repo

func (c repoCache) get(ctx context.Context, rs *repos.Service, id int64) *repos.Repo {
	if r, ok := c[id]; ok {
		return r
	}
	r, err := rs.ByID(ctx, id)
	if err != nil {
		r = nil
	}
	c[id] = r
	return r
}

// backfillHome queues the pointer of every asset in the outbox, in id order (edge.backfill).
func (m *mod) backfillHome(ctx context.Context, v *view) (bool, error) {
	if m.home.Done {
		return false, nil
	}
	start := time.Now()
	cache := repoCache{}
	for time.Since(start) < stepBudget {
		rows, err := m.rq.ListEdgeAssets(ctx, dbq.ListEdgeAssetsParams{ID: m.home.Cursor, Limit: backfillPage})
		if err != nil {
			return false, err
		}
		now := nowMS()
		n := int64(0)
		err = m.h.DB.Tx(ctx, func(tx *sql.Tx) error {
			n = 0
			q := dbq.New(tx)
			for _, a := range rows {
				repo := cache.get(ctx, m.h.Repos, a.RepoID)
				if repo == nil || m.edgeOf(repo) == nil || !m.eligible(repo, a.Path) {
					continue
				}
				if err := q.InsertOutbox(ctx, dbq.InsertOutboxParams{RepoID: a.RepoID, Path: a.Path, Sha256: a.Sha256, DueAt: now}); err != nil {
					return err
				}
				n++
			}
			return nil
		})
		if err != nil {
			return false, err
		}
		if len(rows) > 0 {
			m.home.Cursor = rows[len(rows)-1].ID
		}
		m.home.Queued += n
		m.home.Done = len(rows) < backfillPage
		if err := m.saveHome(ctx); err != nil {
			return false, err
		}
		m.wake()
		if m.home.Done {
			m.h.Log.Info("edge backfill queued every pointer", "pointers", m.home.Queued)
			return false, nil
		}
	}
	return true, nil
}

func markerID(b *bucket) string { return b.label + "\x00" + b.name }

// writeMarkers puts bucket.json into each bucket of v that has none from this process yet (the
// first start's wipe deletes them, so it forgets them). A failed put is tried again at the next
// step. The caller holds stMu.
func (m *mod) writeMarkers(ctx context.Context, v *view) {
	for _, b := range v.all() {
		id := markerID(b)
		if m.marked[id] {
			continue
		}
		body, _ := json.Marshal(markerObject{Hint: b.label, Bucket: b.name})
		if err := m.put(ctx, b, markerKey, body); err != nil {
			m.h.Log.Warn("edge: writing bucket.json failed", "bucket", b.name, "error", err.Error())
			continue
		}
		m.marked[id] = true
	}
}

// homeReady marks the home bucket ready once the backfill queued every pointer and the outbox
// wrote every row it queued, and writes config.json with home ready. The caller holds stMu.
func (m *mod) homeReady(ctx context.Context) error {
	if !m.home.Done || m.home.Ready {
		return nil
	}
	pending, err := m.rq.BackfillPending(ctx)
	if err != nil || pending {
		return err
	}
	m.home.Ready = true
	if err := m.saveHome(ctx); err != nil {
		return err
	}
	m.h.Log.Info("edge backfill wrote every pointer: the edge serves", "pointers", m.home.Queued)
	return m.putConfig(ctx, false)
}

// fillRegion moves a filling region on: it deletes the stale pointers of the bucket, puts the
// pointer of every immutable path, then checks that the bucket has every blob of the home bucket
// and marks the region ready.
func (m *mod) fillRegion(ctx context.Context, v *view, b *bucket, r *regionState) (bool, error) {
	if r.Ready {
		return false, nil
	}
	if m.home.draining(r.Hint, r.Bucket) {
		// Added again during its drain: a Worker with a cached config.json may still read the
		// bucket as ready, so its pointers stay until the drain ends.
		return false, nil
	}
	if !r.Cleared {
		if err := m.wipe(ctx, b, root+pointerPrefix); err != nil {
			return false, err
		}
		r.Cleared = true
		if err := m.saveRegion(ctx, r); err != nil {
			return false, err
		}
	}
	if !r.pointersDone(&m.home) {
		if r.ViaHome {
			return false, nil // the backfill of every pointer writes them
		}
		return m.backfillRegion(ctx, v, b, r)
	}
	return m.checkBlobs(ctx, v, b, r)
}

// backfillRegion puts the pointer of every immutable path in the bucket of a new region, each
// under its path lock from the asset row read again under the lock.
func (m *mod) backfillRegion(ctx context.Context, v *view, b *bucket, r *regionState) (bool, error) {
	start := time.Now()
	cache := repoCache{}
	for time.Since(start) < stepBudget {
		rows, err := m.rq.ListEdgeAssets(ctx, dbq.ListEdgeAssetsParams{ID: r.Cursor, Limit: backfillPage})
		if err != nil {
			return false, err
		}
		type fill struct {
			a     dbq.ListEdgeAssetsRow
			rules []format.EdgeRule // the rules that make it immutable; nil for the format's globs
		}
		var todo []fill
		for _, a := range rows {
			repo := cache.get(ctx, m.h.Repos, a.RepoID)
			if repo == nil {
				continue
			}
			if m.immutable(repo, a.Path) && m.eligible(repo, a.Path) {
				rules, _ := m.pathRules(repo)
				todo = append(todo, fill{a, rules})
			}
		}
		if err := parallel(16, todo, func(f fill) error {
			a := f.a
			key := pointerKey(a.RepoID, a.Path)
			unlock := m.h.Assets.Lock(a.RepoID, a.Path)
			defer unlock()
			cur, err := m.h.Assets.Get(ctx, a.RepoID, a.Path)
			if err != nil {
				return nil // removed: no pointer
			}
			if f.rules != nil {
				if err := m.record(ctx, a.RepoID, f.rules); err != nil {
					return err
				}
			}
			return m.putUnknown(ctx, a.RepoID, a.Path, m.put(ctx, b, key, pointerOf(cur)))
		}); err != nil {
			return false, err
		}
		if len(rows) > 0 {
			r.Cursor = rows[len(rows)-1].ID
		}
		r.Written += int64(len(todo))
		r.Done = len(rows) < backfillPage
		if err := m.saveRegion(ctx, r); err != nil {
			return false, err
		}
		if r.Done {
			return true, nil
		}
	}
	return true, nil
}

// The blob check of a filling region.
const (
	blobsPrefix    = "blobs/"
	checkEvery     = 2 * time.Minute
	checkPages     = 50 // pages of 1000 home keys per step
	gapRequestWait = 30 * time.Minute
)

// checkBlobs compares the home blobs/ and the region's blobs/ in key order, from the last key
// known present, at most checkPages pages per step. A pass that finds every blob marks the
// region ready and writes config.json.
func (m *mod) checkBlobs(ctx context.Context, v *view, b *bucket, r *regionState) (bool, error) {
	now := time.Now().Unix()
	if r.Pass == "" {
		if now-r.CheckedAt < int64(checkEvery.Seconds()) {
			return false, nil
		}
		r.Pass, r.PassMissing = r.Verified, 0
		if r.Pass == "" {
			r.Pass = blobsPrefix
		}
	}
	after, end := r.Pass, false
	for range checkPages {
		home, err := v.home.st.List(ctx, blobsPrefix, after, 1000)
		if err != nil {
			return false, err
		}
		if len(home) == 0 {
			end = true
			break
		}
		last := home[len(home)-1].Key
		have := map[string]bool{}
		for ra := after; ; {
			objs, err := b.st.List(ctx, blobsPrefix, ra, 1000)
			if err != nil {
				return false, err
			}
			for _, o := range objs {
				if o.Key <= last {
					have[o.Key] = true
				}
			}
			if len(objs) < 1000 || objs[len(objs)-1].Key >= last {
				break
			}
			ra = objs[len(objs)-1].Key
		}
		for _, o := range home {
			if !have[o.Key] {
				r.PassMissing++
			}
		}
		if r.PassMissing == 0 {
			r.Verified = last
		}
		after = last
		if len(home) < 1000 {
			end = true
			break
		}
	}
	if !end {
		r.Pass = after
		return true, m.saveRegion(ctx, r)
	}
	missing := r.PassMissing
	if r.Missing == nil || *r.Missing != missing {
		r.MissingSince = now
	}
	r.Missing, r.CheckedAt, r.Pass, r.PassMissing = &missing, now, "", 0
	if missing == 0 {
		// A pointer put of unknown outcome may still land in the region: it becomes ready after
		// the second application of that row, at a later pass.
		if timedOut, err := m.rq.AnyTimedOut(ctx); err != nil || timedOut {
			if err != nil {
				return false, err
			}
			return false, m.saveRegion(ctx, r)
		}
		r.Ready = true
		if err := m.saveRegion(ctx, r); err != nil {
			return false, err
		}
		m.h.Log.Info("edge region is ready", "region", r.Hint)
		return false, m.putConfig(ctx, false)
	}
	if now-r.GapAt >= int64(gapRequestWait.Seconds()) {
		m.requestGap(ctx, v, []string{r.Hint})
	}
	return false, m.saveRegion(ctx, r)
}

// --- the Worker's gap state ---

// readGap reads gap/state.json from the home bucket hb at most once a minute; force reads it now
// (after a check of the Worker).
func (m *mod) readGap(ctx context.Context, hb *bucket, force bool) {
	if g, _ := m.gap.Load().(*gapState); !force && g != nil && time.Since(g.read) < time.Minute {
		return
	}
	rc, _, err := hb.st.Get(ctx, root+gapStateKey)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			m.gap.Store(&gapState{Regions: map[string]bool{}, read: time.Now()})
		}
		return
	}
	defer rc.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(io.LimitReader(rc, 1<<20)); err != nil {
		return
	}
	g := parseGap(buf.Bytes())
	g.read = time.Now()
	m.gap.Store(g)
}
