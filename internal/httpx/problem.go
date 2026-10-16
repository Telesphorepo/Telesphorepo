package httpx

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// ProblemPrefix starts the type of every problem that the server declares.
const ProblemPrefix = "urn:telesphorepo:problem:"

// The remedy actions that the CLI implements. Remedy.Action holds one of them.
const (
	RemedyUpdateCLI    = "update_cli"    // offer "telesphorepo update"
	RemedyUpdateServer = "update_server" // name the server admin and the server version of Version
	RemedyLogin        = "login"         // run the device flow, then the command again
	RemedySetup        = "setup"         // print the Publish tab link of URL
	RemedyRetry        = "retry"         // wait After seconds and try once more
	RemedyOpenURL      = "open_url"      // print URL, and open it on a TTY
	RemedyFixProject   = "fix_project"   // name File and print the detail
)

// Remedy is what a client can do about a problem.
type Remedy struct {
	Action  string `json:"action"`
	URL     string `json:"url,omitempty"`
	After   int    `json:"after,omitempty"`
	File    string `json:"file,omitempty"`
	Version string `json:"version,omitempty"`
}

// Problem is a problem type: a stable name, the status and the title of every answer of that
// type, and the remedy that applies to each of them. DeclareProblem declares each type once.
type Problem struct {
	Name   string // the type after ProblemPrefix, e.g. "rate-limited"
	Status int
	Title  string
	Remedy *Remedy // nil: no remedy
}

// Type returns the type URI of p.
func (p *Problem) Type() string { return ProblemPrefix + p.Name }

// Doc returns the problem document of p with detail.
func (p *Problem) Doc(detail string) Doc {
	d := Doc{Type: p.Type(), Title: p.Title, Detail: detail, Status: p.Status}
	if p.Remedy != nil {
		rm := *p.Remedy
		d.Remedy = &rm
	}
	return d
}

// Write writes the problem document of p with detail.
func (p *Problem) Write(w http.ResponseWriter, r *http.Request, detail string) {
	WriteProblem(w, r, p.Doc(detail))
}

var (
	problemsMu sync.Mutex
	problems   = map[string]*Problem{}
)

// DeclareProblem declares the problem type p and returns it. It panics when the name is empty or
// declared already, or when the status is not an error status.
func DeclareProblem(p Problem) *Problem {
	problemsMu.Lock()
	defer problemsMu.Unlock()
	if p.Name == "" || p.Title == "" || p.Status < 400 || p.Status > 599 {
		panic(fmt.Sprintf("httpx: incomplete problem type %+v", p))
	}
	if _, ok := problems[p.Name]; ok {
		panic("httpx: problem type " + p.Name + " is declared twice")
	}
	problems[p.Name] = &p
	return &p
}

// ProblemRateLimited is the answer to a client that made too many failed attempts. Its remedy
// waits the seconds of the Retry-After header.
var ProblemRateLimited = DeclareProblem(Problem{Name: "rate-limited", Status: http.StatusTooManyRequests,
	Title: "Too many attempts", Remedy: &Remedy{Action: RemedyRetry}})

// Doc is one RFC 9457 problem document. Ext holds the extension members of the answer, e.g.
// "fields" of a settings refusal. A member of Ext never replaces a standard member.
type Doc struct {
	Type   string
	Title  string
	Detail string
	Status int
	Remedy *Remedy
	Ext    map[string]any
}

// Blank returns the problem document of type "about:blank" with status and detail: its title is
// the status text. A 429 is the problem rate-limited.
func Blank(status int, detail string) Doc {
	if status == http.StatusTooManyRequests {
		return ProblemRateLimited.Doc(detail)
	}
	return Doc{Type: "about:blank", Title: http.StatusText(status), Detail: detail, Status: status}
}

// With returns d with the extension members ext added.
func (d Doc) With(ext map[string]any) Doc {
	m := make(map[string]any, len(d.Ext)+len(ext))
	for k, v := range d.Ext {
		m[k] = v
	}
	for k, v := range ext {
		m[k] = v
	}
	d.Ext = m
	return d
}

// ProblemRoute reports whether the route of path answers errors with problem documents: the
// routes under /api/ and /cli/, and the install scripts.
func ProblemRoute(path string) bool {
	return strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/cli/") ||
		path == "/install.sh" || path == "/install.ps1"
}

// WriteProblem writes d. On a route of ProblemRoute it is an application/problem+json document;
// a retry remedy without After takes the seconds of the Retry-After header. Every other route
// (the format routes) answers {"error": detail} with the extension members of d.
func WriteProblem(w http.ResponseWriter, r *http.Request, d Doc) {
	body := make(map[string]any, len(d.Ext)+5)
	for k, v := range d.Ext {
		body[k] = v
	}
	if !ProblemRoute(r.URL.Path) {
		body["error"] = d.Detail
		WriteJSON(w, d.Status, body)
		return
	}
	if d.Remedy != nil && d.Remedy.Action == RemedyRetry && d.Remedy.After == 0 {
		if n, err := strconv.Atoi(w.Header().Get("Retry-After")); err == nil && n > 0 {
			d.Remedy.After = n
		}
	}
	body["type"], body["title"], body["status"] = d.Type, d.Title, d.Status
	if d.Detail != "" {
		body["detail"] = d.Detail
	}
	if d.Remedy != nil {
		body["remedy"] = d.Remedy
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(d.Status)
	_ = json.NewEncoder(w).Encode(body)
}
