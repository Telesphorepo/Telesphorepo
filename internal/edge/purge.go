package edge

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/edge/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
	"github.com/Telesphorepo/Telesphorepo/internal/storage"
)

// The pointer cache. The Worker keeps the pointer of an immutable path of a single repo in the
// Cache API of the data center, keyed by the request's host, a generation, the bucket and the
// pointer key, with a Cache-Tag of the generation, the bucket and the pointer key (Cloudflare purges
// Cache API entries by tag, not by URL). Before a pre-step or the outbox lets a change of such a
// pointer stand, the server purges that tag on the zone of every host, so a copy that the Worker
// reads always holds the bytes of the asset now. config.json turns the cache on (pointer_cache)
// only after the server saw, on every route host, a tag purge remove an entry of the Cache API.
//
// A generation names the hosts and the buckets whose copies the server purges. A change of them,
// or a config.json without the cache, starts a new generation: the Worker never reads the copies
// of an older one once no Worker holds a config.json that names it, so a host or a bucket that
// left the purges (a removed domain, a drained region, edge delivery turned off) never serves an
// old copy when it comes back.

const (
	// pointerTTL is the max age of a cached pointer. The purges keep a copy right; the age only
	// bounds a purge that Cloudflare took but did not apply, and costs one R2 read per pointer, data
	// center and hour.
	pointerTTL = time.Hour
	// purgeDelay is the wait from a change of pointers to their purge. The Worker keeps a copy only
	// when its put ended within 1 s of the start of its R2 read (PUT_WINDOW_MS of
	// edge/src/index.ts), so every copy read before the change is in the cache, or deleted, before
	// the purge.
	purgeDelay = 2 * time.Second
	// purgeBatch is the most tags that one purge call takes.
	purgeBatch = 100
	// The proof that the pointer cache works: again after an hour, or 10 minutes after a failure.
	purgeRecheck = time.Hour
	purgeRetry   = 10 * time.Minute
	// purgePerm is the permission that the pointer cache needs, as Cloudflare's token form names it.
	purgePerm = "Cache & Performance › Cache: Purge, in a policy of All Domains or the domain of each route host"
)

// pointerGen is a generation of the pointer cache.
type pointerGen struct {
	Gen     int64    `json:"gen"`
	TTL     int64    `json:"ttl"`     // seconds
	Prefix  string   `json:"prefix"`  // the key prefix of the Worker: s3.prefix and "/", or ""
	Hosts   []string `json:"hosts"`   // the hosts whose copies are purged
	Buckets []string `json:"buckets"` // the home bucket and the ready regions
	// Until is when no Worker reads its copies any more (unix ms); 0 while a config.json that names
	// it may be the one in the home bucket.
	Until int64 `json:"until"`
}

func (g *pointerGen) same(o *pointerGen) bool {
	return g.TTL == o.TTL && g.Prefix == o.Prefix && slices.Equal(g.Hosts, o.Hosts) && slices.Equal(g.Buckets, o.Buckets)
}

// pointerState is the row "pointers" of edge_state: the generations whose copies a Worker may
// read, and the next generation.
type pointerState struct {
	Next int64        `json:"next"`
	Gens []pointerGen `json:"gens"`
}

// purgeGate is the last proof that the pointer cache works (purgeProof).
type purgeGate struct {
	mu    sync.Mutex
	key   string             // the hash of the token and the route hosts it was checked for
	zones map[string]zoneRef // host -> its zone, from the checks and the purges
	ok    bool
	why   string // why the pointer cache is off; "" while it is on
	at    time.Time
	// running: a proof runs (purgeProof).
	running bool
}

// loadPointers reads the row "pointers".
func (m *mod) loadPointers(ctx context.Context) error {
	raw, err := m.rq.GetState(ctx, "pointers")
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(raw), &m.ptr); err != nil {
		return fmt.Errorf("edge pointer cache state: %w", err)
	}
	return nil
}

// savePointers writes the row "pointers"; the caller holds ptrMu.
func (m *mod) savePointers(ctx context.Context) error {
	b, _ := json.Marshal(m.ptr)
	return m.q.PutState(context.WithoutCancel(ctx), dbq.PutStateParams{Key: "pointers", Value: string(b)})
}

