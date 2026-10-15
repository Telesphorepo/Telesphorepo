// Package files browses and deletes the files of a repo for the JSON API (spec §13): the folder
// tree, file and folder deletes, and repo deletes. It names no format; a format that keeps rows
// about its files implements format.DeleteObserver.
package files

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/db"
	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/httpx"
	"github.com/Telesphorepo/Telesphorepo/internal/provenance"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

var (
	// ErrInvalid is a path, prefix or cursor that is not a valid repo path.
	ErrInvalid = errors.New("invalid path")
	// ErrNotFound is a file that does not exist.
	ErrNotFound = errors.New("file not found")
)

// deleteBatch is how many files one delete transaction removes.
const deleteBatch = 500

// Service browses and deletes repo files.
type Service struct {
	db     *db.DB
	assets *assets.Service
	repos  *repos.Service
	auth   *auth.Service
	log    *slog.Logger
}

func New(d *db.DB, as *assets.Service, rs *repos.Service, a *auth.Service, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{db: d, assets: as, repos: rs, auth: a, log: log}
}

// ValidPath reports whether p is a valid repo path (spec §6).
func ValidPath(p string) bool {
	return httpx.ValidRepoPath(&http.Request{URL: &url.URL{Path: p}}, p)
}

// Folder normalizes a folder path: "" is the repo root, anything else gets one trailing slash.
func Folder(dir string) (string, error) {
	dir = strings.TrimSuffix(dir, "/")
	if dir == "" {
		return "", nil
	}
	if !ValidPath(dir) {
		return "", ErrInvalid
	}
	return dir + "/", nil
}

// Node is one child of a folder. A folder's Path ends in "/".
type Node struct {
	Name       string     `json:"name"`
	Type       string     `json:"type"` // "dir" or "file"
	Path       string     `json:"path"`
	Size       *int64     `json:"size,omitempty"`
	SHA256     string     `json:"sha256,omitempty"`
	UpdatedAt  *time.Time `json:"updated_at,omitempty"`
	UploadedBy string     `json:"uploaded_by,omitempty"` // username; "" when unknown or imported
	// Provenance is the GitHub Actions build that published the file; absent when none.
	Provenance *provenance.Provenance `json:"provenance,omitempty"`
}

// File is one file with its details (GET /api/repos/{name}/file).
type File struct {
	Name        string                 `json:"name"`
	Path        string                 `json:"path"`
	Size        int64                  `json:"size"`
	SHA256      string                 `json:"sha256"`
	ContentType string                 `json:"content_type"`
	Kind        string                 `json:"kind"` // "artifact", "metadata" or "index"
	CreatedAt   time.Time              `json:"created_at"`
	UpdatedAt   time.Time              `json:"updated_at"`
	UploadedBy  string                 `json:"uploaded_by"` // username; "" when unknown or imported
	Provenance  *provenance.Provenance `json:"provenance"`  // null when none
}

// Details returns file p of repo. ErrInvalid for an invalid path, ErrNotFound when it does not
// exist.
func (s *Service) Details(ctx context.Context, repo *repos.Repo, p string) (*File, error) {
	if !ValidPath(p) {
		return nil, ErrInvalid
	}
	a, err := s.assets.Get(ctx, repo.ID, p)
	if errors.Is(err, assets.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	f := &File{Name: path.Base(a.Path), Path: a.Path, Size: a.Size, SHA256: a.SHA256, ContentType: a.ContentType,
		Kind: a.Kind, CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt}
	if a.UploadedBy != 0 {
		if f.UploadedBy, err = s.auth.Username(ctx, a.UploadedBy); err != nil {
			return nil, err
		}
	}
	if f.Provenance, err = provenance.NewLoader(dbq.New(s.db.R)).Get(ctx, a.PublishSessionID); err != nil {
		return nil, err
	}
	return f, nil
}

// Tree returns up to limit direct children of folder dir ("" or "a/b/") in path order, after
// the cursor after (a child name; a folder's name ends in "/"). Each child costs one indexed
// seek on (repo_id, path): after a folder the next seek starts past every path under it, so a
// page costs limit+1 seeks however large the folder is. next is the cursor of the next page,
// "" at the end.
func (s *Service) Tree(ctx context.Context, repo *repos.Repo, dir, after string, limit int) ([]Node, string, error) {
	upper, _ := assets.PrefixEnd(dir) // "" for the root: no upper bound
	lower, inclusive := dir, true
	if after != "" {
		name := strings.TrimSuffix(after, "/")
		if strings.Contains(name, "/") || !ValidPath(name) {
			return nil, "", ErrInvalid
		}
		if strings.HasSuffix(after, "/") {
			lower, _ = assets.PrefixEnd(dir + name + "/")
		} else {
			lower, inclusive = dir+name, false
		}
	}
	users := map[int64]string{}
	builds := provenance.NewLoader(dbq.New(s.db.R))
	var out []Node
	for len(out) <= limit {
		a, err := s.assets.Seek(ctx, repo.ID, lower, inclusive, upper)
		if err != nil {
			return nil, "", err
		}
		if a == nil || !strings.HasPrefix(a.Path, dir) {
			break
		}
		rest := a.Path[len(dir):]
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			name := rest[:i]
			out = append(out, Node{Name: name, Type: "dir", Path: dir + name + "/"})
			lower, _ = assets.PrefixEnd(dir + name + "/")
			inclusive = true
			continue
		}
		n := Node{Name: rest, Type: "file", Path: a.Path, SHA256: a.SHA256}
		size, at := a.Size, a.UpdatedAt
		n.Size, n.UpdatedAt = &size, &at
		if a.UploadedBy != 0 {
			name, ok := users[a.UploadedBy]
			if !ok {
				if name, err = s.auth.Username(ctx, a.UploadedBy); err != nil {
					return nil, "", err
				}
				users[a.UploadedBy] = name
			}
			n.UploadedBy = name
		}
		if n.Provenance, err = builds.Get(ctx, a.PublishSessionID); err != nil {
			return nil, "", err
		}
		out = append(out, n)
		lower, inclusive = a.Path, false
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[limit-1]
		next = last.Name
		if last.Type == "dir" {
			next += "/"
		}
	}
	return out, next, nil
}

