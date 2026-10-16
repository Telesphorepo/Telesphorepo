package maven

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"time"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/demo"
	"github.com/Telesphorepo/Telesphorepo/internal/module"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// The Maven part of the demo dataset (module.DemoData): a large public repo with Gradle plugins
// and a small private one. Most artifacts are built on GitHub Actions, so their builds have
// CI build records; a -SNAPSHOT version holds several timestamped builds.

const (
	demoPublicFiles   = 150_000 // public Maven repo, Gradle plugin markers included
	demoInternalFiles = 2_000   // private Maven repo
)

// demoRepos are the Maven repos of the demo dataset.
var demoRepos = []module.DemoRepo{
	{Name: "maven", Description: "Public Maven and Gradle artifacts", Public: true},
	{Name: "internal", Description: "Internal libraries; sign-in required"},
}

// Demo fills a Maven demo repo.
func (h *Handler) Demo(ctx context.Context, run *demo.Run, repo *repos.Repo, rng *rand.Rand) error {
	plan := mavenPlan{groups: 400, maxArtifacts: 60, plugins: 30, target: demoPublicFiles, orgs: publicOrgs}
	if !repo.AnonymousRead {
		plan = mavenPlan{groups: 14, maxArtifacts: 24, target: demoInternalFiles, orgs: internalOrgs}
	}
	l := planMaven(rng.Uint64(), plan)
	run.Expect(l.files())
	return l.emit(run, repo)
}

var (
	publicOrgs = []string{
		"acme", "northwind", "contoso", "globex", "initech", "brightline", "bluepeak", "ironbark",
		"lumen", "quartzlabs", "redwood", "sunbeam", "tidewater", "zephyr", "kestrel", "fernhill",
		"pinecone", "orbital", "copperleaf", "silverlake", "harborview", "meridian", "nimbus",
		"polaris", "cobalt", "example",
	}
	internalOrgs = []string{"northwind"}
	mavenTLDs    = []string{"com", "com", "com", "org", "org", "io", "io", "net", "dev"}
	mavenAreas   = []string{
		"platform", "data", "tools", "cloud", "security", "billing", "search", "mobile", "infra",
		"analytics", "payments", "identity", "messaging", "observability", "commerce", "media",
		"storage", "network", "ml", "devops", "catalog", "shipping", "ledger", "notify",
	}
	mavenSubs = []string{
		"cli", "core", "api", "client", "server", "common", "sdk", "testing", "gradle", "spring",
		"batch", "stream", "web", "admin", "events", "jobs",
	}
	mavenBases = []string{
		"core", "api", "client", "server", "common", "model", "util", "config", "testing", "cli",
		"sdk", "runtime", "engine", "gateway", "worker", "scheduler", "cache", "codec", "json",
		"schema", "metrics", "tracing", "auth", "store", "query", "bom", "starter", "annotations",
		"processor", "protocol", "events", "migrations", "fixtures", "mock", "bench", "parser",
		"validation", "retry", "pool", "router", "serializer", "dsl",
	}
	mavenSuffixes = []string{"kotlin", "jvm", "spring", "reactive", "grpc", "jdbc", "jackson", "coroutines"}
	pluginWords   = []string{
		"conventions", "publishing", "docker", "protobuf", "codegen", "versioning", "lint", "release",
		"coverage", "openapi", "licenses", "quality", "sbom", "dependency-check", "native", "jmh",
	}
	descWords = []string{
		"fast", "small", "typed", "reactive", "pluggable", "embeddable", "resilient", "streaming",
		"declarative", "portable", "lightweight", "observable",
	}
	descNouns = []string{
		"client", "toolkit", "library", "runtime", "framework", "adapter", "bridge", "layer",
		"helpers", "extensions",
	}
	// ciActors are the GitHub users who start the demo workflow runs.
	ciActors = []string{"jonas-k", "mei-lin", "priya-r", "tomas-n", "release-bot", "dependabot[bot]"}
)

// mavenPlan sets the shape of a Maven demo repo.
type mavenPlan struct {
	groups, maxArtifacts int
	plugins              int // Gradle plugins, each with its marker artifact
	target               int // files, about
	orgs                 []string
}

