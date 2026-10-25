package maven

import (
	"bytes"
	"context"
	"path"
	"slices"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/blobs"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// Web UI uploads (format.UploadSpec). A set of files without folder paths and with at most one
// pom is one artifact: the pom (*.pom or pom.xml) and files named
// <artifactId>-<version>[-<classifier>].<ext>; one file may have another name and becomes the
// main artifact. Coordinates come from the pom; the fields groupId, artifactId, version and
// packaging give the ones the pom lacks (all of them for a lone artifact, which gets a generated
// pom). A dropped folder is one artifact per pom (see planFolder) and takes no fields. The
// server writes the .md5, .sha1 and .sha256 files and the artifact's maven-metadata.xml.

// coordFields are the upload fields of a single artifact.
var coordFields = []format.Field{
	{Key: "groupId", Label: "Group ID", Type: format.FieldString, Required: true, Placeholder: "com.acme"},
	{Key: "artifactId", Label: "Artifact ID", Type: format.FieldString, Required: true, Placeholder: "app"},
	{Key: "version", Label: "Version", Type: format.FieldString, Required: true, Placeholder: "1.0.0"},
	{Key: "packaging", Label: "Packaging", Type: format.FieldString, Placeholder: "jar",
		Help: "The packaging of the pom the server writes for a lone artifact."},
}

var uploadSpec = &format.UploadSpec{
	Single: true, Folder: true,
	Hint:    "A pom and its jars (sources, javadoc), a lone jar, or a Maven repository folder.",
	Keep:    []string{"**/*.pom", "**/pom.xml", "**/*.jar", "**/*.war", "**/*.aar", "**/*.module", "**/*.zip", "**/*.tar.gz"},
	One:     "artifact",
	Many:    "artifacts",
	Note:    "The server adds .md5, .sha1 and .sha256 files and adds the version to maven-metadata.xml.",
	Ignored: "Only poms and the files that belong to them are uploaded. Checksums and signatures are left out: the server writes new checksums.",
	Empty:   "The folder has no pom. Drop a Maven repository folder (com/example/app/1.0/...) or a folder with poms and their jars.",
	Fields:  coordFields,
	Reads:   []format.ReadRule{{Glob: "**/*.pom", MaxBytes: maxPOMBytes}, {Glob: "**/pom.xml", MaxBytes: maxPOMBytes}},
}

// ignoredExts are the checksum and signature files a folder plan leaves out.
var ignoredExts = []string{".md5", ".sha1", ".sha256", ".sha512", ".asc"}

func isIgnored(name string) bool {
	lower := strings.ToLower(name)
	return slices.ContainsFunc(ignoredExts, func(ext string) bool { return strings.HasSuffix(lower, ext) })
}

func isPOMName(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".pom") || lower == "pom.xml"
}

func dirOf(p string) string {
	if d := path.Dir(p); d != "." {
		return d
	}
	return ""
}

