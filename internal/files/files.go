// Package files browses the files of a repo for the JSON API: the folder tree and the details of
// one file. Writes and deletes go through the asset pipeline (internal/assets).
package files

import (
	"context"
	"errors"
	"path"
	"slices"
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
	// Repo is the member that serves the file, in the listing of a group; absent elsewhere and
	// on a folder.
	Repo string `json:"repo,omitempty"`
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
	Repo        string    `json:"repo,omitempty"` // a group: the member that serves the file
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
		next = cursorOf(out[limit-1])
	}
	return out, next, nil
}

// cursorOf returns the cursor that a page ending with n hands on: its name, with "/" for a folder.
func cursorOf(n Node) string {
	if n.Type == "dir" {
		return n.Name + "/"
	}
	return n.Name
}

// GroupTree is Tree for a group: the merged children of folder dir in the members the reader
// may read, in read order. Each member is read with the same cursor and limit, and the pages are
// merged by path. A folder that several members hold is one entry; a file that several members
// hold comes from the first of them, the one the group serves, and names it in Repo. The first
// limit entries of the merge are exactly the first limit of the union: a member holds every one
// of its entries below the limit-th in its own page. next is the last path returned while any
// entry follows it in the union, "" at the end. A page costs at most limit+1 seeks per member.
func (s *Service) GroupTree(ctx context.Context, members []*repos.Repo, dir, after string, limit int) ([]Node, string, error) {
	byPath := map[string]int{}
	var out []Node
	more := false
	for _, m := range members {
		page, next, err := s.Tree(ctx, m, dir, after, limit)
		if err != nil {
			return nil, "", err
		}
		more = more || next != ""
		for _, n := range page {
			if _, seen := byPath[n.Path]; seen {
				continue // an earlier member holds the folder, or serves the file
			}
			if n.Type == "file" {
				n.Repo = m.Name
			}
			byPath[n.Path] = len(out)
			out = append(out, n)
		}
	}
	// Every path shares the prefix dir, so path order is the order of Tree.
	slices.SortFunc(out, func(a, b Node) int { return strings.Compare(a.Path, b.Path) })
	if len(out) > limit {
		out, more = out[:limit], true
	}
	next := ""
	if more && len(out) > 0 {
		next = cursorOf(out[len(out)-1])
	}
	return out, next, nil
}
