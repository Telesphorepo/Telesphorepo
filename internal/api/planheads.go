package api

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
)

// The first bytes that upload plans read stay on the data volume for a while: a plan answers
// heads, the ID of the held bytes, and a later plan of the same files (an edit of a field, an
// added file) names it and sends only the fields, the file list and the heads of new files.
const (
	headsTTL = 30 * time.Minute // a held set goes this long after its last use
	headsMax = 1 << 30          // the held bytes of every set together
)

// problemHeadsGone answers a plan that names held heads that the server no longer has, or that
// lack a file the plan reads: the client sends the first bytes again.
var problemHeadsGone = httpx.DeclareProblem(httpx.Problem{Name: "plan-heads-gone", Status: http.StatusConflict,
	Title: "Send the first bytes of the files again"})

// heldHead is the first bytes of one file of a held set: where they are in its file, and the size
// of the file they come from.
type heldHead struct{ off, n, size int64 }

// heldSet is the first bytes of the files of the plans of one upload, in one file.
type heldSet struct {
	mu                    sync.Mutex
	repoID, user, session int64
	file                  *os.File
	size                  int64
	heads                 map[string]heldHead
	used                  time.Time
	gone                  bool
}

// heldHeads keeps the held sets of the server in memory, their bytes under tmp/plans.
type heldHeads struct {
	dir   func() string
	clean sync.Once

	mu    sync.Mutex
	sets  map[string]*heldSet
	bytes int64
}

func newHeldHeads(dir func() string) *heldHeads {
	return &heldHeads{dir: dir, sets: map[string]*heldSet{}}
}

// get returns the held set id of the owner in repo, or nil.
func (hh *heldHeads) get(id string, repoID, user, session int64) *heldSet {
	if id == "" {
		return nil
	}
	hh.mu.Lock()
	defer hh.mu.Unlock()
	hh.expire(time.Now())
	s := hh.sets[id]
	if s == nil || s.repoID != repoID || s.user != user || s.session != session {
		return nil
	}
	s.used = time.Now()
	return s
}

// expire drops the sets unused for headsTTL. The caller holds hh.mu.
func (hh *heldHeads) expire(now time.Time) {
	for id, s := range hh.sets {
		if now.Sub(s.used) > headsTTL {
			hh.drop(id, s)
		}
	}
}

// drop removes set s. The caller holds hh.mu.
func (hh *heldHeads) drop(id string, s *heldSet) {
	delete(hh.sets, id)
	hh.bytes -= s.size
	s.mu.Lock()
	s.gone = true
	s.file.Close()
	os.Remove(s.file.Name())
	s.mu.Unlock()
}

// read returns the held first bytes of the file at path of size bytes; ok is false when the set
// holds none of that file at that size.
func (s *heldSet) read(path string, size int64) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.heads[path]
	if !ok || s.gone || h.size != size {
		return nil, false, nil
	}
	b := make([]byte, h.n)
	if _, err := s.file.ReadAt(b, h.off); err != nil && !errors.Is(err, io.EOF) {
		return nil, false, err
	}
	return b, true, nil
}

// hold adds the first bytes of the files of fresh (by path, with the size of each file in sizes)
// to the set id of the owner in repo, or to a new set when there is none. It returns the ID of
// the set, or "" when the bytes do not fit under headsMax.
func (hh *heldHeads) hold(id string, repoID, user, session int64, fresh map[string][]byte, sizes map[string]int64) (string, error) {
	var add int64
	for _, b := range fresh {
		add += int64(len(b))
	}
	hh.mu.Lock()
	defer hh.mu.Unlock()
	now := time.Now()
	hh.expire(now)
	s := hh.sets[id]
	if s == nil || s.repoID != repoID || s.user != user || s.session != session {
		if add == 0 {
			return "", nil
		}
		hh.clean.Do(func() { os.RemoveAll(hh.dir()) })
		if err := os.MkdirAll(hh.dir(), 0o750); err != nil {
			return "", err
		}
		var raw [16]byte
		rand.Read(raw[:])
		id = hex.EncodeToString(raw[:])
		f, err := os.OpenFile(filepath.Join(hh.dir(), id), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o640)
		if err != nil {
			return "", err
		}
		s = &heldSet{repoID: repoID, user: user, session: session, file: f, heads: map[string]heldHead{}}
		hh.sets[id] = s
	}
	s.used = now
	// The least recently used other sets make room.
	for hh.bytes+add > headsMax {
		var oldID string
		var old *heldSet
		for oid, o := range hh.sets {
			if o != s && (old == nil || o.used.Before(old.used)) {
				oldID, old = oid, o
			}
		}
		if old == nil {
			hh.drop(id, s)
			return "", nil
		}
		hh.drop(oldID, old)
	}
	s.mu.Lock()
	var err error
	for p, b := range fresh {
		if _, err = s.file.WriteAt(b, s.size); err != nil {
			break
		}
		s.heads[p] = heldHead{off: s.size, n: int64(len(b)), size: sizes[p]}
		s.size += int64(len(b))
		hh.bytes += int64(len(b))
	}
	s.mu.Unlock()
	if err != nil {
		hh.drop(id, s)
		return "", err
	}
	return id, nil
}
