package auth

import (
	"math"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"telesphorepo/internal/httpx"
)

const (
	limitBurst   = 10
	limitEvery   = 6 * time.Second
	limitIdle    = 15 * time.Minute
	limitMax     = 100000
	sweepEvery   = time.Minute
	sweepMaxScan = 1024
)

type limiterEntry struct {
	lim      *rate.Limiter
	seen     time.Time
	inflight int // password verifications started (via begin) but not yet finished
}

// limiterSet tracks failed authentication attempts per key ("ip:..." / "u:...").
type limiterSet struct {
	mu        sync.Mutex
	m         map[string]*limiterEntry
	lastSweep time.Time
}

func newLimiterSet() *limiterSet { return &limiterSet{m: map[string]*limiterEntry{}} }

// blocked reports how long the caller must wait if any key is out of tokens (0 = allowed).
// Verifications still in flight count as already failed. It does not consume tokens.
func (l *limiterSet) blocked(keys ...string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.blockedLocked(keys)
}

func (l *limiterSet) blockedLocked(keys []string) time.Duration {
	var wait time.Duration
	for _, k := range keys {
		e, ok := l.m[k]
		if !ok {
			continue
		}
		if t := e.lim.Tokens(); t < 1 {
			wait = max(wait, time.Duration((1-t)*float64(limitEvery)))
		} else if t-float64(e.inflight) < 1 {
			wait = max(wait, time.Second) // pending attempts will settle shortly
		}
	}
	return wait
}

// begin atomically checks the keys and, if allowed, reserves one in-flight attempt on each so
// concurrent requests cannot all pass the check before any failure is recorded. Every
// successful begin (wait == 0) must be paired with end.
func (l *limiterSet) begin(keys ...string) time.Duration {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if d := l.blockedLocked(keys); d > 0 {
		return d
	}
	for _, k := range keys {
		e := l.entryLocked(k, now)
		e.inflight++
	}
	return 0
}

// end releases a begin reservation, recording a failed attempt when failed.
func (l *limiterSet) end(failed bool, keys ...string) {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		e := l.entryLocked(k, now)
		if e.inflight > 0 {
			e.inflight--
		}
		if failed {
			e.lim.AllowN(now, 1)
		}
	}
}

// fail records a failed attempt against every key.
func (l *limiterSet) fail(keys ...string) {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		l.entryLocked(k, now).lim.AllowN(now, 1)
	}
}

// entryLocked returns the entry for k, creating it (and sweeping occasionally). Caller holds mu.
func (l *limiterSet) entryLocked(k string, now time.Time) *limiterEntry {
	e, ok := l.m[k]
	if !ok {
		if now.Sub(l.lastSweep) > sweepEvery || len(l.m) >= limitMax {
			l.lastSweep = now
			l.sweep(now)
		}
		e = &limiterEntry{lim: rate.NewLimiter(rate.Every(limitEvery), limitBurst)}
		l.m[k] = e
	}
	e.seen = now
	return e
}

// sweep drops idle entries with bounded work; if still over capacity it drops arbitrary ones.
func (l *limiterSet) sweep(now time.Time) {
	n := 0
	for k, e := range l.m {
		if e.inflight == 0 && (now.Sub(e.seen) > limitIdle || len(l.m) >= limitMax) {
			delete(l.m, k)
		}
		if n++; n >= sweepMaxScan && len(l.m) < limitMax {
			return
		}
	}
}

func limitKeys(ip netip.Addr, username string) []string {
	ipk := "ip:?"
	if ip.IsValid() {
		ipk = "ip:" + ip.Unmap().String()
	}
	if len(username) > 128 {
		username = username[:128]
	}
	return []string{ipk, "u:" + username}
}

func (s *Service) clientIP(r *http.Request) netip.Addr {
	return httpx.ClientIP(r, s.cfg.TrustedProxies)
}

func setRetryAfter(w http.ResponseWriter, d time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(d.Seconds())))))
}
