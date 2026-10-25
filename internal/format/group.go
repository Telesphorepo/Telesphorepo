package format

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// The versions, builds and artifact snippets of a group (Spec.GroupVersions): the core answers
// them from the members that the reader reads, in read order, so the Files tab of a group is
// the Files tab of a hosted repo of its format.

// Group version modes (Spec.GroupVersions).
const (
	GroupEach  = "each"
	GroupOwner = "owner"
)

// ErrCursor is a cursor of a group's versions that this server did not hand out.
var ErrCursor = errors.New("invalid cursor")

// groupCursor is the cursor of a page of a group's versions: per member (by repo id) the cursor
// of its own Versions (the last version the merge took from it, also of a member that has no
// more), and the members that have no more versions.
type groupCursor struct {
	After map[string]string `json:"a,omitempty"`
	Done  []int64           `json:"d,omitempty"`
}

func decodeCursor(s string) (*groupCursor, error) {
	c := &groupCursor{}
	if s == "" {
		return c, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || json.Unmarshal(b, c) != nil {
		return nil, ErrCursor
	}
	return c, nil
}

func (c *groupCursor) done(id int64) bool {
	for _, d := range c.Done {
		if d == id {
			return true
		}
	}
	return false
}

func (c *groupCursor) encode() string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

// VersionsOf answers Versions for repo. members are the members of a group repo that the reader
// reads, in read order (nil for another kind). A group with GroupOwner lists the versions of the
// first member that has the artifact; with GroupEach it merges the pages of its members in
// VersionOrder, each version from the first member that has it. Each version of a group names
// its member in Repo. ErrCursor for a group cursor that does not decode.
func (s *Spec) VersionsOf(ctx context.Context, repo *repos.Repo, members []*repos.Repo, p, after string, limit int) (*Artifact, []Version, string, error) {
	if s.Versions == nil {
		return nil, nil, "", nil
	}
	if repo.Kind != repos.KindGroup {
		return s.Versions(ctx, repo, repo, p, after, limit)
	}
	if s.GroupVersions == GroupOwner {
		m, err := s.owner(ctx, repo, members, p)
		if err != nil || m == nil {
			return nil, nil, "", err
		}
		art, items, next, err := s.Versions(ctx, repo, m, p, after, limit)
		for i := range items {
			items[i].Repo = m.Name
		}
		return art, items, next, err
	}
	return s.mergeVersions(ctx, repo, members, p, after, limit)
}

// owner is the first member that has the artifact at p, or nil, as assets.Reader.Owner picks it
// for a group read: a proxy member's Versions reads its remote's document through the read path
// (fetching it when the read path would), and a member whose Versions fails is skipped.
// assets.ErrRemoteFailed when no member has the artifact and a proxy member's remote failed.
func (s *Spec) owner(ctx context.Context, repo *repos.Repo, members []*repos.Repo, p string) (*repos.Repo, error) {
	remoteFailed := false
	for _, m := range members {
		art, _, _, err := s.Versions(ctx, repo, m, p, "", 1)
		switch {
		case ctx.Err() != nil:
			return nil, ctx.Err()
		case err != nil:
			remoteFailed = remoteFailed || errors.Is(err, assets.ErrRemoteFailed)
		case art != nil:
			return m, nil
		}
	}
	if remoteFailed {
		return nil, assets.ErrRemoteFailed
	}
	return nil, nil
}

// memberPage is the page of one member in a merge of versions.
type memberPage struct {
	m     *repos.Repo
	from  string // the member's cursor in the group cursor
	after string // the member's cursor of items
	items []Version
	more  bool // the member has versions after items
	pos   int  // the items taken by the merge
}

// last is the last version that the merge took from the member: its cursor.
func (pg *memberPage) last() string {
	if pg.pos > 0 {
		return pg.items[pg.pos-1].Version
	}
	return pg.after
}

// holds reports whether the member's page holds version v.
func (pg *memberPage) holds(v string) bool {
	for _, x := range pg.items {
		if x.Version == v {
			return true
		}
	}
	return false
}

// mergeVersions is a k-way merge of the versions of the members in VersionOrder. Each member
// reads pages of limit versions from its own cursor. A version comes out while every member that
// has more still holds a version it read, so no unread version of a member sorts before it; a
// member that has taken all it read reads its next page. A version that an earlier member has is
// that member's, and leaves the list of the later one. With VersionsByName a version sorts at one
// place in every member, so the merge takes the versions of all members in one order: an earlier
// member that has the version holds it in its page or took it last on an earlier page (its
// cursor), and a later member that has it holds it in its page. Otherwise a member that does not
// hold it in its page is asked by a lookup of its Builds. A page costs one Versions call per
// member that has more (one more for each page a member takes up), and without VersionsByName at
// most one Builds lookup per other member and version.
func (s *Spec) mergeVersions(ctx context.Context, repo *repos.Repo, members []*repos.Repo, p, after string, limit int) (*Artifact, []Version, string, error) {
	cur, err := decodeCursor(after)
	if err != nil {
		return nil, nil, "", err
	}
	var art *Artifact
	read := func(pg *memberPage, after string) error {
		a, items, next, err := s.Versions(ctx, repo, pg.m, p, after, limit)
		if err != nil {
			return err
		}
		pg.after, pg.items, pg.pos, pg.more = after, nil, 0, false
		if a != nil {
			if art == nil {
				art = a
			}
			pg.items, pg.more = items, next != "" && len(items) > 0
		}
		return nil
	}
	pages := make([]*memberPage, len(members))
	for i, m := range members {
		from := cur.After[strconv.FormatInt(m.ID, 10)]
		pages[i] = &memberPage{m: m, from: from, after: from}
		if cur.done(m.ID) {
			continue
		}
		if err := read(pages[i], from); err != nil {
			return nil, nil, "", err
		}
	}
	if art == nil {
		return nil, nil, "", nil // no member that has more has the artifact
	}
	taken := map[string]bool{}
	var out []Version
	for len(out) < limit {
		best := -1
		for i, pg := range pages {
			if pg.pos == len(pg.items) && pg.more {
				if err := read(pg, pg.items[pg.pos-1].Version); err != nil {
					return nil, nil, "", err
				}
			}
			if pg.pos == len(pg.items) {
				continue
			}
			if best < 0 || s.VersionOrder(pg.items[pg.pos], pages[best].items[pages[best].pos]) < 0 {
				best = i
			}
		}
		if best < 0 {
			break
		}
		pg := pages[best]
		v := pg.items[pg.pos]
		pg.pos++
		if taken[v.Version] {
			continue
		}
		earlier, err := s.earlierHas(ctx, repo, pages[:best], p, v.Version)
		if err != nil {
			return nil, nil, "", err
		}
		if earlier {
			continue // an earlier member serves it
		}
		taken[v.Version] = true
		v.Repo = pg.m.Name
		if v.Folder != "" {
			// The files of a version folder serve path by path: a version that later members hold
			// too is the union of their builds.
			later, err := s.laterHolders(ctx, repo, pages[best+1:], p, v.Version)
			if err != nil {
				return nil, nil, "", err
			}
			if len(later) > 0 {
				u, err := s.union(ctx, repo, append([]*repos.Repo{pg.m}, later...), p, v.Version)
				if err != nil {
					return nil, nil, "", err
				}
				if u != nil {
					v = *u.ver
				}
			}
		}
		out = append(out, v)
	}
	next := &groupCursor{After: map[string]string{}}
	open := false
	for _, pg := range pages {
		if last := pg.last(); last != "" {
			next.After[strconv.FormatInt(pg.m.ID, 10)] = last
		}
		if pg.pos == len(pg.items) && !pg.more {
			next.Done = append(next.Done, pg.m.ID)
		} else {
			open = true
		}
	}
	if !open {
		return art, out, "", nil
	}
	return art, out, next.encode(), nil
}

// earlierHas reports whether one of the earlier members has version v: in the page it read, as
// its cursor (VersionsByName), or else by a lookup of the version's Builds.
func (s *Spec) earlierHas(ctx context.Context, repo *repos.Repo, earlier []*memberPage, p, v string) (bool, error) {
	for _, pg := range earlier {
		found := pg.holds(v) || s.VersionsByName && pg.from == v
		if !found && !s.VersionsByName {
			var err error
			if found, err = s.hasVersion(ctx, repo, pg.m, p, v); err != nil {
				return false, err
			}
		}
		if found {
			return true, nil
		}
	}
	return false, nil
}

// laterHolders returns the members of pages that have version v: in the page they read, or else
// (without VersionsByName) by a lookup of the version's Builds.
func (s *Spec) laterHolders(ctx context.Context, repo *repos.Repo, pages []*memberPage, p, v string) ([]*repos.Repo, error) {
	var out []*repos.Repo
	for _, pg := range pages {
		found := pg.holds(v)
		if !found && !s.VersionsByName {
			var err error
			if found, err = s.hasVersion(ctx, repo, pg.m, p, v); err != nil {
				return nil, err
			}
		}
		if found {
			out = append(out, pg.m)
		}
	}
	return out, nil
}

// hasVersion reports whether member m has version v of the artifact at p: a lookup of its Builds
// (limit 1).
func (s *Spec) hasVersion(ctx context.Context, repo, m *repos.Repo, p, v string) (bool, error) {
	_, ver, _, _, err := s.Builds(ctx, repo, m, p, v, "", "", 1)
	return ver != nil, err
}

// buildsPage is the number of builds that one Builds call of a union reads.
const buildsPage = 1000

// allBuilds returns the version v of member m and all its builds; a nil version when m does not
// have it.
func (s *Spec) allBuilds(ctx context.Context, repo, m *repos.Repo, p, v string) (*Artifact, *Version, []Build, error) {
	var art *Artifact
	var ver *Version
	var out []Build
	after := ""
	for range 1000 {
		a, x, items, next, err := s.Builds(ctx, repo, m, p, v, "", after, buildsPage)
		if err != nil || a == nil || x == nil {
			return nil, nil, nil, err
		}
		if art == nil {
			art, ver = a, x
		}
		out = append(out, items...)
		if next == "" || len(items) == 0 {
			break
		}
		after = next
	}
	return art, ver, out, nil
}

// unionBuilds is the version of a group that the builds of several members make up.
type unionBuilds struct {
	art    *Artifact
	ver    *Version
	builds []Build
}

// union is version v of the members holders (in read order) of a group: the union of their builds,
// a build id once, a file path once from the first member that has it (named in the file's Repo),
// as a group read serves the files of a version folder path by path. The builds are in BuildOrder;
// the version is the first holder's with the count, the latest build, its time and its CI build
// record taken from the union. nil when no holder has the version.
func (s *Spec) union(ctx context.Context, repo *repos.Repo, holders []*repos.Repo, p, v string) (*unionBuilds, error) {
	var u *unionBuilds
	byID := map[string]int{}
	seen := map[string]bool{}
	for _, m := range holders {
		art, ver, builds, err := s.allBuilds(ctx, repo, m, p, v)
		if err != nil {
			return nil, err
		}
		if ver == nil {
			continue
		}
		if u == nil {
			x := *ver
			x.Repo = m.Name
			u = &unionBuilds{art: art, ver: &x}
		}
		for _, b := range builds {
			i, ok := byID[b.Build]
			if !ok {
				i = len(u.builds)
				byID[b.Build] = i
				nb := b
				nb.Files = nil
				nb.PublishedAt = time.Time{}
				u.builds = append(u.builds, nb)
			}
			ub := &u.builds[i]
			for _, f := range b.Files {
				if seen[f.Path] {
					continue // an earlier member serves it
				}
				seen[f.Path] = true
				f.Repo = m.Name
				ub.Files = append(ub.Files, f)
				at := f.UpdatedAt
				if at.IsZero() {
					at = b.PublishedAt
				}
				if at.After(ub.PublishedAt) {
					ub.PublishedAt = at
				}
			}
		}
	}
	if u == nil {
		return nil, nil
	}
	u.builds = slices.DeleteFunc(u.builds, func(b Build) bool { return len(b.Files) == 0 })
	if s.BuildOrder != nil {
		slices.SortStableFunc(u.builds, s.BuildOrder)
	}
	if len(u.builds) > 0 {
		newest := u.builds[0]
		u.ver.BuildsCount, u.ver.PublishedAt, u.ver.CI = len(u.builds), newest.PublishedAt, newest.CI
		if u.ver.LatestBuild != "" {
			u.ver.LatestBuild, u.ver.LatestLabel = newest.Build, newest.Label
		}
	}
	return u, nil
}

// page is the page of builds of u after build after (in BuildOrder; "" for the first), at most
// limit, or the one build of that id when build is set.
func (u *unionBuilds) page(order func(a, b Build) int, build, after string, limit int) ([]Build, string) {
	var out []Build
	from := 0
	if after != "" {
		from = len(u.builds)
		for i, b := range u.builds {
			if b.Build == after {
				from = i + 1
				break
			}
			if order != nil && order(Build{Build: after}, b) < 0 {
				from = i // the first build that sorts after the cursor
				break
			}
		}
	}
	for _, b := range u.builds[from:] {
		if build != "" && b.Build != build {
			continue
		}
		if len(out) == limit {
			return out, out[len(out)-1].Build
		}
		out = append(out, b)
	}
	return out, ""
}

// Stores reports whether member m stores the file at path (a hosted file, a proxy's cached copy).
type Stores func(ctx context.Context, m *repos.Repo, path string) (bool, error)

// BuildsOf answers Builds for repo. In a group (members as for VersionsOf) the builds are those
// that a group read serves for the version, and every file names in Repo the member that serves
// it:
//   - GroupOwner: the builds of the owner of the artifact; every file is the owner's.
//   - GroupEach, a version with a Folder (its files serve path by path): the union of the builds
//     of every member that has the version (see union), found by a lookup in each later member;
//     the page of the first member that has it when no later member does.
//   - GroupEach, a version without a Folder (one path names it, e.g. a Docker tag): the builds of
//     the first member that has the version; each file names the first member that stores its
//     path (stores), else that member.
//
// The version names the first member that has it in Repo.
func (s *Spec) BuildsOf(ctx context.Context, repo *repos.Repo, members []*repos.Repo, stores Stores, p, version, build, after string, limit int) (*Artifact, *Version, []Build, string, error) {
	if s.Builds == nil {
		return nil, nil, nil, "", nil
	}
	if repo.Kind != repos.KindGroup {
		return s.Builds(ctx, repo, repo, p, version, build, after, limit)
	}
	if s.GroupVersions == GroupOwner {
		m, err := s.owner(ctx, repo, members, p)
		if err != nil || m == nil {
			return nil, nil, nil, "", err
		}
		art, ver, items, next, err := s.Builds(ctx, repo, m, p, version, build, after, limit)
		if err != nil || art == nil || ver == nil {
			return nil, nil, nil, "", err
		}
		ver.Repo = m.Name
		for i := range items {
			for j := range items[i].Files {
				items[i].Files[j].Repo = m.Name
			}
		}
		return art, ver, items, next, nil
	}
	for i, m := range members {
		art, ver, items, next, err := s.Builds(ctx, repo, m, p, version, build, after, limit)
		if err != nil {
			return nil, nil, nil, "", err
		}
		if art == nil || ver == nil {
			continue
		}
		folder := ver.Folder != ""
		if folder {
			var later []*repos.Repo
			for _, x := range members[i+1:] {
				has, err := s.hasVersion(ctx, repo, x, p, version)
				if err != nil {
					return nil, nil, nil, "", err
				}
				if has {
					later = append(later, x)
				}
			}
			if len(later) > 0 {
				u, err := s.union(ctx, repo, append([]*repos.Repo{m}, later...), p, version)
				if err != nil || u == nil {
					return nil, nil, nil, "", err
				}
				items, next := u.page(s.BuildOrder, build, after, limit)
				return u.art, u.ver, items, next, nil
			}
			// The only member that has the version serves its files: its own page.
		}
		ver.Repo = m.Name
		for i := range items {
			for j := range items[i].Files {
				f := &items[i].Files[j]
				f.Repo = m.Name
				if folder {
					continue
				}
				for _, x := range members {
					ok, err := stores(ctx, x, f.Path)
					if err != nil {
						return nil, nil, nil, "", err
					}
					if ok {
						f.Repo = x.Name
						break
					}
				}
			}
		}
		return art, ver, items, next, nil
	}
	return nil, nil, nil, "", nil
}

// UsageOf answers ArtifactUsage for repo. In a group (members as for VersionsOf) the snippets
// name the group, and the artifact is read from the first member that answers for the path: the
// one that has the artifact folder, the version or the file.
func (s *Spec) UsageOf(ctx context.Context, repo *repos.Repo, members []*repos.Repo, baseURL, p, version, mode string) (*ArtifactUsage, error) {
	if s.ArtifactUsage == nil {
		return nil, nil
	}
	if repo.Kind != repos.KindGroup {
		return s.ArtifactUsage(ctx, repo, repo, baseURL, p, version, mode)
	}
	if s.GroupVersions == GroupOwner && s.Versions != nil {
		// The owner of an artifact folder; a path that is not one (a file) goes to the first
		// member that answers for it.
		m, err := s.owner(ctx, repo, members, p+"/")
		if err != nil {
			return nil, err
		}
		if m != nil {
			members = []*repos.Repo{m}
		}
	}
	remoteFailed := false
	for _, m := range members {
		u, err := s.ArtifactUsage(ctx, repo, m, baseURL, p, version, mode)
		switch {
		case ctx.Err() != nil:
			return nil, ctx.Err()
		case errors.Is(err, assets.ErrRemoteFailed):
			remoteFailed = true // a proxy whose remote failed: the next member may answer
		case err != nil || u != nil:
			return u, err
		}
	}
	if remoteFailed {
		return nil, assets.ErrRemoteFailed
	}
	return nil, nil
}
