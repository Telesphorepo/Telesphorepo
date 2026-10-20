package format

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// A rendered document is a metadata document that a format builds per request from its rows, a
// cached remote document or the members of a group (an npm packument, a PyPI project page, a
// Cargo index file, a NuGet registration), instead of a stored asset. Its absolute URLs start at
// route.API.BaseURL (Host.API): the base_url setting, else the scheme and host the client used.

// WriteDocument answers a GET or HEAD with the rendered body: Content-Type ct, a strong ETag (the
// SHA-256 of body), Vary with each of vary, and nosniff and a sandbox CSP as every repo file has.
// A matching If-None-Match answers 304.
func WriteDocument(w http.ResponseWriter, r *http.Request, ct string, body []byte, vary ...string) {
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:]) + `"`
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	for _, v := range vary {
		h.Add("Vary", v)
	}
	h.Set("Content-Type", ct)
	h.Set("ETag", etag)
	if inm := r.Header.Get("If-None-Match"); inm != "" && etagMatches(inm, etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		w.Write(body)
	}
}

// etagMatches reports whether the If-None-Match header names etag (weak comparison, or "*").
func etagMatches(header, etag string) bool {
	for _, t := range strings.Split(header, ",") {
		t = strings.TrimPrefix(strings.TrimSpace(t), "W/")
		if t == "*" || t == etag {
			return true
		}
	}
	return false
}

// DocumentKey is a key of a DocumentCache: the parts joined with NUL. A caller names what the
// body depends on, e.g. the kind of document, the SHA-256s of its sources and the base URL, so a
// changed source or base URL renders anew.
func DocumentKey(parts ...string) string { return strings.Join(parts, "\x00") }

// DocumentCache keeps rendered bodies in memory, at most a number of entries and of bytes in all
// (least recently used out first), so a large remote document is parsed once per change and not
// once per request. A body above an eighth of the bytes is not kept. It is safe for concurrent
// use.
type DocumentCache struct {
	mu       sync.Mutex
	entries  int
	maxBytes int64
	bytes    int64
	ll       *list.List
	m        map[string]*list.Element
}

type documentEntry struct {
	key  string
	body []byte
}

// NewDocumentCache returns a cache of at most entries bodies and maxBytes bytes in all.
func NewDocumentCache(entries int, maxBytes int64) *DocumentCache {
	return &DocumentCache{entries: entries, maxBytes: maxBytes, ll: list.New(), m: map[string]*list.Element{}}
}

// Get returns the body kept under key. The caller does not change it.
func (c *DocumentCache) Get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.m[key]
	if !ok {
		return nil, false
	}
	c.ll.MoveToFront(el)
	return el.Value.(*documentEntry).body, true
}

// Put keeps body under key. The caller does not change body afterwards.
func (c *DocumentCache) Put(key string, body []byte) {
	n := int64(len(body))
	if n > c.maxBytes/8 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.m[key]; ok {
		e := el.Value.(*documentEntry)
		c.bytes += n - int64(len(e.body))
		e.body = body
		c.ll.MoveToFront(el)
	} else {
		c.m[key] = c.ll.PushFront(&documentEntry{key, body})
		c.bytes += n
	}
	for c.ll.Len() > c.entries || c.bytes > c.maxBytes {
		old := c.ll.Back()
		e := old.Value.(*documentEntry)
		c.ll.Remove(old)
		delete(c.m, e.key)
		c.bytes -= int64(len(e.body))
	}
}

// Render returns the body kept under key, else the body that build returns, which it then keeps.
// Two requests that miss at once both build: the result is the same.
func (c *DocumentCache) Render(key string, build func() ([]byte, error)) ([]byte, error) {
	if b, ok := c.Get(key); ok {
		return b, nil
	}
	b, err := build()
	if err != nil {
		return nil, err
	}
	c.Put(key, b)
	return b, nil
}
