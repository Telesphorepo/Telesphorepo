package maven

import (
	"context"
	"errors"
	"path"
	"slices"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// Folder scan (spec §13.1). Each pom (*.pom, or pom.xml in a Maven-layout folder) is one
// artifact: its files are the ones next to it named <artifactId>-<version>[-<classifier>].<ext>,
// .module files included. Checksum and signature files are ignored, since the server writes the
// checksums. In a Maven-layout folder (<group path>/<artifactId>/<version>/) the pom's
// coordinates must match the path. Every other file is ignored.

// ignoredExts are the checksum and signature files a scan leaves out.
var ignoredExts = []string{".md5", ".sha1", ".sha256", ".sha512", ".asc"}

func isIgnored(name string) bool {
	lower := strings.ToLower(name)
	return slices.ContainsFunc(ignoredExts, func(ext string) bool { return strings.HasSuffix(lower, ext) })
}

func isPOMName(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".pom") || lower == "pom.xml"
}

func countPOMs(files []format.UploadFile) int {
	n := 0
	for _, f := range files {
		if isPOMName(f.Name) {
			n++
		}
	}
	return n
}

// scanGroup is one pom and the files that go with it.
type scanGroup struct {
	pom      int   // index in the scanned files
	others   []int // indexes of the artifacts next to it
	c        coords
	base     string // <artifactId>-<version> from the pom
	altBase  string // the same from the folder path when it disagrees with the pom; "" otherwise
	title    string
	problems []string
}

func dirOf(p string) string {
	if d := path.Dir(p); d != "." {
		return d
	}
	return ""
}

// scanFolder groups files by pom. It reads each pom; an unreadable one is a group with a
// problem.
func (h *Handler) scanFolder(files []format.UploadFile) ([]*scanGroup, error) {
	var groups []*scanGroup
	byDir := map[string][]*scanGroup{}
	for i, f := range files {
		if !isPOMName(f.Name) {
			continue
		}
		dir := dirOf(f.Path)
		segs := []string{}
		if dir != "" {
			segs = strings.Split(dir, "/")
		}
		g := &scanGroup{pom: i, title: f.Path}
		p, err := readPOM(f.Spool)
		var ue *format.UploadError
		if errors.As(err, &ue) {
			if strings.ToLower(f.Name) == "pom.xml" {
				continue // a pom.xml outside a Maven layout is a project file, not an artifact
			}
			g.problems = append(g.problems, f.Path+": "+ue.Message)
			groups = append(groups, g)
			continue
		}
		if err != nil {
			return nil, err
		}
		g.c = coords{p.GroupID, p.ArtifactID, p.Version, p.Packaging}
		g.base = g.c.artifact + "-" + g.c.version
		layout := false
		if n := len(segs); n >= 2 {
			a, v := segs[n-2], segs[n-1]
			layout = f.Name == a+"-"+v+".pom" || (strings.ToLower(f.Name) == "pom.xml" && a == g.c.artifact && v == g.c.version)
			if layout && a+"-"+v != g.base {
				g.altBase = a + "-" + v
			}
		}
		if strings.ToLower(f.Name) == "pom.xml" && !layout {
			continue // a project's pom.xml, not a published artifact
		}
		switch {
		case !usable(g.c.group) || !groupRE.MatchString(g.c.group) || len(g.c.group) > 200:
			g.problems = append(g.problems, f.Path+": the pom has no valid groupId")
		case !usable(g.c.artifact) || !artifactRE.MatchString(g.c.artifact) || g.c.artifact == "." || g.c.artifact == "..":
			g.problems = append(g.problems, f.Path+": the pom has no valid artifactId")
		case !usable(g.c.version) || !versionRE.MatchString(g.c.version) || g.c.version == "." || g.c.version == "..":
			g.problems = append(g.problems, f.Path+": the pom has no valid version")
		default:
			g.title = g.c.group + ":" + g.c.artifact + ":" + g.c.version
		}
		if layout && len(g.problems) == 0 {
			n := len(segs)
			if segs[n-2] != g.c.artifact {
				g.problems = append(g.problems, "the pom's artifactId "+g.c.artifact+" does not match the folder "+segs[n-2])
			}
			if segs[n-1] != g.c.version {
				g.problems = append(g.problems, "the pom's version "+g.c.version+" does not match the folder "+segs[n-1])
			}
			gs, ds := strings.Split(g.c.group, "."), segs[:n-2]
			for k := 1; k <= min(len(gs), len(ds)); k++ {
				if gs[len(gs)-k] != ds[len(ds)-k] {
					g.problems = append(g.problems, "the pom's groupId "+g.c.group+" does not match the folder path "+strings.Join(ds, "/"))
					break
				}
			}
		}
		groups = append(groups, g)
		byDir[dir] = append(byDir[dir], g)
	}

	for i, f := range files {
		if isPOMName(f.Name) || isIgnored(f.Name) {
			continue
		}
		var best *scanGroup
		bestLen := 0
		for _, g := range byDir[dirOf(f.Path)] {
			for _, base := range []string{g.base, g.altBase} {
				if base == "" || len(base) <= bestLen {
					continue
				}
				if rest, ok := strings.CutPrefix(f.Name, base); ok && (strings.HasPrefix(rest, ".") || strings.HasPrefix(rest, "-")) {
					best, bestLen = g, len(base)
				}
			}
		}
		if best == nil {
			continue
		}
		best.others = append(best.others, i)
		if rest, ok := strings.CutPrefix(f.Name, best.base); ok && len(best.problems) == 0 && (strings.HasPrefix(rest, ".") || strings.HasPrefix(rest, "-")) {
			if msg := checkArtifactName(f.Name, best.base, rest); msg != "" {
				best.problems = append(best.problems, f.Path+": "+msg)
			}
		}
	}

	seen := map[string]string{}
	for _, g := range groups {
		if g.c.artifact == "" {
			continue
		}
		key := g.c.dir()
		if first, ok := seen[key]; ok {
			g.problems = append(g.problems, g.title+" is also at "+first)
			continue
		}
		seen[key] = files[g.pom].Path
	}
	return groups, nil
}

