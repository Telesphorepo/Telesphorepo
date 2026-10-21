package httpx

import (
	"compress/gzip"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// AcceptsEncoding reports whether the Accept-Encoding header of r accepts coding (RFC 9110
// 12.5.3): coding named with a q above 0, or, when it is not named, "*" with a q above 0.
func AcceptsEncoding(r *http.Request, coding string) bool {
	star := false
	for _, v := range r.Header.Values("Accept-Encoding") {
		for _, part := range strings.Split(v, ",") {
			name, params, _ := strings.Cut(part, ";")
			name = strings.TrimSpace(name)
			switch {
			case strings.EqualFold(name, coding):
				return qAboveZero(params)
			case name == "*":
				star = qAboveZero(params)
			}
		}
	}
	return star
}

// qAboveZero reports whether the parameters of an Accept-Encoding member have no q or a q above 0.
func qAboveZero(params string) bool {
	for _, p := range strings.Split(params, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
		if ok && strings.EqualFold(strings.TrimSpace(k), "q") {
			q, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			return err == nil && q > 0
		}
	}
	return true
}

// gzipMin is the smallest JSON body that GzipJSON compresses.
const gzipMin = 1024

var gzipPool = sync.Pool{New: func() any { return gzip.NewWriter(io.Discard) }}

// GzipJSON compresses the JSON answers of next with gzip for a client that accepts gzip. It adds
// Vary: Accept-Encoding to every answer. It passes an answer through unchanged when the request is
// a HEAD or has a Range header, when the status is 1xx, 204 or 304, when the answer sets
// Content-Encoding itself, when its Content-Type is not JSON (application/json or a +json type),
// when the body ends below 1 KiB, or when the handler flushes before the body reaches 1 KiB. A
// compressed answer drops Content-Length and Accept-Ranges. Unwrap gives http.ResponseController the writer below.
func GzipJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Accept-Encoding")
		if r.Method == http.MethodHead || r.Header.Get("Range") != "" || !AcceptsEncoding(r, "gzip") {
			next.ServeHTTP(w, r)
			return
		}
		gw := &gzipWriter{ResponseWriter: w}
		next.ServeHTTP(gw, r)
		gw.finish()
	})
}

// gzipWriter holds the status and the first bytes of the body until it decides: compress
// (gz is set) or pass through (plain).
type gzipWriter struct {
	http.ResponseWriter
	status int // the status of the handler, 0 until it writes one
	buf    []byte
	gz     *gzip.Writer
	plain  bool
}

func (w *gzipWriter) WriteHeader(code int) {
	if w.status != 0 {
		if w.plain {
			w.ResponseWriter.WriteHeader(code)
		}
		return
	}
	if code < 200 {
		w.ResponseWriter.WriteHeader(code)
		return
	}
	w.status = code
	h := w.Header()
	if code == http.StatusNoContent || code == http.StatusNotModified || h.Get("Content-Encoding") != "" || !isJSON(h.Get("Content-Type")) {
		w.passThrough()
	}
}

func (w *gzipWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	switch {
	case w.plain:
		return w.ResponseWriter.Write(b)
	case w.gz != nil:
		return w.gz.Write(b)
	}
	w.buf = append(w.buf, b...)
	if len(w.buf) >= gzipMin {
		if err := w.compress(); err != nil {
			return 0, err
		}
	}
	return len(b), nil
}

// Flush sends what the handler wrote. An undecided answer passes through uncompressed.
func (w *gzipWriter) Flush() {
	switch {
	case w.gz != nil:
		w.gz.Flush()
	case !w.plain:
		if w.status == 0 {
			w.status = http.StatusOK
		}
		w.passThrough()
	}
	http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *gzipWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// passThrough sends the status and the held bytes as they are; later writes go straight through.
func (w *gzipWriter) passThrough() error {
	w.plain = true
	w.ResponseWriter.WriteHeader(w.status)
	if len(w.buf) == 0 {
		return nil
	}
	_, err := w.ResponseWriter.Write(w.buf)
	w.buf = nil
	return err
}

// compress sends the status with Content-Encoding: gzip and starts the gzip stream with the held bytes.
func (w *gzipWriter) compress() error {
	h := w.Header()
	h.Set("Content-Encoding", "gzip")
	h.Del("Content-Length")
	h.Del("Accept-Ranges")
	w.ResponseWriter.WriteHeader(w.status)
	w.gz = gzipPool.Get().(*gzip.Writer)
	w.gz.Reset(w.ResponseWriter)
	_, err := w.gz.Write(w.buf)
	w.buf = nil
	return err
}

// finish ends the answer after the handler returned: it closes the gzip stream, or sends an
// undecided answer (a body below gzipMin) as it is.
func (w *gzipWriter) finish() {
	switch {
	case w.gz != nil:
		w.gz.Close()
		w.gz.Reset(io.Discard)
		gzipPool.Put(w.gz)
		w.gz = nil
	case !w.plain && w.status != 0:
		w.passThrough()
	}
}

// isJSON reports whether the media type of ct is application/json or a +json type.
func isJSON(ct string) bool {
	mt, _, _ := strings.Cut(ct, ";")
	mt = strings.ToLower(strings.TrimSpace(mt))
	return mt == "application/json" || strings.HasSuffix(mt, "+json")
}
