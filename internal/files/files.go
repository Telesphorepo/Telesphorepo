// Package files browses the files of a repo for the JSON API: the folder tree and the details of
// one file. Writes and deletes go through the asset pipeline (internal/assets).
package files

import (
	"bytes"
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
	// SideOf is the name of the file that this checksum or signature belongs to ("a.jar" for
	// a.jar.sha1), when that file is in the same folder; absent elsewhere.
	SideOf string `json:"side_of,omitempty"`

	key []byte // repopath.NameKey of Name: the order of a group's merge
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
	UploadedBy  string    `json:"uploaded_by"`    // username; "" when unknown or imported
	Build       *ci.Build `json:"build"`          // the CI build record; null when none
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

// cursor is the position after which a page of a folder starts: a child's kind and key. The
// zero cursor is the start of the folder.
type cursor struct {
	file bool
	key  []byte // nil at the start
}

// parseCursor reads the cursor after: a child's name, with "/" for a folder; "" for the start.
func parseCursor(after string) (cursor, error) {
	if after == "" {
		return cursor{}, nil
	}
	name, folder := strings.CutSuffix(after, "/")
	if _, err := repopath.Parse(name); err != nil || strings.Contains(name, "/") {
		return cursor{}, ErrInvalid
	}
	return cursor{file: !folder, key: repopath.NameKey(name)}, nil
}

// cursorOf returns the cursor that a page ending with n hands on: its name, with "/" for a folder.
func cursorOf(n Node) string {
	if n.Type == "dir" {
		return n.Name + "/"
	}
	return n.Name
}

// order is the direction of each kind of children of a folder: newest first when the name of
// every child of the kind starts with a digit.
type order struct{ foldersDesc, filesDesc bool }

// versionNames reads which kinds of children of folder dir of repo have names that all start
// with a digit; has reports whether the folder holds a child of each kind. Two seeks.
func (s *Service) versionNames(ctx context.Context, repoID int64, dir string) (hasFolders, foldersDigits, hasFiles, filesDigits bool, err error) {
	if hasFolders, foldersDigits, err = s.assets.VersionNames(ctx, repoID, dir, false); err != nil {
		return
	}
	hasFiles, filesDigits, err = s.assets.VersionNames(ctx, repoID, dir, true)
	return
}

// Tree returns up to limit direct children of folder dir ("" or "a/b/") after the cursor after
// (a child name; a folder's name ends in "/"): the folders, then the files, each kind in the
// order of repopath.NameKey (versions first, "1.2" before "1.10", then the other names by their
// bytes). When the name of every folder starts with a digit, the folders come newest first, and
// the same for the files. A checksum or signature whose file is in the folder names it in
// SideOf. A page is two seeks for the order, one range of the index per kind and one seek per
// checksum or signature on it, however large the folder is. next is the cursor of the next page,
// "" at the end.
func (s *Service) Tree(ctx context.Context, repo *repos.Repo, dir, after string, limit int) ([]Node, string, error) {
	c, err := parseCursor(after)
	if err != nil {
		return nil, "", err
	}
	hasFolders, foldersDigits, hasFiles, filesDigits, err := s.versionNames(ctx, repo.ID, dir)
	if err != nil {
		return nil, "", err
	}
	o := order{foldersDesc: hasFolders && foldersDigits, filesDesc: hasFiles && filesDigits}
	out, err := s.page(ctx, repo, dir, c, o, limit+1)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = cursorOf(out[limit-1])
	}
	if err := s.markSides(ctx, []*repos.Repo{repo}, dir, out); err != nil {
		return nil, "", err
	}
	return out, next, nil
}

// sideExts are the extensions of a checksum or signature of another file.
var sideExts = []string{".md5", ".sha1", ".sha256", ".sha512", ".asc", ".sig"}

// sideBase returns the name of the file that checksum or signature name belongs to
// ("a.jar.sha1" gives "a.jar"); "" for any other name.
func sideBase(name string) string {
	for _, ext := range sideExts {
		if base, ok := strings.CutSuffix(name, ext); ok && base != "" {
			return base
		}
	}
	return ""
}

// markSides sets SideOf of each checksum or signature among nodes whose file is in folder dir of
// one of repos: one seek per such file and repo, until one holds it.
func (s *Service) markSides(ctx context.Context, repos []*repos.Repo, dir string, nodes []Node) error {
	for i := range nodes {
		n := &nodes[i]
		base := ""
		if n.Type == "file" {
			base = sideBase(n.Name)
		}
		if base == "" {
			continue
		}
		for _, r := range repos {
			ok, err := s.assets.HasTreeFile(ctx, r.ID, dir, base)
			if err != nil {
				return err
			}
			if ok {
				n.SideOf = base
				break
			}
		}
	}
	return nil
}

