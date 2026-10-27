package edge

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/edge/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/edgedist"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
	"github.com/Telesphorepo/Telesphorepo/internal/storage"
)

// The deploy of the edge Worker from the server (POST /api/edge/deploy and the job edge.deploy):
// with a Cloudflare API token (edge.cloudflare_token), the server makes in the Cloudflare account
// of the R2 buckets what wrangler.jsonc and scripts/provision.ts describe, through the Cloudflare
// v4 API: the regional buckets, the copy queue, the notification rule of the home bucket, the
// Worker with its bindings, its queue consumer, its cron and its zone routes. Each step reads
// first and changes only what differs, so a deploy runs again safely.

// ActionDeploy is the audit action of a deploy of the Worker (a person's, or the system actor's).
const ActionDeploy = "edge.deploy"

// ProblemDeployRefused answers POST /api/edge/deploy when the settings cannot deploy: no token,
// an S3 endpoint that names no Cloudflare account, or settings that cannot run edge delivery.
var ProblemDeployRefused = httpx.DeclareProblem(httpx.Problem{Name: "edge-deploy-refused", Status: http.StatusConflict,
	Title: "The server cannot deploy the edge Worker"})

// ProblemDeployBusy answers POST /api/edge/deploy while a deploy runs.
var ProblemDeployBusy = httpx.DeclareProblem(httpx.Problem{Name: "edge-deploy-busy", Status: http.StatusConflict,
	Title: "A deploy of the edge Worker runs"})

const (
	// deployDebounce is the quiet time after a settings change before a deploy of its own.
	deployDebounce = 10 * time.Second
	// The backoff of the deploys of its own after a failure.
	deployBackoffMin = time.Minute
	deployBackoffMax = time.Hour
	// deployTimeout bounds one whole deploy.
	deployTimeout = 10 * time.Minute
	// The consumer of the copy queue (wrangler.jsonc: max_batch_size, max_retries,
	// max_batch_timeout).
	consumerBatch   = 100
	consumerRetries = 10
	consumerWaitMS  = 5000
	// notifyDescription names the notification rule of the copy queue.
	notifyDescription = "telesphorepo edge copies"
	// mainModule is the entry module of the bundle (dist/index.js of the Worker folder).
	mainModule = "index.js"
)

// notifyActions are the actions of the notification rule: object-create (PutObject, CopyObject,
// CompleteMultipartUpload) and object-delete (DeleteObject, LifecycleDeletion).
var notifyActions = []string{"PutObject", "CopyObject", "CompleteMultipartUpload", "DeleteObject", "LifecycleDeletion"}

// The steps of a deploy, in order. stepText names each one in words; stepPerm is the token
// permission that it needs (Cloudflare's token form: resource, permission group, access).
const (
	stepVerify       = "verify"
	stepZones        = "zones"
	stepBuckets      = "buckets"
	stepQueue        = "queue"
	stepNotification = "notification"
	stepUpload       = "upload"
	stepConsumer     = "consumer"
	stepTriggers     = "triggers"
	stepRoutes       = "routes"
	stepCheck        = "check"
)

var stepText = map[string]string{
	stepVerify:       "Verify the token",
	stepZones:        "Find the zone of each route host",
	stepBuckets:      "Check the buckets",
	stepQueue:        "Create the copy queue",
	stepNotification: "Put the notification rule of the home bucket",
	stepUpload:       "Upload the Worker",
	stepConsumer:     "Set the queue consumer",
	stepTriggers:     "Set the cron and turn off workers.dev",
	stepRoutes:       "Set the routes",
	stepCheck:        "Check the Worker",
}

// stepPerm is the permission that each step needs, as Cloudflare's token form names it: the scope of
// its policy, then the category › name: level.
var stepPerm = map[string]string{
	stepZones:        "DNS & Zones › Zone: Read, in a policy of All Domains or the domain of each route host",
	stepBuckets:      "Developer Platform › Workers R2 Storage: Edit, in a policy of the Entire Account",
	stepQueue:        "Developer Platform › Queues: Edit, in a policy of the Entire Account",
	stepNotification: "Developer Platform › Workers R2 Storage: Edit, in a policy of the Entire Account",
	stepUpload:       "Developer Platform › Workers: Admin (Editor cannot create the Worker), in a policy of the Entire Account",
	stepConsumer:     "Developer Platform › Queues: Edit, in a policy of the Entire Account",
	stepTriggers:     "Developer Platform › Workers: Admin, in a policy of the Entire Account",
	stepRoutes:       "Developer Platform › Workers Routes: Edit, in a policy of All Domains or the domain of each route host",
}

// r2HostRE is the host of an R2 S3 endpoint: the account, and a jurisdiction.
var r2HostRE = regexp.MustCompile(`^([0-9a-f]{32})(?:\.(eu|fedramp|us))?\.r2\.cloudflarestorage\.com$`)

// r2Account returns the Cloudflare account and the jurisdiction ("" for the default one) of an S3
// endpoint, or why it names none.
func r2Account(endpoint string) (account, jurisdiction string, why string) {
	u, err := url.Parse(endpoint)
	host := ""
	if err == nil {
		host = strings.ToLower(u.Hostname())
	}
	if m := r2HostRE.FindStringSubmatch(host); m != nil {
		return m[1], m[2], ""
	}
	shown := endpoint
	if shown == "" {
		shown = "(empty: AWS)"
	}
	return "", "", fmt.Sprintf("The S3 endpoint %s names no Cloudflare account. The server deploys the Worker to the account of an R2 endpoint "+
		"(https://<account ID>.r2.cloudflarestorage.com, or the .eu, .fedramp or .us one of a jurisdiction).", shown)
}

