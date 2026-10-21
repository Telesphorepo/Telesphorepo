package assets

import (
	"container/list"
	"sync"
)

// mergeCache keeps the merged files of groups in memory, at most a number of entries and of
// bytes in all (least recently used out first). A key names the group, the base path and the
// SHA-256 of each member file, so a changed member file merges anew. A file above an eighth of
// the bytes is not kept. It is safe for concurrent use.
type mergeCache struct {
	mu       sync.Mutex
	entries  int
	maxBytes int64
	bytes    int64
	ll       *list.List
	m        map[string]*list.Element
}

type mergeEntry struct {
	key  string
	data []byte
}

func newMergeCache(entries int, maxBytes int64) *mergeCache {
	return &mergeCache{entries: entries, maxBytes: maxBytes, ll: list.New(), m: map[string]*list.Element{}}
}

// get returns the bytes kept under key. The caller does not change them.
func (c *mergeCache) get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.m[key]
	if !ok {
		return nil, false
	}
	c.ll.MoveToFront(el)
	return el.Value.(*mergeEntry).data, true
}

// put keeps data under key. The caller does not change data afterwards.
func (c *mergeCache) put(key string, data []byte) {
	n := int64(len(data))
	if n > c.maxBytes/8 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.m[key]; ok {
		e := el.Value.(*mergeEntry)
		c.bytes += n - int64(len(e.data))
		e.data = data
		c.ll.MoveToFront(el)
	} else {
		c.m[key] = c.ll.PushFront(&mergeEntry{key, data})
		c.bytes += n
	}
	for c.ll.Len() > c.entries || c.bytes > c.maxBytes {
		old := c.ll.Back()
		e := old.Value.(*mergeEntry)
		c.ll.Remove(old)
		delete(c.m, e.key)
		c.bytes -= int64(len(e.data))
	}
}
