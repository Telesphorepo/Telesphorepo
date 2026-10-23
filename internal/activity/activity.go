// Package activity is the audit module: the API of the audit log (GET /api/audit, its action list
// and its people), the download statistics (GET /api/stats), the permission that reads them, and
// the jobs that write the download counters every minute and delete events and download days
// after their retention settings. The kernel writes the log (internal/audit) and counts the
// downloads of the read path (internal/stats).
package activity

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/audit"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/jobs"
	"github.com/Telesphorepo/Telesphorepo/internal/module"
	"github.com/Telesphorepo/Telesphorepo/internal/perm"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
	"github.com/Telesphorepo/Telesphorepo/internal/settings"
	"github.com/Telesphorepo/Telesphorepo/internal/stats"
)

// PermRead reads the audit log and the download statistics.
const PermRead perm.ID = "activity.read"

// Retention is the setting audit.retention.
var Retention = settings.Duration(&settings.Def{Key: "audit.retention", Default: "365d", Label: "Audit log retention",
	Help: "The audit log keeps each event for this time. A job deletes older events every hour."})

// StatsRetention is the setting stats.retention.
var StatsRetention = settings.Duration(&settings.Def{Key: "stats.retention", Default: "400d", Label: "Download statistics retention",
	Help: "The download counts of each day are kept for this time. A job deletes older days every hour."})

// flushEvery is how often the download counters are written.
const flushEvery = time.Minute

// Stats windows: GET /api/stats?days= takes 1 to maxDays days, 30 by default.
const (
	defaultDays = 30
	maxDays     = 400
	topRepos    = 10
)

// Module returns the audit module.
func Module() module.Module {
	var h *module.Host
	return module.Module{
		ID: "audit", Title: "Activity",
		Register: func(x *module.Extender) {
			x.Permissions(perm.Subsystem{ID: "activity", Scope: perm.ScopeServer, Label: "Activity",
				Help: "The audit log: who did what, when, and from which address."},
				perm.Def{ID: PermRead, Label: "Read the audit log", Help: "See every event of the audit log, with the client addresses, and the download statistics.", SecondFactor: true})
			x.Settings(&settings.Group{ID: "activity", Label: "Activity", Perm: perm.SettingsGeneral,
				Defs: []*settings.Def{Retention.Def(), StatsRetention.Def()}})
			x.Init(func(_ context.Context, host *module.Host) error {
				h = host
				return nil
			})
			x.Job(jobs.Job{Name: "audit.retention", Every: time.Hour, Tick: func(ctx context.Context) error {
				snap := h.Settings.Current()
				return errors.Join(h.Audit.Cleanup(ctx, Retention.Get(snap)), h.Downloads.Cleanup(ctx, StatsRetention.Get(snap)))
			}})
			x.Job(jobs.Job{Name: "audit.downloads", Run: func(ctx context.Context) {
				t := time.NewTicker(flushEvery)
				defer t.Stop()
				for {
					select {
					case <-ctx.Done():
						// The last counts of a shutdown: the database closes after the jobs stop.
						if err := h.Downloads.Flush(context.WithoutCancel(ctx)); err != nil {
							h.Log.Error("write download counts", "err", err.Error())
						}
						return
					case <-t.C:
						if err := h.Downloads.Flush(ctx); err != nil {
							h.Log.Error("write download counts", "err", err.Error())
						}
					}
				}
			}})
			x.Routes(func(r *route.Router) {
				r.Handle("GET /api/audit", route.UIPerm(PermRead), func(w http.ResponseWriter, r *http.Request, _ *route.Call) { list(h, w, r) })
				r.Handle("GET /api/audit/actions", route.UIPerm(PermRead), func(w http.ResponseWriter, r *http.Request, _ *route.Call) {
					httpx.WriteJSON(w, http.StatusOK, h.Audit.Actions())
				})
				r.Handle("GET /api/audit/people", route.UIPerm(PermRead), func(w http.ResponseWriter, r *http.Request, _ *route.Call) { people(h, w, r) })
				r.Handle("GET /api/stats", route.UIPerm(PermRead), func(w http.ResponseWriter, r *http.Request, _ *route.Call) { downloads(h, w, r) })
			})
		},
	}
}