// deployPlan is the wanted state of the Worker in Cloudflare for the settings: what a deploy
// makes, and what its hash covers.
type deployPlan struct {
	Version      string      `json:"version"`
	Compat       string      `json:"compatibility_date"`
	Account      string      `json:"account"`
	Jurisdiction string      `json:"jurisdiction"`
	Home         string      `json:"home"`
	Regions      []regionCfg `json:"regions"`
	Queue        string      `json:"queue"`
	Prefix       string      `json:"prefix"`        // the var PREFIX
	NotifyPrefix string      `json:"notify_prefix"` // [s3.prefix/]blobs/
	Routes       []string    `json:"routes"`
	Cron         string      `json:"cron"`
	Consumer     [3]int      `json:"consumer"` // batch, retries, wait (ms)
	Script       string      `json:"script"`
}

// hash is the hash of the wanted state.
func (p *deployPlan) hash() string {
	b, _ := json.Marshal(p)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}

// plan returns the wanted state for s, or why s cannot run edge delivery (as wrangler.jsonc). The
// account is "" when the S3 endpoint names none.
func (m *mod) plan(s *settings.Snapshot) (*deployPlan, error) {
	if storage.Backend.Get(s) != "s3" {
		return nil, errors.New("edge delivery needs storage s3 (Cloudflare R2)")
	}
	host, err := baseHost(s)
	if err != nil {
		return nil, err
	}
	if HomeHint.Get(s) == "" {
		return nil, errors.New("choose the location of the home bucket (edge.home_hint) first")
	}
	home := storage.S3Bucket.Get(s)
	routes, _ := m.routePatterns(s, host)
	prefix := storage.S3Prefix.Get(s)
	notify := "blobs/"
	if p := strings.Trim(prefix, "/"); p != "" {
		notify = p + "/blobs/"
	}
	account, jurisdiction, _ := r2Account(storage.S3Endpoint.Get(s))
	return &deployPlan{
		Version: m.h.Version, Compat: compatibilityDate, Account: account, Jurisdiction: jurisdiction,
		Home: home, Regions: regionsOf(s), Queue: copyQueue(home), Prefix: prefix, NotifyPrefix: notify,
		Routes: routes, Cron: gapCron, Consumer: [3]int{consumerBatch, consumerRetries, consumerWaitMS}, Script: workerName,
	}, nil
}

// deployUnfit returns why a deploy cannot start with the settings s, or "".
func (m *mod) deployUnfit(s *settings.Snapshot, p *deployPlan, perr error) string {
	if perr != nil {
		return "The settings cannot run edge delivery: " + perr.Error() + "."
	}
	if CloudflareToken.Get(s) == "" {
		return "Set a Cloudflare API token (edge.cloudflare_token) to deploy the Worker from the server."
	}
	if p.Account == "" {
		_, _, why := r2Account(storage.S3Endpoint.Get(s))
		return why
	}
	for _, r := range p.Routes {
		if h, _, _ := strings.Cut(r, "/"); strings.Contains(h, ":") {
			return fmt.Sprintf("The base URL host %s has a port. A Worker route cannot name a port: serve the base URL on port 443 (or 80).", h)
		}
	}
	return m.workerMissing()
}

// deployState is the row "deploy" of edge_state: the last deploy.
type deployState struct {
	// Deployed is the hash of the wanted state of the last deploy that made its routes; Zones are
	// the zones of its routes, so a later deploy deletes the routes of a removed domain there.
	Deployed string    `json:"deployed"`
	Zones    []zoneRef `json:"zones,omitempty"`
	// OutdatedTried is the hash of a deploy that ran because the Worker was outdated: it runs once
	// per wanted state.
	OutdatedTried string `json:"outdated_tried,omitempty"`
	At            int64  `json:"at"` // unix seconds; 0 before the first deploy
	OK            bool   `json:"ok"`
	Step          string `json:"step,omitempty"` // the failed step
	Error         string `json:"error,omitempty"`
	// Call is the Cloudflare call that failed.
	Call    *callError `json:"call,omitempty"`
	Changed []string   `json:"changed,omitempty"`
}

type zoneRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// deployer is the state of the deploys in memory.
type deployer struct {
	mu      sync.Mutex    // held while a deploy runs
	running atomic.Bool   // a deploy runs
	step    atomic.Value  // string: the step that runs
	kick    chan struct{} // a settings change: debounce, then deploy if needed
	reset   atomic.Bool   // the next kick drops the backoff

	stMu    sync.Mutex // guards st, backoff, nextTry
	st      deployState
	backoff time.Duration
	nextTry time.Time
}

func newDeployer() *deployer {
	d := &deployer{kick: make(chan struct{}, 1)}
	d.step.Store("")
	return d
}

func (d *deployer) state() deployState {
	d.stMu.Lock()
	defer d.stMu.Unlock()
	return d.st
}

// loadDeploy reads the row "deploy".
func (m *mod) loadDeploy(ctx context.Context) error {
	raw, err := m.rq.GetState(ctx, "deploy")
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var st deployState
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		return fmt.Errorf("edge deploy state: %w", err)
	}
	m.dep.stMu.Lock()
	m.dep.st = st
	m.dep.stMu.Unlock()
	return nil
}

// saveDeploy writes st as the row "deploy".
func (m *mod) saveDeploy(ctx context.Context, st deployState) {
	m.dep.stMu.Lock()
	m.dep.st = st
	m.dep.stMu.Unlock()
	b, _ := json.Marshal(st)
	if err := m.q.PutState(context.WithoutCancel(ctx), dbq.PutStateParams{Key: "deploy", Value: string(b)}); err != nil {
		m.h.Log.Warn("edge: saving the deploy state failed", "error", err.Error())
	}
}

// kickDeploy notes a settings change for the job edge.deploy.
func (m *mod) kickDeploy() {
	m.dep.reset.Store(true)
	select {
	case m.dep.kick <- struct{}{}:
	default:
	}
}

