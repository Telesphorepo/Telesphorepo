package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/changelog"
)

// The change log check. Before a command, the CLI takes the entries of the change log between its
// own version and the server's version whose scope matches the command:
//
//   - the server is newer: the server's entries (GET /api/changes?since=<CLI version>). An entry
//     that needs the CLI stops the command with an offer to update.
//   - the CLI is newer: its own embedded entries after the server's version. An entry that needs
//     the server takes the older path of the scope when this CLI has one (olderPaths), else it
//     stops the command.
//
// A dev CLI or a dev server skips the check. The server's version and entries stay in
// ~/.telesphorepo/<server>/changes.json; the CLI fetches the entries again only when the
// Telesphorepo-Version header of an answer names another version.

// olderPaths are the IDs of the entries of the embedded change log for which this CLI keeps the
// older path of the scope. A CLI that adds an entry with needs = "server" and keeps the older code
// path gives the entry an id, adds the id here and asks compat.older before it takes the new path.
// The id stays when "go tool task tag" stamps the version of the entry.
var olderPaths = map[string]bool{}

// changesState is changes.json.
type changesState struct {
	ServerVersion string             `json:"server_version"`
	Since         string             `json:"since"` // the CLI version that the entries were fetched for
	Changes       []changelog.Change `json:"changes"`
	DriftNotice   string             `json:"drift_notice"` // the day of the last drift notice, YYYY-MM-DD
}

// compat runs the check for one server.
type compat struct {
	id       identity
	dir      string
	c        *client
	helper   bool // a helper mode: no question, notices on stderr
	state    changesState
	scopes   []string
	fetching bool
}

// compats are the servers that the running command talked to, for the drift notice at its end.
var compats []*compat

// attach starts the check for the server of client c: each answer of c runs it again when its
// version header names a new version.
func attach(c *client, helper bool) *compat {
	dir, _ := serverDir(c.id)
	cp := &compat{id: c.id, dir: dir, c: c, helper: helper}
	if dir != "" {
		if b, err := os.ReadFile(filepath.Join(dir, "changes.json")); err == nil {
			_ = json.Unmarshal(b, &cp.state)
		}
	}
	c.onVersion = cp.observe
	compats = append(compats, cp)
	return cp
}

// devVersion reports whether v is no release version (a dev build).
func devVersion(v string) bool {
	_, err := changelog.ParseVersion(v)
	return err != nil
}

// check runs the check for scope, e.g. "publish" or "publish/maven". It fetches the server's
// entries when changes.json has none for this CLI version.
func (cp *compat) check(ctx context.Context, scope string) error {
	if !slices.Contains(cp.scopes, scope) {
		cp.scopes = append(cp.scopes, scope)
	}
	if devVersion(version) {
		return nil
	}
	if cp.state.ServerVersion == "" || cp.state.Since != version {
		if err := cp.fetch(ctx); err != nil {
			return err
		}
	}
	return cp.evaluate()
}

// observe is the onVersion hook of the client: a new server version fetches the entries again and
// runs the check of the scopes so far.
func (cp *compat) observe(ctx context.Context, v string) error {
	if cp.fetching || devVersion(version) || (v == cp.state.ServerVersion && cp.state.Since == version) {
		return nil
	}
	if len(cp.scopes) == 0 {
		// No command scope yet: the next check fetches.
		cp.state.ServerVersion, cp.state.Since = "", ""
		return nil
	}
	if err := cp.fetch(ctx); err != nil {
		return err
	}
	return cp.evaluate()
}

// fetch reads the server's entries after the CLI version and saves them.
func (cp *compat) fetch(ctx context.Context) error {
	cp.fetching = true
	defer func() { cp.fetching = false }()
	var out struct {
		Version string             `json:"version"`
		Changes []changelog.Change `json:"changes"`
	}
	if _, err := cp.c.call(ctx, request{method: http.MethodGet, path: "/api/changes?since=" + url.QueryEscape(version), out: &out}); err != nil {
		return err
	}
	if out.Version == "" {
		out.Version = cp.c.version
	}
	cp.state.ServerVersion, cp.state.Since, cp.state.Changes = out.Version, version, out.Changes
	cp.save()
	return nil
}

