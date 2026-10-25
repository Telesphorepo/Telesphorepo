package edge

import (
	"archive/zip"
	"bytes"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/edgedist"
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
	Unfit         string  `json:"unfit"`
	ServerVersion string  `json:"server_version"`
	WorkerVersion *string `json:"worker_version"` // the Worker's build, from gap/state.json; null before the first gap check
	// WorkerOutdated says why the deployed Worker sends every request to the server ("" when it
	// serves): it lacks a feature that config.json requires (features.go).
	WorkerOutdated string     `json:"worker_outdated"`
	GapCheckedAt   *time.Time `json:"gap_checked_at"`
	// GapCheckEvery is the interval of the Worker's gap check (its cron), BlobCheckEvery the
	// interval of the server's count of the files of a filling region; both in seconds.
	GapCheckEvery  int64 `json:"gap_check_every"`
	BlobCheckEvery int64 `json:"blob_check_every"`
	// GapPassStarted is the start of the Worker's copy pass that runs now (null between passes).
	GapPassStarted *time.Time `json:"gap_pass_started"`
	// DeployNeeded: a storage change or move, a region change, or an outdated Worker asks for a
	// new deploy. DeployReason names the storage change ("" otherwise).
	DeployNeeded bool   `json:"deploy_needed"`
	DeployReason string `json:"deploy_reason"`
	// WorkerMissing says why GET /api/edge/worker.zip has no Worker of this build ("" when it has).
	WorkerMissing string `json:"worker_missing"`
	// CheckUnfit says why POST /api/edge/check cannot run ("" when it can); LastCheck is what the
	// last check found (null before one).
	CheckUnfit string       `json:"check_unfit"`
	LastCheck  *checkResult `json:"last_check"`
	Home       homeJSON     `json:"home"`
	Backfill   backfillJSON `json:"backfill"`
	Regions    []regionJSON `json:"regions"`
}

type homeJSON struct {
	Hint      string `json:"hint"`
	Bucket    string `json:"bucket"`
	Outbox    int64  `json:"outbox"`   // rows of the outbox: pointers and access objects
	Pointers  int64  `json:"pointers"` // the pointer rows of the outbox
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
	PointersDone    bool   `json:"pointers_done"` // every pointer of the region is written: the blob check runs
	BlobsMissing    *int64 `json:"blobs_missing"`
	// BlobsMissingSince is when the count of the blob check last changed (null before a count);
	// CopiesQueued the copies and deletes that the Worker's last finished pass queued for the
	// region (null before one).
	BlobsMissingSince *time.Time `json:"blobs_missing_since"`
	// BlobsCheckedAt is the end of the last whole count (null before one).
	BlobsCheckedAt *time.Time `json:"blobs_checked_at"`
	CopiesQueued   *int64     `json:"copies_queued"`
	Outbox         int64      `json:"outbox"` // pointer rows of the outbox, which may write this bucket
	LastError      string     `json:"last_error"`
}

