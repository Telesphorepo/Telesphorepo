package edge

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
	"github.com/Telesphorepo/Telesphorepo/internal/storage"
)

// The check of the deployed Worker (POST /api/edge/check). The server writes check.json (the
// SHA-256 of a one-time nonce, its expiry and whether a copy pass is wanted) to the home bucket,
// then sends GET <base URL>/.well-known/telesphorepo-edge/check?n=<nonce>. That request goes
// through Cloudflare: the Worker on the route of the reserved path answers it when the nonce
// matches check.json and it has not expired, deletes check.json, runs its gap check and answers
// with its version. When the request reaches the server instead, the server's own handler of the
// reserved path answers, so the check says that no Worker runs on that route.
const (
	checkPrefix = "/.well-known/telesphorepo-edge/"
	checkPath   = checkPrefix + "check"
	// checkHeader names who answered the reserved path: "worker" or "server".
	checkHeader = "Telesphorepo-Edge-Check"
	// checkTTL is how long check.json is valid; checkWait bounds the request (the Worker runs its
	// gap check for at most 20 s of it); checkSpacing is the shortest time between two checks.
	checkTTL     = 30 * time.Second
	checkWait    = 60 * time.Second
	checkSpacing = 10 * time.Second
)

// ProblemCheckBusy answers a check while one runs or within 10 s of the last one.
var ProblemCheckBusy = httpx.DeclareProblem(httpx.Problem{Name: "edge-check-busy", Status: http.StatusTooManyRequests,
	Title: "A check of the edge Worker runs or ran just now", Remedy: &httpx.Remedy{Action: httpx.RemedyRetry}})

// checkResult is the outcome of a check, the answer of POST /api/edge/check and last_check of the
// status. Outcome: "worker" (the Worker answered and ran its gap check), "refused" (the Worker
// answered and refused the check), "server" (the request reached this server), "other" (something
// else answered), "unreachable" (no answer), "error" (check.json could not be written).
type checkResult struct {
	At            time.Time `json:"at"`
	Outcome       string    `json:"outcome"`
	OK            bool      `json:"ok"`
	Message       string    `json:"message"`
	WorkerVersion string    `json:"worker_version,omitempty"`
}

// pendingCheck is the nonce of the check that runs, for the server's own handler of the reserved
// path.
type pendingCheck struct {
	hash [32]byte
	exp  time.Time
}

// workerAnswer is the JSON of the Worker's answer on the reserved path. Pass is "done" (a copy
// pass ended), "partial" (it goes on at the next run), "none" (no pass was due) or "off" (no pass
// runs: config.json is off or of another version, or the Worker has no copy queue).
type workerAnswer struct {
	Version string `json:"version"`
	Ran     bool   `json:"ran"`
	Pass    string `json:"pass"`
	Error   string `json:"error"`
}