// list returns the audit log, newest first:
// ?after=<id>&limit=&action=&repo=<name>&actor=<username>&since=<RFC 3339>&until=<RFC 3339>.
// actor is a person (actor_user_id); since and until bound the time (at or after since, before
// until).
func list(h *module.Host, w http.ResponseWriter, r *http.Request) {
	after, ok := route.IDCursor(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := audit.Filter{Action: q.Get("action")}
	none := func() { httpx.WriteJSON(w, http.StatusOK, route.Page[audit.Entry]{Items: []audit.Entry{}}) }
	for _, b := range []struct {
		name string
		to   *time.Time
	}{{"since", &f.Since}, {"until", &f.Until}} {
		if v := q.Get(b.name); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				httpx.Error(w, r, http.StatusBadRequest, b.name+" must be a time in RFC 3339, e.g. 2026-10-22T00:00:00Z")
				return
			}
			*b.to = t
		}
	}
	if name := q.Get("repo"); name != "" {
		repo, err := h.Repos.ByName(r.Context(), name)
		if errors.Is(err, repos.ErrNotFound) {
			none()
			return
		}
		if err != nil {
			route.Fail(w, r, err)
			return
		}
		f.RepoID = repo.ID
	}
	if name := q.Get("actor"); name != "" {
		u, err := h.Auth.GetUser(r.Context(), name)
		if errors.Is(err, auth.ErrNotFound) {
			none()
			return
		}
		if err != nil {
			route.Fail(w, r, err)
			return
		}
		f.ActorUserID = u.ID
	}
	items, next, err := h.Audit.List(r.Context(), f, after, httpx.Limit(r))
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, route.Page[audit.Entry]{Items: items, Next: route.IDNext(next)})
}

// people returns the people of the Activity filter: a keyset page of at most 20 enabled usernames
// that start with ?q=, after ?after=.
func people(h *module.Host, w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	names, next, err := h.Auth.People(r.Context(), strings.ToLower(strings.TrimSpace(q.Get("q"))), q.Get("after"), min(httpx.Limit(r), 20))
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	items := make([]map[string]string, len(names))
	for i, n := range names {
		items[i] = map[string]string{"username": n}
	}
	httpx.WriteJSON(w, http.StatusOK, route.Page[map[string]string]{Items: items, Next: route.Cursor(next)})
}

// DayStats is the downloads of one day of every repo.
type DayStats struct {
	Day       string `json:"day"` // 2026-10-22, a day in UTC
	Downloads int64  `json:"downloads"`
	Bytes     int64  `json:"bytes"`
}

// RepoStats is the downloads of one repo over the window.
type RepoStats struct {
	Name      string `json:"name"`
	Format    string `json:"format"`
	Downloads int64  `json:"downloads"`
	Bytes     int64  `json:"bytes"`
}

// Stats is the answer of GET /api/stats.
type Stats struct {
	Days      []DayStats  `json:"days"`  // every day of the window, oldest first, today last
	Repos     []RepoStats `json:"repos"` // the repos with the most downloads, most first
	Downloads int64       `json:"downloads"`
	Bytes     int64       `json:"bytes"`
}

// downloads answers GET /api/stats?days=: the downloads of the last days (today and the days
// before it, in UTC) per day and the top repositories. It writes the pending counters first, so
// the answer holds every download served before the request.
func downloads(h *module.Host, w http.ResponseWriter, r *http.Request) {
	days := defaultDays
	if v := r.URL.Query().Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxDays {
			httpx.Error(w, r, http.StatusBadRequest, "days must be a number from 1 to "+strconv.Itoa(maxDays))
			return
		}
		days = n
	}
	ctx := r.Context()
	if err := h.Downloads.Flush(ctx); err != nil {
		route.Fail(w, r, err)
		return
	}
	today := stats.DayOf(time.Now())
	first := today - int64(days-1)*int64(stats.Day/time.Second)
	rows, err := h.Downloads.Since(ctx, first)
	if err != nil {
		route.Fail(w, r, err)
		return
	}
	out := Stats{Days: make([]DayStats, days), Repos: []RepoStats{}}
	for i := range out.Days {
		out.Days[i].Day = time.Unix(first, 0).UTC().AddDate(0, 0, i).Format(time.DateOnly)
	}
	per := map[int64]*RepoStats{}
	var order []int64
	for _, row := range rows {
		i := int((row.Day - first) / int64(stats.Day/time.Second))
		if i < 0 || i >= days {
			continue
		}
		out.Days[i].Downloads += row.Downloads
		out.Days[i].Bytes += row.Bytes
		out.Downloads += row.Downloads
		out.Bytes += row.Bytes
		p := per[row.RepoID]
		if p == nil {
			p = &RepoStats{}
			per[row.RepoID] = p
			order = append(order, row.RepoID)
		}
		p.Downloads += row.Downloads
		p.Bytes += row.Bytes
	}
	// The most downloads first (then the most bytes, then the oldest repo); only the top ones need
	// their names.
	slices.SortFunc(order, func(a, b int64) int {
		pa, pb := per[a], per[b]
		if c := cmp.Compare(pb.Downloads, pa.Downloads); c != 0 {
			return c
		}
		if c := cmp.Compare(pb.Bytes, pa.Bytes); c != 0 {
			return c
		}
		return cmp.Compare(a, b)
	})
	for _, id := range order {
		if len(out.Repos) == topRepos {
			break
		}
		repo, err := h.Repos.ByID(ctx, id)
		if errors.Is(err, repos.ErrNotFound) {
			continue // deleted since the read: its rows went with it
		}
		if err != nil {
			route.Fail(w, r, err)
			return
		}
		p := per[id]
		p.Name, p.Format = repo.Name, repo.Format
		out.Repos = append(out.Repos, *p)
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