// routeHosts returns the hosts of the Worker's routes for s, lower case and sorted: the base URL
// host and each repo domain.
func (m *mod) routeHosts(s *settings.Snapshot) []string {
	host, err := baseHost(s)
	if err != nil {
		return nil
	}
	out := []string{strings.ToLower(host)}
	for d := range m.h.RepoDomains(s) {
		if d = strings.ToLower(d); !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	slices.Sort(out)
	return out
}

// gateKey is the key of a check of the purge permission.
func gateKey(token string, hosts []string) string {
	return hashOf([]byte(token + "\x00" + strings.Join(hosts, "\n")))
}

// keyPrefixOf is the key prefix of the Worker for the store cfg (keyPrefix of edge/src/access.ts).
func keyPrefixOf(cfg storage.Config) string {
	if p := strings.Trim(cfg.S3Prefix, "/"); p != "" {
		return p + "/"
	}
	return ""
}

// pointerCacheOf returns the pointer_cache of config.json o, or nil while the token cannot purge
// on every route host. Its generation is the current one when it names the same hosts and buckets,
// else the next one, which the write of config.json records (pointersWriting).
func (m *mod) pointerCacheOf(o *configObject, home *bucket) *pointerCacheObject {
	snap := m.h.Settings.Current()
	hosts := m.routeHosts(snap)
	g := &m.gate
	g.mu.Lock()
	ok := g.ok && g.key == gateKey(CloudflareToken.Get(snap), hosts)
	g.mu.Unlock()
	if !ok || len(hosts) == 0 {
		return nil
	}
	cfg := storage.ConfigView.Get(snap)
	if home != nil {
		cfg = home.cfg
	}
	spec := pointerGen{TTL: ttlSeconds(pointerTTL), Prefix: keyPrefixOf(cfg), Hosts: hosts, Buckets: bucketsOf(o)}
	m.ptrMu.Lock()
	defer m.ptrMu.Unlock()
	gen := max(m.ptr.Next, 1)
	for i := len(m.ptr.Gens) - 1; i >= 0; i-- {
		if c := &m.ptr.Gens[i]; c.Until == 0 && c.same(&spec) {
			gen = c.Gen
			break
		}
	}
	return &pointerCacheObject{TTL: spec.TTL, Gen: gen, Hosts: hosts}
}

// bucketsOf returns the buckets whose pointers a Worker with config.json o reads: the home bucket
// and the ready regions.
func bucketsOf(o *configObject) []string {
	out := []string{o.Home.Bucket}
	for _, r := range o.Regions {
		if r.Ready {
			out = append(out, r.Bucket)
		}
	}
	return out
}

// pointersWriting records the generation of config.json body before its put, so the purges cover
// its copies from the moment a Worker may read it. home is the bucket it goes to.
func (m *mod) pointersWriting(ctx context.Context, body []byte, home *bucket) int64 {
	var o configObject
	if json.Unmarshal(body, &o) != nil || o.PointerCache == nil {
		return 0
	}
	pc := o.PointerCache
	spec := pointerGen{Gen: pc.Gen, TTL: pc.TTL, Prefix: keyPrefixOf(home.cfg), Hosts: pc.Hosts, Buckets: bucketsOf(&o)}
	m.ptrMu.Lock()
	defer m.ptrMu.Unlock()
	if slices.ContainsFunc(m.ptr.Gens, func(g pointerGen) bool { return g.Gen == pc.Gen && g.Until == 0 }) {
		return pc.Gen
	}
	m.ptr.Gens = append(m.ptr.Gens, spec)
	m.ptr.Next = max(m.ptr.Next, pc.Gen+1)
	if err := m.savePointers(ctx); err != nil {
		m.h.Log.Warn("edge: saving the pointer cache state failed", "error", err.Error())
	}
	return pc.Gen
}

// pointersPublished follows a put of config.json naming generation gen (0 for none): after one
// that succeeded, every other generation is read only until no Worker holds an earlier
// config.json (ttlState), with the margin of a drain. After a failed put the bucket holds the old
// or the new one, so both stay.
func (m *mod) pointersPublished(ctx context.Context, gen int64, ok bool) {
	if !ok {
		return
	}
	m.ttlMu.Lock()
	until := m.ttl.HoldUntil + (5 * time.Second).Milliseconds()
	m.ttlMu.Unlock()
	m.ptrMu.Lock()
	defer m.ptrMu.Unlock()
	changed := false
	for i := range m.ptr.Gens {
		if g := &m.ptr.Gens[i]; g.Gen != gen && g.Until == 0 {
			g.Until, changed = until, true
		}
	}
	if changed {
		if err := m.savePointers(ctx); err != nil {
			m.h.Log.Warn("edge: saving the pointer cache state failed", "error", err.Error())
		}
	}
}

// liveGens returns the generations whose copies a Worker may still read, and forgets the others.
func (m *mod) liveGens(ctx context.Context) []pointerGen {
	m.ptrMu.Lock()
	defer m.ptrMu.Unlock()
	now := nowMS()
	live := m.ptr.Gens[:0:0]
	for _, g := range m.ptr.Gens {
		if g.Until == 0 || g.Until > now {
			live = append(live, g)
		}
	}
	if len(live) != len(m.ptr.Gens) {
		m.ptr.Gens = live
		if err := m.savePointers(ctx); err != nil {
			m.h.Log.Warn("edge: saving the pointer cache state failed", "error", err.Error())
		}
	}
	return slices.Clone(live)
}

// pointerTag is the Cache-Tag of the cached pointer key of bucket in generation gen (pointerTag of
// edge/src/index.ts). It names no host: a purge of the tag on a zone reaches the copies of every
// host of that zone.
func pointerTag(gen int64, bucket, key string) string {
	return "tp/" + strconv.FormatInt(gen, 10) + "/" + bucket + "/" + key
}

// purgePointers purges the cached copies of the pointer keys of bs, of every generation that a
// Worker may still read, once purgeDelay passed since changed (when the last change of the keys
// returned): a purge by Cache-Tag on the zone of each host of those generations. A purge that
// fails fails the caller: a pre-step fails its commit or remove, the outbox tries the row again. A
// refusal of the token turns the pointer cache off.
func (m *mod) purgePointers(ctx context.Context, bs []*bucket, keys []string, changed time.Time) error {
	if len(keys) == 0 {
		return nil
	}
	var tags []string
	hosts := map[string]bool{}
	for _, g := range m.liveGens(ctx) {
		n := len(tags)
		for _, b := range bs {
			if !slices.Contains(g.Buckets, b.name) {
				continue
			}
			for _, k := range keys {
				tags = append(tags, pointerTag(g.Gen, b.name, k))
			}
		}
		if len(tags) > n {
			for _, h := range g.Hosts {
				hosts[h] = true
			}
		}
	}
	if len(tags) == 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Until(changed.Add(purgeDelay))):
	}
	snap := m.h.Settings.Current()
	token := CloudflareToken.Get(snap)
	if token == "" {
		return errors.New("purge the cached pointers: no Cloudflare API token is set, and Workers may still read cached pointers until their config.json without the cache expires")
	}
	c := &cfClient{base: cfAPI, token: token}
	zones, err := m.zonesOf(ctx, c, snap, slices.Sorted(maps.Keys(hosts)))
	if err != nil {
		return fmt.Errorf("purge the cached pointers: %w", err)
	}
	type batch struct {
		zone zoneRef
		tags []string
	}
	var batches []batch
	for _, z := range distinctZones(zones) {
		for rest := tags; len(rest) > 0; {
			n := min(purgeBatch, len(rest))
			batches = append(batches, batch{z, rest[:n]})
			rest = rest[n:]
		}
	}
	err = parallel(4, batches, func(b batch) error { return purgeTags(ctx, c, b.zone, b.tags) })
	if err != nil {
		m.purgeRefused(ctx, err)
		return fmt.Errorf("purge the cached pointers: %w", err)
	}
	return nil
}

