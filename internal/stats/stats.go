// Package stats counts the downloads of each repo per day, and keeps the last use of each repo
// redirect. The read path adds to counters in memory (Add, UseRedirect); Flush writes them, one
// upsert per repo and day and one update per used redirect in one transaction, so a download
// costs no write of its own. The audit module flushes every minute and at shutdown, deletes the
// days past its retention setting and answers GET /api/stats.
package stats

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
)

// Day is the length of a day of the counters, which start at 00:00 UTC.
const Day = 24 * time.Hour

type key struct {
	repo, day int64
}

type count struct {
	downloads, bytes int64
}

// Counter holds the downloads that are not written yet.
type Counter struct {
	db     *db.DB
	rq, wq *dbq.Queries

	mu      sync.Mutex
	pending map[key]count
	used    map[string]int64 // redirect name -> unix time of its last use, not written yet

	flushMu sync.Mutex // one flush at a time
}

// New returns an empty counter.
func New(d *db.DB) *Counter {
	return &Counter{db: d, rq: dbq.New(d.R), wq: dbq.New(d.W), pending: map[key]count{}, used: map[string]int64{}}
}

// DayOf returns the day of t: the Unix time of 00:00 UTC of it.
func DayOf(t time.Time) int64 { return t.UTC().Truncate(Day).Unix() }

// Add counts one download of bytes from repo now.
func (c *Counter) Add(repo, bytes int64) {
	k := key{repo, DayOf(time.Now())}
	c.mu.Lock()
	n := c.pending[k]
	n.downloads++
	n.bytes += bytes
	c.pending[k] = n
	c.mu.Unlock()
}

// UseRedirect notes a request through the redirect name now.
func (c *Counter) UseRedirect(name string) {
	now := time.Now().Unix()
	c.mu.Lock()
	c.used[name] = now
	c.mu.Unlock()
}

// RedirectUsed returns the use of the redirect name that is not written yet; zero for none.
func (c *Counter) RedirectUsed(name string) time.Time {
	c.mu.Lock()
	at := c.used[name]
	c.mu.Unlock()
	if at == 0 {
		return time.Time{}
	}
	return time.Unix(at, 0)
}

// Flush writes the pending counters and redirect uses in one transaction: one upsert per repo
// and day, one update per redirect. On a failed write they go back to the pending ones, so the
// next flush writes them.
func (c *Counter) Flush(ctx context.Context) error {
	c.flushMu.Lock()
	defer c.flushMu.Unlock()
	c.mu.Lock()
	batch, used := c.pending, c.used
	if len(batch) == 0 && len(used) == 0 {
		c.mu.Unlock()
		return c.roll(ctx)
	}
	c.pending, c.used = map[key]count{}, map[string]int64{}
	c.mu.Unlock()
	err := c.db.Tx(ctx, func(tx *sql.Tx) error {
		q := c.wq.WithTx(tx)
		if err := rollWindow(ctx, q); err != nil {
			return err
		}
		for k, n := range batch {
			if err := q.AddDownloads(ctx, dbq.AddDownloadsParams{RepoID: k.repo, Day: k.day, Downloads: n.downloads, Bytes: n.bytes}); err != nil {
				return err
			}
		}
		for name, at := range used {
			if err := q.TouchRepoRedirect(ctx, dbq.TouchRepoRedirectParams{At: at, Name: name}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		c.mu.Lock()
		for k, n := range batch {
			p := c.pending[k]
			p.downloads += n.downloads
			p.bytes += n.bytes
			c.pending[k] = p
		}
		for name, at := range used {
			c.used[name] = max(c.used[name], at)
		}
		c.mu.Unlock()
	}
	return err
}

// RecentDays is the length of the download window: the last 30 UTC days, today included. The
// window is kept in download_window_days and download_window_repos (see the migration): the
// triggers of download_stats add every written row to them in the transaction of the write.
const RecentDays = 30

// windowFirst is the first day of the window now.
func windowFirst() int64 { return DayOf(time.Now()) - (RecentDays-1)*int64(Day/time.Second) }

// roll moves the window to today when a day has left it, in one transaction.
func (c *Counter) roll(ctx context.Context) error {
	first, err := c.rq.GetWindowFirst(ctx)
	if err != nil {
		return err
	}
	if first >= windowFirst() {
		return nil
	}
	return c.db.Tx(ctx, func(tx *sql.Tx) error { return rollWindow(ctx, c.wq.WithTx(tx)) })
}

// rollWindow subtracts each day that has left the window from the totals, reading only the rows of
// that day, and moves the first day of the window to today's.
func rollWindow(ctx context.Context, q *dbq.Queries) error {
	first, err := q.GetWindowFirst(ctx)
	if err != nil {
		return err
	}
	want := windowFirst()
	if first >= want {
		return nil
	}
	for day := first; day < want; day += int64(Day / time.Second) {
		rows, err := q.ListDownloadsOfDay(ctx, day)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if err := q.SubWindowRepo(ctx, dbq.SubWindowRepoParams{Downloads: r.Downloads, Bytes: r.Bytes, RepoID: r.RepoID}); err != nil {
				return err
			}
			if err := q.DeleteEmptyWindowRepo(ctx, r.RepoID); err != nil {
				return err
			}
		}
	}
	if err := q.DeleteWindowDaysBefore(ctx, want); err != nil {
		return err
	}
	return q.SetWindowFirst(ctx, want)
}

// DayRow is the downloads of every repo on one day.
type DayRow struct {
	Day       int64
	Downloads int64
	Bytes     int64
}

// TopRow is the downloads of one repo over the window.
type TopRow struct {
	Name      string
	Format    string
	Downloads int64
	Bytes     int64
}

// Days returns the written downloads of each day of the window that has any, oldest first: a seek
// of at most RecentDays rows.
func (c *Counter) Days(ctx context.Context) ([]DayRow, error) {
	rows, err := c.rq.ListWindowDays(ctx, windowFirst())
	if err != nil {
		return nil, err
	}
	out := make([]DayRow, len(rows))
	for i, r := range rows {
		out[i] = DayRow{Day: r.Day, Downloads: r.Downloads, Bytes: r.Bytes}
	}
	return out, nil
}

// Top returns the n repos with the most written downloads in the window, most first (then the most
// bytes, then the oldest repo): the first rows of an index.
func (c *Counter) Top(ctx context.Context, n int) ([]TopRow, error) {
	rows, err := c.rq.TopWindowRepos(ctx, int64(n))
	if err != nil {
		return nil, err
	}
	out := make([]TopRow, len(rows))
	for i, r := range rows {
		out[i] = TopRow{Name: r.Name, Format: r.Format, Downloads: r.Downloads, Bytes: r.Bytes}
	}
	return out, nil
}

// Recent returns the written downloads of the repos ids over the window, one seek per repo; a repo
// without downloads has no entry.
func (c *Counter) Recent(ctx context.Context, ids []int64) (map[int64]int64, error) {
	out := make(map[int64]int64, len(ids))
	for _, id := range ids {
		n, err := c.rq.GetWindowRepoDownloads(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if n > 0 {
			out[id] = n
		}
	}
	return out, nil
}

// Cleanup deletes the days that started more than retention ago.
func (c *Counter) Cleanup(ctx context.Context, retention time.Duration) error {
	_, err := c.wq.DeleteOldDownloads(ctx, DayOf(time.Now().Add(-retention)))
	return err
}