// status answers the state of edge delivery for the Edge delivery page.
func (m *mod) status(w http.ResponseWriter, r *http.Request, c *route.Call) {
	snap := m.h.Settings.Current()
	v := m.cur.Load()
	p := m.published()
	var counts outboxCounts
	if v.mode == modeOn {
		counts = m.askCounts(r.Context())
	}
	out := statusJSON{
		Enabled: v.mode == modeOn, Mode: [...]string{"off", "on", "draining"}[v.mode], Unfit: unfit(snap),
		ServerVersion: m.h.Version, GapCheckEvery: int64(gapEvery / time.Second), BlobCheckEvery: int64(checkEvery / time.Second),
		DeployNeeded: p.home.DeployNeeded, DeployReason: p.home.Reason, WorkerMissing: m.workerMissing(),
		Home:     homeJSON{Hint: HomeHint.Get(snap), Bucket: storage.S3Bucket.Get(snap), LastError: m.lastErr("home")},
		Backfill: backfillJSON{Running: v.mode == modeOn && p.home.Started && !p.home.Ready, Queued: p.home.Queued, Total: p.home.Total},
		Regions:  []regionJSON{},
	}
	_, out.CheckUnfit = checkBase(snap)
	out.LastCheck = m.lastResult.Load()
	if out.Unfit != "" && !Enabled.Get(snap) {
		out.Unfit = ""
	}
	if v.mode == modeOn {
		out.Home.Outbox, out.Home.Pointers = counts.Total, counts.Pointers
		if out.Home.LastError == "" {
			out.Home.LastError = counts.LastError
		}
	}
	g, _ := m.gap.Load().(*gapState)
	if g != nil && g.Version != "" {
		ver := g.Version
		out.WorkerVersion = &ver
		out.WorkerOutdated = m.outdated(ver, g.Features, g.Reported)
	}
	if g != nil && !g.CheckedAt.IsZero() {
		t := g.CheckedAt
		out.GapCheckedAt = &t
	}
	if g != nil && !g.PassStarted.IsZero() {
		t := g.PassStarted
		out.GapPassStarted = &t
	}
	for _, rs := range p.regions {
		rj := regionJSON{Hint: rs.Hint, Area: hintAreas[rs.Hint], Bucket: rs.Bucket, State: "filling",
			PointersWritten: rs.Written, PointersTotal: rs.Total, PointersDone: rs.pointersDone(&p.home), BlobsMissing: rs.Missing,
			LastError: m.lastErr(rs.Hint)}
		if rs.MissingSince != 0 {
			t := time.Unix(rs.MissingSince, 0).UTC()
			rj.BlobsMissingSince = &t
		}
		if rs.CheckedAt != 0 {
			t := time.Unix(rs.CheckedAt, 0).UTC()
			rj.BlobsCheckedAt = &t
		}
		if q, ok := g.copies(rs.Hint); ok {
			rj.CopiesQueued = &q
		}
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
	out.DeployNeeded = out.DeployNeeded || out.WorkerOutdated != ""
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

// wranglerConfig answers the wrangler.jsonc of the Worker for the current settings, for a
// checkout of edge/; 409 edge-not-configured when they cannot run edge delivery.
func (m *mod) wranglerConfig(w http.ResponseWriter, r *http.Request, c *route.Call) {
	body, err := m.wrangler(m.h.Settings.Current(), false)
	if err != nil {
		ProblemNotConfigured.Write(w, r, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	w.Write(body)
}

// workerZip answers the edge Worker folder of the server's build as a zip, with the wrangler.jsonc
// of the current settings in it; 409 edge-not-configured when they cannot run edge delivery, 404
// no-edge-worker when the build holds no Worker of the server's version.
func (m *mod) workerZip(w http.ResponseWriter, r *http.Request, c *route.Call) {
	cfg, err := m.wrangler(m.h.Settings.Current(), true)
	if err != nil {
		ProblemNotConfigured.Write(w, r, err.Error())
		return
	}
	src, version := edgedist.Zip()
	if why := m.workerMissing(); why != "" {
		ProblemNoWorker.Write(w, r, why)
		return
	}
	zr, err := zip.NewReader(bytes.NewReader(src), int64(len(src)))
	if err != nil {
		httpx.Error(w, r, http.StatusInternalServerError, "the edge Worker of this build does not open")
		return
	}
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for _, f := range zr.File {
		if err := zw.Copy(f); err != nil {
			httpx.Error(w, r, http.StatusInternalServerError, "the edge Worker of this build does not open")
			return
		}
	}
	fw, err := zw.CreateHeader(&zip.FileHeader{Name: edgedist.Dir + "wrangler.jsonc", Method: zip.Deflate, Modified: time.Now()})
	if err == nil {
		_, err = fw.Write(cfg)
	}
	if err == nil {
		err = zw.Close()
	}
	if err != nil {
		httpx.Error(w, r, http.StatusInternalServerError, "the edge Worker zip could not be written")
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="telesphorepo-edge_`+safeFileVersion(version)+`.zip"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(out.Len()))
	w.WriteHeader(http.StatusOK)
	w.Write(out.Bytes())
}

// workerMissing says why the build holds no edge Worker of the server's version, or "".
func (m *mod) workerMissing() string {
	src, version := edgedist.Zip()
	if src == nil {
		return "This server was built without the edge Worker. Build it with go tool task build (it needs bun), or install a release."
	}
	if version != m.h.Version {
		return fmt.Sprintf("This server %s holds the edge Worker of %s. Build both again with go tool task build.", m.h.Version, version)
	}
	return ""
}

// safeFileVersion is version with every character outside [A-Za-z0-9.+-] replaced by "_", for a
// file name.
func safeFileVersion(version string) string {
	return strings.Map(func(c rune) rune {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '+' || c == '-' {
			return c
		}
		return '_'
	}, version)
}
