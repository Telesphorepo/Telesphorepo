package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// The remedy actions of a problem document that the CLI implements. An unknown action prints the
// detail and the URL.
const (
	remedyUpdateCLI    = "update_cli"
	remedyUpdateServer = "update_server"
	remedyLogin        = "login"
	remedySetup        = "setup"
	remedyRetry        = "retry"
	remedyOpenURL      = "open_url"
	remedyFixProject   = "fix_project"
)

// problemPrefix starts the type of each named problem of the server.
const problemPrefix = "urn:telesphorepo:problem:"

// problem is an RFC 9457 problem document of the server. A field that the CLI does not know is
// ignored.
type problem struct {
	Type     string  `json:"type"`
	Title    string  `json:"title"`
	Detail   string  `json:"detail"`
	Status   int     `json:"status"`
	Remedy   *remedy `json:"remedy"`
	Received *int64  `json:"received"` // a resumable upload: the bytes that the server holds
	ETag     *string `json:"etag"`     // a failed precondition: the ETag of the file now (nil: none)
}

// remedy is what the CLI can do about a problem.
type remedy struct {
	Action  string `json:"action"`
	URL     string `json:"url"`
	After   int    `json:"after"`
	File    string `json:"file"`
	Version string `json:"version"`
}

// problemError is a problem document that the server at server answered. version is the server
// version of the answer's header ("" when it sent none).
type problemError struct {
	p       problem
	server  string
	version string
}

func (e *problemError) Error() string {
	if e.p.Detail == "" {
		return e.p.Title
	}
	return e.p.Title + ": " + e.p.Detail
}

// name is the name of the problem type ("" for about:blank and for a type of another scheme).
func (e *problemError) name() string {
	if n, ok := strings.CutPrefix(e.p.Type, problemPrefix); ok {
		return n
	}
	return ""
}

// render prints the problem as the server wrote it, title and detail, and then what its remedy
// says. The remedies that run something (update_cli, login) are offered by the caller of render
// (offerRemedy); render only prints. On a styled stderr the title is a red ✗ line and the remedy
// a "→" line with its command highlighted.
func (e *problemError) render(w io.Writer) {
	p := paint(false)
	if w == os.Stderr {
		p = errp
	}
	fmt.Fprintln(w, p.mark("✗", "31;1")+p.wrap("1;31", e.p.Title))
	if e.p.Detail != "" && e.p.Detail != e.p.Title {
		fmt.Fprintln(w, p.indent()+e.p.Detail)
	}
	r := e.p.Remedy
	if r == nil {
		return
	}
	// hint prints a remedy line: label, then cmd highlighted.
	hint := func(label, cmd, end string) {
		fmt.Fprintln(w, p.mark("→", "36")+label+p.wrap("1;36", cmd)+end)
	}
	switch r.Action {
	case remedyUpdateCLI:
		hint("Update the CLI: ", "telesphorepo update "+e.server, "")
	case remedyUpdateServer:
		v := r.Version
		if v == "" {
			v = "a newer version"
		}
		running := ""
		if e.version != "" {
			running = " (it runs " + e.version + ")"
		}
		fmt.Fprintf(w, "%sAsk the admin of %s to update the server%s to %s.\n", p.mark("→", "36"), e.server, running, p.bold(v))
	case remedyLogin:
		hint("Approve this computer again: ", "telesphorepo login "+e.server, "")
	case remedySetup:
		if r.URL != "" {
			hint("Copy a setup line from the Publish tab: ", r.URL, "")
		}
	case remedyRetry:
		if r.After > 0 {
			fmt.Fprintf(w, "%sTry again in %d seconds.\n", p.mark("→", "36"), r.After)
		}
	case remedyOpenURL:
		if r.URL != "" {
			hint("", r.URL, "")
			if isTerminal(os.Stdout) && isTerminal(os.Stdin) {
				openBrowser(r.URL)
			}
		}
	case remedyFixProject:
		if r.File != "" {
			hint("Fix ", r.File, ".")
		}
	default:
		if r.URL != "" {
			hint("", r.URL, "")
		}
	}
}