// deployJSON is deploy of GET /api/edge/status.
type deployJSON struct {
	TokenSet bool `json:"token_set"`
	// Auto: edge.auto_deploy is on and a token is set.
	Auto    bool `json:"auto"`
	Running bool `json:"running"`
	// Step is the step that runs, or the step of the last deploy that failed ("" after a success).
	Step string `json:"step"`
	// Unfit says why a deploy cannot start now ("" when it can).
	Unfit string `json:"unfit"`
	// Wanted is the hash of the wanted state of the settings ("" when they cannot run edge
	// delivery); Deployed the hash of the last deploy that made its routes ("" before one).
	Wanted   string `json:"wanted"`
	Deployed string `json:"deployed"`
	// Needed: a token is set and the wanted state differs from the deployed one.
	Needed    bool       `json:"needed"`
	LastAt    *time.Time `json:"last_at"`
	LastOK    bool       `json:"last_ok"`
	LastError string     `json:"last_error"`
	// LastCall is the Cloudflare call of the last failure (null when it failed without one).
	LastCall *callError `json:"last_call"`
	Changed  []string   `json:"changed"`
	// NextAt is when the job tries again after a failure (null when no try waits).
	NextAt *time.Time `json:"next_at"`
}

// deployStatus is the deploy part of the status.
func (m *mod) deployStatus(s *settings.Snapshot) deployJSON {
	p, perr := m.plan(s)
	st := m.dep.state()
	out := deployJSON{TokenSet: CloudflareToken.Get(s) != "", Running: m.dep.running.Load(),
		Unfit: m.deployUnfit(s, p, perr), Deployed: st.Deployed, Changed: st.Changed}
	if out.Changed == nil {
		out.Changed = []string{}
	}
	out.Auto = out.TokenSet && AutoDeploy.Get(s)
	if p != nil {
		out.Wanted = p.hash()
	}
	out.Needed = out.TokenSet && out.Wanted != "" && out.Wanted != out.Deployed
	if out.Running {
		out.Step, _ = m.dep.step.Load().(string)
	} else if !st.OK {
		out.Step = st.Step
	}
	if st.At != 0 {
		t := time.Unix(st.At, 0).UTC()
		out.LastAt, out.LastOK, out.LastError, out.LastCall = &t, st.OK, st.Error, st.Call
	}
	m.dep.stMu.Lock()
	if !m.dep.nextTry.IsZero() && out.Auto && !out.Running {
		t := m.dep.nextTry.UTC()
		out.NextAt = &t
	}
	m.dep.stMu.Unlock()
	return out
}

// deployNow is POST /api/edge/deploy: it starts a deploy and answers 202 with the deploy part of
// the status; 409 edge-deploy-refused when the settings cannot deploy, 409 edge-deploy-busy while
// one runs.
func (m *mod) deployNow(w http.ResponseWriter, r *http.Request, c *route.Call) {
	snap := m.h.Settings.Current()
	p, perr := m.plan(snap)
	if why := m.deployUnfit(snap, p, perr); why != "" {
		ProblemDeployRefused.Write(w, r, why)
		return
	}
	if !m.dep.mu.TryLock() {
		ProblemDeployBusy.Write(w, r, "A deploy of the edge Worker runs. Its step shows on the Edge delivery page.")
		return
	}
	m.dep.running.Store(true)
	actor := audit.ActorOf(r.Context())
	m.h.Go("edge.deploy-now", func(ctx context.Context) {
		defer m.dep.mu.Unlock()
		m.runOne(audit.WithActor(ctx, actor), "manual")
	})
	httpx.WriteJSON(w, http.StatusAccepted, m.deployStatus(snap))
}

// runDeployJob is the job edge.deploy: when a token is set and edge.auto_deploy is on, it deploys
// after a settings change (once the settings were quiet for deployDebounce) and when it starts,
// if the wanted state differs from the deployed one or the deployed Worker is outdated. A
// failure waits from deployBackoffMin, doubling to deployBackoffMax; a settings change drops the
// wait.
func (m *mod) runDeployJob(ctx context.Context) {
	quiet := time.Now().Add(deployDebounce)
	wait := deployDebounce
	for {
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-m.dep.kick:
			t.Stop()
			quiet = time.Now().Add(deployDebounce)
			if m.dep.reset.Swap(false) {
				m.dep.stMu.Lock()
				m.dep.backoff, m.dep.nextTry = 0, time.Time{}
				m.dep.stMu.Unlock()
			}
		case <-t.C:
		}
		now := time.Now()
		if now.Before(quiet) {
			wait = quiet.Sub(now)
			continue
		}
		wait = time.Minute
		m.dep.stMu.Lock()
		next := m.dep.nextTry
		m.dep.stMu.Unlock()
		if now.Before(next) {
			wait = min(wait, next.Sub(now))
			continue
		}
		if !m.autoDue() || !m.dep.mu.TryLock() {
			continue
		}
		m.dep.running.Store(true)
		m.runOne(ctx, "auto")
		m.dep.mu.Unlock()
	}
}

// autoDue reports whether the job deploys now.
func (m *mod) autoDue() bool {
	s := m.h.Settings.Current()
	if CloudflareToken.Get(s) == "" || !AutoDeploy.Get(s) {
		return false
	}
	p, perr := m.plan(s)
	if m.deployUnfit(s, p, perr) != "" {
		return false
	}
	st := m.dep.state()
	want := p.hash()
	if want != st.Deployed {
		return true
	}
	g, _ := m.gap.Load().(*gapState)
	return g != nil && g.Version != "" && m.outdated(g.Features) != "" && st.OutdatedTried != want
}

