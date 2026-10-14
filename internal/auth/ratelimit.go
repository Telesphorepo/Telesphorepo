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
	lim  *rate.Limiter
	seen time.Time
}

// limiterSet tracks failed authentication attempts per key ("ip:..." / "u:...").
type limiterSet struct {
	mu        sync.Mutex
	m         map[string]*limiterEntry
	lastSweep time.Time
}

func newLimiterSet() *limiterSet { return &limiterSet{m: map[string]*limiterEntry{}} }

// blocked reports how long the caller must wait if any key is out of tokens (0 = allowed).
// It does not consume tokens.
func (l *limiterSet) blocked(keys ...string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	var wait time.Duration
	for _, k := range keys {
		e, ok := l.m[k]
		if !ok {
			continue
		}
		if t := e.lim.Tokens(); t < 1 {
			d := time.Duration((1 - t) * float64(limitEvery))
			wait = max(wait, d)
		}
	}
	return wait
}

// fail records a failed attempt against every key.
func (l *limiterSet) fail(keys ...string) {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.lastSweep) > sweepEvery || len(l.m) >= limitMax {
		l.lastSweep = now
		l.sweep(now)
	}
	for _, k := range keys {
		e, ok := l.m[k]
		if !ok {
			e = &limiterEntry{lim: rate.NewLimiter(rate.Every(limitEvery), limitBurst)}
			l.m[k] = e
		}
		e.seen = now
		e.lim.AllowN(now, 1)
	}
}

// sweep drops idle entries with bounded work; if still over capacity it drops arbitrary ones.
func (l *limiterSet) sweep(now time.Time) {
	n := 0
	for k, e := range l.m {
		if now.Sub(e.seen) > limitIdle || len(l.m) >= limitMax {
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
