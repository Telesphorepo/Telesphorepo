package publish

import (
	"bytes"
	_ "embed"
	"io/fs"
	"net/http"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/clidist"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/route"
)

// The install scripts of the Telesphorepo CLI (GET /install.sh for sh, GET /install.ps1 for
// PowerShell) and the CLI archives that they download (GET /cli/<archive> and
// GET /cli/SHA256SUMS). The archives are those of the server's own build (internal/clidist). The
// server writes its public address and the SHA-256 of each archive into the scripts.

//go:embed install.sh
var installSh string

//go:embed install.ps1
var installPs1 string

// ProblemNoCLIDownloads is the answer of /cli/ and of the install scripts on a server whose build
// holds no CLI archives.
var ProblemNoCLIDownloads = httpx.DeclareProblem(httpx.Problem{Name: "no-cli-downloads", Status: http.StatusNotFound,
	Title: "This build has no CLI downloads"})

// installRoutes mounts the install scripts and the CLI downloads. All are public.
func (m *mod) installRoutes(r *route.Router) {
	r.Handle("GET /install.sh", route.Public, m.installScript(false))
	r.Handle("GET /install.ps1", route.Public, m.installScript(true))
	r.Handle("GET /cli/{file}", route.Public, m.cliFile)
}

// noDownloads writes ProblemNoCLIDownloads.
func noDownloads(w http.ResponseWriter, r *http.Request) {
	ProblemNoCLIDownloads.Write(w, r, "This server was built without the CLI archives. Build it with go tool task build, or install a release.")
}

// installScript serves the sh script, or the PowerShell script with ps1, with the base URL and
// the SHA-256 of each archive filled in. The PowerShell script has Windows line ends.
func (m *mod) installScript(ps1 bool) route.Handler {
	return func(w http.ResponseWriter, r *http.Request, _ *route.Call) {
		sums := clidist.Sums()
		if len(sums) == 0 {
			noDownloads(w, r)
			return
		}
		base, ok := m.scriptBaseURL(r)
		if !ok {
			httpx.Error(w, r, http.StatusBadRequest, "the server address cannot go in a script; set the public base URL (base_url)")
			return
		}
		names := make([]string, 0, len(sums))
		for n := range sums {
			names = append(names, n)
		}
		slices.Sort(names)
		var lines []string
		script := installSh
		for _, n := range names {
			if ps1 {
				lines = append(lines, "\t'"+n+"' = '"+sums[n]+"'")
			} else {
				lines = append(lines, "\t"+n+") echo '"+sums[n]+"' ;;")
			}
		}
		if ps1 {
			script = installPs1
		}
		body := strings.NewReplacer("@@URL@@", base, "@@SUMS@@", strings.Join(lines, "\n")).
			Replace(strings.ReplaceAll(script, "\r\n", "\n"))
		if ps1 {
			body = strings.ReplaceAll(body, "\n", "\r\n")
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write([]byte(body))
	}
}

// cliFile serves one CLI archive of the build, or SHA256SUMS.
func (m *mod) cliFile(w http.ResponseWriter, r *http.Request, _ *route.Call) {
	sums := clidist.Sums()
	if len(sums) == 0 {
		noDownloads(w, r)
		return
	}
	name := r.PathValue("file")
	if _, ok := sums[name]; !ok && name != clidist.SumsFile {
		httpx.Error(w, r, http.StatusNotFound, "no such CLI download; the archives are listed in /cli/"+clidist.SumsFile)
		return
	}
	b, err := fs.ReadFile(clidist.FS(), name)
	if err != nil {
		httpx.Error(w, r, http.StatusNotFound, "no such CLI download")
		return
	}
	switch path.Ext(name) {
	case ".zip":
		w.Header().Set("Content-Type", "application/zip")
	case ".gz":
		w.Header().Set("Content-Type", "application/gzip")
	default:
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}
	// The names carry no version: a client checks again after a server update.
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(b))
}

// scriptBaseURL is the base_url setting, else the URL the client reached the server at. The
// scripts quote it in single quotes, so it must be a plain URL: no quote, space, backslash or
// control character.
func (m *mod) scriptBaseURL(r *http.Request) (string, bool) {
	base := m.baseURL(r)
	for _, c := range base {
		if c <= ' ' || c == 0x7f || strings.ContainsRune("'\"`$\\", c) {
			return "", false
		}
	}
	return base, true
}
