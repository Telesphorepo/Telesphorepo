// Package stats counts the downloads of each repo per day, and keeps the last use of each repo
// redirect. The read path adds to counters in memory (Add, UseRedirect); Flush writes them, one
// upsert per repo and day and one update per used redirect in one transaction, so a download
// costs no write of its own. The audit module flushes every minute and at shutdown, deletes the
// days past its retention setting and answers GET /api/stats.
package stats

import (
	"context"
	"database/sql"
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
		return nil
	}
	c.pending, c.used = map[key]count{}, map[string]int64{}
	c.mu.Unlock()
	err := c.db.Tx(ctx, func(tx *sql.Tx) error {
		q := c.wq.WithTx(tx)
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

// DayRow is the downloads of every repo on one day.
type DayRow struct {
	Day       int64
	Downloads int64
	Bytes     int64
}

// TopRow is the downloads of one repo over a window.
type TopRow struct {
	Name      string
	Format    string
	Downloads int64
	Bytes     int64
}

// Days returns the written downloads of each day from day on that has any, oldest first. SQL sums
// them from download_stats_window.
func (c *Counter) Days(ctx context.Context, day int64) ([]DayRow, error) {
	rows, err := c.rq.SumDownloadsByDay(ctx, day)
	if err != nil {
		return nil, err
	}
	out := make([]DayRow, len(rows))
	for i, r := range rows {
		out[i] = DayRow{Day: r.Day, Downloads: r.Downloads, Bytes: r.Bytes}
	}
	return out, nil
}

// Top returns the n repos with the most written downloads from day on, most first (then the most
// bytes, then the oldest repo).
func (c *Counter) Top(ctx context.Context, day int64, n int) ([]TopRow, error) {
	rows, err := c.rq.TopDownloadRepos(ctx, dbq.TopDownloadReposParams{Since: day, N: int64(n)})
	if err != nil {
		return nil, err
	}
	out := make([]TopRow, len(rows))
	for i, r := range rows {
		out[i] = TopRow{Name: r.Name, Format: r.Format, Downloads: r.Downloads, Bytes: r.Bytes}
	}
	return out, nil
}

// RecentDays is the window of Recent: the last 30 days, today included, the window of the
// Downloads card of the settings Overview.
const RecentDays = 30

// Recent returns the written downloads of each repo over the last RecentDays days; a repo without
// downloads has no entry. It reads the rows of ListDownloadsSince, at most one per repo and day.
func (c *Counter) Recent(ctx context.Context) (map[int64]int64, error) {
	first := DayOf(time.Now()) - (RecentDays-1)*int64(Day/time.Second)
	rows, err := c.rq.ListDownloadsSince(ctx, first)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]int64)
	for _, r := range rows {
		out[r.RepoID] += r.Downloads
	}
	return out, nil
}

// Cleanup deletes the days that started more than retention ago.
func (c *Counter) Cleanup(ctx context.Context, retention time.Duration) error {
	_, err := c.wq.DeleteOldDownloads(ctx, DayOf(time.Now().Add(-retention)))
	return err
}