type mavenLayout struct {
	seed uint64
	arts []*mvnArtifact
}

type mvnArtifact struct {
	group, id, desc string
	u               float64 // draw for the version count
	javadoc, module bool
	snapshot        bool // the newest version is a -SNAPSHOT
	builds          int  // the snapshot's timestamped builds
	ci              bool // built on GitHub Actions: its builds have CI build records
	ghRepo          string
	plugin          string
	deps            []*mvnArtifact // earlier artifacts of the same group
	junit           bool
	n               int
	versions        []string
	times           []int64
	owner           int
}

// planMaven lays out groups and artifacts and picks version counts so that the repo holds about
// p.target files. Version counts run from 1 to 40; a few artifacts have many.
func planMaven(seed uint64, p mavenPlan) *mavenLayout {
	rng := demo.Rand(seed, 0)
	l := &mavenLayout{seed: seed}

	seen := map[string]bool{}
	var groups []string
	for len(groups) < p.groups {
		g := demo.Pick(rng, mavenTLDs) + "." + demo.Pick(rng, p.orgs)
		if x := rng.IntN(100); x >= 15 {
			g += "." + demo.Pick(rng, mavenAreas)
			if x >= 70 {
				g += "." + demo.Pick(rng, mavenSubs)
			}
		}
		if !seen[g] {
			seen[g] = true
			groups = append(groups, g)
		}
	}
	slices.Sort(groups)

	// Zipf-like artifact counts: the group of rank r gets about max/r^0.8, at least 3 to 6.
	ranks := rng.Perm(len(groups))
	pluginGroups := map[int]bool{}
	for _, i := range rng.Perm(len(groups))[:min(p.plugins, len(groups))] {
		pluginGroups[i] = true
	}
	for gi, g := range groups {
		count := int(math.Round(float64(p.maxArtifacts) / math.Pow(float64(ranks[gi]+1), 0.8)))
		if count <= 3 {
			count = 3 + rng.IntN(4)
		}
		count = min(count, p.maxArtifacts)
		last := g[strings.LastIndexByte(g, '.')+1:]
		names := map[string]bool{}
		var arts []*mvnArtifact
		for len(arts) < count {
			id := demo.Pick(rng, mavenBases)
			if rng.IntN(2) == 0 {
				id = last + "-" + id
			}
			if rng.IntN(5) == 0 {
				id += "-" + demo.Pick(rng, mavenSuffixes)
			}
			if names[id] {
				continue
			}
			names[id] = true
			arts = append(arts, l.newArtifact(rng, g, id))
		}
		if pluginGroups[gi] {
			word := demo.Pick(rng, pluginWords)
			id := word + "-gradle-plugin"
			if !names[id] {
				a := l.newArtifact(rng, g, id)
				a.plugin, a.module, a.snapshot = g+"."+word, true, false
				a.desc = "Gradle plugin " + a.plugin
				arts = append(arts, a)
			}
		}
		for i, a := range arts {
			for d := rng.IntN(4); d > 0 && i > 0; d-- {
				dep := arts[rng.IntN(i)]
				if dep.plugin == "" && !slices.Contains(a.deps, dep) {
					a.deps = append(a.deps, dep)
				}
			}
		}
		l.arts = append(l.arts, arts...)
	}

	// The exponent k shapes the version counts: 1 + 39*u^k. Find the smallest k that keeps the
	// repo at or under the target.
	lo, hi := 0.2, 400.0
	for range 60 {
		mid := (lo + hi) / 2
		if l.filesAt(mid) > p.target {
			lo = mid
		} else {
			hi = mid
		}
	}
	for i, a := range l.arts {
		a.n = versionCount(a.u, hi)
		vr := demo.Rand(seed, uint64(i)+1)
		a.versions = mvnVersions(vr, a.n, a.snapshot && a.n >= 2)
		a.times = demo.Times(vr, a.n)
	}
	return l
}

