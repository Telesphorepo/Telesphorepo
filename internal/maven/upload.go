package maven

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// Manual uploads (spec §13): a pom plus its artifacts, or a lone artifact with coordinates in
// the form fields. The server writes the .md5, .sha1 and .sha256 files and creates or updates
// the artifact's maven-metadata.xml.

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

// readPOM parses the coordinates of an uploaded pom.
func readPOM(sp *assets.Spooled) (pomXML, error) {
	var p pomXML
	if sp.Size > maxPOMBytes {
		return p, format.BadUpload("the pom is larger than %d bytes", maxPOMBytes)
	}
	rd, err := sp.Reader()
	if err != nil {
		return p, err
	}
	if err := xml.NewDecoder(io.LimitReader(rd, maxPOMBytes)).Decode(&p); err != nil {
		return p, format.BadUpload("the pom is not valid XML: %v", err)
	}
	p.GroupID, p.ArtifactID, p.Version = strings.TrimSpace(p.GroupID), strings.TrimSpace(p.ArtifactID), strings.TrimSpace(p.Version)
	p.Packaging = strings.TrimSpace(p.Packaging)
	if p.GroupID == "" {
		p.GroupID = strings.TrimSpace(p.Parent.GroupID)
	}
	if p.Version == "" {
		p.Version = strings.TrimSpace(p.Parent.Version)
	}
	return p, nil
}

// artifactFile is one uploaded file with the repo path it goes to.
type artifactFile struct {
	path string
	sp   *assets.Spooled
}

// Upload implements format.ManualUploader. A dropped folder (files with folder paths, or more
// than one pom) is stored as the artifacts that Scan finds, each like an upload of its own,
// and only when Scan reports no problem. Otherwise the files are at most one pom (*.pom or pom.xml) and any artifacts named
// <artifactId>-<version>[-<classifier>].<ext>; one artifact may have any other name, it becomes
// the main artifact. Coordinates come from the pom; the fields groupId, artifactId, version and
// packaging give the ones the pom does not (or all of them for a lone artifact, which gets a
// generated pom).
func (h *Handler) Upload(ctx context.Context, repo *repos.Repo, files []format.UploadFile, fields map[string]string, userID int64) ([]format.Uploaded, error) {
	if format.InFolder(files) || countPOMs(files) > 1 {
		return h.uploadFolder(ctx, repo, files, fields, userID)
	}
	return h.uploadSet(ctx, repo, files, fields, userID)
}

