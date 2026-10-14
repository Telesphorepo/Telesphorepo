// Package web embeds the built React UI (web/dist) and serves it with SPA fallback.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// Handler serves static files from the embedded dist/ and falls back to index.html for
// unknown paths that don't look like files.
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
			files.ServeHTTP(w, r)
			return
		}
		if p != "/" {
			if st, err := fs.Stat(sub, p[1:]); err == nil && !st.IsDir() {
				if p == "/index.html" {
					w.Header().Set("Cache-Control", "no-cache")
				}
				files.ServeHTTP(w, r)
				return
			}
			if path.Ext(p) != "" {
				http.NotFound(w, r)
				return
			}
		}
		b, err := fs.ReadFile(sub, "index.html")
		if err != nil {
			b = []byte(notBuiltPage)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		if r.Method == http.MethodGet {
			w.Write(b)
		}
	})
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
<p><strong>UI not built — run <code>bun run dev</code> (hot reload) or <code>bun run build</code>.</strong></p>
<p>The server is running, but this binary was built without the web UI. The Maven, Debian and JSON API endpoints work normally.</p>
<p>From the repository root, either run everything with hot reload:</p>
<pre>bun run dev</pre>
<p>and open <a href="http://localhost:5173">http://localhost:5173</a>, or build the binary with the UI embedded:</p>
<pre>bun run build</pre>
<p>(<code>npm run dev</code> / <code>npm run build</code> work too.)</p>
</main></body></html>
`