func (l *mavenLayout) newArtifact(rng *rand.Rand, group, id string) *mvnArtifact {
	org := strings.Split(group, ".")[1]
	return &mvnArtifact{
		group: group, id: id,
		desc:     "A " + demo.Pick(rng, descWords) + " " + demo.Pick(rng, descWords) + " " + strings.ReplaceAll(id, "-", " ") + " " + demo.Pick(rng, descNouns) + ".",
		u:        rng.Float64(),
		javadoc:  rng.IntN(2) == 0,
		module:   rng.IntN(5) < 2,
		snapshot: rng.IntN(5) == 0,
		builds:   2 + rng.IntN(11),
		ci:       rng.IntN(4) > 0,
		ghRepo:   org + "/" + group[strings.LastIndexByte(group, '.')+1:],
		junit:    rng.IntN(2) == 0,
		owner:    rng.IntN(16),
	}
}

func versionCount(u, k float64) int { return 1 + int(39*math.Pow(u, k)) }

func (l *mavenLayout) filesAt(k float64) int {
	n := 0
	for _, a := range l.arts {
		n += a.fileCount(versionCount(a.u, k))
	}
	return n
}

func (l *mavenLayout) files() int {
	n := 0
	for _, a := range l.arts {
		n += a.fileCount(a.n)
	}
	return n
}

// perBuild is the number of files one build stores: the pom, jar, sources jar and maybe javadoc
// jar and .module, each with three checksums.
func (a *mvnArtifact) perBuild() int {
	per := 3
	if a.javadoc {
		per++
	}
	if a.module {
		per++
	}
	return per * 4
}

// fileCount is the number of files the artifact stores with n versions: one build per release,
// a.builds builds and a maven-metadata.xml (with checksums) for a snapshot, the artifact's
// maven-metadata.xml, and for a Gradle plugin the marker's poms and metadata.
func (a *mvnArtifact) fileCount(n int) int {
	f := 4 + n*a.perBuild()
	releases := n
	if a.snapshot && n >= 2 {
		f += (a.builds-1)*a.perBuild() + 4
		releases--
	}
	if a.plugin != "" {
		f += 4 + releases*4
	}
	return f
}

// mvnVersions returns n ascending versions; with snapshot the last one is the -SNAPSHOT of the
// next minor version.
func mvnVersions(rng *rand.Rand, n int, snapshot bool) []string {
	major := []int{0, 1, 1, 1, 2, 2, 3, 4}[rng.IntN(8)]
	minor, patch := rng.IntN(4), 0
	releases := n
	if snapshot {
		releases--
	}
	out := make([]string, 0, n)
	lastMajor, lastMinor := major, minor
	for len(out) < releases {
		out = append(out, fmt.Sprintf("%d.%d.%d", major, minor, patch))
		lastMajor, lastMinor = major, minor
		switch x := rng.IntN(100); {
		case x < 60:
			patch++
		case x < 93:
			minor, patch = minor+1, 0
		default:
			major, minor, patch = major+1, 0, 0
		}
	}
	if snapshot {
		out = append(out, fmt.Sprintf("%d.%d.0-SNAPSHOT", lastMajor, lastMinor+1))
	}
	return out
}

// emit stores the layout in repo.
func (l *mavenLayout) emit(run *demo.Run, repo *repos.Repo) error {
	for i, a := range l.arts {
		rng := demo.Rand(l.seed, uint64(i)+1_000_000)
		files, err := a.build(run, rng, repo)
		if err != nil {
			return err
		}
		for _, f := range files {
			if err := run.Add(repo, f); err != nil {
				return err
			}
		}
	}
	return nil
}

// withSums returns f and its .md5, .sha1 and .sha256 files (lowercase hex, as Maven writes them).
func withSums(f demo.File) []demo.File {
	m, s1, s256 := md5.Sum(f.Data), sha1.Sum(f.Data), sha256.Sum256(f.Data)
	out := []demo.File{f}
	for _, s := range []struct {
		ext string
		sum []byte
	}{{".md5", m[:]}, {".sha1", s1[:]}, {".sha256", s256[:]}} {
		c := f
		c.Path += s.ext
		c.Data = []byte(hex.EncodeToString(s.sum))
		out = append(out, c)
	}
	return out
}