// save writes changes.json. A failed write only means a fetch on the next command.
func (cp *compat) save() {
	if cp.dir == "" {
		return
	}
	b, err := json.MarshalIndent(cp.state, "", "  ")
	if err == nil {
		err = ownerOnlyDir(cp.dir)
	}
	if err == nil {
		err = writeOwnerOnly(filepath.Join(cp.dir, "changes.json"), append(b, '\n'))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}

// compatError stops a command: the CLI must update (cli), or the server must (else).
type compatError struct {
	cli       bool
	server    string // the canonical URL of the server
	summaries []string
	need      string // the version that the side must have at least
	running   string // the version that the side runs
}

func (e *compatError) Error() string { return strings.Join(e.summaries, "\n") }

// evaluate applies the decision table to the scopes of the command.
func (cp *compat) evaluate() error {
	srv, err := changelog.ParseVersion(cp.state.ServerVersion)
	if err != nil {
		return nil // a dev server
	}
	cli, err := changelog.ParseVersion(version)
	if err != nil {
		return nil
	}
	switch c := cli.Compare(srv); {
	case c < 0:
		var e *compatError
		for _, ch := range cp.state.Changes {
			v, err := changelog.ParseVersion(ch.Version)
			if err != nil || ch.NeedsServer() || v.Compare(cli) <= 0 || v.Compare(srv) > 0 || !cp.matches(ch) {
				continue
			}
			if e == nil {
				e = &compatError{cli: true, server: cp.id.base(), running: version}
			}
			e.summaries = append(e.summaries, ch.Summary)
			e.need = ch.Version
		}
		if e != nil {
			return e
		}
	case c > 0:
		all, err := changelog.Embedded()
		if err != nil {
			return nil
		}
		newer, err := changelog.Since(all, cp.state.ServerVersion)
		if err != nil {
			return nil
		}
		var e *compatError
		for _, ch := range newer {
			if !ch.NeedsServer() || !cp.matches(ch) || (ch.ID != "" && olderPaths[ch.ID]) {
				continue
			}
			if e == nil {
				e = &compatError{server: cp.id.base(), running: cp.state.ServerVersion}
			}
			e.summaries = append(e.summaries, ch.Summary)
			e.need = ch.Version
		}
		if e != nil {
			return e
		}
	}
	return nil
}

// matches reports whether ch concerns a scope of the command.
func (cp *compat) matches(ch changelog.Change) bool {
	for _, s := range cp.scopes {
		if ch.Matches(s) {
			return true
		}
	}
	return false
}

// older reports whether the CLI must take the older path of the entry id of olderPaths: the
// server runs a release older than the version of the entry. An entry that is still "dev" (a dev
// CLI) takes the new path.
func (cp *compat) older(id string) bool {
	if !olderPaths[id] {
		return false
	}
	srv, err := changelog.ParseVersion(cp.state.ServerVersion)
	if err != nil {
		return false
	}
	all, err := changelog.Embedded()
	if err != nil {
		return false
	}
	for _, ch := range all {
		if ch.ID == id {
			v, err := changelog.ParseVersion(ch.Version)
			return err == nil && srv.Compare(v) < 0
		}
	}
	return false
}

// drift tells the person once a day per server that the server has a newer CLI: on a terminal
// it offers the update, else (and in a helper mode) it prints one line on stderr.
func (cp *compat) drift(ctx context.Context) {
	srv, err1 := changelog.ParseVersion(cp.state.ServerVersion)
	cli, err2 := changelog.ParseVersion(version)
	if err1 != nil || err2 != nil || cli.Compare(srv) >= 0 {
		return
	}
	today := time.Now().Format(time.DateOnly)
	if cp.state.DriftNotice == today {
		return
	}
	cp.state.DriftNotice = today
	cp.save()
	if cp.helper || !interactive() {
		fmt.Fprintf(os.Stderr, "telesphorepo: the server %s has CLI %s, you have %s. Run: telesphorepo update %s\n",
			cp.id.base(), cp.state.ServerVersion, version, cp.id.base())
		return
	}
	if confirm(fmt.Sprintf("The server has CLI %s, you have %s. Update now?", cp.state.ServerVersion, version), true) {
		if err := update(ctx, cp.id); err != nil {
			report(err)
		}
	}
}
