package maven

import (
	"context"
	"errors"
	"io"
	"path"
	"slices"
	"strings"
	"unicode"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/ci"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// Snippets of the Maven format (format.Spec Usage and ArtifactUsage). Gradle reads a
// private repo's credentials from the Gradle properties <name>Username and <name>Password
// (credentials(PasswordCredentials)), where <name> is the repo name as a Gradle identifier; Maven
// reads them from the server of ~/.m2/settings.xml whose id is the repo name.

// gradleName turns a repo name into the name of a Gradle repository: "my-libs" -> "myLibs".
func gradleName(repo string) string {
	var b strings.Builder
	up := false
	for _, r := range repo {
		if r == '-' || r == '.' || r == '_' {
			up = b.Len() > 0
			continue
		}
		if up {
			r, up = unicode.ToUpper(r), false
		}
		b.WriteRune(r)
	}
	s := b.String()
	if s == "" || unicode.IsDigit(rune(s[0])) {
		s = "repo" + strings.ToUpper(s[:min(1, len(s))]) + s[min(1, len(s)):]
	}
	return s
}

// snippets builds the snippets of one repo for one purpose and context.
type snippets struct {
	repo    *repos.Repo
	url     string // <base>/maven/<repo>
	name    string // Gradle repository name
	purpose string
	context string
	via     string
	out     []format.Snippet
	remotes []string // the remote URLs the repo serves (a proxy, or a group's proxies)
}

func (h *Handler) newSnippets(repo *repos.Repo, baseURL string) *snippets {
	return &snippets{repo: repo, url: baseURL + "/maven/" + repo.Name, name: gradleName(repo.Name), remotes: h.remotes(repo)}
}

// remotes are the remote URLs that repo serves: its own for a proxy, its proxy members' for a
// group, none for a hosted repo.
func (h *Handler) remotes(repo *repos.Repo) []string {
	ids := []int64{repo.ID}
	if repo.Kind == repos.KindGroup {
		ids = h.host.Repos.Members(repo.ID)
	}
	var out []string
	for _, id := range ids {
		if p, ok := h.host.Repos.Proxy(id); ok {
			out = append(out, p.RemoteURL)
		}
	}
	return out
}

// serves reports whether the repo serves one of the well-known remotes (each with a trailing
// slash), so a build needs no line of its own for it.
func (s *snippets) serves(known ...string) bool {
	for _, r := range s.remotes {
		if slices.Contains(known, strings.TrimRight(r, "/")+"/") {
			return true
		}
	}
	return false
}

// add adds a snippet of tool with the ID <tool>.<key>. file is the file it belongs in ("" for a
// command), title what the block is.
func (s *snippets) add(tool, key, file, title, lang, code, note string) {
	s.out = append(s.out, format.Snippet{ID: tool + "." + key, Tool: tool, Purpose: s.purpose, Context: s.context, Via: s.via,
		File: file, Title: title, Lang: lang, Code: code, Note: note})
}

func (s *snippets) private() bool { return !s.repo.AnonymousRead }

// gradleRepo is the maven { } block of the repo for Kotlin (kts) or Groovy.
func (s *snippets) gradleRepo(kts bool, indent string) string {
	var b strings.Builder
	if kts {
		b.WriteString("maven {\n    name = \"" + s.name + "\"\n    url = uri(\"" + s.url + "\")\n")
		if s.private() {
			b.WriteString("    credentials(PasswordCredentials::class)\n")
		}
	} else {
		b.WriteString("maven {\n    name = '" + s.name + "'\n    url = '" + s.url + "'\n")
		if s.private() {
			b.WriteString("    credentials(PasswordCredentials)\n")
		}
	}
	b.WriteString("}")
	return strings.TrimSuffix(ci.Indent(b.String(), indent), "\n")
}

// gradleRepos adds the block that resolves dependencies (plugins false: repositories in the build
// file) or plugins (plugins true: pluginManagement in the settings file) from the repo, and the
// credentials of a private repo. Dependencies go in the build file because Gradle ignores the
// repositories of the settings file when the build file declares its own, as a gradle init
// project does.
func (s *snippets) gradleRepos(plugins bool) {
	var portal string
	switch {
	case plugins && !s.serves("https://plugins.gradle.org/m2/"):
		portal = "gradlePluginPortal()\n"
	case !plugins && !s.serves("https://repo1.maven.org/maven2/", "https://repo.maven.apache.org/maven2/"):
		portal = "mavenCentral()\n"
	}
	for _, kts := range []bool{true, false} {
		client, file := "gradle-groovy", "build.gradle"
		if kts {
			client, file = "gradle-kotlin", "build.gradle.kts"
		}
		code := "repositories {\n" + s.gradleRepo(kts, "    ") + "\n"
		if portal != "" {
			code += "    " + portal
		}
		code += "}\n"
		note, key := "If the build file has a repositories block, put these lines in it.", "repositories"
		if plugins {
			file = "settings.gradle" + strings.TrimPrefix(file, "build.gradle")
			code = "pluginManagement {\n" + ci.Indent(code, "    ") + "}\n"
			note, key = "Put the pluginManagement block at the top of the settings file.", "plugin-repositories"
		}
		s.add(client, key, file, file, langOf(kts), code, note)
		if s.private() {
			gp := "~/.gradle/gradle.properties"
			s.add(client, "credentials", gp, gp, "properties",
				s.name+"Username=YOUR_USERNAME\n"+s.name+"Password=YOUR_READ_TOKEN\n",
				"Create a read token on Your account › Tokens. In CI, set ORG_GRADLE_PROJECT_"+s.name+"Username and ORG_GRADLE_PROJECT_"+s.name+"Password instead.")
		}
	}
}

func langOf(kts bool) string {
	if kts {
		return "kotlin"
	}
	return "groovy"
}

// mavenRepos adds the pom.xml repository block and the credentials of a private repo.
func (s *snippets) mavenRepos() {
	s.add("maven", "repositories", "pom.xml", "pom.xml", "xml", "<repositories>\n  <repository>\n    <id>"+s.repo.Name+"</id>\n    <url>"+s.url+
		"</url>\n  </repository>\n</repositories>\n", "")
	s.mavenCredentials()
}

func (s *snippets) mavenCredentials() {
	if !s.private() {
		return
	}
	s.add("maven", "credentials", "~/.m2/settings.xml", "~/.m2/settings.xml", "xml", "<settings>\n  <servers>\n    <server>\n      <id>"+s.repo.Name+
		"</id>\n      <username>YOUR_USERNAME</username>\n      <password>YOUR_READ_TOKEN</password>\n    </server>\n  </servers>\n</settings>\n",
		"Create a read token on Your account › Tokens. In CI, write ${env.REPO_USERNAME} and ${env.REPO_TOKEN} instead and set those variables.")
}

// Usage returns the repo snippets: how to use the repo in Gradle (Kotlin and Groovy) and
// Maven builds, and how to publish to it from a computer with the Telesphorepo CLI. The core
// adds the CI files of the tools gradle and maven.
func (h *Handler) Usage(repo *repos.Repo, baseURL string) []format.Snippet {
	s := h.newSnippets(repo, baseURL)
	s.purpose, s.context = format.SnippetUse, format.SnippetRepo
	s.gradleRepos(false)
	s.mavenRepos()
	return append(s.out, format.CLISnippet("telesphorepo", format.CLIPublish,
		"In a Maven project the CLI runs mvn deploy; in a Gradle project, gradle publish of each project that applies maven-publish. The build files stay as they are."))
}

// ciNote goes under the CI files of the Gradle and Maven tools.
const ciNote = "Commit telesphorepo.toml first: Set up a project on this tab writes it."

// tools are the build tools that publish to Maven repos. Their CI jobs need Java. The CLI
// requests the GitHub Actions identity token itself; on Jenkins it reads the one in OIDC_TOKEN.
func tools() []format.Tool {
	java := []ci.WorkflowStep{{Uses: "actions/setup-java@v6", With: [][2]string{{"distribution", "temurin"}, {"java-version", "21"}}}}
	return []format.Tool{
		{ID: "gradle", Label: "Gradle", CINote: ciNote,
			Publish: func(_ *repos.Repo, _, system string) ci.PublishSteps {
				if system == ci.Jenkins {
					return ci.PublishSteps{Build: "./gradlew build", BuildPS: `.\gradlew.bat build`, Publish: format.CLIPublish}
				}
				return ci.PublishSteps{Toolchain: java, Publish: format.CLIPublish}
			}},
		{ID: "maven", Label: "Maven", CINote: ciNote,
			Publish: func(_ *repos.Repo, _, system string) ci.PublishSteps {
				if system == ci.Jenkins {
					return ci.PublishSteps{Build: "mvn -B verify", Publish: format.CLIPublish}
				}
				return ci.PublishSteps{Toolchain: java, Publish: format.CLIPublish}
			}},
	}
}

// readPackaging returns the packaging of the pom at p ("jar" when it has none) and whether it
// has a dependencyManagement section. A missing or unreadable pom gives "jar".
func (h *Handler) readPackaging(ctx context.Context, repo *repos.Repo, p string) (string, bool, error) {
	a, err := h.assets.Get(ctx, repo.ID, p)
	if errors.Is(err, assets.ErrNotFound) || (err == nil && a.Size > maxPOMBytes) {
		return "jar", false, nil
	}
	if err != nil {
		return "", false, err
	}
	body, err := h.assets.Open(ctx, a)
	if err != nil {
		return "jar", false, nil
	}
	defer body.Close()
	var pom struct {
		Packaging string    `xml:"packaging"`
		DM        *struct{} `xml:"dependencyManagement"`
	}
	if decodeXML(io.LimitReader(body, maxPOMBytes), &pom) != nil {
		return "jar", false, nil
	}
	pk := strings.TrimSpace(pom.Packaging)
	if pk == "" {
		pk = "jar"
	}
	return pk, pom.DM != nil, nil
}

// ArtifactUsage returns the snippets of one artifact. p is an artifact folder, a version folder
// or a file in one. The pinned mode names the version of a version folder, else the newest
// release (the newest snapshot when there is none); the latest mode names Gradle's
// latest.release and Maven's RELEASE (latest.integration and LATEST for a snapshot-only
// artifact). A Gradle plugin marker gets a plugins { } block; a BOM an import of the platform; a
// parent pom a <parent> block.
func (h *Handler) ArtifactUsage(ctx context.Context, repo, from *repos.Repo, baseURL, p, v, mode string) (*format.ArtifactUsage, error) {
	dir := strings.Trim(p, "/")
	if dir == "" {
		return nil, nil
	}
	if _, err := h.assets.Get(ctx, from.ID, dir); err == nil {
		dir = path.Dir(dir) // a file: its folder
	} else if !errors.Is(err, assets.ErrNotFound) {
		return nil, err
	} else if v != "" && v != "." && v != ".." && !strings.Contains(v, "/") {
		dir += "/" + v // a version of the artifact folder: its version folder
	}
	dir += "/"
	version := ""
	artDir := dir
	if parent := path.Dir(strings.TrimSuffix(dir, "/")); parent != "." {
		if art, _, id, ok := artifactOf(parent); ok {
			v := path.Base(strings.TrimSuffix(dir, "/"))
			bs, err := h.readBuilds(ctx, from.ID, dir, id, v)
			if err != nil {
				return nil, err
			}
			if len(bs.builds) > 0 {
				artDir, version = art.Path, v
			}
		}
	}
	art, group, id, ok := artifactOf(artDir)
	if !ok {
		return nil, nil
	}
	if version == "" {
		v, err := h.latestVersion(ctx, from, artDir)
		if err != nil || v == "" {
			return nil, err
		}
		version = v
	}
	if art.Kind != "gradle-plugin" {
		pk, dm, err := h.readPackaging(ctx, from, artDir+version+"/"+id+"-"+version+".pom")
		if err != nil {
			return nil, err
		}
		if pk == "pom" {
			art.Kind = "parent"
			if dm {
				art.Kind = "bom"
			}
		}
	}
	if mode != format.VersionLatest {
		mode = format.VersionPinned
	}
	snapshot := strings.HasSuffix(version, "-SNAPSHOT")
	gv, mv, note := version, version, ""
	if mode == format.VersionLatest {
		gv, mv = "latest.release", "RELEASE"
		if snapshot {
			gv, mv = "latest.integration", "LATEST"
		}
		note = "Each build takes the newest version at that moment, so builds are not reproducible. Pin a version for releases."
	}

	s := h.newSnippets(repo, baseURL)
	s.purpose, s.context = format.SnippetUse, format.SnippetArtifact
	coord := group + ":" + id + ":"
	switch art.Kind {
	case "gradle-plugin":
		pluginNote := ""
		if mode == format.VersionLatest {
			pluginNote = "The plugins block takes a fixed version: this is the newest, " + version + "."
		}
		s.gradleRepos(true)
		s.add("gradle-kotlin", "plugin", "build.gradle.kts", "build.gradle.kts", "kotlin", "plugins {\n    id(\""+art.Title+"\") version \""+version+"\"\n}\n", pluginNote)
		s.add("gradle-groovy", "plugin", "build.gradle", "build.gradle", "groovy", "plugins {\n    id '"+art.Title+"' version '"+version+"'\n}\n", pluginNote)
		s.out = order(s.out)
	case "parent":
		s.mavenRepos()
		s.add("maven", "parent", "pom.xml", "pom.xml (parent)", "xml", "<parent>\n  <groupId>"+group+"</groupId>\n  <artifactId>"+id+
			"</artifactId>\n  <version>"+version+"</version>\n</parent>\n", "A parent pom takes a fixed version.")
	default:
		s.gradleRepos(false)
		kt, gr := "implementation(\""+coord+gv+"\")", "implementation '"+coord+gv+"'"
		mdep := "<dependencies>\n  <dependency>\n    <groupId>" + group + "</groupId>\n    <artifactId>" + id +
			"</artifactId>\n    <version>" + mv + "</version>\n  </dependency>\n</dependencies>\n"
		if art.Kind == "bom" {
			kt, gr = "implementation(platform(\""+coord+gv+"\"))", "implementation platform('"+coord+gv+"')"
			mdep = "<dependencyManagement>\n  <dependencies>\n    <dependency>\n      <groupId>" + group + "</groupId>\n      <artifactId>" + id +
				"</artifactId>\n      <version>" + mv + "</version>\n      <type>pom</type>\n      <scope>import</scope>\n    </dependency>\n  </dependencies>\n</dependencyManagement>\n"
		}
		s.add("gradle-kotlin", "dependency", "build.gradle.kts", "build.gradle.kts", "kotlin", "dependencies {\n    "+kt+"\n}\n", note)
		s.add("gradle-groovy", "dependency", "build.gradle", "build.gradle", "groovy", "dependencies {\n    "+gr+"\n}\n", note)
		s.out = order(s.out)
		s.mavenRepos()
		s.add("maven", "dependency", "pom.xml", "pom.xml (dependency)", "xml", mdep, note)
	}
	return &format.ArtifactUsage{Artifact: art, Version: version, Mode: mode, FixedVersion: art.Kind == "parent", Snippets: s.out}, nil
}

// order puts the snippets of each tool together, in the order the tools first appear.
func order(in []format.Snippet) []format.Snippet {
	var tools []string
	by := map[string][]format.Snippet{}
	for _, s := range in {
		if _, ok := by[s.Tool]; !ok {
			tools = append(tools, s.Tool)
		}
		by[s.Tool] = append(by[s.Tool], s)
	}
	out := make([]format.Snippet, 0, len(in))
	for _, c := range tools {
		out = append(out, by[c]...)
	}
	return out
}