// ciBuild records a GitHub Actions run of the artifact at time at that published ref, and
// returns its build record (0 when the artifact is not built on CI).
func (a *mvnArtifact) ciBuild(run *demo.Run, rng *rand.Rand, ref, workflow, file string, at int64) (int64, error) {
	if !a.ci {
		return 0, nil
	}
	attempt := "1"
	if rng.IntN(12) == 0 {
		attempt = "2"
	}
	return run.Build(map[string]any{
		"repository": a.ghRepo, "sha": demo.Hex(rng, 40), "ref": ref,
		"run_id": fmt.Sprint(4_000_000_000 + rng.Int64N(6_000_000_000)), "run_attempt": attempt, "workflow": workflow,
		"workflow_ref": a.ghRepo + "/.github/workflows/" + file + "@" + ref, "actor": demo.Pick(rng, ciActors),
	}, at)
}

// uploader is by, or none for a file a CI build published: a CI session has no user.
func uploader(by, build int64) int64 {
	if build != 0 {
		return 0
	}
	return by
}

// build returns every file of the artifact.
func (a *mvnArtifact) build(run *demo.Run, rng *rand.Rand, repo *repos.Repo) ([]demo.File, error) {
	dir := strings.ReplaceAll(a.group, ".", "/") + "/" + a.id
	var out []demo.File
	var berr error
	ciBuild := func(ref, workflow, file string, at int64) int64 {
		id, err := a.ciBuild(run, rng, ref, workflow, file, at)
		if berr == nil {
			berr = err
		}
		return id
	}
	add := func(f demo.File) { out = append(out, withSums(f)...) }
	var lastBy, lastAt int64
	sessions := map[string]int64{} // release version -> its build record
	for vi, v := range a.versions {
		at := run.Anchor - a.times[vi]
		by := run.Uploader(rng, repo, a.owner)
		lastBy, lastAt = by, at
		vdir := dir + "/" + v
		snap := strings.HasSuffix(v, "-SNAPSHOT")
		jar := demoJar(a, v, "")
		sources := demoJar(a, v, "sources")
		var javadoc []byte
		if a.javadoc {
			javadoc = demoJar(a, v, "javadoc")
		}
		// files stores one build: fileVersion is the version in the file names.
		files := func(fileVersion string, at, session int64) {
			base := vdir + "/" + a.id + "-" + fileVersion
			file := func(p string, data []byte) demo.File {
				return demo.File{Path: p, Data: data, Kind: assets.KindArtifact, By: uploader(by, session), At: at, Build: session}
			}
			jar, sources, javadoc := jar, sources, javadoc
			if fileVersion != v {
				// Each snapshot build has bytes of its own.
				jar, sources = demoJar(a, fileVersion, ""), demoJar(a, fileVersion, "sources")
				if a.javadoc {
					javadoc = demoJar(a, fileVersion, "javadoc")
				}
			}
			add(file(base+".pom", a.pom(v)))
			add(file(base+".jar", jar))
			add(file(base+"-sources.jar", sources))
			if a.javadoc {
				add(file(base+"-javadoc.jar", javadoc))
			}
			if a.module {
				add(file(base+".module", a.gradleModule(v, fileVersion, jar, sources, javadoc)))
			}
		}
		if !snap {
			s := ciBuild("refs/tags/v"+v, "Release", "release.yml", at)
			sessions[v] = s
			files(v, at, s)
			continue
		}
		// A snapshot: a.builds builds from main, a few hours to days apart, the newest at at.
		base := strings.TrimSuffix(v, "-SNAPSHOT")
		gap := (2 + rng.Int64N(70)) * 3600
		var stamp string
		var session int64
		for b := range a.builds {
			bat := at - int64(a.builds-1-b)*gap - rng.Int64N(1800)
			if b == a.builds-1 {
				bat = at
			}
			stamp = time.Unix(bat, 0).UTC().Format("20060102.150405")
			session = ciBuild("refs/heads/main", "Snapshot", "snapshot.yml", bat)
			files(base+"-"+stamp+"-"+fmt.Sprint(b+1), bat, session)
		}
		add(demo.File{Path: vdir + "/maven-metadata.xml", Data: a.snapshotMetadata(v, base+"-"+stamp+"-"+fmt.Sprint(a.builds), stamp, a.builds, at),
			Kind: assets.KindMetadata, By: uploader(by, session), At: at, Build: session})
	}
	add(demo.File{Path: dir + "/maven-metadata.xml", Data: mavenMetadata(a.group, a.id, a.versions, lastAt),
		Kind: assets.KindMetadata, By: lastBy, At: lastAt})

	if a.plugin != "" {
		mid := a.plugin + pluginSuffix
		mdir := strings.ReplaceAll(a.plugin, ".", "/") + "/" + mid
		var rel []string
		for vi, v := range a.versions {
			if strings.HasSuffix(v, "-SNAPSHOT") {
				continue
			}
			rel = append(rel, v)
			at := run.Anchor - a.times[vi]
			add(demo.File{Path: mdir + "/" + v + "/" + mid + "-" + v + ".pom", Data: a.markerPom(v),
				Kind: assets.KindArtifact, By: uploader(lastBy, sessions[v]), At: at, Build: sessions[v]})
			lastAt = at
		}
		add(demo.File{Path: mdir + "/maven-metadata.xml", Data: mavenMetadata(a.plugin, mid, rel, lastAt),
			Kind: assets.KindMetadata, By: lastBy, At: lastAt})
	}
	return out, berr
}

