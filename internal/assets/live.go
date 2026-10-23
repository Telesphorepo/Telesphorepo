package assets

import (
	"net/http"
	"strconv"
	"time"
)

// liveWriteTimeout bounds one write to a client that a proxy fetch streams to: a client that
// takes no bytes for this long is dropped, and the fetch goes on without it.
const liveWriteTimeout = 30 * time.Second

// Live is a client that waits for a file that a proxy fetches now. A fetch of a file that is not
// cached sends the remote's bytes to it while it stores them (Fetcher.Ensure), so the client does
// not wait for the whole download first. The last block of the body goes out only after the
// commit: a file that the remote breaks off, or that the format refuses, never reaches the client
// whole, and its answer is aborted. A Live serves one request; nil is a client that takes no
// stream.
type Live struct {
	w      http.ResponseWriter
	rc     *http.ResponseController
	header func(h http.Header)
	count  func(repo, bytes int64) // the download counter of the reader; nil counts nothing

	started bool   // the answer's headers went out
	broken  bool   // the answer cannot complete: the client went away, or the fetch failed
	held    []byte // the last block of the body, sent after the commit
}

// Live returns the client of a GET that a proxy fetch may stream to, or nil when r cannot take a
// stream: a HEAD, a range or a conditional request. header sets the format's own headers of the
// answer (e.g. a digest that the path names); nil sets none.
func (rd *Reader) Live(w http.ResponseWriter, r *http.Request, header func(h http.Header)) *Live {
	if r.Method != http.MethodGet {
		return nil
	}
	for _, k := range []string{"Range", "If-Range", "If-None-Match", "If-Modified-Since", "If-Match", "If-Unmodified-Since"} {
		if r.Header.Get(k) != "" {
			return nil
		}
	}
	return &Live{w: w, rc: http.NewResponseController(w), header: header, count: rd.count}
}

// Begin sends the headers of the answer: 200 with ct, the remote's Last-Modified modified and the
// length size (-1: unknown, the body goes chunked). The hashes are not known yet: the answer has
// no ETag and no checksum headers.
func (l *Live) Begin(ct string, size int64, modified time.Time) {
	h := l.w.Header()
	if l.header != nil {
		l.header(h)
	}
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	if !inline(ct) {
		h.Set("Content-Disposition", "attachment")
	}
	h.Set("Content-Type", ct)
	if !modified.IsZero() {
		h.Set("Last-Modified", modified.UTC().Format(http.TimeFormat))
	}
	if size >= 0 {
		h.Set("Content-Length", strconv.FormatInt(size, 10))
	}
	l.started = true
	l.w.WriteHeader(http.StatusOK)
}

// Write takes the next block of the body: it sends the block before it and holds this one. It
// never fails, so a client that goes away does not stop the fetch.
func (l *Live) Write(p []byte) (int, error) {
	l.send()
	if !l.broken {
		l.held = append(l.held[:0], p...)
	}
	return len(p), nil
}

// send sends the held block, unless the answer is broken.
func (l *Live) send() {
	if l.broken || len(l.held) == 0 {
		l.held = l.held[:0]
		return
	}
	_ = l.rc.SetWriteDeadline(time.Now().Add(liveWriteTimeout))
	if _, err := l.w.Write(l.held); err != nil {
		l.broken = true
	}
	l.held = l.held[:0]
}

// End sends the held block after the commit of a: the answer is complete, and it counts as a
// download of a when the client took all of it.
func (l *Live) End(a *Asset) {
	if l == nil || !l.started {
		return
	}
	l.send()
	_ = l.rc.SetWriteDeadline(time.Time{})
	if !l.broken && l.count != nil && a != nil {
		l.count(a.RepoID, a.Size)
	}
}

// Abort drops the held block: the answer that started is aborted (Done).
func (l *Live) Abort() {
	if l == nil {
		return
	}
	l.broken, l.held = true, nil
}

// Done reports whether the answer went out while the proxy fetched the file, so the caller writes
// nothing more. An answer that started and broke off aborts the connection here (a panic with
// http.ErrAbortHandler), so the client does not take a short body for the file.
func (l *Live) Done() bool {
	if l == nil || !l.started {
		return false
	}
	if l.broken {
		panic(http.ErrAbortHandler)
	}
	return true
}