// runOne runs one deploy with the current settings; the caller holds dep.mu and set running. It
// keeps the result, records the audit event and sets the backoff.
func (m *mod) runOne(ctx context.Context, trigger string) {
	defer m.dep.running.Store(false)
	defer m.dep.step.Store("")
	ctx, cancel := context.WithTimeout(ctx, deployTimeout)
	defer cancel()
	snap := m.h.Settings.Current()
	p, perr := m.plan(snap)
	prev := m.dep.state()
	st := deployState{Deployed: prev.Deployed, Zones: prev.Zones, OutdatedTried: prev.OutdatedTried, At: time.Now().Unix()}
	if why := m.deployUnfit(snap, p, perr); why != "" {
		st.Error = why
	} else {
		g, _ := m.gap.Load().(*gapState)
		outdated := g != nil && g.Version != "" && m.outdated(g.Features) != ""
		r := &deployRun{m: m, p: p, c: &cfClient{base: cfAPI, token: CloudflareToken.Get(snap)}, prev: prev.Zones}
		err := r.run(ctx)
		st.Changed = r.changed
		if r.routed {
			st.Deployed, st.Zones = p.hash(), r.zoneList()
			if outdated {
				st.OutdatedTried = st.Deployed
			}
		}
		var se *stepError
		switch {
		case err == nil:
			st.OK = true
		case errors.As(err, &se):
			st.Step, st.Error, st.Call = se.step, se.Error(), se.call
		default:
			st.Error = r.c.redact(err.Error())
		}
		if r.routed && !m.moves.Running() {
			// The Worker of these settings runs: edge delivery may start in a new home.
			m.ack.Store(true)
			m.signal()
		}
	}
	m.saveDeploy(ctx, st)

	m.dep.stMu.Lock()
	if st.OK {
		m.dep.backoff, m.dep.nextTry = 0, time.Time{}
	} else {
		m.dep.backoff = min(max(deployBackoffMin, 2*m.dep.backoff), deployBackoffMax)
		wait := m.dep.backoff
		if st.Call != nil && st.Call.RetryAfter > 0 {
			// Cloudflare asked for a longer wait than the call could take.
			wait = max(wait, time.Duration(st.Call.RetryAfter)*time.Second)
		}
		m.dep.nextTry = time.Now().Add(wait)
	}
	m.dep.stMu.Unlock()

	detail := map[string]any{"ok": st.OK, "trigger": trigger, "version": m.h.Version, "changed": st.Changed}
	if !st.OK {
		detail["step"], detail["error"] = st.Step, st.Error
	}
	_ = m.h.Audit.Record(ctx, audit.Event{Action: ActionDeploy, Target: workerName, Detail: detail})
	if st.OK {
		m.h.Log.Info("edge: deployed the Worker", "version", m.h.Version, "trigger", trigger, "changed", len(st.Changed))
	} else {
		m.h.Log.Warn("edge: the deploy of the Worker failed", "trigger", trigger, "step", st.Step, "error", st.Error)
	}
}

// stepError is the failure of one step: what failed, what to do, and the Cloudflare call.
type stepError struct {
	step string
	msg  string
	call *callError
}

func (e *stepError) Error() string { return stepText[e.step] + ": " + e.msg }

// deployRun is one deploy.
type deployRun struct {
	m       *mod
	p       *deployPlan
	c       *cfClient
	prev    []zoneRef          // the zones of the last deploy's routes
	zones   map[string]zoneRef // route host -> its zone
	queueID string
	changed []string
	routed  bool // the routes match the wanted ones
}

// acct is the path of the account.
func (r *deployRun) acct() string { return "/accounts/" + seg(r.p.Account) }

// jur is the header of the jurisdiction of the R2 calls.
func (r *deployRun) jur() map[string]string {
	if r.p.Jurisdiction == "" {
		return nil
	}
	return map[string]string{"cf-r2-jurisdiction": r.p.Jurisdiction}
}

func (r *deployRun) note(format string, args ...any) {
	r.changed = append(r.changed, fmt.Sprintf(format, args...))
}

// fail is the failure of step: what (in words) failed with err, and what to do.
func (r *deployRun) fail(step, what string, err error) error {
	ce := asCall(err)
	if ce == nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return &stepError{step: step, msg: what + ": the deploy ran out of time or the server is stopping. Deploy again."}
		}
		return &stepError{step: step, msg: what + ": " + r.c.redact(err.Error())}
	}
	msg := what + ": " + strings.TrimSuffix(ce.Error(), ".") + "."
	switch {
	case ce.Status == http.StatusUnauthorized:
		msg += " Cloudflare does not accept the token: set a valid API token under Edge delivery."
	case ce.Status == http.StatusForbidden || ce.has(10000):
		if p := stepPerm[step]; p != "" {
			msg += " The token lacks a permission: this step needs " + p + ". Edit the token in the Cloudflare dashboard, add it, and deploy again."
		} else {
			msg += " The token lacks a permission for this call."
		}
	case ce.Status == http.StatusTooManyRequests:
		msg += " Cloudflare limits the rate of API calls. Deploy again later."
	case ce.Status >= 500 || ce.Transport != "":
		msg += " Cloudflare did not answer as expected. Deploy again later."
	}
	return &stepError{step: step, msg: msg, call: ce}
}

// run runs the steps in order.
func (r *deployRun) run(ctx context.Context) error {
	steps := []struct {
		id string
		fn func(context.Context) error
	}{
		{stepVerify, r.verify}, {stepZones, r.findZones}, {stepBuckets, r.buckets}, {stepQueue, r.queue},
		{stepNotification, r.notification}, {stepUpload, r.upload}, {stepConsumer, r.consumer},
		{stepTriggers, r.triggers}, {stepRoutes, r.setRoutes}, {stepCheck, r.check},
	}
	for _, s := range steps {
		r.m.dep.step.Store(s.id)
		if err := s.fn(ctx); err != nil {
			return err
		}
	}
	return nil
}