// DeletePaths deletes the files of paths in one transaction and returns the paths it deleted. A
// path that does not exist is skipped. The caller checks each path with ValidPath first. It runs
// to the end when the client goes away.
func (s *Service) DeletePaths(ctx context.Context, repo *repos.Repo, paths []string) ([]string, error) {
	paths = slices.Clone(paths)
	slices.Sort(paths)
	return s.deleteBatch(context.WithoutCancel(ctx), repo, slices.Compact(paths))
}

// DeletePrefix deletes every file under folder prefix (a version, a package, any subtree) in
// batches and returns how many it deleted. It runs to the end when the client goes away.
func (s *Service) DeletePrefix(ctx context.Context, repo *repos.Repo, prefix string) (int, error) {
	dir, err := Folder(prefix)
	if err != nil || dir == "" {
		return 0, ErrInvalid
	}
	return s.deleteAll(context.WithoutCancel(ctx), repo, dir)
}

// DeleteRepo deletes every file of repo and then the repo. It locks the repo first, so writes
// answer 409 while it runs. It returns the number of files deleted.
func (s *Service) DeleteRepo(ctx context.Context, repo *repos.Repo) (int, error) {
	ctx = context.WithoutCancel(ctx)
	if err := s.repos.SetLockedBy(ctx, nil, repo.ID, format.RepoDeleting); err != nil {
		return 0, err
	}
	r := *repo
	r.LockedBy = format.RepoDeleting
	n, err := s.deleteAll(ctx, &r, "")
	if err != nil {
		return n, err
	}
	return n, s.repos.DeleteRow(ctx, repo.ID)
}

// deleteAll deletes the files under dir ("" for all) batch by batch.
func (s *Service) deleteAll(ctx context.Context, repo *repos.Repo, dir string) (int, error) {
	total := 0
	for {
		paths, err := s.assets.FirstPaths(ctx, repo.ID, dir, deleteBatch)
		if err != nil || len(paths) == 0 {
			return total, err
		}
		deleted, err := s.deleteBatch(ctx, repo, paths)
		n := len(deleted)
		total += n
		if err != nil {
			return total, err
		}
		if n == 0 {
			return total, nil // every path was gone already; the next read sees the same rows
		}
	}
}

// deleteBatch deletes the rows of paths (sorted, each once) in one transaction, after the
// format's DeleteObserver, and then their blobs. It returns the paths whose rows it deleted. It
// holds the asset lock of each path (the lock uploads take), taken in path order, until the
// blobs are gone.
func (s *Service) deleteBatch(ctx context.Context, repo *repos.Repo, paths []string) ([]string, error) {
	for _, p := range paths {
		unlock := s.assets.Lock(repo.ID, p)
		defer unlock()
	}
	var deleted []string
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		deleted = deleted[:0]
		if o, ok := format.Get(repo.Format).(format.DeleteObserver); ok {
			if err := o.AssetsDeleted(ctx, tx, repo, paths); err != nil {
				return err
			}
		}
		for _, p := range paths {
			ok, err := assets.DeleteTx(ctx, tx, repo.ID, p)
			if err != nil {
				return err
			}
			if ok {
				deleted = append(deleted, p)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, p := range deleted {
		if err := s.assets.DeleteBlob(ctx, repo.Name, p); err != nil {
			s.log.Warn("delete blob after its row", "repo", repo.Name, "path", p, "err", err.Error())
		}
	}
	return deleted, nil
}
