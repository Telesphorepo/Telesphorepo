package npm

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/blobs"
	"github.com/Telesphorepo/Telesphorepo/internal/npm/dbq"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/version"
)

// maxVersions is the most versions of one package that a packument lists.
const maxVersions = 100000

// publishMeta is the Meta of the write of a tarball that adds a version: the rows that
// onCommitted writes in the transaction of the commit.
type publishMeta struct {
	name, version string
	manifest      map[string]json.RawMessage // normalized (normalize)
	tags          []string                   // the dist-tags that name the new version
	integrity     string
	shasum        string
	// latestIfHigher moves latest to the new version when it is a release above the version that
	// latest names (an upload and the demo data; a publish names its tags).
	latestIfHigher bool
}

// lockKey serializes the changes of one package: a publish, a tag change, a deprecation and an
// unpublish.
func lockKey(pkg string) string { return "npm:" + pkg }

// addVersion stores version m of a package with its tarball sp, and the provenance bundle sig
// next to it when there is one, in one commit: the tarball's commit writes the rows. The result
// is the tarball's. A refusal is an *assets.Refusal.
func (h *Handler) addVersion(ctx context.Context, repo *repos.Repo, m *publishMeta, sp *assets.Spooled, sig []byte, o assets.Origin) (*assets.Result, error) {
	if err := checkNewName(m.name); err != nil {
		return nil, assets.Refuse(400, "%s: %v", m.name, err)
	}
	if _, err := version.ParseSemVer(m.version); err != nil {
		return nil, assets.Refuse(400, "%v", err)
	}
	p, err := repopath.Parse(tarballPath(m.name, m.version))
	if err != nil {
		return nil, assets.Refuse(400, "the package name or version is too long")
	}
	if m.integrity, err = integrityOf(sp); err != nil {
		return nil, err
	}
	m.shasum = sp.SHA1
	ws := []assets.Write{{Repo: repo, Path: p, Body: assets.Body{Spool: sp}, Origin: o, Locks: []string{lockKey(m.name)}, Meta: m}}
	if sig != nil {
		sp, _ := repopath.Parse(sigstorePath(m.name, m.version))
		ws = append(ws, assets.Write{Repo: repo, Path: sp, Body: assets.Body{Spool: blobs.Bytes(sig)}, Origin: o, Meta: sidecarMeta{}})
	}
	rs, err := h.assets.CommitAll(ctx, ws)
	if err != nil {
		return nil, err
	}
	return rs[0], nil
}

// onCommitted writes the version of a stored tarball (format.Spec OnCommitted): its row, the
// dist-tags of the publish, latest when no latest is left, and the package's revision.
func (h *Handler) onCommitted(ctx context.Context, tx *sql.Tx, w *assets.Write, a *assets.Asset, _ *assets.Asset) error {
	m, ok := w.Meta.(*publishMeta)
	if !ok {
		return nil
	}
	sv, err := version.ParseSemVer(m.version)
	if err != nil {
		return err
	}
	manifest, err := json.Marshal(m.manifest)
	if err != nil {
		return err
	}
	q := dbq.New(tx)
	deprecated := ""
	if d, ok := m.manifest["deprecated"]; ok {
		json.Unmarshal(d, &deprecated)
	}
	if err := q.InsertVersion(ctx, dbq.InsertVersionParams{RepoID: w.Repo.ID, Name: m.name, Version: m.version, SortKey: sv.SortKey(),
		Manifest: string(manifest), Tarball: a.Path, Integrity: m.integrity, Shasum: m.shasum,
		PublishedAt: a.UpdatedAt.Unix(), Deprecated: deprecated}); err != nil {
		return err
	}
	tags := m.tags
	if m.latestIfHigher && !sv.Prerelease() {
		cur, err := q.ListTags(ctx, dbq.ListTagsParams{RepoID: w.Repo.ID, Name: m.name})
		if err != nil {
			return err
		}
		for _, t := range cur {
			if old, err := version.ParseSemVer(t.Version); t.Tag == "latest" && err == nil && old.Compare(sv) < 0 {
				tags = append(tags, "latest")
			}
		}
	}
	for _, t := range tags {
		if err := q.UpsertTag(ctx, dbq.UpsertTagParams{RepoID: w.Repo.ID, Name: m.name, Tag: t, Version: m.version}); err != nil {
			return err
		}
	}
	return refresh(ctx, q, w.Repo.ID, m.name, a.UpdatedAt)
}

