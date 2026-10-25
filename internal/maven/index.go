package maven

import (
	"bytes"
	"context"
	"database/sql"
	"path"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/maven/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// The index of versions and builds (maven_versions, maven_builds). The hooks of the format keep it
// in the transaction of every write and remove: a file of a build records its build and its
// version; the remove of the last file of a build deletes the build, and of the last build its
// version. The sort keys make a page of versions or builds, newest first, one seek past its cursor.

// Version key tokens, in byte order. A version ends in keyEnd; a part that equals the end (a zero,
// or ga, final, release) sorts below it when the next part that does not sorts below it (a
// pre-release qualifier), else above it.
const (
	keyNullLow  = 0x06 // ga, final or release before a pre-release
	keyZeroLow  = 0x07 // a zero before a pre-release
	keyEnd      = 0x08
	keyNullHigh = 0x09 // ga, final or release before a number or a later qualifier
	keySP       = 0x0A
	keyQual     = 0x0B // an unknown qualifier, then its bytes and 0x00
	keyZeroHigh = 0x0C // a zero before a number or a later qualifier
	keyNum      = 0x0D // a number, then its length (two bytes) and its digits
)

// keyTop sorts after every version key and build key: the cursor of a first page.
var keyTop = []byte{0xFF}

// nullItem reports whether a version part compares equal to a missing part: zero, or a qualifier
// that ranks as the release.
func nullItem(it versionItem) bool {
	if it.isNum {
		return it.num == ""
	}
	return qualifierRank(it.qual) == qualifierRank("")
}

// versionKey is the sort key of version v: byte order is version order, oldest first. The parts
// are those of splitVersion without trailing zeros and release qualifiers ("1.0.Final" is "1"):
// the pre-release qualifiers (alpha, beta, milestone, rc, snapshot) sort before the end of a
// version, sp and unknown qualifiers (alphabetically) after it, and numbers (numerically) after
// every qualifier. Versions equal in those parts ("1.0" and "1") sort by their bytes.
func versionKey(v string) []byte {
	items := splitVersion(v)
	for len(items) > 0 && nullItem(items[len(items)-1]) {
		items = items[:len(items)-1]
	}
	b := make([]byte, 0, 2*len(v)+8)
	for i, it := range items {
		switch {
		case nullItem(it):
			high := true // the next part that is not null sorts above the end
			for _, nx := range items[i+1:] {
				if !nullItem(nx) {
					high = nx.isNum || qualifierRank(nx.qual) > qualifierRank("")
					break
				}
			}
			switch {
			case it.isNum && high:
				b = append(b, keyZeroHigh)
			case it.isNum:
				b = append(b, keyZeroLow)
			case high:
				b = append(b, keyNullHigh)
			default:
				b = append(b, keyNullLow)
			}
		case it.isNum:
			b = append(b, keyNum, byte(len(it.num)>>8), byte(len(it.num)))
			b = append(b, it.num...)
		default:
			switch r := qualifierRank(it.qual); {
			case r < qualifierRank(""):
				b = append(b, byte(r)) // 0x01 alpha to 0x05 snapshot
			case r == qualifierRank("sp"):
				b = append(b, keySP)
			default:
				b = append(b, keyQual)
				b = append(b, it.qual...)
				b = append(b, 0)
			}
		}
	}
	b = append(b, keyEnd)
	return append(b, v...)
}

// compareVersions orders Maven versions as versionKey does.
func compareVersions(a, b string) int {
	if a == b {
		return 0
	}
	return bytes.Compare(versionKey(a), versionKey(b))
}

// buildKey is the sort key of build id: byte order is build order, oldest first. The build without
// a timestamp comes first, then the timestamped builds by timestamp and number; a release build
// (its version) after them.
func buildKey(id string) []byte {
	if id == nonUniqueName {
		return []byte{0}
	}
	if m := buildRE.FindStringSubmatch(id); m != nil && m[2] == "" {
		ts, n, _ := strings.Cut(id, "-")
		n = strings.TrimLeft(n, "0")
		b := append([]byte{1}, ts...)
		b = append(b, byte(len(n)>>8), byte(len(n)))
		b = append(b, n...)
		b = append(b, 0)
		return append(b, id...)
	}
	return append([]byte{2}, id...)
}

// compareBuilds orders the build ids of a version as buildKey does.
func compareBuilds(a, b string) int {
	if a == b {
		return 0
	}
	return bytes.Compare(buildKey(a), buildKey(b))
}

// fileRef is what the path of a build's file names.
type fileRef struct {
	art, dir        string // the artifact folder and the version folder, each ending in "/"
	id, version     string // the artifactId and the version (the version folder's name)
	build           string // the build id (see nameBuild)
	classifier, ext string
}

// parseBuildFile reads the path of a file of a build: <group path>/<artifactId>/<version>/<name>,
// with a name that nameBuild reads. ok is false for any other path.
func parseBuildFile(p string) (fileRef, bool) {
	dir, name := path.Split(p)
	if name == "" || sideFile(name) {
		return fileRef{}, false
	}
	art, v := path.Split(strings.TrimSuffix(dir, "/"))
	if v == "" || !strings.Contains(strings.TrimSuffix(art, "/"), "/") {
		return fileRef{}, false // a version folder needs an artifact folder of two segments or more
	}
	id := path.Base(art)
	build, classifier, ext, ok := nameBuild(name, id, v)
	if !ok {
		return fileRef{}, false
	}
	return fileRef{art: art, dir: dir, id: id, version: v, build: build, classifier: classifier, ext: ext}, true
}

// nameBuild reads the name of a file in the folder of version v of artifact id: the build it
// belongs to, its classifier and its extension. A release has one build, named v:
// <id>-<v>[-<classifier>].<ext>. A -SNAPSHOT has a build per timestamp,
// <id>-<base>-<yyyyMMdd.HHmmss>-<n>[-<classifier>].<ext>, and the build "SNAPSHOT" of the files
// named like a release.
func nameBuild(name, id, v string) (build, classifier, ext string, ok bool) {
	snapshot := strings.HasSuffix(v, "-SNAPSHOT")
	plain := id + "-" + v
	stamped := id + "-" + strings.TrimSuffix(v, "-SNAPSHOT") + "-"
	var rest string
	switch {
	case strings.HasPrefix(name, plain) && (len(name) == len(plain) || name[len(plain)] == '.' || name[len(plain)] == '-'):
		build, rest = v, name[len(plain):]
		if snapshot {
			build = nonUniqueName
		}
	case snapshot && strings.HasPrefix(name, stamped):
		m := buildRE.FindStringSubmatch(name[len(stamped):])
		if m == nil {
			return "", "", "", false
		}
		build, rest = m[1], m[2]
	default:
		return "", "", "", false
	}
	classifier, ext, ok = tail(rest)
	return build, classifier, ext, ok
}

// buildStem is the start of the name of every file of build b of version v of artifact id.
func buildStem(id, v, b string) string {
	if strings.HasSuffix(v, "-SNAPSHOT") && b != nonUniqueName {
		return id + "-" + strings.TrimSuffix(v, "-SNAPSHOT") + "-" + b
	}
	return id + "-" + v
}

func snapshotFlag(v string) int64 {
	if strings.HasSuffix(v, "-SNAPSHOT") {
		return 1
	}
	return 0
}

// addBuild records the build of f and, when the build is new, counts it in its version.
func addBuild(ctx context.Context, q *dbq.Queries, repoID int64, f fileRef) error {
	n, err := q.InsertBuild(ctx, dbq.InsertBuildParams{RepoID: repoID, Folder: f.dir, SortKey: buildKey(f.build), Build: f.build})
	if err != nil || n == 0 {
		return err
	}
	return q.AddVersionBuild(ctx, dbq.AddVersionBuildParams{RepoID: repoID, Folder: f.art, SortKey: versionKey(f.version),
		Version: f.version, Snapshot: snapshotFlag(f.version)})
}

// onCommitted records the build of a stored file (format.Spec OnCommitted).
func (h *Handler) onCommitted(ctx context.Context, tx *sql.Tx, w *assets.Write, a *assets.Asset, _ *assets.Asset) error {
	f, ok := parseBuildFile(a.Path)
	if !ok {
		return nil
	}
	return addBuild(ctx, dbq.New(tx), w.Repo.ID, f)
}

// rangePage is how many paths one read of the files of a build takes.
const rangePage = 200

// onRemoved deletes the builds that lose their last file in a remove batch, and the versions
// that lose their last build (format.Spec OnRemoved). A build is checked once per batch: one
// range read of the files named like it.
func (h *Handler) onRemoved(ctx context.Context, tx *sql.Tx, repo *repos.Repo, removed []*assets.Asset, _ []repopath.Path) ([]repopath.Path, error) {
	gone := make(map[string]bool, len(removed))
	for _, a := range removed {
		gone[a.Path] = true
	}
	q := dbq.New(tx)
	checked := map[string]bool{}
	for _, a := range removed {
		f, ok := parseBuildFile(a.Path)
		if !ok || checked[f.dir+"\x00"+f.build] {
			continue
		}
		checked[f.dir+"\x00"+f.build] = true
		left, err := buildHasFile(ctx, q, repo.ID, f, gone)
		if err != nil {
			return nil, err
		}
		if left {
			continue
		}
		n, err := q.DeleteBuild(ctx, dbq.DeleteBuildParams{RepoID: repo.ID, Folder: f.dir, SortKey: buildKey(f.build)})
		if err != nil {
			return nil, err
		}
		if n == 0 {
			continue
		}
		vk := versionKey(f.version)
		if err := q.DropVersionBuild(ctx, dbq.DropVersionBuildParams{RepoID: repo.ID, Folder: f.art, SortKey: vk}); err != nil {
			return nil, err
		}
		if err := q.DeleteEmptyVersion(ctx, dbq.DeleteEmptyVersionParams{RepoID: repo.ID, Folder: f.art, SortKey: vk}); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

// buildHasFile reports whether the build of f has a file that is not in gone.
func buildHasFile(ctx context.Context, q *dbq.Queries, repoID int64, f fileRef, gone map[string]bool) (bool, error) {
	lower := f.dir + buildStem(f.id, f.version, f.build)
	upper, _ := assets.PrefixEnd(lower)
	for {
		ps, err := q.ListPathsInRange(ctx, dbq.ListPathsInRangeParams{RepoID: repoID, Lower: lower, Upper: upper, Limit: rangePage})
		if err != nil {
			return false, err
		}
		for _, p := range ps {
			if gone[p] {
				continue
			}
			if g, ok := parseBuildFile(p); ok && g.dir == f.dir && g.build == f.build {
				return true, nil
			}
		}
		if len(ps) < rangePage {
			return false, nil
		}
		lower = ps[len(ps)-1] + "\x00"
	}
}

// indexBatch is how many paths one transaction of indexExisting reads.
const indexBatch = 2000

// indexExisting indexes the files that the Maven repos hold while maven_index_pending has its
// row (the database had files before the index), batch by batch, then deletes the row.
func (h *Handler) indexExisting(ctx context.Context) error {
	n, err := dbq.New(h.host.DB.R).IndexPending(ctx)
	if err != nil || n == 0 {
		return err
	}
	all, err := h.host.Repos.List(ctx)
	if err != nil {
		return err
	}
	for _, repo := range all {
		if repo.Format != "maven" || repo.Kind == repos.KindGroup {
			continue
		}
		for after := ""; ; {
			next := ""
			err := h.host.DB.Tx(ctx, func(tx *sql.Tx) error {
				q := dbq.New(tx)
				ps, err := q.ListPathsAfter(ctx, dbq.ListPathsAfterParams{RepoID: repo.ID, Path: after, Limit: indexBatch})
				if err != nil {
					return err
				}
				for _, p := range ps {
					if f, ok := parseBuildFile(p); ok {
						if err := addBuild(ctx, q, repo.ID, f); err != nil {
							return err
						}
					}
				}
				if len(ps) == indexBatch {
					next = ps[len(ps)-1]
				}
				return nil
			})
			if err != nil {
				return err
			}
			if next == "" {
				break
			}
			after = next
		}
	}
	return dbq.New(h.host.DB.W).IndexDone(ctx)
}

// purge is the repos.Purge of the module: it deletes one batch of the rows of a repo.
func purge(q *dbq.Queries) repos.Purge {
	return func(ctx context.Context, repoID int64) (int64, error) {
		n, err := q.PurgeMavenBuilds(ctx, dbq.PurgeMavenBuildsParams{RepoID: repoID, Limit: repos.PurgeBatch})
		if err != nil || n > 0 {
			return n, err
		}
		return q.PurgeMavenVersions(ctx, dbq.PurgeMavenVersionsParams{RepoID: repoID, Limit: repos.PurgeBatch})
	}
}
