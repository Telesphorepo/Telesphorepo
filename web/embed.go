// Package web embeds the built web UI (web/dist) and serves it with SPA fallback.
package web

import (
	"bytes"
	"embed"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
)

//go:embed all:dist
var dist embed.FS

// Handler serves static files from the embedded dist/ and falls back to index.html for
// unknown paths that don't look like files. A text file that the build precompressed goes out
// as its .br or .gz file to a client that accepts the coding (serveEncoded).
func Handler() http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	files := http.FileServerFS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p := path.Clean("/" + r.URL.Path)
		if strings.HasPrefix(p, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			if !serveEncoded(w, r, sub, p[1:]) {
				files.ServeHTTP(w, r)
			}
			return
		}
		if p != "/" {
			if st, err := fs.Stat(sub, p[1:]); err == nil && !st.IsDir() {
				if p == "/index.html" {
					// The file server answers /index.html with a redirect to /.
					w.Header().Set("Cache-Control", "no-cache")
				} else if serveEncoded(w, r, sub, p[1:]) {
					return
				}
				files.ServeHTTP(w, r)
				return
			}
			if path.Ext(p) != "" {
				http.NotFound(w, r)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-cache")
		if serveEncoded(w, r, sub, "index.html") {
			return
		}
		b, err := fs.ReadFile(sub, "index.html")
		if err != nil {
			b = []byte(notBuiltPage)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(b))
	})
}

// encodings are the precompressed files that the web build writes next to a text file of
// dist (see vite.config.ts), in the order the server prefers them.
var encodings = []struct{ coding, ext string }{{"br", ".br"}, {"gzip", ".gz"}}

// serveEncoded serves the precompressed file of name (a file of fsys) in the first coding of
// encodings that the client accepts, with Content-Encoding, the Content-Type of name and the
// headers already set on w. A file that has a precompressed file gets Vary: Accept-Encoding in
// every form. It serves nothing and returns false when name is not a file, has no precompressed
// file the client accepts, or the request has a Range header (a range applies to the plain file).
func serveEncoded(w http.ResponseWriter, r *http.Request, fsys fs.FS, name string) bool {
	if st, err := fs.Stat(fsys, name); err != nil || st.IsDir() {
		return false
	}
	vary := false
	for _, e := range encodings {
		f, err := fsys.Open(name + e.ext)
		if err != nil {
			continue
		}
		if !vary {
			w.Header().Add("Vary", "Accept-Encoding")
			vary = true
		}
		rs, ok := f.(io.ReadSeeker)
		if !ok || r.Header.Get("Range") != "" || !httpx.AcceptsEncoding(r, e.coding) {
			f.Close()
			continue
		}
		defer f.Close()
		w.Header().Set("Content-Encoding", e.coding)
		// ServeContent takes the Content-Type from the extension of name.
		http.ServeContent(w, r, name, time.Time{}, rs)
		return true
	}
	return false
}

// notBuiltPage is served in place of the UI when web/dist has no index.html (only .keep).
const notBuiltPage = `<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Telesphorepo - UI not built</title>
<style>
:root{color-scheme:light dark;--bg:#fafafa;--fg:#1d1d1f;--mute:#666;--code:#ececec}
@media (prefers-color-scheme:dark){:root{--bg:#141416;--fg:#ededed;--mute:#a0a0a0;--code:#26262a}}
body{margin:0;background:var(--bg);color:var(--fg);font:16px/1.55 system-ui,sans-serif}
main{max-width:40rem;margin:0 auto;padding:3rem 1rem}
h1{font-size:1.4rem;margin:0 0 .5rem}p{color:var(--mute)}
pre{background:var(--code);padding:.75rem 1rem;border-radius:6px;overflow-x:auto}
</style></head>
<body><main>
<h1>UI not built</h1>
<p><strong>UI not built — run <code>go tool task dev</code> (hot reload) or <code>go tool task build</code>.</strong></p>
<p>The server is running, but this binary was built without the web UI. The Maven, Debian and JSON API endpoints work normally.</p>
<p>From the repository root, either run everything with hot reload:</p>
<pre>go tool task dev</pre>
<p>and open <a href="http://localhost:5173">http://localhost:5173</a>, or build the binary with the UI embedded:</p>
<pre>go tool task build</pre>
</main></body></html>
`
