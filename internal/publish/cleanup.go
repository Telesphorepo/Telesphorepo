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

// Start runs the cleanup job until ctx ends: every minute it deletes expired challenges,
// enrollments and seen CI tokens, and sessions that ended more than 30 days ago. Wait returns
// once it has stopped.
func (s *Service) Start(ctx context.Context) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		t := time.NewTicker(cleanupEvery)
		defer t.Stop()
		for {
			s.cleanup(ctx)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

// Wait blocks until the cleanup job started by Start has stopped.
func (s *Service) Wait() { s.wg.Wait() }

func (s *Service) cleanup(ctx context.Context) {
	now := time.Now()
	s.enrollLimits.sweep(now)
	jobs := []struct {
		name string
		del  func(context.Context, int64) (int64, error)
		arg  int64
	}{
		{"challenges", s.wq.DeleteExpiredChallenges, now.Unix()},
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
