package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/jobs"
	"github.com/Telesphorepo/Telesphorepo/internal/module"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// The counters of group repos (SPEC.md "Group counters"). A group counts each file that it
// serves once: a file with no unit from the first member that has its path, a file of a unit
// (a package of npm, PyPI, Cargo, NuGet) from the first member that has a file of the unit, as a
// group read takes it. The counters depend on the members that the reader reads, so each group
// keeps one row of group_counts per set of members that a reader asked for. The job
// core.group-counts counts a row in one transaction; the triggers of the assets table keep it in
// the transaction of every write after that. A request reads its row with one indexed lookup.

const (
	unitBatch  = 500             // asset rows whose unit one transaction of the backfill sets
	countsPoll = 5 * time.Minute // how often the job checks that every group has its full count
)

// groupCounts is the counting of the core.group-counts job.
type groupCounts struct {
	a     *API
	wake  chan struct{}
	ready atomic.Bool // every asset row has its unit

	mu   sync.Mutex
	want map[int64][][]int64 // group id -> the member sets that readers asked for
}

func newGroupCounts(a *API) *groupCounts {
	return &groupCounts{a: a, wake: make(chan struct{}, 1), want: map[int64][][]int64{}}
}

// register adds the job core.group-counts and wakes it on every change of repos.
func (g *groupCounts) register(x *module.Extender) {
	x.Job(jobs.Job{Name: "core.group-counts", Run: g.run})
	x.Init(func(_ context.Context, h *module.Host) error {
		h.Repos.AfterChange(func(context.Context, repos.Notice) { g.poke() })
		return nil
	})
}

func (g *groupCounts) poke() {
	select {
	case g.wake <- struct{}{}:
	default:
	}
}

// membersKey is the key of a set of members in group_counts.members.
func membersKey(ids []int64) string {
	var b strings.Builder
	for i, id := range ids {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatInt(id, 10))
	}
	return b.String()
}

// counters is a group's counters for one set of members; pending while the job has not counted
// them yet.
type counters struct {
	files, size int64
	last        time.Time
	pending     bool
}

// of returns the counters of group for the members that a reader reads (in read order). A set
// that has no row yet is pending: the job counts it next.
func (g *groupCounts) of(ctx context.Context, group *repos.Repo, members []*repos.Repo) (counters, error) {
	ids := make([]int64, len(members))
	for i, m := range members {
		ids[i] = m.ID
	}
	if len(ids) == 0 {
		return counters{}, nil
	}
	if g.ready.Load() {
		row, err := dbq.New(g.a.h.DB.R).GetGroupCount(ctx, dbq.GetGroupCountParams{GroupID: group.ID, Members: membersKey(ids)})
		switch {
		case err == nil:
			c := counters{files: row.FileCount, size: row.TotalSize}
			if row.LastPublishedAt > 0 {
				c.last = time.Unix(row.LastPublishedAt, 0)
			}
			return c, nil
		case !errors.Is(err, sql.ErrNoRows):
			return counters{}, err
		}
	}
	g.mu.Lock()
	sets := g.want[group.ID]
	known := false
	for _, s := range sets {
		if membersKey(s) == membersKey(ids) {
			known = true
			break
		}
	}
	if !known {
		g.want[group.ID] = append(sets, ids)
	}
	g.mu.Unlock()
	g.poke()
	return counters{pending: true}, nil
}

// run sets the unit of every asset row that has none, then counts, until ctx ends: the full set of
// members of every group, and every set that a reader asked for.
func (g *groupCounts) run(ctx context.Context) {
	log := g.a.h.Log
	for {
		if err := g.backfill(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Error("group counts: set the units of files", "err", err)
		} else {
			g.ready.Store(true)
			if err := g.countAll(ctx); err != nil && ctx.Err() == nil {
				log.Error("group counts: count", "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-g.wake:
		case <-time.After(countsPoll):
		}
	}
}

// backfill sets the unit of the asset rows that have none (written before units, or by the seed
// command), batch by batch, and deletes the counts of their repos, which counted them without
// one.
func (g *groupCounts) backfill(ctx context.Context) error {
	for {
		rows, err := dbq.New(g.a.h.DB.R).ListUnsetUnits(ctx, unitBatch)
		if err != nil || len(rows) == 0 {
			return err
		}
		touched := map[int64]bool{}
		err = g.a.h.DB.Tx(ctx, func(tx *sql.Tx) error {
			q := dbq.New(tx)
			for _, r := range rows {
				unit := g.a.assets.UnitOf(r.Format, r.Path)
				if err := q.SetAssetUnit(ctx, dbq.SetAssetUnitParams{Unit: sql.NullString{String: unit, Valid: true}, ID: r.ID}); err != nil {
					return err
				}
				touched[r.RepoID] = true
			}
			for id := range touched {
				if err := q.DeleteGroupCountsOfMember(ctx, id); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
}

// countAll counts the full set of members of every group and the sets that readers asked for,
// each that has no row yet.
func (g *groupCounts) countAll(ctx context.Context) error {
	all, err := g.a.repos.List(ctx)
	if err != nil {
		return err
	}
	for _, repo := range all {
		if repo.Kind != repos.KindGroup {
			continue
		}
		g.mu.Lock()
		sets := append([][]int64{g.a.repos.Members(repo.ID)}, g.want[repo.ID]...)
		delete(g.want, repo.ID)
		g.mu.Unlock()
		for _, ids := range sets {
			if len(ids) == 0 {
				continue
			}
			if err := g.count(ctx, repo.ID, ids); err != nil {
				return err
			}
		}
	}
	return nil
}

// count counts the row of group and the members ids, unless it exists, in one transaction: no
// asset write lands between the count and the triggers that keep it.
func (g *groupCounts) count(ctx context.Context, group int64, ids []int64) error {
	key := membersKey(ids)
	return g.a.h.DB.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		_, err := q.GetGroupCount(ctx, dbq.GetGroupCountParams{GroupID: group, Members: key})
		switch {
		case err == nil:
			return nil
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		id, err := q.InsertGroupCount(ctx, dbq.InsertGroupCountParams{GroupID: group, Members: key})
		if err != nil {
			return err
		}
		for i, m := range ids {
			if err := q.InsertGroupCountMember(ctx, dbq.InsertGroupCountMemberParams{CountID: id, Position: int64(i), MemberID: m}); err != nil {
				return err
			}
		}
		paths, err := q.CountGroupPaths(ctx, id)
		if err != nil {
			return err
		}
		units, err := q.CountGroupUnits(ctx, id)
		if err != nil {
			return err
		}
		return q.SetGroupCount(ctx, dbq.SetGroupCountParams{FileCount: paths.Files + units.Files, TotalSize: paths.Size + units.Size,
			LastPublishedAt: max(paths.Newest, units.Newest), ID: id})
	})
}

// groupCounters sets the counters of out, the JSON of group repo, for p who reads through r: those
// of the members that p reads as a group read (access.Engine.GroupMembers).
func (a *API) groupCounters(r *http.Request, p *auth.Principal, group *repos.Repo, out *repoJSON) error {
	members, err := a.access.GroupMembers(r.Context(), r, p, group)
	if err != nil {
		return err
	}
	c, err := a.counts.of(r.Context(), group, members)
	if err != nil {
		return err
	}
	out.FileCount, out.TotalSize, out.CountsPending = c.files, c.size, c.pending
	out.LastPublishedAt = timeOrNil(c.last)
	return nil
}
