package maven

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/blobs"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// The coordinates, checksums and maven-metadata.xml of the files that uploads write (plan.go).

const (
	maxPOMBytes      = 1 << 20 // a larger pom is refused
	maxMetadataBytes = 4 << 20 // a larger stored maven-metadata.xml is refused
)

var (
	groupRE    = regexp.MustCompile(`^[A-Za-z0-9_-]+(\.[A-Za-z0-9_-]+)*$`)
	artifactRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,199}$`)
	versionRE  = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.+-]{0,199}$`)
	extRE      = regexp.MustCompile(`^[a-z0-9]+(\.[a-z0-9]+)?$`)
)

// pomXML holds the coordinates of a pom. Element names match in any namespace.
type pomXML struct {
	GroupID    string `xml:"groupId"`
	ArtifactID string `xml:"artifactId"`
	Version    string `xml:"version"`
	Packaging  string `xml:"packaging"`
	Parent     struct {
		GroupID string `xml:"groupId"`
		Version string `xml:"version"`
	} `xml:"parent"`
}

type coords struct {
	group, artifact, version, packaging string
}

func (c coords) dir() string {
	return strings.ReplaceAll(c.group, ".", "/") + "/" + c.artifact + "/" + c.version
}

// usable reports whether a pom value is set and has no unresolved ${property}.
func usable(v string) bool { return v != "" && !strings.Contains(v, "${") }

// checksumFiles are the checksum files the server writes next to a file, in order.
var checksumFiles = []string{".md5", ".sha1", ".sha256"}

// checksumWrites returns the writes of the .md5, .sha1 and .sha256 files of p with the hashes of
// sp.
func checksumWrites(repo *repos.Repo, p repopath.Path, sp *assets.Spooled, o assets.Origin) []assets.Write {
	sums := map[string]string{".md5": sp.MD5, ".sha1": sp.SHA1, ".sha256": sp.SHA256}
	out := make([]assets.Write, 0, len(checksumFiles))
	for _, ext := range checksumFiles {
		out = append(out, assets.Write{Repo: repo, Path: p + repopath.Path(ext), Body: assets.Body{Spool: blobs.Bytes([]byte(sums[ext]))}, Origin: o})
	}
	return out
}