// verify checks the token: as an account token, else as a user token. It must be active.
func (r *deployRun) verify(ctx context.Context) error {
	var res struct {
		Status string `json:"status"`
	}
	_, err := r.c.get(ctx, r.acct()+"/tokens/verify", nil, nil, &res)
	if err != nil {
		var ures struct {
			Status string `json:"status"`
		}
		if _, uerr := r.c.get(ctx, "/user/tokens/verify", nil, nil, &ures); uerr == nil {
			res = ures
			err = nil
		} else if ce, uce := asCall(err), asCall(uerr); ce != nil && uce != nil && ce.Transport == "" && uce.Transport == "" {
			return &stepError{step: stepVerify, call: ce, msg: fmt.Sprintf("Cloudflare refused the token as a token of the account %s (%s) and as a user token (%s). "+
				"Set a valid API token under Edge delivery: an account API token of the account of the R2 buckets, or a user API token that reaches it.",
				r.p.Account, ce.Error(), uce.Error())}
		}
	}
	if err != nil {
		return r.fail(stepVerify, "The check of the token failed", err)
	}
	if res.Status != "active" {
		return &stepError{step: stepVerify, msg: fmt.Sprintf("The token is %s in Cloudflare. Set an active API token under Edge delivery.", res.Status)}
	}
	return nil
}

