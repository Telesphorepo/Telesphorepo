package maven

import (
	"context"
	"encoding/xml"
	"errors"
	"path"
	"slices"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
)

// Proxy and group repos (SPEC.md "Group and proxy repositories"). The core read path
// serves them; this file classifies the paths of a proxy and merges maven-metadata.xml in a
// group.

// classify tells the proxy how to cache a path: maven-metadata.xml and its checksums, and the
// files of a -SNAPSHOT version, change at the remote; every other file is fetched once.
func classify(p repopath.Path) (assets.Class, bool) {
	return assets.Class{Changing: mutable(string(p))}, true
}

// checksumNames maps the extension of a checksum of maven-metadata.xml to its algorithm name.
var checksumNames = map[string]string{".md5": "md5", ".sha1": "sha1", ".sha256": "sha256", ".sha512": "sha512"}

// matchMerged reports whether p is an artifact-level (or group-level) maven-metadata.xml or one of
// its checksums, which a group merges, and returns the metadata path and the checksum ("" for the
// file itself). A version-level file (under a -SNAPSHOT folder) is not merged: it comes from the
// first member that has it.
func matchMerged(p repopath.Path) (repopath.Path, string, bool) {
	base := p.Base()
	rest, found := strings.CutPrefix(base, "maven-metadata.xml")
	if !found || (rest != "" && checksumNames[rest] == "") {
		return "", "", false
	}
	if strings.HasSuffix(path.Base(path.Dir(string(p))), "-SNAPSHOT") {
		return "", "", false
	}
	return repopath.Path(strings.TrimSuffix(string(p), rest)), checksumNames[rest], true
}

// groupMetadata is the part of a maven-metadata.xml that a group merges: the versions of an
// artifact, or the plugins of a group id.
type groupMetadata struct {
	XMLName    xml.Name         `xml:"metadata"`
	GroupID    string           `xml:"groupId,omitempty"`
	ArtifactID string           `xml:"artifactId,omitempty"`
	Versioning *groupVersioning `xml:"versioning"`
	Plugins    *groupPlugins    `xml:"plugins"` // nil: no plugins element
}

type groupPlugins struct {
	Plugin []groupPlugin `xml:"plugin"`
}

type groupVersioning struct {
	Latest      string   `xml:"latest,omitempty"`
	Release     string   `xml:"release,omitempty"`
	Versions    []string `xml:"versions>version"`
	LastUpdated string   `xml:"lastUpdated,omitempty"`
}

type groupPlugin struct {
	Name       string `xml:"name,omitempty"`
	Prefix     string `xml:"prefix"`
	ArtifactID string `xml:"artifactId"`
}

// newer returns the newer of two versions ("" is older than any).
func newer(a, b string) string {
	if a == "" || (b != "" && compareVersions(b, a) > 0) {
		return b
	}
	return a
}

// mergeMetadata merges the metadata of the members, as a Nexus group does: the union of the
// versions in Maven order; latest is the highest version and release the highest version that is
// not a -SNAPSHOT (also when no member names them); the newest lastUpdated; the union of the
// plugins (no plugins element when there are none).
func mergeMetadata(all []groupMetadata) groupMetadata {
	var out groupMetadata
	var versions []string
	for _, m := range all {
		if out.GroupID == "" {
			out.GroupID = m.GroupID
		}
		if out.ArtifactID == "" {
			out.ArtifactID = m.ArtifactID
		}
		if v := m.Versioning; v != nil {
			if out.Versioning == nil {
				out.Versioning = &groupVersioning{}
			}
			o := out.Versioning
			o.Latest, o.Release = newer(o.Latest, strings.TrimSpace(v.Latest)), newer(o.Release, strings.TrimSpace(v.Release))
			if lu := strings.TrimSpace(v.LastUpdated); lu > o.LastUpdated {
				o.LastUpdated = lu
			}
			for _, x := range v.Versions {
				if x = strings.TrimSpace(x); x != "" {
					versions = append(versions, x)
				}
			}
		}
		if m.Plugins == nil {
			continue
		}
		for _, pl := range m.Plugins.Plugin {
			if out.Plugins == nil {
				out.Plugins = &groupPlugins{}
			}
			if !slices.ContainsFunc(out.Plugins.Plugin, func(x groupPlugin) bool { return x.Prefix == pl.Prefix }) {
				out.Plugins.Plugin = append(out.Plugins.Plugin, pl)
			}
		}
	}
	if out.Versioning != nil {
		slices.SortFunc(versions, compareVersions)
		o := out.Versioning
		o.Versions = slices.Compact(versions)
		for _, v := range o.Versions {
			o.Latest = newer(o.Latest, v)
			if !strings.HasSuffix(v, "-SNAPSHOT") {
				o.Release = newer(o.Release, v)
			}
		}
	}
	return out
}

// mergeParts merges the maven-metadata.xml files of the members of a group (assets.Merger). A
// part that is not valid XML is left out.
func mergeParts(_ context.Context, _ repopath.Path, parts [][]byte) ([]byte, error) {
	var all []groupMetadata
	for _, b := range parts {
		var m groupMetadata
		if xml.Unmarshal(b, &m) == nil {
			all = append(all, m)
		}
	}
	if len(all) == 0 {
		return nil, errors.New("no member has a valid maven-metadata.xml")
	}
	x, err := xml.MarshalIndent(mergeMetadata(all), "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), append(x, '\n')...), nil
}