// checkArtifactName checks the part rest of an artifact's name after <artifactId>-<version>:
// "-<classifier>.<ext>" or ".<ext>". It returns a problem, or "".
func checkArtifactName(name, base, rest string) string {
	var classifier, ext string
	if strings.HasPrefix(rest, "-") {
		dot := strings.IndexByte(rest, '.')
		if dot < 2 {
			return "name it " + base + "-<classifier>.<ext>"
		}
		classifier, ext = rest[1:dot], rest[dot+1:]
	} else {
		ext = rest[1:]
	}
	if !extRE.MatchString(ext) || (classifier != "" && !artifactRE.MatchString(classifier)) {
		return "invalid classifier or extension in " + name
	}
	return ""
}

// Scan implements format.ManualUploader: one Found of kind "maven" per pom.
func (h *Handler) Scan(ctx context.Context, repo *repos.Repo, files []format.UploadFile) ([]format.Found, error) {
	groups, err := h.scanFolder(files)
	if err != nil {
		return nil, err
	}
	out := make([]format.Found, 0, len(groups))
	for _, g := range groups {
		fs := []string{files[g.pom].Path}
		for _, i := range g.others {
			fs = append(fs, files[i].Path)
		}
		out = append(out, format.Found{Kind: "maven", Title: g.title, Files: fs, Problems: g.problems})
	}
	return out, nil
}

// uploadFolder stores each artifact that Scan finds in a dropped folder, after checking that
// none has a problem. Files that no pom takes are left out.
func (h *Handler) uploadFolder(ctx context.Context, repo *repos.Repo, files []format.UploadFile, fields map[string]string, userID int64) ([]format.Uploaded, error) {
	for k := range fields {
		return nil, format.BadUpload("unknown field %q; a folder upload takes no fields", k)
	}
	groups, err := h.scanFolder(files)
	if err != nil {
		return nil, err
	}
	if len(groups) == 0 {
		return nil, format.BadUpload("the folder has no pom (*.pom, or pom.xml in a Maven layout)")
	}
	for _, g := range groups {
		if len(g.problems) > 0 {
			return nil, format.BadUpload("%s: %s", g.title, strings.Join(g.problems, "; "))
		}
	}
	var out []format.Uploaded
	for _, g := range groups {
		set := []format.UploadFile{files[g.pom]}
		for _, i := range g.others {
			set = append(set, files[i])
		}
		res, err := h.uploadSet(ctx, repo, set, nil, userID)
		out = append(out, res...)
		if err != nil {
			return out, err
		}
	}
	return out, nil
}
