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
	// It returns follow-up paths that the pipeline removes next (e.g. the tags of a deleted
	// manifest digest).
	OnRemoved func(ctx context.Context, tx *sql.Tx, repo *repos.Repo, removed []*Asset) (follow []repopath.Path, err error)
	// AfterRemove runs after a remove, without locks (e.g. Maven writes maven-metadata.xml again).
	AfterRemove func(ctx context.Context, repo *repos.Repo, removed []*Asset)

	// Classify tells the proxy how to cache a path; ok=false refuses the path (404).
	Classify func(path repopath.Path) (Class, bool)
	// Merge merges a file of a group from its members (e.g. maven-metadata.xml).
	Merge *Merger
	// RemoteAuth answers a remote's 401 challenge with the headers of a retry (e.g. a registry's
	// token exchange). The proxy calls it once per fetch. nil: Basic credentials only.
	RemoteAuth func(ctx context.Context, remote *repos.Proxy, challenge http.Header) (http.Header, error)
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
}

// Merger merges a file that several members of a group have.
type Merger struct {
	// Match names the base file of path and the checksum path is ("" for the file itself;
	// "md5", "sha1", "sha256" or "sha512" for a checksum file of the base).
	Match func(path repopath.Path) (base repopath.Path, checksum string, ok bool)
	// Merge merges the bytes of the base file of each member that has it, in member order.
	Merge func(ctx context.Context, base repopath.Path, parts [][]byte) ([]byte, error)
}
