package assets

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"

	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// Hooks are the parts of a format that the pipeline and the read path call. Every hook is
// optional. The format registry builds them from format.Spec.
type Hooks struct {
	// Describe gives the content type and the kind of a path; ok=false refuses the path (400).
	// nil: FallbackContentType and KindArtifact.
	Describe func(path repopath.Path) (Describe, bool)
	// Admit decides a write: Store, Keep (the same bytes; nothing is written) or a
	// *Refusal. existing is the asset at the path, nil when none. nil: the same bytes are
	// kept, other bytes replace the file.
	Admit func(ctx context.Context, w *Write, existing *Asset) (Decision, error)
	// OnCommitted runs in the transaction of the write, after the asset row of a stored write: the
	// format writes its own rows (e.g. the Debian package of a .deb) and marks rebuilds. prev is
	// the asset that was at the path, nil when none.
	OnCommitted func(ctx context.Context, tx *sql.Tx, w *Write, a *Asset, prev *Asset) error
	// AfterCommit runs after the commit, without locks, with every result.
	AfterCommit func(ctx context.Context, repo *repos.Repo, rs []*Result)
	// OnRemoved runs in the transaction of a remove, while the removed rows are still readable.
	// prefixes are the folders of the removal (Removal.Prefixes): a file under one of them goes in
	// this batch or a later batch of the same removal. It returns follow-up paths that the
	// pipeline removes next (e.g. the tags of a deleted manifest digest).
	OnRemoved func(ctx context.Context, tx *sql.Tx, repo *repos.Repo, removed []*Asset, prefixes []repopath.Path) (follow []repopath.Path, err error)
	// AfterRemove runs after a remove, without locks (e.g. Maven writes maven-metadata.xml again).
	AfterRemove func(ctx context.Context, repo *repos.Repo, removed []*Asset)

	// Classify tells the proxy how to cache a path of proxy repo repo, whose remote is remote (the
	// repo config is repo.Config); ok=false refuses the path (404).
	Classify func(repo *repos.Repo, remote *repos.Proxy, path repopath.Path) (Class, bool)
	// Merge merges a file of a group from its members (e.g. maven-metadata.xml).
	Merge *Merger
	// RemoteAuth answers a remote's 401 challenge with the headers of a retry (e.g. a registry's
	// token exchange). The proxy calls it once per fetch. nil: the proxy sends the remote's
	// credentials as HTTP Basic on every request. With RemoteAuth, the credentials go out only in
	// the answer to a challenge.
	RemoteAuth func(ctx context.Context, remote *repos.Proxy, challenge http.Header) (http.Header, error)
	// Unit names the part of a repo that a group serves from one member (the asset's unit): a
	// package whose files a group takes from the member that owns its name. "" (and nil): a group
	// serves the path from the first member that has it.
	Unit func(path string) string
}

// unitOf is the unit of path (Unit).
func (h *Hooks) unitOf(path string) string {
	if h == nil || h.Unit == nil {
		return ""
	}
	return h.Unit(path)
}

// UnitOf is the unit of path in a repo of format (Hooks.Unit).
func (p *Pipeline) UnitOf(format, path string) string { return p.Hooks(format).unitOf(path) }

// Watcher sees every commit and remove of every format and every writer (format routes, uploads,
// imports, proxy fetches, generated files), unlike Hooks, which belong to one format. A module
// registers one with Pipeline.Watch in its Init (the edge module keeps its R2 pointers with it).
// An error of Replacing, Creating, Committed, Removing or Removed fails the commit or the remove
// batch.
type Watcher interface {
	// Replacing runs in CommitAll after Admit and before the blobs are ensured, under the path
	// locks, with the existing assets that the commit replaces (only when there are some).
	Replacing(ctx context.Context, repo *repos.Repo, prev []*Asset) error
	// Creating runs at the same point as Replacing, after it, with the paths that the commit
	// stores where no asset was (only when there are some).
	Creating(ctx context.Context, repo *repos.Repo, paths []string) error
	// Committed runs in the commit transaction, after OnCommitted, with every asset it stored. A
	// failed transaction that is tried again calls it again.
	Committed(ctx context.Context, tx *sql.Tx, repo *repos.Repo, stored []*Asset) error
	// Removing runs in each remove batch after the path locks and before the transaction, with
	// the paths of the batch (sorted, each once; a path may have no asset).
	Removing(ctx context.Context, repo *repos.Repo, paths []string) error
	// Removed runs in the remove transaction, after OnRemoved, with the assets it deletes (the
	// rows are still readable).
	Removed(ctx context.Context, tx *sql.Tx, repo *repos.Repo, removed []*Asset) error
	// Touched runs after SetTimes changed the dates of an asset outside the pipeline transaction.
	Touched(ctx context.Context, repo *repos.Repo, paths []string) error
}

// Describe is what a format says about a path.
type Describe struct {
	ContentType string
	Kind        string // KindArtifact, KindMetadata or KindIndex
}

// Decision is the answer of Admit.
type Decision int

const (
	Store Decision = iota // write the file
	Keep                  // the file has these bytes already: write nothing (status unchanged)
)

// Refusal is a write or a path that the format refuses: Status is 400, 404 or 409.
type Refusal struct {
	Status  int
	Message string
}

func (r *Refusal) Error() string { return r.Message }

// HTTPStatus is the status of the answer (httpx.StatusError).
func (r *Refusal) HTTPStatus() int { return r.Status }

// Refuse returns a *Refusal.
func Refuse(status int, format string, args ...any) error {
	return &Refusal{Status: status, Message: fmt.Sprintf(format, args...)}
}

// Class is how a proxy caches a path.
type Class struct {
	// Changing files can change at the remote (maven-metadata.xml, InRelease): they are checked
	// again after the repo's metadata_ttl. Other files are fetched once.
	Changing bool
	// Addressed is a path that names its bytes by their digest (an OCI blob or a manifest by
	// digest): a stored copy of the path in any member of a group is the same file, so a group
	// serves a stored copy before it asks a proxy member's remote.
	Addressed bool
	// Remote is the path of the file at the remote: relative to the remote URL (to Files when it
	// is set), or from the root of that URL's host when it starts with "/". "" is the local path.
	Remote string
	// Files is an absolute http(s) base URL that the file comes from instead of the remote URL,
	// e.g. the host a registry serves its package files from. A format takes it only from the
	// repo config that an admin wrote, never from a remote document. The remote's credentials go
	// with the request only when Files has the scheme and host of the remote URL.
	Files string
	// Header holds extra headers of the remote request, e.g. Accept.
	Header http.Header
	// KeepType stores the Content-Type of the remote's answer as the content type of the file.
	KeepType bool
	// Unchanged is how a cached changing file is checked again: nil sends a conditional GET. With
	// Unchanged, the proxy sends a HEAD with the same headers and keeps the file when Unchanged
	// returns true for the answer; else it GETs the file.
	Unchanged func(head *http.Response, cached *Asset) bool
}

// Merger merges a file that several members of a group have.
type Merger struct {
	// Match names the base file of path and the checksum path is ("" for the file itself;
	// "md5", "sha1", "sha256" or "sha512" for a checksum file of the base).
	Match func(path repopath.Path) (base repopath.Path, checksum string, ok bool)
	// Merge merges the bytes of the base file of each member that has it, in member order.
	Merge func(ctx context.Context, base repopath.Path, parts [][]byte) ([]byte, error)
}