// uploadSet stores one pom with its artifacts, or one lone artifact (see Upload).
func (h *Handler) uploadSet(ctx context.Context, repo *repos.Repo, files []format.UploadFile, fields map[string]string, userID int64) ([]format.Uploaded, error) {
	for k := range fields {
		switch k {
		case "groupId", "artifactId", "version", "packaging":
		default:
			return nil, format.BadUpload("unknown field %q; Maven uploads take groupId, artifactId, version and packaging", k)
		}
	}
	var pomFile *format.UploadFile
	var others []format.UploadFile
	for i := range files {
		if lower := strings.ToLower(files[i].Name); strings.HasSuffix(lower, ".pom") || lower == "pom.xml" {
			if pomFile != nil {
				return nil, format.BadUpload("upload one pom at a time")
			}
			pomFile = &files[i]
			continue
		}
		others = append(others, files[i])
	}

	var p pomXML
	if pomFile != nil {
		var err error
		if p, err = readPOM(pomFile.Spool); err != nil {
			return nil, err
		}
	}
	pick := func(fromPOM, field string) string {
		if usable(fromPOM) {
			return fromPOM
		}
		return strings.TrimSpace(fields[field])
	}
	c := coords{pick(p.GroupID, "groupId"), pick(p.ArtifactID, "artifactId"), pick(p.Version, "version"), pick(p.Packaging, "packaging")}
	switch {
	case !groupRE.MatchString(c.group) || len(c.group) > 200:
		return nil, format.BadUpload("give a valid groupId (in the pom or the groupId field)")
	case !artifactRE.MatchString(c.artifact) || c.artifact == "." || c.artifact == "..":
		return nil, format.BadUpload("give a valid artifactId (in the pom or the artifactId field)")
	case !versionRE.MatchString(c.version) || c.version == "." || c.version == "..":
		return nil, format.BadUpload("give a valid version (in the pom or the version field)")
	}

	// Name each artifact in the version folder.
	base := c.artifact + "-" + c.version
	dir := c.dir()
	var arts []artifactFile
	mainExt := ""
	free := false // an artifact with a name outside the pattern was taken as the main artifact
	for _, f := range others {
		name := f.Name
		var classifier, ext string
		if rest, ok := strings.CutPrefix(name, base); ok && (strings.HasPrefix(rest, "-") || strings.HasPrefix(rest, ".")) {
			if strings.HasPrefix(rest, "-") {
				dot := strings.IndexByte(rest, '.')
				if dot < 2 {
					return nil, format.BadUpload("%s: name it %s-<classifier>.<ext>", name, base)
				}
				classifier, ext = rest[1:dot], rest[dot+1:]
			} else {
				ext = rest[1:]
			}
		} else {
			if free {
				return nil, format.BadUpload("%s: name the files %s[-<classifier>].<ext>; only one file may have another name", name, base)
			}
			free = true
			dot := strings.LastIndexByte(name, '.')
			if dot < 1 {
				return nil, format.BadUpload("%s has no file extension", name)
			}
			ext = strings.ToLower(name[dot+1:])
		}
		if !extRE.MatchString(ext) || (classifier != "" && !artifactRE.MatchString(classifier)) {
			return nil, format.BadUpload("%s: invalid classifier or extension", name)
		}
		file := base + "." + ext
		if classifier != "" {
			file = base + "-" + classifier + "." + ext
		} else if mainExt == "" {
			mainExt = ext
		}
		target := dir + "/" + file
		if slices.ContainsFunc(arts, func(a artifactFile) bool { return a.path == target }) {
			return nil, format.BadUpload("two files go to %s", file)
		}
		arts = append(arts, artifactFile{target, f.Spool})
	}
	if pomFile == nil && len(arts) == 0 {
		return nil, format.BadUpload("upload a pom, an artifact, or both")
	}

	var out []format.Uploaded
	store := func(p string, sp *assets.Spooled) error {
		created, unchanged, err := h.put(ctx, repo, p, sp, userID)
		if errors.Is(err, errImmutable) {
			return &format.UploadError{Status: http.StatusConflict, Message: p + ": " + errImmutable.Error()}
		}
		if err != nil {
			return err
		}
		st := format.UploadReplaced
		switch {
		case created:
			st = format.UploadCreated
		case unchanged:
			st = format.UploadUnchanged
		}
		out = append(out, format.Uploaded{Path: p, Status: st, Size: sp.Size, SHA256: sp.SHA256})
		return nil
	}
	// storeWithSums stores a file and its .md5, .sha1 and .sha256 files.
	storeWithSums := func(p string, sp *assets.Spooled) error {
		if err := store(p, sp); err != nil {
			return err
		}
		for ext, sum := range map[string]string{".md5": sp.MD5, ".sha1": sp.SHA1, ".sha256": sp.SHA256} {
			s, err := h.assets.SpoolBytes([]byte(sum))
			if err != nil {
				return err
			}
			err = store(p+ext, s)
			s.Close()
			if err != nil {
				return err
			}
		}
		return nil
	}

	for _, a := range arts {
		if err := storeWithSums(a.path, a.sp); err != nil {
			return out, err
		}
	}
	pomPath := dir + "/" + base + ".pom"
	if pomFile != nil {
		if err := storeWithSums(pomPath, pomFile.Spool); err != nil {
			return out, err
		}
	} else {
		packaging := c.packaging
		if packaging == "" {
			packaging = mainExt
		}
		if packaging == "" || !extRE.MatchString(packaging) {
			packaging = "jar"
		}
		sp, err := h.assets.SpoolBytes(generatedPOM(c, packaging))
		if err != nil {
			return out, err
		}
		err = storeWithSums(pomPath, sp)
		sp.Close()
		if err != nil {
			return out, err
		}
	}
	err := h.updateMetadata(ctx, repo, c, storeWithSums)
	return out, err
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
// does not exist, with the versions sorted and latest and release set. A version it lists
// already leaves the file as it is. The read-merge-write runs under a lock of its own (store
// takes the path lock).
func (h *Handler) updateMetadata(ctx context.Context, repo *repos.Repo, c coords, store func(string, *assets.Spooled) error) error {
	mdPath := strings.ReplaceAll(c.group, ".", "/") + "/" + c.artifact + "/maven-metadata.xml"
	unlock := h.assets.Lock(repo.ID, "maven-metadata-merge:"+mdPath)
	defer unlock()

	var md metadataXML
	existing, err := h.assets.Get(ctx, repo.ID, mdPath)
	switch {
	case err == nil:
		if existing.Size > maxMetadataBytes {
			return &format.UploadError{Status: http.StatusConflict, Message: mdPath + " is too large to update"}
		}
		body, _, err := h.assets.Store().Get(ctx, assets.Key(repo.Name, mdPath))
		if err != nil {
			return fmt.Errorf("read %s: %w", mdPath, err)
		}
		err = xml.NewDecoder(io.LimitReader(body, maxMetadataBytes)).Decode(&md)
		body.Close()
		if err != nil {
			return &format.UploadError{Status: http.StatusConflict, Message: mdPath + " is not valid XML; fix or delete it first"}
		}
		if slices.Contains(md.Versioning.Versions, c.version) {
			return nil
		}
	case !errors.Is(err, assets.ErrNotFound):
		return err
	}

	md.GroupID, md.ArtifactID = c.group, c.artifact
	versions := append(slices.Clone(md.Versioning.Versions), c.version)
	slices.SortFunc(versions, compareVersions)
	versions = slices.Compact(versions)
	md.Versioning.Versions = versions
	md.Versioning.Latest = versions[len(versions)-1]
	md.Versioning.Release = ""
	for i := len(versions) - 1; i >= 0; i-- {
		if !strings.HasSuffix(versions[i], "-SNAPSHOT") {
			md.Versioning.Release = versions[i]
			break
		}
	}
	md.Versioning.LastUpdated = time.Now().UTC().Format("20060102150405")
	body, err := xml.MarshalIndent(md, "", "  ")
	if err != nil {
		return err
	}
	sp, err := h.assets.SpoolBytes(append([]byte(xml.Header), append(body, '\n')...))
	if err != nil {
		return err
	}
	defer sp.Close()
	return store(mdPath, sp)
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

func compareNum(a, b string) int {
	if len(a) != len(b) {
		return len(a) - len(b)
	}
	return strings.Compare(a, b)
}

// compareVersions orders Maven versions: numbers numerically, pre-release qualifiers before the
// release, a number after a qualifier.
func compareVersions(a, b string) int {
	if a == b {
		return 0
	}
	x, y := splitVersion(a), splitVersion(b)
	for i := 0; i < max(len(x), len(y)); i++ {
		var p, q *versionItem
		if i < len(x) {
			p = &x[i]
		}
		if i < len(y) {
			q = &y[i]
		}
		var c int
		switch {
		case p == nil && q.isNum:
			c = compareNum("", q.num)
		case p == nil:
			c = qualifierRank("") - qualifierRank(q.qual)
		case q == nil && p.isNum:
			c = compareNum(p.num, "")
		case q == nil:
			c = qualifierRank(p.qual) - qualifierRank("")
		case p.isNum && q.isNum:
			c = compareNum(p.num, q.num)
		case p.isNum:
			c = 1
		case q.isNum:
			c = -1
		default:
			c = qualifierRank(p.qual) - qualifierRank(q.qual)
			if c == 0 && qualifierRank(p.qual) == 8 {
				c = strings.Compare(p.qual, q.qual)
			}
		}
		if c != 0 {
			return c
		}
	}
	return strings.Compare(a, b)
}
