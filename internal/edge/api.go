package edge

import (
	"net/http"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
	"github.com/Telesphorepo/Telesphorepo/internal/storage"
)

// statusJSON is GET /api/edge/status.
type statusJSON struct {
	// Enabled: edge delivery is on now (the setting, and settings that can run it). Mode is "on",
	// "draining" (stopped; the Workers' cached config.json still says on) or "off".
	Enabled bool   `json:"enabled"`
	Mode    string `json:"mode"`
	// Unfit says why the settings cannot run edge delivery ("" when they can).
	Unfit         string     `json:"unfit"`
	ServerVersion string     `json:"server_version"`
	WorkerVersion *string    `json:"worker_version"` // from gap/state.json; null before the first gap check
	Mismatch      bool       `json:"version_mismatch"`
	GapCheckedAt  *time.Time `json:"gap_checked_at"`
	// DeployNeeded: a storage change or move, a region change, or a Worker of another version
	// asks for a new deploy. DeployReason names the storage change ("" otherwise).
	DeployNeeded bool         `json:"deploy_needed"`
	DeployReason string       `json:"deploy_reason"`
	Home         homeJSON     `json:"home"`
	Backfill     backfillJSON `json:"backfill"`
	Regions      []regionJSON `json:"regions"`
}

type homeJSON struct {
	Hint      string `json:"hint"`
	Bucket    string `json:"bucket"`
	Outbox    int64  `json:"outbox"` // rows of the outbox: pointers and access objects
	LastError string `json:"last_error"`
}

// backfillJSON is the backfill of every pointer: Running until the outbox wrote every pointer it
// queued (the edge serves nothing before).
type backfillJSON struct {
	Running bool  `json:"running"`
	Queued  int64 `json:"queued"`
	Total   int64 `json:"total"`
}

type regionJSON struct {
	Hint   string `json:"hint"`
	Area   string `json:"area"`
	Bucket string `json:"bucket"`
	// State is "filling", "ready", or "waiting": the Worker of the last gap check has no binding
	// for the bucket, so it needs a deploy.
	State           string `json:"state"`
	PointersWritten int64  `json:"pointers_written"`
	PointersTotal   int64  `json:"pointers_total"`
	BlobsMissing    *int64 `json:"blobs_missing"`
	Outbox          int64  `json:"outbox"` // pointer rows of the outbox, which may write this bucket
	LastError       string `json:"last_error"`
}

// status answers the state of edge delivery for the Edge delivery page.
func (m *mod) status(w http.ResponseWriter, r *http.Request, c *route.Call) {
	snap := m.h.Settings.Current()
	v := m.cur.Load()
	p := m.published()
	counts, _ := m.counts.Load().(outboxCounts)
	out := statusJSON{
		Enabled: v.mode == modeOn, Mode: [...]string{"off", "on", "draining"}[v.mode], Unfit: unfit(snap),
		ServerVersion: m.h.Version, DeployNeeded: p.home.DeployNeeded, DeployReason: p.home.Reason,
		Home:     homeJSON{Hint: HomeHint.Get(snap), Bucket: storage.S3Bucket.Get(snap), LastError: m.lastErr("home")},
		Backfill: backfillJSON{Running: v.mode == modeOn && p.home.Started && !p.home.Ready, Queued: p.home.Queued, Total: p.home.Total},
		Regions:  []regionJSON{},
	}
	if out.Unfit != "" && !Enabled.Get(snap) {
		out.Unfit = ""
	}
	if v.mode == modeOn {
		out.Home.Outbox = counts.Total
		if out.Home.LastError == "" {
			out.Home.LastError = counts.LastError
		}
	}
	g, _ := m.gap.Load().(*gapState)
	if g != nil && g.Version != "" {
		ver := g.Version
		out.WorkerVersion = &ver
		out.Mismatch = ver != m.h.Version
	}
	if g != nil && !g.CheckedAt.IsZero() {
		t := g.CheckedAt
		out.GapCheckedAt = &t
	}
	for _, rs := range p.regions {
		rj := regionJSON{Hint: rs.Hint, Area: hintAreas[rs.Hint], Bucket: rs.Bucket, State: "filling",
			PointersWritten: rs.Written, PointersTotal: rs.Total, BlobsMissing: rs.Missing, LastError: m.lastErr(rs.Hint)}
		if rs.ViaHome {
			rj.PointersWritten, rj.PointersTotal = p.home.Queued, p.home.Total
		}
		if rs.Ready {
			rj.State = "ready"
		}
		if g != nil && g.Version != "" && (!g.Regions[rs.Hint] || (g.Markers[rs.Hint] != "" && g.Markers[rs.Hint] != rs.Bucket)) {
			// No binding, or the binding is another bucket (its bucket.json; none yet is a bucket.json not
			// written yet, not a mismatch): the Worker does not read the region until a deploy.
			rj.State = "waiting"
			out.DeployNeeded = true
		}
		if v.mode == modeOn {
			rj.Outbox = counts.Pointers
		}
		out.Regions = append(out.Regions, rj)
	}
	if g != nil && g.Version != "" {
		if g.HomeBucket != nil && *g.HomeBucket != "" && v.home != nil && *g.HomeBucket != v.home.name {
			out.DeployNeeded = true // the Worker's HOME binding is another bucket: it serves nothing
		}
		for h := range g.Regions {
			if !containsHint(p.regions, h) {
				out.DeployNeeded = true // the Worker still reads a removed region
			}
		}
	}
	out.DeployNeeded = out.DeployNeeded || out.Mismatch
	httpx.WriteJSON(w, http.StatusOK, out)
}

func containsHint(rs []regionState, h string) bool {
	for _, r := range rs {
		if r.Hint == h {
			return true
		}
	}
	return false
}

// wranglerConfig answers the wrangler.jsonc of the Worker for the current settings; 409
// edge-not-configured when they cannot run edge delivery.
func (m *mod) wranglerConfig(w http.ResponseWriter, r *http.Request, c *route.Call) {
	body, err := m.wrangler(m.h.Settings.Current())
	if err != nil {
		ProblemNotConfigured.Write(w, r, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	w.Write(body)
}
