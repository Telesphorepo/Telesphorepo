package assets

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
)

// tree_entries holds the children of each folder of a repo, keyed by repopath.FolderKey or
// repopath.FileKey of their names, so a page of a folder is one range of its primary key. The pipeline writes it in the
// transaction of every asset insert and delete; the repo row's delete takes it with it.

// treeAdd records file p, asset assetID, in tree_entries. A file that has its row (a replace)
// changes nothing; a new one adds its row and counts one more file in each folder on its path.
func treeAdd(ctx context.Context, q *dbq.Queries, repoID, assetID int64, p string) error {
	dir, name := splitPath(p)
	n, err := q.AddTreeFile(ctx, dbq.AddTreeFileParams{RepoID: repoID, Parent: dir, SortKey: repopath.FileKey(name), Name: name,
		AssetID: sql.NullInt64{Int64: assetID, Valid: true}})
	if err != nil || n == 0 {
		return err
	}
	for parent, folder := range foldersOf(dir) {
		if err := q.AddTreeFolderFile(ctx, dbq.AddTreeFolderFileParams{RepoID: repoID, Parent: parent,
			SortKey: repopath.FolderKey(folder), Name: folder}); err != nil {
			return err
		}
	}
	return nil
}

// treeRemove deletes the row of file p from tree_entries and counts one file less in each folder
// on its path; a folder without files goes.
func treeRemove(ctx context.Context, q *dbq.Queries, repoID int64, p string) error {
	dir, name := splitPath(p)
	n, err := q.DeleteTreeFile(ctx, dbq.DeleteTreeFileParams{RepoID: repoID, Parent: dir, SortKey: repopath.FileKey(name)})
	if err != nil || n == 0 {
		return err
	}
	for parent, folder := range foldersOf(dir) {
		key := repopath.FolderKey(folder)
		files, err := q.DropTreeFolderFile(ctx, dbq.DropTreeFolderFileParams{RepoID: repoID, Parent: parent, SortKey: key})
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if files <= 0 {
			if err := q.DeleteTreeFolder(ctx, dbq.DeleteTreeFolderParams{RepoID: repoID, Parent: parent, SortKey: key}); err != nil {
				return err
			}
		}
	}
	return nil
}

// splitPath returns the folder of p ("" or ending in "/") and its name.
func splitPath(p string) (dir, name string) {
	i := strings.LastIndexByte(p, '/')
	return p[:i+1], p[i+1:]
}

// foldersOf yields each folder on the way to dir ("" or ending in "/") as its parent and its
// name: "a/b/" gives ("", "a") and ("a/", "b").
func foldersOf(dir string) func(yield func(parent, name string) bool) {
	return func(yield func(parent, name string) bool) {
		start := 0
		for i := 0; i < len(dir); i++ {
			if dir[i] != '/' {
				continue
			}
			if !yield(dir[:start], dir[start:i]) {
				return
			}
			start = i + 1
		}
	}
}

// TreeFolder is a sub-folder in a page of a folder.
type TreeFolder struct {
	Key  []byte // repopath.FolderKey(Name)
	Name string
}

// TreeFile is a file in a page of a folder.
type TreeFile struct {
	Key   []byte // repopath.FileKey of its name
	Asset *Asset // ID, RepoID, Path, Size, SHA256, UploadedBy, UpdatedAt and BuildID
}

// TreeFolders returns up to limit sub-folders of folder dir ("" or ending in "/") of repo, in key
// order (descending when desc), after the key after (from the first when nil). It is one range of
// the primary key of tree_entries.
func (p *Pipeline) TreeFolders(ctx context.Context, repoID int64, dir string, after []byte, desc bool, limit int) ([]TreeFolder, error) {
	q := dbq.New(p.db.R)
	var out []TreeFolder
	if desc {
		upper := after
		if upper == nil {
			upper = []byte{0xFF} // above every folder key: one starts with a tag
		}
		rows, err := q.ListTreeFoldersDesc(ctx, dbq.ListTreeFoldersDescParams{RepoID: repoID, Parent: dir, Upper: upper, Limit: int64(limit)})
		for _, r := range rows {
			out = append(out, TreeFolder{Key: r.SortKey, Name: r.Name})
		}
		return out, err
	}
	rows, err := q.ListTreeFolders(ctx, dbq.ListTreeFoldersParams{RepoID: repoID, Parent: dir, Lower: lowerAfter(after), Limit: int64(limit)})
	for _, r := range rows {
		out = append(out, TreeFolder{Key: r.SortKey, Name: r.Name})
	}
	return out, err
}

// TreeFiles returns up to limit files of folder dir of repo in key order, after the key after
// (from the first when nil): one range of the primary key of tree_entries and a lookup of each
// file's asset row.
func (p *Pipeline) TreeFiles(ctx context.Context, repoID int64, dir string, after []byte, limit int) ([]TreeFile, error) {
	rows, err := dbq.New(p.db.R).ListTreeFiles(ctx, dbq.ListTreeFilesParams{RepoID: repoID, Parent: dir, Lower: lowerAfter(after), Limit: int64(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]TreeFile, len(rows))
	for i, r := range rows {
		out[i] = TreeFile{Key: r.SortKey, Asset: &Asset{ID: r.ID, RepoID: repoID, Path: r.Path, Size: r.Size, SHA256: r.Sha256,
			UploadedBy: r.UploadedBy.Int64, UpdatedAt: time.Unix(r.UpdatedAt, 0).UTC(), BuildID: r.BuildID.Int64}}
	}
	return out, nil
}

// VersionFolders reports whether folder dir of repo has sub-folders, and whether the name of
// every one starts with a digit (the versions of an artifact). One seek: the versions sort before
// every other folder, so the last folder is a version only when every folder is.
func (p *Pipeline) VersionFolders(ctx context.Context, repoID int64, dir string) (has, digits bool, err error) {
	last, err := p.TreeFolders(ctx, repoID, dir, nil, true, 1)
	if err != nil || len(last) == 0 {
		return false, false, err
	}
	return true, repopath.IsVersionKey(last[0].Key), nil
}

// lowerAfter returns the smallest key after key, the key with a 0 byte appended; the empty key
// (below every key) for nil. Never nil: a NULL bound matches no row.
func lowerAfter(key []byte) []byte {
	if key == nil {
		return []byte{}
	}
	return append(slices.Clip(key), 0x00)
}