// latestRelease is the newest non-snapshot version.
func (a *mvnArtifact) latestRelease() string {
	for i := len(a.versions) - 1; i >= 0; i-- {
		if !strings.HasSuffix(a.versions[i], "-SNAPSHOT") {
			return a.versions[i]
		}
	}
	return a.versions[0]
}

const pomHead = `<?xml version="1.0" encoding="UTF-8"?>
<project xmlns="http://maven.apache.org/POM/4.0.0" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:schemaLocation="http://maven.apache.org/POM/4.0.0 https://maven.apache.org/xsd/maven-4.0.0.xsd">
`

func (a *mvnArtifact) pom(v string) []byte {
	var b strings.Builder
	b.WriteString(pomHead)
	if a.module {
		b.WriteString("  <!-- do_not_remove: published-with-gradle-metadata -->\n")
	}
	fmt.Fprintf(&b, "  <modelVersion>4.0.0</modelVersion>\n  <groupId>%s</groupId>\n  <artifactId>%s</artifactId>\n  <version>%s</version>\n", a.group, a.id, v)
	fmt.Fprintf(&b, "  <packaging>jar</packaging>\n  <name>%s</name>\n  <description>%s</description>\n", a.id, a.desc)
	fmt.Fprintf(&b, "  <url>https://%s.example.com/%s</url>\n", strings.Split(a.group, ".")[1], a.id)
	b.WriteString("  <licenses>\n    <license>\n      <name>Apache-2.0</name>\n      <url>https://www.apache.org/licenses/LICENSE-2.0.txt</url>\n    </license>\n  </licenses>\n")
	if len(a.deps) > 0 || a.junit {
		b.WriteString("  <dependencies>\n")
		for _, d := range a.deps {
			fmt.Fprintf(&b, "    <dependency>\n      <groupId>%s</groupId>\n      <artifactId>%s</artifactId>\n      <version>%s</version>\n      <scope>compile</scope>\n    </dependency>\n", d.group, d.id, d.latestRelease())
		}
		if a.junit {
			b.WriteString("    <dependency>\n      <groupId>org.junit.jupiter</groupId>\n      <artifactId>junit-jupiter</artifactId>\n      <version>5.11.3</version>\n      <scope>test</scope>\n    </dependency>\n")
		}
		b.WriteString("  </dependencies>\n")
	}
	b.WriteString("</project>\n")
	return []byte(b.String())
}