// cfZone is a zone of GET /zones.
type cfZone struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Account struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"account"`
}

// zoneNames returns the names that may be the zone of host: host, then each parent down to two
// labels.
func zoneNames(host string) []string {
	labels := strings.Split(host, ".")
	var out []string
	for i := 0; i+2 <= len(labels); i++ {
		out = append(out, strings.Join(labels[i:], "."))
	}
	return out
}

// lookupZone finds the zone named name, in the account when account is set.
func (r *deployRun) lookupZone(ctx context.Context, name, account string) (*cfZone, error) {
	q := url.Values{"name": {name}}
	if account != "" {
		q.Set("account.id", account)
	}
	var zs []cfZone
	if _, err := r.c.get(ctx, "/zones", q, nil, &zs); err != nil {
		return nil, err
	}
	for i := range zs {
		if strings.EqualFold(zs[i].Name, name) {
			return &zs[i], nil
		}
	}
	return nil, nil
}

// findZones finds the zone of each route host in the account, walking up its labels.
func (r *deployRun) findZones(ctx context.Context) error {
	r.zones = map[string]zoneRef{}
	for _, host := range r.hosts() {
		z, err := r.zoneOf(ctx, host)
		if err != nil {
			return err
		}
		r.zones[host] = z
	}
	return nil
}

// zoneOf finds the zone of host in the account, walking up its labels.
func (r *deployRun) zoneOf(ctx context.Context, host string) (zoneRef, error) {
	var found *cfZone
	for _, name := range zoneNames(host) {
		z, err := r.lookupZone(ctx, name, r.p.Account)
		if err != nil {
			return zoneRef{}, r.fail(stepZones, "Finding the zone of "+host+" failed", err)
		}
		if z != nil {
			found = z
			break
		}
	}
	if found == nil {
		msg := fmt.Sprintf("No zone of the Cloudflare account %s holds %s. A Worker route must be on a zone of the same Cloudflare account as the Worker (the account of the R2 buckets).", r.p.Account, host)
		for _, name := range zoneNames(host) {
			if z, err := r.lookupZone(ctx, name, ""); err == nil && z != nil {
				msg += fmt.Sprintf(" The zone %s is in the account %s (%s).", z.Name, z.Account.Name, z.Account.ID)
				break
			}
		}
		msg += " Move the zone into this account, or deploy from a computer with the route of " + host +
			" taken out of wrangler.jsonc: the server then serves that host itself. If the zone is in this account, give the token DNS & Zones › Zone: Read in a policy of All Domains or of that domain."
		return zoneRef{}, &stepError{step: stepZones, msg: msg}
	}
	if found.Status != "" && found.Status != "active" {
		return zoneRef{}, &stepError{step: stepZones, msg: fmt.Sprintf("The zone %s of %s is %s in Cloudflare, so a Worker route on it runs nothing. Finish the setup of the zone in Cloudflare, then deploy again.", found.Name, host, found.Status)}
	}
	return zoneRef{ID: found.ID, Name: found.Name}, nil
}

// hosts returns the hosts of the routes, sorted.
func (r *deployRun) hosts() []string {
	var out []string
	for _, p := range r.p.Routes {
		h, _, _ := strings.Cut(p, "/")
		h = strings.ToLower(h)
		if !slices.Contains(out, h) {
			out = append(out, h)
		}
	}
	slices.Sort(out)
	return out
}

// zoneList returns the zones of the routes, each once, sorted by name.
func (r *deployRun) zoneList() []zoneRef {
	var out []zoneRef
	for _, z := range r.zones {
		if !slices.Contains(out, z) {
			out = append(out, z)
		}
	}
	slices.SortFunc(out, func(a, b zoneRef) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// bucketMissing reports whether err is Cloudflare's answer for a bucket that does not exist.
func bucketMissing(err error) bool {
	ce := asCall(err)
	return ce != nil && (ce.Status == http.StatusNotFound || ce.has(10006))
}

// buckets checks the home bucket and creates each regional bucket that is missing, with its
// location hint.
func (r *deployRun) buckets(ctx context.Context) error {
	bp := r.acct() + "/r2/buckets/"
	if _, err := r.c.get(ctx, bp+seg(r.p.Home), nil, r.jur(), nil); err != nil {
		if bucketMissing(err) {
			e := r.fail(stepBuckets, "The home bucket "+r.p.Home+" does not exist in the Cloudflare account "+r.p.Account, err).(*stepError)
			e.msg += " It is the S3 bucket of the server (s3.bucket): check that s3.endpoint names the account of that bucket."
			return e
		}
		return r.fail(stepBuckets, "Reading the home bucket "+r.p.Home+" failed", err)
	}
	for _, rc := range r.p.Regions {
		_, err := r.c.get(ctx, bp+seg(rc.Bucket), nil, r.jur(), nil)
		if err == nil {
			continue
		}
		if !bucketMissing(err) {
			return r.fail(stepBuckets, "Reading the bucket "+rc.Bucket+" of "+rc.Hint+" failed", err)
		}
		body := map[string]string{"name": rc.Bucket, "locationHint": rc.Hint}
		if err := r.c.send(ctx, http.MethodPost, r.acct()+"/r2/buckets", r.jur(), body, nil); err != nil {
			return r.fail(stepBuckets, "Creating the bucket "+rc.Bucket+" in "+rc.Hint+" failed", err)
		}
		r.note("created the bucket %s in %s", rc.Bucket, rc.Hint)
	}
	return nil
}

// cfQueue is a queue of GET /accounts/{id}/queues.
type cfQueue struct {
	ID        string       `json:"queue_id"`
	Name      string       `json:"queue_name"`
	Consumers []cfConsumer `json:"consumers"`
}

type cfConsumer struct {
	ID         string `json:"consumer_id"`
	Type       string `json:"type"`
	ScriptName string `json:"script_name"`
	Script     string `json:"script"`
	Settings   struct {
		BatchSize     *int `json:"batch_size"`
		MaxRetries    *int `json:"max_retries"`
		MaxWaitTimeMS *int `json:"max_wait_time_ms"`
	} `json:"settings"`
}

func (c *cfConsumer) script() string {
	if c.ScriptName != "" {
		return c.ScriptName
	}
	return c.Script
}

// findQueue returns the queue named name, nil when the account has none.
func (r *deployRun) findQueue(ctx context.Context, name string) (*cfQueue, error) {
	for page := 1; page <= 100; page++ {
		var qs []cfQueue
		env, err := r.c.get(ctx, r.acct()+"/queues", url.Values{"page": {fmt.Sprint(page)}}, nil, &qs)
		if err != nil {
			return nil, err
		}
		for i := range qs {
			if qs[i].Name == name {
				return &qs[i], nil
			}
		}
		if len(qs) == 0 || env.ResultInfo == nil || env.ResultInfo.TotalPages <= page {
			return nil, nil
		}
	}
	return nil, nil
}

// queue creates the copy queue when it is missing.
func (r *deployRun) queue(ctx context.Context) error {
	q, err := r.findQueue(ctx, r.p.Queue)
	if err != nil {
		return r.fail(stepQueue, "Listing the queues failed", err)
	}
	if q == nil {
		var created cfQueue
		if err := r.c.send(ctx, http.MethodPost, r.acct()+"/queues", nil, map[string]string{"queue_name": r.p.Queue}, &created); err != nil {
			return r.fail(stepQueue, "Creating the queue "+r.p.Queue+" failed", err)
		}
		r.note("created the queue %s", r.p.Queue)
		q = &created
	}
	if q.ID == "" {
		return &stepError{step: stepQueue, msg: "Cloudflare named no ID for the queue " + r.p.Queue + "."}
	}
	r.queueID = q.ID
	return nil
}

// notification puts the notification rule of the home bucket when no rule of the copy queue
// covers [s3.prefix/]blobs/ with every action.
func (r *deployRun) notification(ctx context.Context) error {
	cfgPath := r.acct() + "/event_notifications/r2/" + seg(r.p.Home) + "/configuration"
	var cfg struct {
		Queues []struct {
			QueueID   string `json:"queueId"`
			QueueName string `json:"queueName"`
			Rules     []struct {
				Prefix  string   `json:"prefix"`
				Suffix  string   `json:"suffix"`
				Actions []string `json:"actions"`
			} `json:"rules"`
		} `json:"queues"`
	}
	if _, err := r.c.get(ctx, cfgPath, nil, r.jur(), &cfg); err != nil {
		// A bucket without notification rules answers 404 with error 11015.
		if ce := asCall(err); ce == nil || !ce.has(11015) {
			return r.fail(stepNotification, "Reading the notification rules of "+r.p.Home+" failed", err)
		}
	}
	for _, q := range cfg.Queues {
		if q.QueueID != r.queueID && q.QueueName != r.p.Queue {
			continue
		}
		for _, rule := range q.Rules {
			if rule.Prefix == r.p.NotifyPrefix && rule.Suffix == "" && covers(rule.Actions, notifyActions) {
				return nil
			}
		}
	}
	body := map[string]any{"rules": []map[string]any{{"actions": notifyActions, "prefix": r.p.NotifyPrefix, "description": notifyDescription}}}
	if err := r.c.send(ctx, http.MethodPut, cfgPath+"/queues/"+seg(r.queueID), r.jur(), body, nil); err != nil {
		e := r.fail(stepNotification, "Putting the notification rule of "+r.p.Home+" (prefix "+r.p.NotifyPrefix+", to the queue "+r.p.Queue+") failed", err).(*stepError)
		if e.call != nil && e.call.Status == http.StatusBadRequest {
			e.msg += " A rule of the bucket that overlaps it (the same prefix, or a prefix inside it) makes Cloudflare refuse it: delete that rule in the bucket's settings in Cloudflare, then deploy again."
		}
		return e
	}
	r.note("put the notification rule of %s on %s to %s", r.p.Home, r.p.NotifyPrefix, r.p.Queue)
	return nil
}

func covers(have, want []string) bool {
	for _, w := range want {
		if !slices.Contains(have, w) {
			return false
		}
	}
	return true
}

// bundleFile is one module of the bundle.
type bundleFile struct {
	name string
	body []byte
}

// bundle returns the modules of dist/ in the Worker folder of the build (source maps left out).
func bundle() ([]bundleFile, error) {
	src, _ := edgedist.Zip()
	if src == nil {
		return nil, errors.New("this server was built without the edge Worker")
	}
	zr, err := zip.NewReader(bytes.NewReader(src), int64(len(src)))
	if err != nil {
		return nil, fmt.Errorf("the edge Worker of this build does not open: %w", err)
	}
	dir := edgedist.Dir + "dist/"
	var out []bundleFile
	for _, f := range zr.File {
		name, ok := strings.CutPrefix(f.Name, dir)
		if !ok || name == "" || strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".map") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, err
		}
		out = append(out, bundleFile{name: name, body: b})
	}
	if !slices.ContainsFunc(out, func(b bundleFile) bool { return b.name == mainModule }) {
		return nil, errors.New("the edge Worker of this build has no dist/" + mainModule)
	}
	return out, nil
}

// moduleType is the Content-Type of a module part of the upload.
func moduleType(name string) string {
	switch path.Ext(name) {
	case ".js", ".mjs":
		return "application/javascript+module"
	case ".wasm":
		return "application/wasm"
	case ".txt", ".html":
		return "text/plain"
	}
	return "application/octet-stream"
}

// metadata is the metadata part of the upload: the main module, the compatibility date, the
// bindings (HOME and R2_<HINT>, COPY, PREFIX) and observability on.
func (r *deployRun) metadata() map[string]any {
	r2 := func(name, bucket string) map[string]any {
		b := map[string]any{"type": "r2_bucket", "name": name, "bucket_name": bucket}
		if r.p.Jurisdiction != "" {
			b["jurisdiction"] = r.p.Jurisdiction
		}
		return b
	}
	bindings := []map[string]any{r2("HOME", r.p.Home)}
	for _, rc := range r.p.Regions {
		bindings = append(bindings, r2(binding(rc.Hint), rc.Bucket))
	}
	bindings = append(bindings,
		map[string]any{"type": "queue", "name": copyQueueBinding, "queue_name": r.p.Queue},
		map[string]any{"type": "plain_text", "name": "PREFIX", "text": r.p.Prefix})
	return map[string]any{
		"main_module":        mainModule,
		"compatibility_date": r.p.Compat,
		"bindings":           bindings,
		"observability":      map[string]any{"enabled": true},
	}
}

// upload uploads the Worker: the module Worker of the build with its metadata, as multipart.
func (r *deployRun) upload(ctx context.Context) error {
	if why := r.m.workerMissing(); why != "" {
		return &stepError{step: stepUpload, msg: why}
	}
	files, err := bundle()
	if err != nil {
		return &stepError{step: stepUpload, msg: err.Error() + ". Build the server with go tool task build, or install a release."}
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	meta, _ := json.Marshal(r.metadata())
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="metadata"`)
	h.Set("Content-Type", "application/json")
	pw, err := mw.CreatePart(h)
	if err == nil {
		_, err = pw.Write(meta)
	}
	for _, f := range files {
		if err != nil {
			break
		}
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, f.name, f.name))
		h.Set("Content-Type", moduleType(f.name))
		pw, err = mw.CreatePart(h)
		if err == nil {
			_, err = pw.Write(f.body)
		}
	}
	if err == nil {
		err = mw.Close()
	}
	if err != nil {
		return &stepError{step: stepUpload, msg: "Packing the Worker failed: " + err.Error()}
	}
	_, err = r.c.do(ctx, cfRequest{method: http.MethodPut, path: r.acct() + "/workers/scripts/" + seg(workerName),
		body: body.Bytes(), ctype: mw.FormDataContentType(), upload: true}, nil)
	if err != nil {
		return r.fail(stepUpload, "Uploading the Worker "+workerName+" "+r.p.Version+" failed", err)
	}
	r.note("uploaded the Worker %s %s", workerName, r.p.Version)
	return nil
}