// distinctZones returns the zones of zones, each once, sorted by name.
func distinctZones(zones map[string]zoneRef) []zoneRef {
	var out []zoneRef
	for _, z := range zones {
		if !slices.Contains(out, z) {
			out = append(out, z)
		}
	}
	slices.SortFunc(out, func(a, b zoneRef) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// purgeTags purges the cached entries with one of tags on zone.
func purgeTags(ctx context.Context, c *cfClient, zone zoneRef, tags []string) error {
	b, _ := json.Marshal(map[string]any{"tags": tags})
	_, err := c.do(ctx, cfRequest{method: http.MethodPost, path: "/zones/" + seg(zone.ID) + "/purge_cache", body: b, again: true}, nil)
	return err
}

// zonesOf returns the zone of each host, from the checks and earlier purges, or found now.
func (m *mod) zonesOf(ctx context.Context, c *cfClient, snap *settings.Snapshot, hosts []string) (map[string]zoneRef, error) {
	g := &m.gate
	g.mu.Lock()
	out := map[string]zoneRef{}
	var missing []string
	for _, h := range hosts {
		if z, ok := g.zones[h]; ok {
			out[h] = z
		} else {
			missing = append(missing, h)
		}
	}
	g.mu.Unlock()
	if len(missing) == 0 {
		return out, nil
	}
	account, _, why := r2Account(storage.S3Endpoint.Get(snap))
	if account == "" {
		return nil, errors.New(why)
	}
	r := &deployRun{m: m, p: &deployPlan{Account: account}, c: c}
	found := map[string]zoneRef{}
	for _, h := range missing {
		z, err := r.zoneOf(ctx, h)
		if err != nil {
			return nil, err
		}
		found[h], out[h] = z, z
	}
	g.mu.Lock()
	if g.zones == nil {
		g.zones = map[string]zoneRef{}
	}
	maps.Copy(g.zones, found)
	g.mu.Unlock()
	return out, nil
}

// purgeRefused turns the pointer cache off when Cloudflare refused the token: config.json is
// written again without it.
func (m *mod) purgeRefused(ctx context.Context, err error) {
	ce := asCall(err)
	if ce == nil || (ce.Status != http.StatusUnauthorized && ce.Status != http.StatusForbidden && !ce.has(10000)) {
		return
	}
	g := &m.gate
	g.mu.Lock()
	was := g.ok
	g.ok, g.why, g.at = false, refusedWhy(ce), time.Now()
	g.mu.Unlock()
	if was {
		m.mark(context.WithoutCancel(ctx), configKey)
	}
}

func refusedWhy(ce *callError) string {
	if ce.Status == http.StatusUnauthorized {
		return "Cloudflare does not accept the API token (" + ce.Error() + "), so the server cannot purge cached pointers: the Worker reads every pointer in R2."
	}
	return "The API token lacks " + purgePerm + " (" + ce.Error() + "), so the server cannot purge cached pointers: the Worker reads every pointer in R2."
}

// checkPurge starts, while edge delivery is on, the proof that the pointer cache works when it is
// due (purgeProof); when its answer turns the pointer cache on or off, config.json is written
// again.
func (m *mod) checkPurge(ctx context.Context) {
	snap := m.h.Settings.Current()
	token := CloudflareToken.Get(snap)
	hosts := m.routeHosts(snap)
	key := gateKey(token, hosts)
	g := &m.gate
	g.mu.Lock()
	every := purgeRetry
	if g.ok {
		every = purgeRecheck
	}
	due := !g.running && (g.key != key || time.Since(g.at) >= every)
	was := g.ok && g.key == key
	if due {
		g.running = true
	}
	g.mu.Unlock()
	if !due {
		return
	}
	home := m.cur.Load().home
	m.h.Go("edge.purge-proof", func(ctx context.Context) {
		ok, why := m.purgeProof(ctx, snap, home, token, hosts)
		g.mu.Lock()
		g.running = false
		if ctx.Err() == nil {
			g.key, g.ok, g.why, g.at = key, ok, why, time.Now()
		}
		g.mu.Unlock()
		if ctx.Err() == nil && ok != was {
			m.mark(ctx, configKey)
		}
	})
}

// purgeProof reports whether the pointer cache works on every route host, or why not: the token
// purges by tag on the zone of each host (a purge of an unused tag), and on each host a tag purge
// removes an entry that the Worker put into the Cache API while a control entry without a purge
// stays.
func (m *mod) purgeProof(ctx context.Context, snap *settings.Snapshot, home *bucket, token string, hosts []string) (bool, string) {
	if token == "" {
		return false, "Set a Cloudflare API token with " + purgePerm + " to cache pointers at the edge: until then the Worker reads every pointer in R2."
	}
	if len(hosts) == 0 || home == nil {
		return false, "Edge delivery is not on."
	}
	c := &cfClient{base: cfAPI, token: token}
	zones, err := m.zonesOf(ctx, c, snap, hosts)
	if err != nil {
		return false, "The zones of the routes are not known, so the server cannot purge cached pointers: " + c.redact(err.Error())
	}
	for _, z := range distinctZones(zones) {
		if err := purgeTags(ctx, c, z, []string{"tp-probe/" + randomHex()}); err != nil {
			if ce := asCall(err); ce != nil && (ce.Status == http.StatusUnauthorized || ce.Status == http.StatusForbidden || ce.has(10000)) {
				return false, refusedWhy(ce)
			}
			return false, "The check of the purge permission on the zone " + z.Name + " failed: " + c.redact(err.Error())
		}
	}
	scheme := "https"
	if u, err := url.Parse(settings.BaseURL.Get(snap)); err == nil && u.Scheme != "" {
		scheme = u.Scheme
	}
	for _, h := range hosts {
		if why := m.cacheProof(ctx, c, home, scheme, h, zones[h]); why != "" {
			return false, why
		}
	}
	return true, ""
}

// probeSettle is how long the proof waits after its purge before it asks the Worker again.
const probeSettle = 5 * time.Second

// cacheProof makes the Worker on host put two entries with fresh tags into the Cache API, purges
// the tag of the second on zone, waits probeSettle and asks which entries are left: the first
// (the control) must be, the second must not. It returns why the proof failed, or "".
func (m *mod) cacheProof(ctx context.Context, c *cfClient, home *bucket, scheme, host string, zone zoneRef) string {
	type entry struct {
		Key string `json:"key"`
		Tag string `json:"tag"`
	}
	entries := []entry{{randomHex(), "tp-probe/" + randomHex()}, {randomHex(), "tp-probe/" + randomHex()}}
	if got, why := m.askProbe(ctx, home, scheme, host, "put", entries); why != "" {
		return why
	} else if len(got) != 2 {
		return "The Worker on " + host + " answered the probe of the pointer cache with " + strconv.Itoa(len(got)) + " results, not 2."
	}
	if err := purgeTags(ctx, c, zone, []string{entries[1].Tag}); err != nil {
		if ce := asCall(err); ce != nil && (ce.Status == http.StatusUnauthorized || ce.Status == http.StatusForbidden || ce.has(10000)) {
			return refusedWhy(ce)
		}
		return "The purge of the probe entry on the zone " + zone.Name + " failed: " + c.redact(err.Error())
	}
	select {
	case <-ctx.Done():
		return ctx.Err().Error()
	case <-time.After(probeSettle):
	}
	got, why := m.askProbe(ctx, home, scheme, host, "match", entries)
	switch {
	case why != "":
		return why
	case len(got) != 2 || !got[0]:
		return "The Cache API on " + host + " did not keep the control entry of the probe, so the server cannot prove that a purge removes a cached pointer: the Worker reads every pointer in R2."
	case got[1]:
		return fmt.Sprintf("A purge by Cache-Tag on the zone %s did not remove the probe entry on %s within %s: the Worker reads every pointer in R2.", zone.Name, host, probeSettle)
	}
	return ""
}

// askProbe writes probe/<hex SHA-256 of a fresh nonce>.json with op and entries to the home
// bucket and asks the Worker on host to run it (edge/src/check.ts). It returns the Worker's
// results, or why it gave none.
func (m *mod) askProbe(ctx context.Context, home *bucket, scheme, host, op string, entries any) ([]bool, string) {
	raw := make([]byte, 32)
	rand.Read(raw)
	nonce := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(nonce))
	key := root + "probe/" + hex.EncodeToString(sum[:]) + ".json"
	body, _ := json.Marshal(map[string]any{"exp": time.Now().Add(checkTTL).Unix(), "op": op, "entries": entries})
	pctx, cancel := context.WithTimeout(ctx, opTimeout)
	err := home.st.Put(pctx, key, bytes.NewReader(body), int64(len(body)), "application/json")
	cancel()
	if err != nil {
		return nil, "Writing the probe of the pointer cache to the home bucket failed: " + errText(err)
	}
	defer func() {
		// The Worker deletes it when it takes it; else it goes here.
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), opTimeout)
		defer cancel()
		_ = home.st.Delete(dctx, key)
	}()
	u := url.URL{Scheme: scheme, Host: host, Path: probePath, RawQuery: "n=" + nonce}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "No probe request to " + host + ": " + err.Error()
	}
	req.Header.Set("Cache-Control", "no-store")
	resp, err := checkClient.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // the URL holds the nonce
		}
		return nil, "No answer from " + host + " to the probe of the pointer cache: " + err.Error()
	}
	defer resp.Body.Close()
	var a struct {
		Ran     bool   `json:"ran"`
		Error   string `json:"error"`
		Results []bool `json:"results"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&a)
	if resp.Header.Get(checkHeader) != "worker" || resp.StatusCode != http.StatusOK || !a.Ran {
		detail := a.Error
		if detail == "" {
			detail = "HTTP " + strconv.Itoa(resp.StatusCode)
		}
		return nil, "The Worker on " + host + " did not run the probe of the pointer cache (" + detail + "): deploy the Worker of this server version."
	}
	return a.Results, ""
}

func randomHex() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// pointerCacheJSON is pointer_cache of GET /api/edge/status.
type pointerCacheJSON struct {
	On  bool   `json:"on"`
	TTL int64  `json:"ttl"` // seconds
	Why string `json:"why"` // why it is off ("" while on, and before the first check)
}

func (m *mod) pointerCacheStatus() pointerCacheJSON {
	snap := m.h.Settings.Current()
	g := &m.gate
	g.mu.Lock()
	defer g.mu.Unlock()
	on := g.ok && g.key == gateKey(CloudflareToken.Get(snap), m.routeHosts(snap))
	why := g.why
	if on {
		why = ""
	}
	return pointerCacheJSON{On: on, TTL: ttlSeconds(pointerTTL), Why: why}
}
