package publish

import (
	"context"
	"net/netip"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const (
	cleanupEvery = time.Minute
	cleanupBatch = 100 // the LIMIT of each cleanup query
)

// Cleanup deletes expired nonces, enrollments and seen CI tokens, the sessions that ended more
// than 30 days ago, and the build records older than 30 days that no file references. The
// publish.cleanup job runs it every minute; a failed delete is logged and the next table goes on.
func (s *Service) Cleanup(ctx context.Context) error {
	now := time.Now()
	s.enrollLimits.sweep(now)
	jobs := []struct {
		name string
		del  func(context.Context, int64) (int64, error)
		arg  int64
	}{
		{"nonces", s.wq.DeleteExpiredNonces, now.Unix()},
		{"enrollments", s.wq.DeleteExpiredEnrollments, now.Unix()},
		{"seen CI tokens", s.wq.DeleteExpiredSeenOIDCTokens, now.Unix()},
		{"publish sessions", s.wq.DeleteOldPublishSessions, now.Add(-logRetention).Unix()},
	}
	for _, j := range jobs {
		for {
			n, err := j.del(ctx, j.arg)
			if err != nil {
				if ctx.Err() == nil {
					s.log.ErrorContext(ctx, "publish cleanup", "table", j.name, "error", err.Error())
				}
				break
			}
			if n < cleanupBatch {
				break
			}
		}
	}
	// The build records take one batch per run: builds that files still reference stay, so a walk
	// over all old builds spreads over many runs.
	if err := s.builds.Cleanup(ctx, now.Add(-logRetention)); err != nil && ctx.Err() == nil {
		s.log.ErrorContext(ctx, "publish cleanup", "table", "build records", "error", err.Error())
	}
	return nil
}

// ipLimiter gives each client address a budget of unauthenticated enrollments: a burst of 10,
// then one every 30 s. Entries idle for 15 min are swept.
type ipLimiter struct {
	mu sync.Mutex
	m  map[netip.Addr]*ipEntry
}

type ipEntry struct {
	lim  *rate.Limiter
	seen time.Time
}

const (
	enrollBurst   = 10
	enrollEvery   = 30 * time.Second
	limiterIdle   = 15 * time.Minute
	limiterMaxIPs = 10000
)

func newIPLimiter() *ipLimiter { return &ipLimiter{m: map[netip.Addr]*ipEntry{}} }

func (l *ipLimiter) allow(ip netip.Addr) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.m[ip]
	if !ok {
		if len(l.m) >= limiterMaxIPs {
			l.sweepLocked(now)
			if len(l.m) >= limiterMaxIPs {
				return false
			}
		}
		e = &ipEntry{lim: rate.NewLimiter(rate.Every(enrollEvery), enrollBurst)}
		l.m[ip] = e
	}
	e.seen = now
	return e.lim.AllowN(now, 1)
}

func (l *ipLimiter) sweep(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepLocked(now)
}

func (l *ipLimiter) sweepLocked(now time.Time) {
	for ip, e := range l.m {
		if now.Sub(e.seen) > limiterIdle {
			delete(l.m, ip)
		}
	}
}