// consumer makes the Worker the consumer of the copy queue, with batches of 100, 10 retries and
// a 5 s wait. A queue that another Worker consumes is refused, never taken over.
func (r *deployRun) consumer(ctx context.Context) error {
	q, err := r.findQueue(ctx, r.p.Queue)
	if err != nil {
		return r.fail(stepConsumer, "Listing the queues failed", err)
	}
	if q == nil {
		return &stepError{step: stepConsumer, msg: "The queue " + r.p.Queue + " is gone from the account. Deploy again to create it."}
	}
	want := map[string]any{"batch_size": consumerBatch, "max_retries": consumerRetries, "max_wait_time_ms": consumerWaitMS}
	body := map[string]any{"type": "worker", "script_name": workerName, "settings": want}
	cpath := r.acct() + "/queues/" + seg(q.ID) + "/consumers"
	for _, c := range q.Consumers {
		if c.Type != "" && c.Type != "worker" {
			continue
		}
		if c.script() != workerName {
			return &stepError{step: stepConsumer, msg: fmt.Sprintf("The queue %s has the consumer Worker %s. The server never takes over a queue of another Worker: remove that consumer in Cloudflare (Queues › %s › Settings), then deploy again.", r.p.Queue, c.script(), r.p.Queue)}
		}
		s := c.Settings
		if s.BatchSize != nil && *s.BatchSize == consumerBatch && s.MaxRetries != nil && *s.MaxRetries == consumerRetries &&
			s.MaxWaitTimeMS != nil && *s.MaxWaitTimeMS == consumerWaitMS {
			return nil
		}
		if err := r.c.send(ctx, http.MethodPut, cpath+"/"+seg(c.ID), nil, body, nil); err != nil {
			return r.fail(stepConsumer, "Updating the consumer of "+r.p.Queue+" failed", err)
		}
		r.note("set the consumer of %s", r.p.Queue)
		return nil
	}
	if err := r.c.send(ctx, http.MethodPost, cpath, nil, body, nil); err != nil {
		return r.fail(stepConsumer, "Making the Worker the consumer of "+r.p.Queue+" failed", err)
	}
	r.note("made the Worker the consumer of %s", r.p.Queue)
	return nil
}

