package format

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"

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
// of its own Versions, and the members that have no more versions.
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

// owner is the first member that has the artifact at p, or nil.
func (s *Spec) owner(ctx context.Context, repo *repos.Repo, members []*repos.Repo, p string) (*repos.Repo, error) {
	for _, m := range members {
		art, _, _, err := s.Versions(ctx, repo, m, p, "", 1)
		if err != nil {
			return nil, err
		}
		if art != nil {
			return m, nil
		}
	}
	return nil, nil
}

// memberPage is the page of one member in a merge of versions.
type memberPage struct {
	m     *repos.Repo
	after string // the member's cursor of items
	items []Version
	more  bool // the member has versions after items
	pos   int  // the items taken by the merge
}

// mergeVersions is a k-way merge of the versions of the members in VersionOrder. Each member
// reads pages of limit versions from its own cursor. A version comes out while every member that
// has more still holds a version it read, so no unread version of a member sorts before it; a
// member that has taken all it read reads its next page. A version that an earlier member has (in
// its page, or by a lookup of its Builds) is that member's, and leaves the list of the later one.
// A page costs one Versions call per member that has more (one more for each page a member
// takes up), and at most one Builds lookup per earlier member and version.
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
		pages[i] = &memberPage{m: m}
		if cur.done(m.ID) {
			continue
		}
		if err := read(pages[i], cur.After[strconv.FormatInt(m.ID, 10)]); err != nil {
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
		out = append(out, v)
	}
	next := &groupCursor{After: map[string]string{}}
	open := false
	for _, pg := range pages {
		key := strconv.FormatInt(pg.m.ID, 10)
		switch {
		case pg.pos == len(pg.items) && !pg.more:
			next.Done = append(next.Done, pg.m.ID)
		case pg.pos > 0:
			next.After[key] = pg.items[pg.pos-1].Version
			open = true
		default:
			if pg.after != "" {
				next.After[key] = pg.after
			}
			open = true
		}
	}
	if !open {
		return art, out, "", nil
	}
	return art, out, next.encode(), nil
}

// earlierHas reports whether one of the earlier members has version v: in the page it read, or
// else by a lookup of the version's Builds.
func (s *Spec) earlierHas(ctx context.Context, repo *repos.Repo, earlier []*memberPage, p, v string) (bool, error) {
	for _, pg := range earlier {
		found := false
		for _, x := range pg.items {
			if x.Version == v {
				found = true
				break
			}
		}
		if !found {
			_, ver, _, _, err := s.Builds(ctx, repo, pg.m, p, v, "", "", 1)
			if err != nil {
				return false, err
			}
			found = ver != nil
		}
		if found {
			return true, nil
		}
	}
	return false, nil
}

// BuildsOf answers Builds for repo. In a group (members as for VersionsOf) the builds are those of
// the member that serves the version: the owner of the artifact (GroupOwner), else the first
// member that has the version. The version names that member in Repo.
func (s *Spec) BuildsOf(ctx context.Context, repo *repos.Repo, members []*repos.Repo, p, version, build, after string, limit int) (*Artifact, *Version, []Build, string, error) {
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
		members = []*repos.Repo{m}
	}
	for _, m := range members {
		art, ver, items, next, err := s.Builds(ctx, repo, m, p, version, build, after, limit)
		if err != nil {
			return nil, nil, nil, "", err
		}
		if art != nil && ver != nil {
			ver.Repo = m.Name
			return art, ver, items, next, nil
		}
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
	for _, m := range members {
		u, err := s.ArtifactUsage(ctx, repo, m, baseURL, p, version, mode)
		if err != nil || u != nil {
			return u, err
		}
	}
	return nil, nil
}