func (a *mvnArtifact) markerPom(v string) []byte {
	mid := a.plugin + ".gradle.plugin"
	return []byte(pomHead + fmt.Sprintf(`  <modelVersion>4.0.0</modelVersion>
  <groupId>%s</groupId>
  <artifactId>%s</artifactId>
  <version>%s</version>
  <packaging>pom</packaging>
  <name>%s</name>
  <description>%s</description>
  <dependencies>
    <dependency>
      <groupId>%s</groupId>
      <artifactId>%s</artifactId>
      <version>%s</version>
    </dependency>
  </dependencies>
</project>
`, a.plugin, mid, v, a.plugin, a.desc, a.group, a.id, v))
}

func mavenMetadata(group, id string, versions []string, at int64) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<metadata>\n  <groupId>%s</groupId>\n  <artifactId>%s</artifactId>\n  <versioning>\n", group, id)
	if len(versions) > 0 {
		fmt.Fprintf(&b, "    <latest>%s</latest>\n", versions[len(versions)-1])
	}
	for i := len(versions) - 1; i >= 0; i-- {
		if !strings.HasSuffix(versions[i], "-SNAPSHOT") {
			fmt.Fprintf(&b, "    <release>%s</release>\n", versions[i])
			break
		}
	}
	b.WriteString("    <versions>\n")
	for _, v := range versions {
		fmt.Fprintf(&b, "      <version>%s</version>\n", v)
	}
	fmt.Fprintf(&b, "    </versions>\n    <lastUpdated>%s</lastUpdated>\n  </versioning>\n</metadata>\n", time.Unix(at, 0).UTC().Format("20060102150405"))
	return []byte(b.String())
}

func (a *mvnArtifact) snapshotMetadata(v, fileVersion, stamp string, buildNumber int, at int64) []byte {
	updated := time.Unix(at, 0).UTC().Format("20060102150405")
	var b strings.Builder
	fmt.Fprintf(&b, "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<metadata modelVersion=\"1.1.0\">\n  <groupId>%s</groupId>\n  <artifactId>%s</artifactId>\n  <version>%s</version>\n  <versioning>\n", a.group, a.id, v)
	fmt.Fprintf(&b, "    <snapshot>\n      <timestamp>%s</timestamp>\n      <buildNumber>%d</buildNumber>\n    </snapshot>\n    <lastUpdated>%s</lastUpdated>\n    <snapshotVersions>\n", stamp, buildNumber, updated)
	type sv struct{ classifier, ext string }
	list := []sv{{"", "pom"}, {"", "jar"}, {"sources", "jar"}}
	if a.javadoc {
		list = append(list, sv{"javadoc", "jar"})
	}
	if a.module {
		list = append(list, sv{"", "module"})
	}
	for _, s := range list {
		b.WriteString("      <snapshotVersion>\n")
		if s.classifier != "" {
			fmt.Fprintf(&b, "        <classifier>%s</classifier>\n", s.classifier)
		}
		fmt.Fprintf(&b, "        <extension>%s</extension>\n        <value>%s</value>\n        <updated>%s</updated>\n      </snapshotVersion>\n", s.ext, fileVersion, updated)
	}
	b.WriteString("    </snapshotVersions>\n  </versioning>\n</metadata>\n")
	return []byte(b.String())
}

// demoJar is a valid jar (zip) with a manifest; a sources jar adds a package-info.java and a
// javadoc jar an index.html.
func demoJar(a *mvnArtifact, v, classifier string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	entry := func(name, body string) {
		f, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: demo.ZipTime})
		if err != nil {
			panic(err) // writing to a bytes.Buffer does not fail
		}
		f.Write([]byte(body))
	}
	entry("META-INF/MANIFEST.MF", fmt.Sprintf("Manifest-Version: 1.0\r\nCreated-By: Telesphorepo demo data\r\nImplementation-Title: %s:%s\r\nImplementation-Version: %s\r\n\r\n", a.group, a.id, v))
	pkg := strings.ReplaceAll(a.group+"."+strings.ReplaceAll(a.id, "-", ""), ".", "/")
	switch classifier {
	case "sources":
		entry(pkg+"/package-info.java", "/** "+a.desc+" */\npackage "+strings.ReplaceAll(pkg, "/", ".")+";\n")
	case "javadoc":
		entry("index.html", "<!DOCTYPE html>\n<html><head><title>"+a.id+" "+v+" API</title></head><body><h1>"+a.id+" "+v+"</h1><p>"+a.desc+"</p></body></html>\n")
	}
	if err := zw.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

