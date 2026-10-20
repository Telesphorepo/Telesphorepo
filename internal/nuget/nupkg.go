package nuget

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/version"
)

// maxNuspec is the largest .nuspec that a package may hold.
const maxNuspec = 1 << 20

// idRE is a NuGet package id (NuGet's PackageIdValidator, ASCII only): word runs joined by "."
// or "-", at most 100 characters.
var idRE = regexp.MustCompile(`^[A-Za-z0-9_]+([.-][A-Za-z0-9_]+)*$`)

// validID reports whether id is a package id this server takes.
func validID(id string) bool { return len(id) <= 100 && idRE.MatchString(id) }

// badPackage is a .nupkg that the server refuses (400).
type badPackage struct{ msg string }

func (b badPackage) Error() string { return b.msg }

func bad(format string, args ...any) error { return badPackage{fmt.Sprintf(format, args...)} }

// pkgInfo is what a .nupkg says about itself: its root .nuspec.
type pkgInfo struct {
	ID      string
	Version version.NuGet
	Nuspec  []byte // the exact bytes of the root .nuspec
	Meta    pkgMeta
}

// lowerID is the package id as the flat container names it.
func (p *pkgInfo) lowerID() string { return strings.ToLower(p.ID) }

// lowerVersion is the normalised version as the flat container names it.
func (p *pkgInfo) lowerVersion() string { return strings.ToLower(p.Version.Normalized()) }

// pkgMeta are the .nuspec fields that the registration and the search show. It is stored as
// the meta JSON of a version row.
type pkgMeta struct {
	Title                    string     `json:"title,omitempty"`
	Authors                  string     `json:"authors,omitempty"`
	Owners                   string     `json:"owners,omitempty"`
	Description              string     `json:"description,omitempty"`
	Summary                  string     `json:"summary,omitempty"`
	ReleaseNotes             string     `json:"releaseNotes,omitempty"`
	Copyright                string     `json:"copyright,omitempty"`
	Language                 string     `json:"language,omitempty"`
	Tags                     []string   `json:"tags,omitempty"`
	ProjectURL               string     `json:"projectUrl,omitempty"`
	IconURL                  string     `json:"iconUrl,omitempty"`
	LicenseURL               string     `json:"licenseUrl,omitempty"`
	LicenseExpression        string     `json:"licenseExpression,omitempty"`
	RequireLicenseAcceptance bool       `json:"requireLicenseAcceptance,omitempty"`
	MinClientVersion         string     `json:"minClientVersion,omitempty"`
	DependencyGroups         []depGroup `json:"dependencyGroups,omitempty"`
	PackageTypes             []string   `json:"packageTypes,omitempty"`
}

// depGroup is the dependencies of one target framework ("" for every framework).
type depGroup struct {
	TargetFramework string `json:"targetFramework,omitempty"`
	Dependencies    []dep  `json:"dependencies,omitempty"`
}

// dep is one dependency: its id and its version range.
type dep struct {
	ID    string `json:"id"`
	Range string `json:"range"`
}

// nuspecXML is the part of a .nuspec that the server reads. Field tags without a namespace match
// every namespace of the .nuspec schema versions.
type nuspecXML struct {
	Metadata struct {
		MinClientVersion         string `xml:"minClientVersion,attr"`
		ID                       string `xml:"id"`
		Version                  string `xml:"version"`
		Title                    string `xml:"title"`
		Authors                  string `xml:"authors"`
		Owners                   string `xml:"owners"`
		Description              string `xml:"description"`
		Summary                  string `xml:"summary"`
		ReleaseNotes             string `xml:"releaseNotes"`
		Copyright                string `xml:"copyright"`
		Language                 string `xml:"language"`
		Tags                     string `xml:"tags"`
		ProjectURL               string `xml:"projectUrl"`
		IconURL                  string `xml:"iconUrl"`
		LicenseURL               string `xml:"licenseUrl"`
		RequireLicenseAcceptance string `xml:"requireLicenseAcceptance"`
		License                  struct {
			Type  string `xml:"type,attr"`
			Value string `xml:",chardata"`
		} `xml:"license"`
		Dependencies struct {
			Groups []struct {
				TargetFramework string      `xml:"targetFramework,attr"`
				Deps            []nuspecDep `xml:"dependency"`
			} `xml:"group"`
			Deps []nuspecDep `xml:"dependency"`
		} `xml:"dependencies"`
		PackageTypes struct {
			Types []struct {
				Name string `xml:"name,attr"`
			} `xml:"packageType"`
		} `xml:"packageTypes"`
	} `xml:"metadata"`
}

type nuspecDep struct {
	ID      string `xml:"id,attr"`
	Version string `xml:"version,attr"`
}

// readerAt returns the content of sp for the zip reader.
func readerAt(sp *assets.Spooled) io.ReaderAt {
	if sp.File != nil {
		return sp.File
	}
	return bytes.NewReader(sp.Data)
}