// readPOM parses the coordinates of an uploaded pom from its first bytes.
func readPOM(f format.PlanFile) (pomXML, error) {
	var p pomXML
	if f.Size > maxPOMBytes {
		return p, format.BadUpload("the pom is larger than %d bytes", maxPOMBytes)
	}
	if f.Head == nil || f.Truncated() {
		return p, format.BadUpload("the pom could not be read")
	}
	if err := decodeXML(bytes.NewReader(f.Head), &p); err != nil {
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

// setPlan is what an upload of one pom with its artifacts, or of one lone artifact, stores.
type setPlan struct {
	c       coords
	pom     string         // the path of the uploaded pom in the upload; "" generates one
	arts    []artifactFile // the artifacts: upload path and target
	mainExt string         // the extension of the main artifact
}

// artifactFile is one uploaded file and the repo path it goes to.
type artifactFile struct {
	src, target string
}

// pomPath is the repo path of the set's pom.
func (s *setPlan) pomPath() string {
	return s.c.dir() + "/" + s.c.artifact + "-" + s.c.version + ".pom"
}

// packaging is the packaging of a generated pom: the field, else the main artifact's extension,
// else jar.
func (s *setPlan) packaging() string {
	p := s.c.packaging
	if p == "" {
		p = s.mainExt
	}
	if p == "" || !extRE.MatchString(p) {
		p = "jar"
	}
	return p
}

// planSet plans one pom with its artifacts, or one lone artifact. fields give the coordinates the
// pom lacks. It returns the plan, the target of each file, the problems of the set, and the
// coordinates the pom gives (for the UI).
func planSet(files []format.PlanFile, fields map[string]string) (*setPlan, []format.PlanTarget, []string, coords) {
	var problems []string
	var pomFile *format.PlanFile
	var others []format.PlanFile
	for i := range files {
		if isPOMName(files[i].Name()) {
			if pomFile != nil {
				problems = append(problems, "upload one pom at a time")
				continue
			}
			pomFile = &files[i]
			continue
		}
		others = append(others, files[i])
	}
	var p pomXML
	if pomFile != nil {
		var err error
		if p, err = readPOM(*pomFile); err != nil {
			problems = append(problems, pomFile.Path+": "+refusalText(err))
		}
	}
	fromPOM := coords{}
	for _, v := range []struct {
		val string
		out *string
	}{{p.GroupID, &fromPOM.group}, {p.ArtifactID, &fromPOM.artifact}, {p.Version, &fromPOM.version}, {p.Packaging, &fromPOM.packaging}} {
		if usable(v.val) {
			*v.out = v.val
		}
	}
	pick := func(fromPOM, field string) string {
		if fromPOM != "" {
			return fromPOM
		}
		return strings.TrimSpace(fields[field])
	}
	s := &setPlan{c: coords{pick(fromPOM.group, "groupId"), pick(fromPOM.artifact, "artifactId"), pick(fromPOM.version, "version"), pick(fromPOM.packaging, "packaging")}}
	if pomFile != nil {
		s.pom = pomFile.Path
	}
	targets := make([]format.PlanTarget, 0, len(files))
	if _, msg := coordProblem(s.c); msg != "" {
		problems = append(problems, msg)
		for _, f := range files {
			targets = append(targets, format.PlanTarget{Path: f.Path})
		}
		return s, targets, problems, fromPOM
	}
	if pomFile != nil {
		targets = append(targets, format.PlanTarget{Path: pomFile.Path, Target: s.pomPath()})
	}
	base := s.c.artifact + "-" + s.c.version
	free := false // a file with a name outside the pattern was taken as the main artifact
	for _, f := range others {
		name := f.Name()
		var classifier, ext, problem string
		if rest, ok := strings.CutPrefix(name, base); ok && (strings.HasPrefix(rest, "-") || strings.HasPrefix(rest, ".")) {
			if strings.HasPrefix(rest, "-") {
				dot := strings.IndexByte(rest, '.')
				if dot < 2 {
					problem = "name it " + base + "-<classifier>.<ext>"
				} else {
					classifier, ext = rest[1:dot], rest[dot+1:]
				}
			} else {
				ext = rest[1:]
			}
		} else if free {
			problem = "name the files " + base + "[-<classifier>].<ext>; only one file may have another name"
		} else {
			free = true
			if dot := strings.LastIndexByte(name, '.'); dot < 1 {
				problem = "the file has no extension"
			} else {
				ext = strings.ToLower(name[dot+1:])
			}
		}
		if problem == "" && (!extRE.MatchString(ext) || (classifier != "" && !artifactRE.MatchString(classifier))) {
			problem = "invalid classifier or extension"
		}
		target := ""
		if problem == "" {
			file := base + "." + ext
			if classifier != "" {
				file = base + "-" + classifier + "." + ext
			} else if s.mainExt == "" {
				s.mainExt = ext
			}
			target = s.c.dir() + "/" + file
			if _, err := repopath.Parse(target); err != nil {
				problem = "invalid path " + target
			} else if slices.ContainsFunc(s.arts, func(a artifactFile) bool { return a.target == target }) {
				problem = "two files go to " + file
			}
		}
		if problem != "" {
			problems = append(problems, f.Path+": "+problem)
			target = ""
		} else {
			s.arts = append(s.arts, artifactFile{f.Path, target})
		}
		targets = append(targets, format.PlanTarget{Path: f.Path, Target: target})
	}
	if pomFile == nil && len(others) == 0 {
		problems = append(problems, "upload a pom, an artifact, or both")
	}
	return s, targets, problems, fromPOM
}

// coordProblem checks the coordinates of an upload: the field of the first invalid one and the
// problem; "", "" when they are valid.
func coordProblem(c coords) (key, msg string) {
	switch {
	case !groupRE.MatchString(c.group) || len(c.group) > 200:
		return "groupId", "give a valid groupId (in the pom or the Group ID field)"
	case !artifactRE.MatchString(c.artifact) || c.artifact == "." || c.artifact == "..":
		return "artifactId", "give a valid artifactId (in the pom or the Artifact ID field)"
	case !versionRE.MatchString(c.version) || c.version == "." || c.version == "..":
		return "version", "give a valid version (in the pom or the Version field)"
	}
	return "", ""
}

// refusalText is the message of a refusal, or the error's text.
func refusalText(err error) string {
	if r, ok := err.(*assets.Refusal); ok {
		return r.Message
	}
	return err.Error()
}

// Plan plans a web UI upload: a dropped folder (files with folder paths, or more than one pom) as
// the artifacts planFolder finds, otherwise one artifact (planSet) with the coordinate fields.
func (h *Handler) Plan(ctx context.Context, repo *repos.Repo, in format.PlanInput) (*format.Plan, error) {
	if err := format.CheckFields(in.Fields, coordFields); err != nil {
		return nil, err
	}
	poms := 0
	for _, f := range in.Files {
		if isPOMName(f.Name()) {
			poms++
		}
	}
	if format.InFolder(in.Files) || poms > 1 {
		for k := range in.Fields {
			return nil, format.BadUpload("unknown field %q; a folder upload takes no fields", k)
		}
		return planFolder(in.Files), nil
	}
	s, targets, problems, fromPOM := planSet(in.Files, in.Fields)
	item := format.PlanItem{Title: "Artifact", Files: targets, Meta: s}
	key, msg := coordProblem(s.c)
	if key == "" {
		item.Title = s.c.group + ":" + s.c.artifact + ":" + s.c.version
		item.Facts = map[string]string{"pom": s.pomPath()}
	}
	plan := &format.Plan{}
	for _, f := range coordFields {
		read := map[string]string{"groupId": fromPOM.group, "artifactId": fromPOM.artifact, "version": fromPOM.version, "packaging": fromPOM.packaging}[f.Key]
		if f.Key == "packaging" && s.pom != "" {
			continue // only a generated pom takes it
		}
		pf := format.PlanField{Field: f, Value: strings.TrimSpace(in.Fields[f.Key])}
		if read != "" {
			pf.Value, pf.Read = read, true
		}
		plan.Fields = append(plan.Fields, pf)
	}
	// A coordinate problem belongs to the field that gives the coordinate.
	if key != "" {
		for i := range plan.Fields {
			if f := &plan.Fields[i]; f.Key == key {
				f.Problem = "Enter a valid " + f.Label + ", e.g. " + f.Placeholder + "."
			}
		}
		problems = slices.DeleteFunc(problems, func(p string) bool { return p == msg })
	}
	item.Problems = problems
	plan.Items = []format.PlanItem{item}
	return plan, nil
}

// planFolder plans a dropped folder. Each pom (*.pom, or pom.xml in a Maven-layout folder) is one
// artifact: its files are the ones next to it named <artifactId>-<version>[-<classifier>].<ext>,
// .module files included. Checksum and signature files are left out, since the server writes the
// checksums. In a Maven-layout folder (<group path>/<artifactId>/<version>/) the pom's
// coordinates must match the path. Every other file is left out.
func planFolder(files []format.PlanFile) *format.Plan {
	type group struct {
		pom      int   // index in files
		others   []int // indexes of the artifacts next to it
		c        coords
		base     string // <artifactId>-<version> from the pom
		altBase  string // the same from the folder path when it disagrees with the pom; "" otherwise
		title    string
		problems []string
	}
	var groups []*group
	byDir := map[string][]*group{}
	for i, f := range files {
		if !isPOMName(f.Name()) {
			continue
		}
		dir := dirOf(f.Path)
		segs := []string{}
		if dir != "" {
			segs = strings.Split(dir, "/")
		}
		g := &group{pom: i, title: f.Path}
		p, err := readPOM(f)
		if err != nil {
			if strings.ToLower(f.Name()) == "pom.xml" {
				continue // a pom.xml outside a Maven layout is a project file, not an artifact
			}
			g.problems = append(g.problems, f.Path+": "+refusalText(err))
			groups = append(groups, g)
			continue
		}
		g.c = coords{p.GroupID, p.ArtifactID, p.Version, p.Packaging}
		g.base = g.c.artifact + "-" + g.c.version
		layout := false
		if n := len(segs); n >= 2 {
			a, v := segs[n-2], segs[n-1]
			layout = f.Name() == a+"-"+v+".pom" || (strings.ToLower(f.Name()) == "pom.xml" && a == g.c.artifact && v == g.c.version)
			if layout && a+"-"+v != g.base {
				g.altBase = a + "-" + v
			}
		}
		if strings.ToLower(f.Name()) == "pom.xml" && !layout {
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
		if isPOMName(f.Name()) || isIgnored(f.Name()) {
			continue
		}
		var best *group
		bestLen := 0
		for _, g := range byDir[dirOf(f.Path)] {
			for _, base := range []string{g.base, g.altBase} {
				if base == "" || len(base) <= bestLen {
					continue
				}
				if rest, ok := strings.CutPrefix(f.Name(), base); ok && (strings.HasPrefix(rest, ".") || strings.HasPrefix(rest, "-")) {
					best, bestLen = g, len(base)
				}
			}
		}
		if best != nil {
			best.others = append(best.others, i)
		}
	}

	seen := map[string]string{}
	plan := &format.Plan{}
	taken := map[string]bool{}
	for _, g := range groups {
		if g.c.artifact != "" {
			key := g.c.dir()
			if first, ok := seen[key]; ok {
				g.problems = append(g.problems, g.title+" is also at "+first)
			} else {
				seen[key] = files[g.pom].Path
			}
		}
		set := []format.PlanFile{files[g.pom]}
		for _, i := range g.others {
			set = append(set, files[i])
		}
		item := format.PlanItem{Title: g.title}
		if len(g.problems) == 0 {
			s, targets, problems, _ := planSet(set, nil)
			item.Files, item.Problems, item.Meta = targets, problems, s
		} else {
			for _, f := range set {
				item.Files = append(item.Files, format.PlanTarget{Path: f.Path})
			}
			item.Problems = g.problems
		}
		for _, f := range set {
			taken[f.Path] = true
		}
		plan.Items = append(plan.Items, item)
	}
	for _, f := range files {
		if !taken[f.Path] {
			plan.Ignored = append(plan.Ignored, f.Path)
		}
	}
	return plan
}

// Store stores each artifact of a plan with its checksums and its maven-metadata.xml. The plan
// has no problem (the core checked it).
func (h *Handler) Store(ctx context.Context, repo *repos.Repo, plan *format.Plan, files map[string]*assets.Spooled, o assets.Origin) ([]*assets.Result, error) {
	var out []*assets.Result
	for _, it := range plan.Items {
		s, ok := it.Meta.(*setPlan)
		if !ok {
			continue
		}
		rs, err := h.storeSet(ctx, repo, s, files, o)
		out = append(out, rs...)
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

// storeSet stores one set: its artifacts and its pom (uploaded, or generated with source
// generated), each with its .md5, .sha1 and .sha256 files (source generated), in one commit; then
// the artifact's maven-metadata.xml.
func (h *Handler) storeSet(ctx context.Context, repo *repos.Repo, s *setPlan, files map[string]*assets.Spooled, o assets.Origin) ([]*assets.Result, error) {
	gen := o
	gen.Source = assets.SourceGenerated
	var ws []assets.Write
	add := func(p string, sp *assets.Spooled, src assets.Origin) error {
		rp, err := repopath.Parse(p)
		if err != nil {
			return format.BadUpload("invalid path %s", p)
		}
		if sp == nil {
			return format.BadUpload("the upload has no file for %s", p)
		}
		ws = append(ws, assets.Write{Repo: repo, Path: rp, Body: assets.Body{Spool: sp}, Origin: src})
		ws = append(ws, checksumWrites(repo, rp, sp, gen)...)
		return nil
	}
	for _, a := range s.arts {
		if err := add(a.target, files[a.src], o); err != nil {
			return nil, err
		}
	}
	if s.pom != "" {
		if err := add(s.pomPath(), files[s.pom], o); err != nil {
			return nil, err
		}
	} else if err := add(s.pomPath(), blobs.Bytes(generatedPOM(s.c, s.packaging())), gen); err != nil {
		return nil, err
	}
	rs, err := h.assets.CommitAll(ctx, ws)
	if err != nil {
		return nil, err
	}
	md, err := h.updateMetadata(ctx, repo, s.c, gen)
	return append(rs, md...), err
}
