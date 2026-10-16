// Package files browses the files of a repo for the JSON API: the folder tree and the details of
// one file. Writes and deletes go through the asset pipeline (internal/assets).
package files

import (
	"context"
	"errors"
	"path"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/auth"
	"github.com/Telesphorepo/Telesphorepo/internal/ci"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

var (
	// ErrInvalid is a path, prefix or cursor that is not a valid repo path.
	ErrInvalid = errors.New("invalid path")
	// ErrNotFound is a file that does not exist.
	ErrNotFound = errors.New("file not found")
)

// Service browses repo files.
type Service struct {
	assets *assets.Pipeline
	auth   *auth.Service
	builds *ci.Builds
}

func New(as *assets.Pipeline, a *auth.Service, b *ci.Builds) *Service {
	return &Service{assets: as, auth: a, builds: b}
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
	// Build is the CI build record that published the file; absent when none.
	Build *ci.Build `json:"build,omitempty"`
}

// File is one file with its details (GET /api/repos/{name}/file).
type File struct {
	Name        string    `json:"name"`
	Path        string    `json:"path"`
	Size        int64     `json:"size"`
	SHA256      string    `json:"sha256"`
	ContentType string    `json:"content_type"`
	Kind        string    `json:"kind"` // "artifact", "metadata" or "index"
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	UploadedBy  string    `json:"uploaded_by"` // username; "" when unknown or imported
	Build       *ci.Build `json:"build"`       // the CI build record; null when none
}

// Details returns file p of repo. ErrInvalid for an invalid path, ErrNotFound when it does not
// exist.
func (s *Service) Details(ctx context.Context, repo *repos.Repo, p string) (*File, error) {
	if _, err := repopath.Parse(p); err != nil {
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
	if f.Build, err = s.builds.Loader().Get(ctx, a.BuildID); err != nil {
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
		if _, err := repopath.Parse(name); err != nil || strings.Contains(name, "/") {
			return nil, "", ErrInvalid
		}
		if strings.HasSuffix(after, "/") {
			lower, _ = assets.PrefixEnd(dir + name + "/")
		} else {
			lower, inclusive = dir+name, false
		}
	}
	users := map[int64]string{}
	builds := s.builds.Loader()
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
		if n.Build, err = builds.Get(ctx, a.BuildID); err != nil {
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