// onRemoved removes the version of each removed tarball (format.Spec OnRemoved), then fixes the
// tags and the revision of its package; a package without versions goes. The provenance bundle
// of a removed version follows it.
func (h *Handler) onRemoved(ctx context.Context, tx *sql.Tx, repo *repos.Repo, removed []*assets.Asset, _ []repopath.Path) ([]repopath.Path, error) {
	q := dbq.New(tx)
	var names []string
	var follow []repopath.Path
	for _, a := range removed {
		if !strings.HasSuffix(a.Path, ".tgz") {
			continue
		}
		v, err := q.GetVersionOfTarball(ctx, dbq.GetVersionOfTarballParams{RepoID: repo.ID, Tarball: a.Path})
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if err := q.DeleteVersion(ctx, dbq.DeleteVersionParams{RepoID: repo.ID, Name: v.Name, Version: v.Version}); err != nil {
			return nil, err
		}
		if !slices.Contains(names, v.Name) {
			names = append(names, v.Name)
		}
		if p, err := repopath.Parse(sigstorePath(v.Name, v.Version)); err == nil {
			follow = append(follow, p)
		}
	}
	now := time.Now()
	for _, n := range names {
		if err := refresh(ctx, q, repo.ID, n, now); err != nil {
			return nil, err
		}
	}
	return follow, nil
}

// refresh brings the package row of name in line with its versions after a change at time at:
// tags that name a missing version go, latest names the highest release (the highest version
// when every version is a prerelease) when no latest is left, and the revision moves on. A
// package without versions loses its row and its tags.
func refresh(ctx context.Context, q *dbq.Queries, repoID int64, name string, at time.Time) error {
	vs, err := q.ListVersionStates(ctx, dbq.ListVersionStatesParams{RepoID: repoID, Name: name, Limit: maxVersions})
	if err != nil {
		return err
	}
	if len(vs) == 0 {
		if err := q.DeleteTagsOfPackage(ctx, dbq.DeleteTagsOfPackageParams{RepoID: repoID, Name: name}); err != nil {
			return err
		}
		return q.DeletePackage(ctx, dbq.DeletePackageParams{RepoID: repoID, Name: name})
	}
	have := map[string]bool{}
	created := vs[0].PublishedAt
	for _, v := range vs {
		have[v.Version] = true
		created = min(created, v.PublishedAt)
	}
	tags, err := q.ListTags(ctx, dbq.ListTagsParams{RepoID: repoID, Name: name})
	if err != nil {
		return err
	}
	latest := false
	kept := tags[:0]
	for _, t := range tags {
		if !have[t.Version] {
			if err := q.DeleteTag(ctx, dbq.DeleteTagParams{RepoID: repoID, Name: name, Tag: t.Tag}); err != nil {
				return err
			}
			continue
		}
		latest = latest || t.Tag == "latest"
		kept = append(kept, t)
	}
	if !latest {
		v := highest(vs)
		if err := q.UpsertTag(ctx, dbq.UpsertTagParams{RepoID: repoID, Name: name, Tag: "latest", Version: v}); err != nil {
			return err
		}
		kept = append(kept, dbq.ListTagsRow{Tag: "latest", Version: v})
		slices.SortFunc(kept, func(a, b dbq.ListTagsRow) int { return strings.Compare(a.Tag, b.Tag) })
	}
	sum := sha256.New()
	for _, v := range vs {
		fmt.Fprintf(sum, "v\x00%s\x00%s\x00", v.Version, v.Deprecated)
	}
	for _, t := range kept {
		fmt.Fprintf(sum, "t\x00%s\x00%s\x00", t.Tag, t.Version)
	}
	n := int64(0)
	if p, err := q.GetPackage(ctx, dbq.GetPackageParams{RepoID: repoID, Name: name}); err == nil {
		head, _, _ := strings.Cut(p.Rev, "-")
		n, _ = strconv.ParseInt(head, 10, 64)
		created = min(created, p.Created)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	rev := strconv.FormatInt(n+1, 10) + "-" + hex.EncodeToString(sum.Sum(nil))[:32]
	return q.UpsertPackage(ctx, dbq.UpsertPackageParams{RepoID: repoID, Name: name, Created: created, Modified: at.Unix(), Rev: rev})
}

// highest returns the highest release of vs (in SemVer order), or the highest version when every
// version is a prerelease.
func highest(vs []dbq.ListVersionStatesRow) string {
	for i := len(vs) - 1; i >= 0; i-- {
		if sv, err := version.ParseSemVer(vs[i].Version); err == nil && !sv.Prerelease() {
			return vs[i].Version
		}
	}
	return vs[len(vs)-1].Version
}

// change runs fn in a write transaction under the lock of package name, then refreshes the
// package when fn reports a change.
func (h *Handler) change(ctx context.Context, repo *repos.Repo, name string, fn func(q *dbq.Queries) (bool, error)) error {
	unlock := h.assets.Lock(repo.ID, lockKey(name))
	defer unlock()
	return h.db.Tx(ctx, func(tx *sql.Tx) error {
		q := dbq.New(tx)
		changed, err := fn(q)
		if err != nil || !changed {
			return err
		}
		return refresh(ctx, q, repo.ID, name, time.Now())
	})
}
