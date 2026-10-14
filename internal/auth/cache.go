package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"sync"
	"time"
)

const (
	cacheTTL = 10 * time.Minute
	cacheMax = 10000
)

type cacheEntry struct {
	userID  int64
	hash    string // password hash the entry was verified against
	gen     uint64
	expires time.Time
}

// verifyCache remembers successful Basic password checks. Entries are invalidated per user by
// bumping that user's generation.
type verifyCache struct {
	mu  sync.Mutex
	m   map[[32]byte]cacheEntry
	gen map[int64]uint64
}

func newVerifyCache() *verifyCache {
	return &verifyCache{m: map[[32]byte]cacheEntry{}, gen: map[int64]uint64{}}
}

func (s *Service) cacheKey(username, password string) [32]byte {
	h := hmac.New(sha256.New, s.secret)
	h.Write([]byte(username))
	h.Write([]byte{0})
	h.Write([]byte(password))
	var k [32]byte
	copy(k[:], h.Sum(nil))
	return k
}

func (c *verifyCache) get(k [32]byte) (int64, string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[k]
	if !ok {
		return 0, "", false
	}
	if time.Now().After(e.expires) || e.gen != c.gen[e.userID] {
		delete(c.m, k)
		return 0, "", false
	}
	return e.userID, e.hash, true
}

// generation returns the user's current generation; capture it before verifying so a
// concurrent invalidation makes the later put a no-op.
func (c *verifyCache) generation(userID int64) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gen[userID]
}

func (c *verifyCache) put(k [32]byte, userID int64, hash string, gen uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen != c.gen[userID] {
		return
	}
	if len(c.m) >= cacheMax {
		now := time.Now()
		var oldestK [32]byte
		var oldest time.Time
		scanned := 0
		for key, e := range c.m {
			if now.After(e.expires) || e.gen != c.gen[e.userID] {
				delete(c.m, key)
			} else if oldest.IsZero() || e.expires.Before(oldest) {
				oldestK, oldest = key, e.expires
			}
			if scanned++; scanned >= 256 && len(c.m) < cacheMax {
				break
			}
		}
		if len(c.m) >= cacheMax {
			delete(c.m, oldestK)
		}
	}
	c.m[k] = cacheEntry{userID: userID, hash: hash, gen: gen, expires: time.Now().Add(cacheTTL)}
}

func (c *verifyCache) invalidate(userID int64) {
	c.mu.Lock()
	c.gen[userID]++
	c.mu.Unlock()
}