// generatedPOM is the pom of a lone artifact: its coordinates and packaging.
func generatedPOM(c coords, packaging string) []byte {
	return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<project xmlns="http://maven.apache.org/POM/4.0.0" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:schemaLocation="http://maven.apache.org/POM/4.0.0 https://maven.apache.org/xsd/maven-4.0.0.xsd">
  <modelVersion>4.0.0</modelVersion>
  <groupId>%s</groupId>
  <artifactId>%s</artifactId>
  <version>%s</version>
  <packaging>%s</packaging>
</project>
`, c.group, c.artifact, c.version, packaging))
}

// metadataXML is the artifact-level maven-metadata.xml.
type metadataXML struct {
	XMLName    xml.Name `xml:"metadata"`
	GroupID    string   `xml:"groupId"`
	ArtifactID string   `xml:"artifactId"`
	Versioning struct {
		Latest      string   `xml:"latest,omitempty"`
		Release     string   `xml:"release,omitempty"`
		Versions    []string `xml:"versions>version"`
		LastUpdated string   `xml:"lastUpdated"`
	} `xml:"versioning"`
}

// updateMetadata adds c.version to <group>/<artifact>/maven-metadata.xml, creating it when it
// does not exist, with the versions sorted and latest and release set, and writes its .md5,
// .sha1 and .sha256 files (source generated). A version it lists already leaves the file as it
// is. The read-merge-write runs under a lock of its own (the commit takes the path locks).
func (h *Handler) updateMetadata(ctx context.Context, repo *repos.Repo, c coords, o assets.Origin) ([]*assets.Result, error) {
	mdPath := strings.ReplaceAll(c.group, ".", "/") + "/" + c.artifact + "/maven-metadata.xml"
	unlock := h.assets.Lock(repo.ID, "maven-metadata-merge:"+mdPath)
	defer unlock()

	md, found, err := h.readMetadata(ctx, repo, mdPath)
	if err != nil {
		return nil, err
	}
	if found && slices.Contains(md.Versioning.Versions, c.version) {
		return nil, nil
	}
	md.GroupID, md.ArtifactID = c.group, c.artifact
	return h.writeMetadata(ctx, repo, mdPath, md, append(slices.Clone(md.Versioning.Versions), c.version), true, o)
}

// readMetadata reads the maven-metadata.xml at mdPath; found is false when there is none. A file
// that is too large or not valid XML is a 409.
func (h *Handler) readMetadata(ctx context.Context, repo *repos.Repo, mdPath string) (metadataXML, bool, error) {
	var md metadataXML
	a, err := h.assets.Get(ctx, repo.ID, mdPath)
	if errors.Is(err, assets.ErrNotFound) {
		return md, false, nil
	}
	if err != nil {
		return md, false, err
	}
	b, err := h.assets.ReadAll(ctx, a, maxMetadataBytes)
	if errors.Is(err, assets.ErrTooLarge) {
		return md, false, assets.Refuse(http.StatusConflict, "%s is too large to update", mdPath)
	}
	if err != nil {
		return md, false, fmt.Errorf("read %s: %w", mdPath, err)
	}
	if decodeXML(bytes.NewReader(b), &md) != nil {
		return md, false, assets.Refuse(http.StatusConflict, "%s is not valid XML; fix or delete it first", mdPath)
	}
	return md, true, nil
}

// writeMetadata writes md with versions (sorted, latest and release set, lastUpdated now) to
// mdPath. With sums it writes the .md5, .sha1 and .sha256 files too; without, it writes again
// only the checksum files that exist. The caller holds the merge lock of mdPath.
func (h *Handler) writeMetadata(ctx context.Context, repo *repos.Repo, mdPath string, md metadataXML, versions []string, sums bool, o assets.Origin) ([]*assets.Result, error) {
	slices.SortFunc(versions, compareVersions)
	versions = slices.Compact(versions)
	md.Versioning.Versions = versions
	md.Versioning.Latest, md.Versioning.Release = "", ""
	if len(versions) > 0 {
		md.Versioning.Latest = versions[len(versions)-1]
	}
	for i := len(versions) - 1; i >= 0; i-- {
		if !strings.HasSuffix(versions[i], "-SNAPSHOT") {
			md.Versioning.Release = versions[i]
			break
		}
	}
	md.Versioning.LastUpdated = time.Now().UTC().Format("20060102150405")
	body, err := xml.MarshalIndent(md, "", "  ")
	if err != nil {
		return nil, err
	}
	p := repopath.Path(mdPath)
	sp := blobs.Bytes(append([]byte(xml.Header), append(body, '\n')...))
	ws := []assets.Write{{Repo: repo, Path: p, Body: assets.Body{Spool: sp}, Origin: o}}
	for _, w := range checksumWrites(repo, p, sp, o) {
		if !sums {
			if _, err := h.assets.Get(ctx, repo.ID, string(w.Path)); errors.Is(err, assets.ErrNotFound) {
				continue
			} else if err != nil {
				return nil, err
			}
		}
		ws = append(ws, w)
	}
	return h.assets.CommitAll(ctx, ws)
}

// afterRemove writes the artifact-level maven-metadata.xml of every artifact that lost files
// again (format.Spec AfterRemove), so a deleted version leaves the metadata: its versions are
// the version folders that still hold files. An artifact without a maven-metadata.xml stays
// without one.
func (h *Handler) afterRemove(ctx context.Context, repo *repos.Repo, removed []*assets.Asset) {
	var dirs []string
	for _, a := range removed {
		if strings.HasPrefix(path.Base(a.Path), "maven-metadata.xml") {
			continue
		}
		version := path.Dir(a.Path)
		if version == "." {
			continue
		}
		artifact := path.Dir(version)
		if artifact == "." {
			continue
		}
		if d := artifact + "/"; !slices.Contains(dirs, d) {
			dirs = append(dirs, d)
		}
	}
	for _, d := range dirs {
		if err := h.refreshMetadata(ctx, repo, d); err != nil {
			h.host.Log.Warn("write maven-metadata.xml after a delete", "repo", repo.Name, "artifact", d, "err", err)
		}
	}
}

// maxVersions is the most version folders that a metadata refresh lists.
const maxVersions = 10000

// refreshMetadata writes the maven-metadata.xml of artifact folder dir with the version folders
// that hold files, when it exists and its versions differ.
func (h *Handler) refreshMetadata(ctx context.Context, repo *repos.Repo, dir string) error {
	mdPath := dir + "maven-metadata.xml"
	unlock := h.assets.Lock(repo.ID, "maven-metadata-merge:"+mdPath)
	defer unlock()
	md, found, err := h.readMetadata(ctx, repo, mdPath)
	if err != nil || !found {
		return err
	}
	subs, err := h.assets.Children(ctx, repo.ID, dir, maxVersions)
	if err != nil {
		return err
	}
	versions := make([]string, 0, len(subs))
	for _, s := range subs {
		versions = append(versions, strings.TrimSuffix(s[len(dir):], "/"))
	}
	have := slices.Clone(md.Versioning.Versions)
	slices.SortFunc(have, compareVersions)
	slices.SortFunc(versions, compareVersions)
	if slices.Equal(have, versions) {
		return nil
	}
	_, err = h.writeMetadata(ctx, repo, mdPath, md, versions, false, assets.Origin{Source: assets.SourceGenerated})
	return err
}

// versionItem is one part of a version: a number or a qualifier.
type versionItem struct {
	num   string // digits without leading zeros; "" for a qualifier
	qual  string // lower-case qualifier
	isNum bool
}

// splitVersion splits a version at '.', '-' and at changes between digits and letters, the way
// Maven's ComparableVersion does.
func splitVersion(v string) []versionItem {
	var items []versionItem
	var cur strings.Builder
	digits := false
	flush := func() {
		s := cur.String()
		cur.Reset()
		if s == "" {
			return
		}
		if digits {
			n := strings.TrimLeft(s, "0")
			items = append(items, versionItem{num: n, isNum: true})
		} else {
			items = append(items, versionItem{qual: strings.ToLower(s)})
		}
	}
	for _, r := range v {
		switch {
		case r == '.' || r == '-':
			flush()
		case r >= '0' && r <= '9':
			if !digits && cur.Len() > 0 {
				flush()
			}
			digits = true
			cur.WriteRune(r)
		default:
			if digits && cur.Len() > 0 {
				flush()
			}
			digits = false
			cur.WriteRune(r)
		}
	}
	flush()
	return items
}

// qualifierRank orders the well-known qualifiers; a release ("" or ga/final/release) ranks
// above the pre-releases and below sp and unknown qualifiers.
func qualifierRank(q string) int {
	switch q {
	case "alpha", "a":
		return 1
	case "beta", "b":
		return 2
	case "milestone", "m":
		return 3
	case "rc", "cr":
		return 4
	case "snapshot":
		return 5
	case "", "ga", "final", "release":
		return 6
	case "sp":
		return 7
	}
	return 8
}