// checkClient sends the request of the check; it follows no redirect.
var checkClient = &http.Client{
	Timeout:       checkWait,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// check runs one check of the deployed Worker: one at a time, at most one every 10 seconds.
func (m *mod) check(w http.ResponseWriter, r *http.Request, c *route.Call) {
	snap := m.h.Settings.Current()
	base, why := checkBase(snap)
	if why != "" {
		ProblemNotConfigured.Write(w, r, why)
		return
	}
	if !m.checkMu.TryLock() {
		w.Header().Set("Retry-After", strconv.Itoa(int(checkSpacing/time.Second)))
		ProblemCheckBusy.Write(w, r, "A check runs already.")
		return
	}
	defer m.checkMu.Unlock()
	if wait := checkSpacing - time.Since(m.lastCheck); wait > 0 {
		secs := int(math.Ceil(wait.Seconds()))
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		ProblemCheckBusy.Write(w, r, fmt.Sprintf("Check again in %d seconds.", secs))
		return
	}
	m.lastCheck = time.Now()
	res := m.runCheck(r.Context(), base)
	m.lastResult.Store(&res)
	httpx.WriteJSON(w, http.StatusOK, res)
}

// checkBase returns the base URL that the check sends its request to, or why the settings cannot
// run a check: it needs storage s3 (check.json goes to the home bucket) and a base URL without a
// path (the Worker's routes are on its host).
func checkBase(s *settings.Snapshot) (*url.URL, string) {
	if storage.Backend.Get(s) != "s3" {
		return nil, "edge delivery needs storage s3 (Cloudflare R2)"
	}
	if _, err := baseHost(s); err != nil {
		return nil, err.Error()
	}
	u, _ := url.Parse(settings.BaseURL.Get(s))
	return u, ""
}

// runCheck writes check.json, sends the request and reads gap/state.json again at once.
func (m *mod) runCheck(ctx context.Context, base *url.URL) checkResult {
	res := checkResult{At: time.Now().UTC()}
	home, err := m.gapHome(ctx)
	if err != nil {
		res.Outcome, res.Message = "error", "The home bucket did not open: "+errText(err)
		return res
	}
	defer m.readGap(ctx, home, true)

	raw := make([]byte, 32)
	rand.Read(raw)
	nonce := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(nonce))
	exp := time.Now().Add(checkTTL)
	body, _ := json.Marshal(map[string]any{"hash": hex.EncodeToString(sum[:]), "exp": exp.Unix(), "pass": m.passWanted()})
	pctx, cancel := context.WithTimeout(ctx, opTimeout)
	err = home.st.Put(pctx, root+checkKey, bytes.NewReader(body), int64(len(body)), "application/json")
	cancel()
	if err != nil {
		res.Outcome, res.Message = "error", "Writing the check request to the home bucket failed: "+errText(err)
		return res
	}
	m.pending.Store(&pendingCheck{hash: sum, exp: exp})
	defer func() {
		m.pending.Store(nil)
		// The Worker deletes check.json when it takes it; else it goes here.
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), opTimeout)
		defer cancel()
		_ = home.st.Delete(dctx, root+checkKey)
	}()

	u := url.URL{Scheme: base.Scheme, Host: base.Host, Path: checkPath, RawQuery: "n=" + nonce}
	shown := base.Scheme + "://" + base.Host
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		res.Outcome, res.Message = "unreachable", "No request to "+shown+": "+err.Error()
		return res
	}
	req.Header.Set("Cache-Control", "no-store")
	resp, err := checkClient.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // the URL holds the nonce
		}
		res.Outcome, res.Message = "unreachable", "No answer from "+shown+": "+err.Error()
		return res
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	switch resp.Header.Get(checkHeader) {
	case "server":
		res.Outcome = "server"
		res.Message = fmt.Sprintf("The request reached the server, not the Worker: the Worker is not deployed on %s, or not on this route. Deploy the Worker (Deploy, below), then check again.", base.Host)
	case "worker":
		var a workerAnswer
		_ = json.Unmarshal(answer, &a)
		res.WorkerVersion = a.Version
		switch {
		case resp.StatusCode != http.StatusOK || !a.Ran:
			res.Outcome = "refused"
			reason := a.Error
			if reason == "" {
				reason = "HTTP " + strconv.Itoa(resp.StatusCode)
			}
			res.Message = fmt.Sprintf("The Worker %s on %s refused the check: %s.", a.Version, base.Host, reason)
		case a.Version != m.h.Version:
			res.Outcome = "worker"
			res.Message = fmt.Sprintf("The Worker on %s is %s, and this server is %s. Download the Worker of this server version and deploy it.", base.Host, a.Version, m.h.Version)
		default:
			res.Outcome, res.OK = "worker", true
			res.Message = fmt.Sprintf("The Worker %s answered on %s and ran its gap check", a.Version, base.Host) + passText[a.Pass]
		}
	default:
		res.Outcome = "other"
		res.Message = fmt.Sprintf("%s answered HTTP %d, but neither the Worker nor this server sent it: a rule in front of the server (Cloudflare Access, a firewall rule) may block %s.", shown, resp.StatusCode, checkPrefix)
	}
	return res
}

// passText ends the message of a check that the Worker ran, by its copy pass.
var passText = map[string]string{
	"done":    ": the copy pass is done.",
	"partial": ": the copy pass goes on at its next run.",
	"none":    ". No copy pass was due.",
	"off":     ". It copies files once edge delivery is on.",
	"":        ".",
}

// passWanted reports whether the check asks the Worker for a copy pass: edge delivery is on and a
// region is not ready yet.
func (m *mod) passWanted() bool {
	if m.cur.Load().mode != modeOn {
		return false
	}
	for _, r := range m.published().regions {
		if !r.Ready {
			return true
		}
	}
	return false
}

// checkAnswer is the server's own answer on the reserved path: the request reached the origin, so
// no Worker answered it. It answers 200 only to the nonce of the check that runs.
func (m *mod) checkAnswer(w http.ResponseWriter, r *http.Request, c *route.Call) {
	w.Header().Set(checkHeader, "server")
	w.Header().Set("Cache-Control", "no-store")
	n := r.URL.Query().Get("n")
	p := m.pending.Load()
	sum := sha256.Sum256([]byte(n))
	if n == "" || p == nil || time.Now().After(p.exp) || subtle.ConstantTimeCompare(sum[:], p.hash[:]) != 1 {
		httpx.Error(w, r, http.StatusForbidden, "no check of the edge Worker was asked for with this nonce")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"answered_by": "server"})
}

// gapHome is the home bucket where gap/state.json and check.json live: the home bucket of edge
// delivery while it is on, else the S3 bucket of the storage settings (opened once per location).
func (m *mod) gapHome(ctx context.Context) (*bucket, error) {
	if v := m.cur.Load(); v.mode == modeOn && v.home != nil {
		return v.home, nil
	}
	cfg := storage.ConfigView.Get(m.h.Settings.Current())
	if cfg.Backend != "s3" {
		return nil, errors.New("edge delivery needs storage s3")
	}
	m.offMu.Lock()
	defer m.offMu.Unlock()
	loc := location(cfg)
	if m.offHome != nil && m.offLoc == loc {
		return m.offHome, nil
	}
	b, err := m.openHome(ctx, cfg)
	if err != nil {
		return nil, err
	}
	m.offHome, m.offLoc = b, loc
	return b, nil
}