// page returns up to n children of folder dir of repo after c: the folders, then the files, each
// in the direction of o.
func (s *Service) page(ctx context.Context, repo *repos.Repo, dir string, c cursor, o order, n int) ([]Node, error) {
	var out []Node
	fileAfter := c.key
	if !c.file {
		folders, err := s.assets.TreeFolders(ctx, repo.ID, dir, c.key, o.foldersDesc, n)
		if err != nil {
			return nil, err
		}
		for _, f := range folders {
			out = append(out, Node{Name: f.Name, Type: "dir", Path: dir + f.Name + "/", key: f.Key})
		}
		fileAfter = nil
	}
	if len(out) >= n {
		return out, nil
	}
	files, err := s.assets.TreeFiles(ctx, repo.ID, dir, fileAfter, o.filesDesc, n-len(out))
	if err != nil {
		return nil, err
	}
	users := map[int64]string{}
	builds := s.builds.Loader()
	for _, f := range files {
		a := f.Asset
		node := Node{Name: a.Path[len(dir):], Type: "file", Path: a.Path, SHA256: a.SHA256, key: f.Key}
		size, at := a.Size, a.UpdatedAt
		node.Size, node.UpdatedAt = &size, &at
		if a.UploadedBy != 0 {
			name, ok := users[a.UploadedBy]
			if !ok {
				if name, err = s.auth.Username(ctx, a.UploadedBy); err != nil {
					return nil, err
				}
				users[a.UploadedBy] = name
			}
			node.UploadedBy = name
		}
		if node.Build, err = builds.Get(ctx, a.BuildID); err != nil {
			return nil, err
		}
		out = append(out, node)
	}
	return out, nil
}

// GroupTree is Tree for a group: the merged children of folder dir in the members the reader
// may read, in read order. Each kind comes newest first when the name of every child of that
// kind in every member starts with a digit (two seeks per member). Each member is read from the
// same cursor, limit+1 children, and the pages merge in the order of Tree. A folder that several
// members hold is one entry; a file that several members hold comes from the first of them, the
// one the group serves, and names it in Repo. A checksum or signature names its file in SideOf
// when any member holds that file. The first limit entries of the merge are exactly the first
// limit of the union: a member holds every one of its entries below the limit-th in its own
// page. next is the cursor of the last entry while any entry follows it in the union, "" at the
// end.
func (s *Service) GroupTree(ctx context.Context, members []*repos.Repo, dir, after string, limit int) ([]Node, string, error) {
	c, err := parseCursor(after)
	if err != nil {
		return nil, "", err
	}
	anyFolder, foldersDigits, anyFile, filesDigits := false, true, false, true
	for _, m := range members {
		hasFolders, fd, hasFiles, fi, err := s.versionNames(ctx, m.ID, dir)
		if err != nil {
			return nil, "", err
		}
		anyFolder = anyFolder || hasFolders
		foldersDigits = foldersDigits && (!hasFolders || fd)
		anyFile = anyFile || hasFiles
		filesDigits = filesDigits && (!hasFiles || fi)
	}
	o := order{foldersDesc: anyFolder && foldersDigits, filesDesc: anyFile && filesDigits}
	var all []Node
	for _, m := range members {
		page, err := s.page(ctx, m, dir, c, o, limit+1)
		if err != nil {
			return nil, "", err
		}
		for _, n := range page {
			if n.Type == "file" {
				n.Repo = m.Name
			}
			all = append(all, n)
		}
	}
	// A stable sort keeps the members' read order among the copies of one path, and the first
	// copy is the one the group serves.
	slices.SortStableFunc(all, func(a, b Node) int { return compareNodes(a, b, o) })
	out := slices.CompactFunc(all, func(a, b Node) bool { return a.Type == b.Type && bytes.Equal(a.key, b.key) })
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = cursorOf(out[limit-1])
	}
	if err := s.markSides(ctx, members, dir, out); err != nil {
		return nil, "", err
	}
	return out, next, nil
}

// compareNodes orders two children of one folder as Tree lists them: folders, then files, each
// kind in the direction of o.
func compareNodes(a, b Node, o order) int {
	if a.Type != b.Type {
		if a.Type == "dir" {
			return -1
		}
		return 1
	}
	if desc := (a.Type == "dir" && o.foldersDesc) || (a.Type == "file" && o.filesDesc); desc {
		return bytes.Compare(b.key, a.key)
	}
	return bytes.Compare(a.key, b.key)
}
