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

// Document is a rendered body and its strong ETag (quoted).
type Document struct {
	Body []byte
	ETag string
}

// NewDocument is body with the SHA-256 of body as its ETag: for a document that no cache keeps.
func NewDocument(body []byte) Document {
	sum := sha256.Sum256(body)
	return Document{Body: body, ETag: `"` + hex.EncodeToString(sum[:]) + `"`}
}

// keyETag is the ETag of the document kept under key: the SHA-256 of the key. A key names
// everything its body depends on, so one key always renders one body.
func keyETag(key string) string {
	sum := sha256.Sum256([]byte(key))
	return `"` + hex.EncodeToString(sum[:]) + `"`
}

// WriteDocument answers a GET or HEAD with document d: Content-Type ct, the ETag of d, Vary with
// each of vary, and nosniff and a sandbox CSP as every repo file has. A matching If-None-Match
// answers 304.
func WriteDocument(w http.ResponseWriter, r *http.Request, ct string, d Document, vary ...string) {
	writeHeaders(w, ct, d.ETag, vary)
	if notModified(r, d.ETag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(d.Body)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		w.Write(d.Body)
	}
}

func writeHeaders(w http.ResponseWriter, ct, etag string, vary []string) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	for _, v := range vary {
		h.Add("Vary", v)
	}
	h.Set("Content-Type", ct)
	h.Set("ETag", etag)
}

// notModified reports whether the If-None-Match header of r names etag (weak comparison, or
// "*").
func notModified(r *http.Request, etag string) bool {
	header := r.Header.Get("If-None-Match")
	if header == "" {
		return false
	}
	for _, t := range strings.Split(header, ",") {
		t = strings.TrimPrefix(strings.TrimSpace(t), "W/")
		if t == "*" || t == etag {
			return true
		}
	}
	return false
}

// DocumentKey is a key of a DocumentCache: the parts joined with NUL. A caller names what the
// body depends on, e.g. the kind of document, the revision of its rows or the SHA-256s of its
// sources, and the base URL, so a changed source or base URL renders anew.
func DocumentKey(parts ...string) string { return strings.Join(parts, "\x00") }

// DocumentCache keeps rendered documents in memory, at most a number of entries and of bytes in
// all (least recently used out first), so a document is rendered once per change of what it
// depends on and not once per request. A body above the entry limit is not kept. It is safe for
// concurrent use.
type DocumentCache struct {
	mu       sync.Mutex
	entries  int
	maxBytes int64
	maxEntry int64
	bytes    int64
	ll       *list.List
	m        map[string]*list.Element
}

type documentEntry struct {
	key string
	doc Document
}

// NewDocumentCache returns a cache of at most entries documents and maxBytes bytes in all, each
// at most maxEntry bytes.
func NewDocumentCache(entries int, maxBytes, maxEntry int64) *DocumentCache {
	return &DocumentCache{entries: entries, maxBytes: maxBytes, maxEntry: min(maxEntry, maxBytes), ll: list.New(), m: map[string]*list.Element{}}
}

// get returns the document kept under key. The caller does not change its body.
func (c *DocumentCache) get(key string) (Document, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.m[key]
	if !ok {
		return Document{}, false
	}
	c.ll.MoveToFront(el)
	return el.Value.(*documentEntry).doc, true
}

// put keeps d under key. The caller does not change its body afterwards.
func (c *DocumentCache) put(key string, d Document) {
	n := int64(len(d.Body))
	if n > c.maxEntry {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.m[key]; ok {
		e := el.Value.(*documentEntry)
		c.bytes += n - int64(len(e.doc.Body))
		e.doc = d
		c.ll.MoveToFront(el)
	} else {
		c.m[key] = c.ll.PushFront(&documentEntry{key, d})
		c.bytes += n
	}
	for c.ll.Len() > c.entries || c.bytes > c.maxBytes {
		old := c.ll.Back()
		e := old.Value.(*documentEntry)
		c.ll.Remove(old)
		delete(c.m, e.key)
		c.bytes -= int64(len(e.doc.Body))
	}
}

// Render returns the document kept under key, else the body that build returns, which it then
// keeps. The ETag of the document is that of key. Two requests that miss at once both build: the
// result is the same.
func (c *DocumentCache) Render(key string, build func() ([]byte, error)) (Document, error) {
	if d, ok := c.get(key); ok {
		return d, nil
	}
	b, err := build()
	if err != nil {
		return Document{}, err
	}
	d := Document{Body: b, ETag: keyETag(key)}
	c.put(key, d)
	return d, nil
}

// Serve answers a GET or HEAD with the document under key (Render) in content type ct, as
// WriteDocument does. An If-None-Match that names the ETag of key answers 304 without a lookup
// or a build. It returns the error of build and then writes nothing.
func (c *DocumentCache) Serve(w http.ResponseWriter, r *http.Request, ct, key string, build func() ([]byte, error), vary ...string) error {
	if etag := keyETag(key); notModified(r, etag) {
		writeHeaders(w, ct, etag, vary)
		w.WriteHeader(http.StatusNotModified)
		return nil
	}
	d, err := c.Render(key, build)
	if err != nil {
		return err
	}
	WriteDocument(w, r, ct, d, vary...)
	return nil
}