// parseNupkg reads the root .nuspec of a .nupkg (a zip archive) of size bytes. A package that
// is not a zip, has no root .nuspec or several, or names an id or a version that does not parse
// is a badPackage. The bytes of the package are not changed: a signed package keeps its
// signature.
func parseNupkg(r io.ReaderAt, size int64) (*pkgInfo, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, bad("the package is not a .nupkg (a zip archive): %v", err)
	}
	var spec *zip.File
	for _, f := range zr.File {
		if strings.Contains(f.Name, "/") || strings.Contains(f.Name, `\`) || !strings.HasSuffix(strings.ToLower(f.Name), ".nuspec") {
			continue
		}
		if spec != nil {
			return nil, bad("the package has more than one .nuspec at its root")
		}
		spec = f
	}
	if spec == nil {
		return nil, bad("the package has no .nuspec at its root")
	}
	if spec.UncompressedSize64 > maxNuspec {
		return nil, bad("the .nuspec of the package is larger than %d bytes", maxNuspec)
	}
	rc, err := spec.Open()
	if err != nil {
		return nil, bad("the .nuspec of the package does not open: %v", err)
	}
	defer rc.Close()
	raw, err := io.ReadAll(io.LimitReader(rc, maxNuspec+1))
	if err != nil {
		return nil, bad("the .nuspec of the package does not read: %v", err)
	}
	if len(raw) > maxNuspec {
		return nil, bad("the .nuspec of the package is larger than %d bytes", maxNuspec)
	}
	return parseNuspec(raw)
}

// parseNuspec reads the identity and the fields of a .nuspec.
func parseNuspec(raw []byte) (*pkgInfo, error) {
	var x nuspecXML
	dec := xml.NewDecoder(bytes.NewReader(raw))
	dec.Strict = true
	if err := dec.Decode(&x); err != nil {
		return nil, bad("the .nuspec of the package is not valid XML: %v", err)
	}
	m := x.Metadata
	id := strings.TrimSpace(m.ID)
	if !validID(id) {
		return nil, bad("the package id %q is not valid: letters, digits and _, joined by . or -, at most 100 characters", id)
	}
	v, err := version.ParseNuGet(strings.TrimSpace(m.Version))
	if err != nil {
		return nil, bad("the package version %q is not a NuGet version", m.Version)
	}
	meta := pkgMeta{
		Title: strings.TrimSpace(m.Title), Authors: strings.TrimSpace(m.Authors), Owners: strings.TrimSpace(m.Owners),
		Description: strings.TrimSpace(m.Description), Summary: strings.TrimSpace(m.Summary),
		ReleaseNotes: strings.TrimSpace(m.ReleaseNotes), Copyright: strings.TrimSpace(m.Copyright),
		Language: strings.TrimSpace(m.Language), ProjectURL: strings.TrimSpace(m.ProjectURL),
		IconURL: strings.TrimSpace(m.IconURL), LicenseURL: strings.TrimSpace(m.LicenseURL),
		RequireLicenseAcceptance: strings.EqualFold(strings.TrimSpace(m.RequireLicenseAcceptance), "true"),
		MinClientVersion:         strings.TrimSpace(m.MinClientVersion),
	}
	if strings.EqualFold(m.License.Type, "expression") {
		meta.LicenseExpression = strings.TrimSpace(m.License.Value)
	}
	for _, t := range strings.FieldsFunc(m.Tags, func(r rune) bool { return r == ' ' || r == ',' || r == ';' || r == '\t' || r == '\n' }) {
		meta.Tags = append(meta.Tags, t)
	}
	for _, g := range m.Dependencies.Groups {
		dg := depGroup{TargetFramework: strings.TrimSpace(g.TargetFramework)}
		for _, d := range g.Deps {
			if dd, ok := depOf(d); ok {
				dg.Dependencies = append(dg.Dependencies, dd)
			}
		}
		meta.DependencyGroups = append(meta.DependencyGroups, dg)
	}
	if len(m.Dependencies.Deps) > 0 {
		dg := depGroup{}
		for _, d := range m.Dependencies.Deps {
			if dd, ok := depOf(d); ok {
				dg.Dependencies = append(dg.Dependencies, dd)
			}
		}
		meta.DependencyGroups = append(meta.DependencyGroups, dg)
	}
	for _, t := range m.PackageTypes.Types {
		if n := strings.TrimSpace(t.Name); n != "" {
			meta.PackageTypes = append(meta.PackageTypes, n)
		}
	}
	return &pkgInfo{ID: id, Version: v, Nuspec: raw, Meta: meta}, nil
}

// depOf is the dependency of a .nuspec entry: a plain version "1.2" is the range "[1.2, )", as
// NuGet reads it.
func depOf(d nuspecDep) (dep, bool) {
	id := strings.TrimSpace(d.ID)
	if id == "" {
		return dep{}, false
	}
	r := strings.TrimSpace(d.Version)
	switch {
	case r == "":
		r = "(, )"
	case !strings.HasPrefix(r, "[") && !strings.HasPrefix(r, "("):
		r = "[" + r + ", )"
	}
	return dep{ID: id, Range: r}, true
}

// asBad returns the 400 refusal of a badPackage, else err.
func asBad(err error) error {
	var b badPackage
	if errors.As(err, &b) {
		return assets.Refuse(400, "%s", b.msg)
	}
	return err
}