type gmFile struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	Size   int    `json:"size"`
	SHA512 string `json:"sha512"`
	SHA256 string `json:"sha256"`
	SHA1   string `json:"sha1"`
	MD5    string `json:"md5"`
}

type gmDep struct {
	Group   string            `json:"group"`
	Module  string            `json:"module"`
	Version map[string]string `json:"version"`
}

type gmVariant struct {
	Name         string         `json:"name"`
	Attributes   map[string]any `json:"attributes"`
	Dependencies []gmDep        `json:"dependencies,omitempty"`
	Files        []gmFile       `json:"files"`
}

func gmFileOf(name string, data []byte) gmFile {
	s512, s256, s1, m := sha512.Sum512(data), sha256.Sum256(data), sha1.Sum(data), md5.Sum(data)
	return gmFile{Name: name, URL: name, Size: len(data), SHA512: hex.EncodeToString(s512[:]),
		SHA256: hex.EncodeToString(s256[:]), SHA1: hex.EncodeToString(s1[:]), MD5: hex.EncodeToString(m[:])}
}

// gradleModule is Gradle module metadata (.module) for the version's jars.
func (a *mvnArtifact) gradleModule(v, fileVersion string, jar, sources, javadoc []byte) []byte {
	status := "release"
	if strings.HasSuffix(v, "-SNAPSHOT") {
		status = "integration"
	}
	var deps []gmDep
	for _, d := range a.deps {
		deps = append(deps, gmDep{Group: d.group, Module: d.id, Version: map[string]string{"requires": d.latestRelease()}})
	}
	lib := func(usage string) map[string]any {
		return map[string]any{
			"org.gradle.category": "library", "org.gradle.dependency.bundling": "external",
			"org.gradle.jvm.version": 17, "org.gradle.libraryelements": "jar", "org.gradle.usage": usage,
		}
	}
	doc := func(kind string) map[string]any {
		return map[string]any{
			"org.gradle.category": "documentation", "org.gradle.dependency.bundling": "external",
			"org.gradle.docstype": kind, "org.gradle.usage": "java-runtime",
		}
	}
	jarFile := gmFileOf(a.id+"-"+fileVersion+".jar", jar)
	variants := []gmVariant{
		{Name: "apiElements", Attributes: lib("java-api"), Dependencies: deps, Files: []gmFile{jarFile}},
		{Name: "runtimeElements", Attributes: lib("java-runtime"), Dependencies: deps, Files: []gmFile{jarFile}},
		{Name: "sourcesElements", Attributes: doc("sources"), Files: []gmFile{gmFileOf(a.id+"-"+fileVersion+"-sources.jar", sources)}},
	}
	if javadoc != nil {
		variants = append(variants, gmVariant{Name: "javadocElements", Attributes: doc("javadoc"),
			Files: []gmFile{gmFileOf(a.id+"-"+fileVersion+"-javadoc.jar", javadoc)}})
	}
	m := struct {
		FormatVersion string `json:"formatVersion"`
		Component     struct {
			Group      string            `json:"group"`
			Module     string            `json:"module"`
			Version    string            `json:"version"`
			Attributes map[string]string `json:"attributes"`
		} `json:"component"`
		CreatedBy map[string]map[string]string `json:"createdBy"`
		Variants  []gmVariant                  `json:"variants"`
	}{FormatVersion: "1.1", CreatedBy: map[string]map[string]string{"gradle": {"version": "8.14"}}, Variants: variants}
	m.Component.Group, m.Component.Module, m.Component.Version = a.group, a.id, v
	m.Component.Attributes = map[string]string{"org.gradle.status": status}
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		panic(err) // plain structs and maps always marshal
	}
	return append(out, '\n')
}