// triggers sets the cron of the gap check and turns off workers.dev and its preview URLs.
func (r *deployRun) triggers(ctx context.Context) error {
	sp := r.acct() + "/workers/scripts/" + seg(workerName)
	if err := r.c.send(ctx, http.MethodPut, sp+"/schedules", nil, []map[string]string{{"cron": r.p.Cron}}, nil); err != nil {
		return r.fail(stepTriggers, "Setting the cron "+r.p.Cron+" failed", err)
	}
	if err := r.c.send(ctx, http.MethodPost, sp+"/subdomain", nil, map[string]bool{"enabled": false, "previews_enabled": false}, nil); err != nil {
		return r.fail(stepTriggers, "Turning off workers.dev failed", err)
	}
	return nil
}

// cfRoute is a route of a zone.
type cfRoute struct {
	ID      string `json:"id"`
	Pattern string `json:"pattern"`
	Script  string `json:"script"`
}

// setRoutes makes the routes of each zone match the wanted ones: it creates the missing ones and
// deletes the routes of this Worker that are not wanted (a removed domain, on the zones of this
// deploy and of the last one). A wanted pattern that another Worker (or no Worker) holds is
// refused, never taken over.
func (r *deployRun) setRoutes(ctx context.Context) error {
	want := map[string][]string{} // zone id -> patterns
	for _, p := range r.p.Routes {
		h, _, _ := strings.Cut(p, "/")
		z := r.zones[strings.ToLower(h)]
		want[z.ID] = append(want[z.ID], p)
	}
	zones := r.zoneList()
	for _, z := range r.prev {
		if !slices.ContainsFunc(zones, func(x zoneRef) bool { return x.ID == z.ID }) {
			zones = append(zones, z)
		}
	}
	// First every refusal, then the changes, so a refused deploy changes no route.
	type zoneRoutes struct {
		z      zoneRef
		routes []cfRoute
	}
	var all []zoneRoutes
	for _, z := range zones {
		var routes []cfRoute
		if _, err := r.c.get(ctx, "/zones/"+seg(z.ID)+"/workers/routes", nil, nil, &routes); err != nil {
			if ce := asCall(err); ce != nil && ce.Status == http.StatusNotFound && len(want[z.ID]) == 0 {
				continue // a zone of the last deploy that is gone
			}
			return r.fail(stepRoutes, "Listing the routes of the zone "+z.Name+" failed", err)
		}
		for _, p := range want[z.ID] {
			for _, rt := range routes {
				if strings.EqualFold(rt.Pattern, p) && rt.Script != workerName {
					holder := "no Worker (a route that keeps the path off every Worker)"
					if rt.Script != "" {
						holder = "the Worker " + rt.Script
					}
					return &stepError{step: stepRoutes, msg: fmt.Sprintf("The route %s of the zone %s runs %s. The server never takes over a route of another Worker: delete that route in Cloudflare (the zone's Workers Routes), then deploy again.", p, z.Name, holder)}
				}
			}
		}
		all = append(all, zoneRoutes{z, routes})
	}
	for _, zr := range all {
		zp := "/zones/" + seg(zr.z.ID) + "/workers/routes"
		for _, p := range want[zr.z.ID] {
			if slices.ContainsFunc(zr.routes, func(rt cfRoute) bool { return strings.EqualFold(rt.Pattern, p) }) {
				continue
			}
			if err := r.c.send(ctx, http.MethodPost, zp, nil, map[string]string{"pattern": p, "script": workerName}, nil); err != nil {
				return r.fail(stepRoutes, "Creating the route "+p+" on the zone "+zr.z.Name+" failed", err)
			}
			r.note("created the route %s", p)
		}
		for _, rt := range zr.routes {
			if rt.Script != workerName || slices.ContainsFunc(want[zr.z.ID], func(p string) bool { return strings.EqualFold(rt.Pattern, p) }) {
				continue
			}
			if _, err := r.c.do(ctx, cfRequest{method: http.MethodDelete, path: zp + "/" + seg(rt.ID)}, nil); err != nil {
				return r.fail(stepRoutes, "Deleting the route "+rt.Pattern+" of the zone "+zr.z.Name+" failed", err)
			}
			r.note("deleted the route %s", rt.Pattern)
		}
	}
	r.routed = true
	return nil
}

// check runs the check of the Worker (as Check now) until the Worker answers, at most four times
// 10 s apart: a new route takes a few seconds to reach every data center.
func (r *deployRun) check(ctx context.Context) error {
	base, why := checkBase(r.m.h.Settings.Current())
	if why != "" {
		return &stepError{step: stepCheck, msg: why}
	}
	var res checkResult
	for try := 0; try < 4; try++ {
		if try > 0 {
			select {
			case <-ctx.Done():
				return r.fail(stepCheck, "The check of the Worker", ctx.Err())
			case <-time.After(checkSpacing):
			}
		}
		res = r.m.checkOnce(ctx, base)
		if res.OK {
			return nil
		}
	}
	return &stepError{step: stepCheck, msg: "Cloudflare took the Worker and its routes, but its check did not pass: " + res.Message}
}

// checkOnce runs one check of the Worker, waiting for a check that runs and for the spacing
// between checks, and keeps its result for the status.
func (m *mod) checkOnce(ctx context.Context, base *url.URL) checkResult {
	m.checkMu.Lock()
	defer m.checkMu.Unlock()
	if wait := checkSpacing - time.Since(m.lastCheck); wait > 0 {
		select {
		case <-ctx.Done():
			return checkResult{At: time.Now().UTC(), Outcome: "error", Message: "The deploy ran out of time."}
		case <-time.After(wait):
		}
	}
	m.lastCheck = time.Now()
	res := m.runCheck(ctx, base)
	m.lastResult.Store(&res)
	return res
}
