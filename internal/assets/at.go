package assets

import (
	"context"
	"encoding/json"

	"github.com/Telesphorepo/Telesphorepo/internal/db/dbq"
)

// The point lookups of a list: the assets at a path in each member of a group, the files of a
// batch of writes or deletes, the picked files and folders of a group delete. Each reads every
// pair of repo and path in one query (a seek of UNIQUE (repo_id, path) per pair), where Get would
// cost one query per pair.

// jsonText is v as JSON text, the argument of a json_each query.
func jsonText(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

// getIn returns the assets at paths in the repos of repoIDs that exist, by repo id and path,
// reading in q.
func getIn(ctx context.Context, q *dbq.Queries, repoIDs []int64, paths []string) (map[int64]map[string]*Asset, error) {
	out := map[int64]map[string]*Asset{}
	if len(repoIDs) == 0 || len(paths) == 0 {
		return out, nil
	}
	repos, err := jsonText(repoIDs)
	if err != nil {
		return nil, err
	}
	ps, err := jsonText(paths)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListAssetsAt(ctx, dbq.ListAssetsAtParams{Repos: repos, Paths: ps})
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if out[r.RepoID] == nil {
			out[r.RepoID] = map[string]*Asset{}
		}
		out[r.RepoID][r.Path] = fromRow(r)
	}
	return out, nil
}

// getAll returns the assets of repo repoID at paths that exist, by path, reading in q: one query.
func getAll(ctx context.Context, q *dbq.Queries, repoID int64, paths []string) (map[string]*Asset, error) {
	in, err := getIn(ctx, q, []int64{repoID}, paths)
	if err != nil {
		return nil, err
	}
	if in[repoID] == nil {
		return map[string]*Asset{}, nil
	}
	return in[repoID], nil
}

// GetAt returns the asset at path in each repo of repoIDs that stores it, by repo id: one query.
func (p *Pipeline) GetAt(ctx context.Context, repoIDs []int64, path string) (map[int64]*Asset, error) {
	in, err := getIn(ctx, dbq.New(p.db.R), repoIDs, []string{path})
	if err != nil {
		return nil, err
	}
	out := make(map[int64]*Asset, len(in))
	for id, m := range in {
		out[id] = m[path]
	}
	return out, nil
}

// Held returns, for each path of paths, the repos of repoIDs that store it, and for each folder of
// folders (ending in "/", never the top), the repos that store a file under it: two queries, a seek
// per repo and item.
func (p *Pipeline) Held(ctx context.Context, repoIDs []int64, paths, folders []string) (files, dirs map[string]map[int64]bool, err error) {
	q := dbq.New(p.db.R)
	files, dirs = map[string]map[int64]bool{}, map[string]map[int64]bool{}
	in, err := getIn(ctx, q, repoIDs, paths)
	if err != nil {
		return nil, nil, err
	}
	for id, m := range in {
		for path := range m {
			if files[path] == nil {
				files[path] = map[int64]bool{}
			}
			files[path][id] = true
		}
	}
	if len(repoIDs) == 0 || len(folders) == 0 {
		return files, dirs, nil
	}
	bounds := make([][2]string, len(folders))
	for i, f := range folders {
		end, ok := PrefixEnd(f)
		if !ok {
			end = "\U0010FFFF" // the top folder: a JSON text holds no byte 0xFF, so the last rune
		}
		bounds[i] = [2]string{f, end}
	}
	repos, err := jsonText(repoIDs)
	if err != nil {
		return nil, nil, err
	}
	fs, err := jsonText(bounds)
	if err != nil {
		return nil, nil, err
	}
	rows, err := q.ListFoldersHeld(ctx, dbq.ListFoldersHeldParams{Repos: repos, Folders: fs})
	if err != nil {
		return nil, nil, err
	}
	for _, r := range rows {
		if dirs[r.Folder] == nil {
			dirs[r.Folder] = map[int64]bool{}
		}
		dirs[r.Folder][r.RepoID] = true
	}
	return files, dirs, nil
}
