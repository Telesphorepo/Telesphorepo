package maven

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"path"
	"strings"
	"unicode"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// Snippets of the Maven format (format.Describer, format.ArtifactDescriber). Gradle reads a
// private repo's credentials from the Gradle properties <name>Username and <name>Password
// (credentials(PasswordCredentials)), where <name> is the repo name as a Gradle identifier; Maven
// reads them from the server of ~/.m2/settings.xml whose id is the repo name.

const clientsNote = "Replace VERSION with the latest release on github.com/Telesphorepo/clients."

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
}

func newSnippets(repo *repos.Repo, baseURL string) *snippets {
	return &snippets{repo: repo, url: baseURL + "/maven/" + repo.Name, name: gradleName(repo.Name)}
}

func (s *snippets) add(client, title, lang, code, note string) {
	s.out = append(s.out, format.Snippet{Client: client, Purpose: s.purpose, Context: s.context, Via: s.via,
		Title: title, Lang: lang, Code: code, Note: note})
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
	return indentLines(b.String(), indent)
}

// gradleRepos adds the settings file that resolves dependencies (block "dependencyResolutionManagement")
// or plugins (block "pluginManagement") from the repo, and the credentials of a private repo.
func (s *snippets) gradleRepos(block string) {
	var portal string
	if block == "pluginManagement" {
		portal = "        gradlePluginPortal()\n"
	} else {
		portal = "        mavenCentral()\n"
	}
	for _, kts := range []bool{true, false} {
		client, file := "gradle-groovy", "settings.gradle"
		if kts {
			client, file = "gradle-kotlin", "settings.gradle.kts"
		}
		code := block + " {\n    repositories {\n" + s.gradleRepo(kts, "        ") + "\n" + portal + "    }\n}\n"
		s.add(client, file, langOf(kts), code, "")
		if s.private() {
			s.add(client, "~/.gradle/gradle.properties", "properties",
				s.name+"Username=YOUR_USERNAME\n"+s.name+"Password=YOUR_READ_TOKEN\n",
				"Create a read token on Settings › Tokens. In CI, set ORG_GRADLE_PROJECT_"+s.name+"Username and ORG_GRADLE_PROJECT_"+s.name+"Password instead.")
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
	s.add("maven", "pom.xml", "xml", "<repositories>\n  <repository>\n    <id>"+s.repo.Name+"</id>\n    <url>"+s.url+
		"</url>\n  </repository>\n</repositories>\n", "")
	s.mavenCredentials()
}

func (s *snippets) mavenCredentials() {
	if !s.private() {
		return
	}
	s.add("maven", "~/.m2/settings.xml", "xml", "<settings>\n  <servers>\n    <server>\n      <id>"+s.repo.Name+
		"</id>\n      <username>YOUR_USERNAME</username>\n      <password>YOUR_READ_TOKEN</password>\n    </server>\n  </servers>\n</settings>\n",
		"Create a read token on Settings › Tokens. In CI, write ${env.REPO_USERNAME} and ${env.REPO_TOKEN} instead and set those variables.")
}

// Usage implements format.Describer: how to use the repo in Gradle (Kotlin and Groovy) and
// Maven builds, and how to publish to it from a computer (the Telesphorepo Gradle plugin or
// Maven extension), from CI (GitHub Actions or Jenkins) and by hand.
func (h *Handler) Usage(repo *repos.Repo, baseURL string) []format.Snippet {
	s := newSnippets(repo, baseURL)
	s.purpose, s.context = format.SnippetUse, format.SnippetRepo
	s.gradleRepos("dependencyResolutionManagement")
	s.mavenRepos()

	s.purpose, s.via = format.SnippetPublish, format.ViaComputer
	for _, kts := range []bool{true, false} {
		client, ext, q := "gradle-groovy", "", "'"
		if kts {
			client, ext, q = "gradle-kotlin", ".kts", "\""
		}
		plugin := "plugins {\n    id(" + q + "com.telesphorepo" + q + ") version " + q + "VERSION" + q + "\n}\n"
		if !kts {
			plugin = "plugins {\n    id 'com.telesphorepo' version 'VERSION'\n}\n"
		}
		s.add(client, "settings.gradle"+ext, langOf(kts), plugin+"\ntelesphorepo {\n    url = "+q+baseURL+q+"\n}\n", clientsNote)
		url := "uri(\"" + s.url + "\")"
		if !kts {
			url = "'" + s.url + "'"
		}
		s.add(client, "build.gradle"+ext, langOf(kts), "publishing {\n    repositories {\n        maven {\n            name = "+q+s.name+q+
			"\n            url = "+url+"\n        }\n    }\n}\n", "The plugin gives each publish a session of its own; there is no password to store.")
		s.add(client, "Once per computer", "sh", "./gradlew telesphorepoLogin\n",
			"Open the printed link, sign in and approve the device with your second factor. After that, ./gradlew publish publishes.")
	}
	s.add("maven", ".mvn/extensions.xml", "xml", "<extensions>\n  <extension>\n    <groupId>com.telesphorepo</groupId>\n"+
		"    <artifactId>telesphorepo-maven-extension</artifactId>\n    <version>VERSION</version>\n  </extension>\n</extensions>\n", clientsNote)
	s.add("maven", ".mvn/maven.config", "sh", "-Dtelesphorepo.url="+baseURL+"\n", "")
	s.add("maven", "pom.xml", "xml", "<distributionManagement>\n  <repository>\n    <id>"+repo.Name+"</id>\n    <url>"+s.url+
		"</url>\n  </repository>\n</distributionManagement>\n", "")
	s.add("maven", "Once per computer", "sh", "mvn com.telesphorepo:telesphorepo-maven-plugin:VERSION:login\n",
		"Open the printed link, sign in and approve the device with your second factor. After that, mvn deploy publishes.")

	s.via = format.ViaCI
	s.add("github-actions", ".github/workflows/publish.yml (Gradle)", "yaml", workflow("./gradlew publish"),
		"The Gradle plugin uses the job's identity token, so the job stores no secret.")
	s.add("github-actions", ".github/workflows/publish.yml (Maven)", "yaml", workflow("mvn -B deploy"),
		"The Maven extension uses the job's identity token, so the job stores no secret.")
	jenkinsNote := "Bind an \"OpenID Connect id token\" credential with audience " + baseURL +
		" as telesphorepo-id-token."
	s.add("jenkins", "Jenkinsfile (Gradle)", "groovy", jenkinsStage(repo.Name, "./gradlew publish"),
		"Add oidcTokenEnv = \"OIDC_TOKEN\" to the telesphorepo block in settings.gradle(.kts). "+jenkinsNote)
	s.add("jenkins", "Jenkinsfile (Maven)", "groovy", jenkinsStage(repo.Name, "mvn -B deploy -Dtelesphorepo.oidcTokenEnv=OIDC_TOKEN"), jenkinsNote)

	out := s.out
	return append(out, format.HandSnippet("curl -f -u publish:$TOKEN -T lib-1.0.0.jar "+s.url+"/com/example/lib/1.0.0/lib-1.0.0.jar"))
}

func workflow(run string) string {
	return "permissions:\n  id-token: write\n  contents: read\n\njobs:\n  publish:\n    runs-on: ubuntu-latest\n    steps:\n" +
		"      - uses: actions/checkout@v4\n      - uses: actions/setup-java@v4\n        with:\n          distribution: temurin\n" +
		"          java-version: 21\n      - run: " + run + "\n"
}

func jenkinsStage(repo, run string) string {
	return "stage('Publish to " + repo + "') {\n  steps {\n" +
		"    withCredentials([string(credentialsId: 'telesphorepo-id-token', variable: 'OIDC_TOKEN')]) {\n" +
		"      sh '" + run + "'\n    }\n  }\n}\n"
}

func indentLines(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n")
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
	body, _, err := h.assets.Store().Get(ctx, assets.Key(repo.Name, p))
	if err != nil {
		return "jar", false, nil
	}
	defer body.Close()
	var pom struct {
		Packaging string    `xml:"packaging"`
		DM        *struct{} `xml:"dependencyManagement"`
	}
	if xml.NewDecoder(io.LimitReader(body, maxPOMBytes)).Decode(&pom) != nil {
		return "jar", false, nil
	}
	pk := strings.TrimSpace(pom.Packaging)
	if pk == "" {
		pk = "jar"
	}
	return pk, pom.DM != nil, nil
}

// ArtifactUsage implements format.ArtifactDescriber. p is an artifact folder, a version folder
// or a file in one. The pinned mode names the version of a version folder, else the newest
// release (the newest snapshot when there is none); the latest mode names Gradle's
// latest.release and Maven's RELEASE (latest.integration and LATEST for a snapshot-only
// artifact). A Gradle plugin marker gets a plugins { } block; a BOM an import of the platform; a
// parent pom a <parent> block.
func (h *Handler) ArtifactUsage(ctx context.Context, repo *repos.Repo, baseURL, p, v, mode string) (*format.ArtifactUsage, error) {
	dir := strings.Trim(p, "/")
	if dir == "" {
		return nil, nil
	}
	if _, err := h.assets.Get(ctx, repo.ID, dir); err == nil {
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
			bs, err := h.readBuilds(ctx, repo.ID, dir, id, v)
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
		v, err := h.latestVersion(ctx, repo, artDir)
		if err != nil || v == "" {
			return nil, err
		}
		version = v
	}
	if art.Kind != "gradle-plugin" {
		pk, dm, err := h.readPackaging(ctx, repo, artDir+version+"/"+id+"-"+version+".pom")
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

	s := newSnippets(repo, baseURL)
	s.purpose, s.context = format.SnippetUse, format.SnippetArtifact
	coord := group + ":" + id + ":"
	switch art.Kind {
	case "gradle-plugin":
		pluginNote := ""
		if mode == format.VersionLatest {
			pluginNote = "The plugins block takes a fixed version: this is the newest, " + version + "."
		}
		s.gradleRepos("pluginManagement")
		s.add("gradle-kotlin", "build.gradle.kts", "kotlin", "plugins {\n    id(\""+art.Title+"\") version \""+version+"\"\n}\n", pluginNote)
		s.add("gradle-groovy", "build.gradle", "groovy", "plugins {\n    id '"+art.Title+"' version '"+version+"'\n}\n", pluginNote)
		s.out = order(s.out)
	case "parent":
		s.mavenRepos()
		s.add("maven", "pom.xml (parent)", "xml", "<parent>\n  <groupId>"+group+"</groupId>\n  <artifactId>"+id+
			"</artifactId>\n  <version>"+version+"</version>\n</parent>\n", "A parent pom takes a fixed version.")
	default:
		s.gradleRepos("dependencyResolutionManagement")
		kt, gr := "implementation(\""+coord+gv+"\")", "implementation '"+coord+gv+"'"
		mdep := "<dependencies>\n  <dependency>\n    <groupId>" + group + "</groupId>\n    <artifactId>" + id +
			"</artifactId>\n    <version>" + mv + "</version>\n  </dependency>\n</dependencies>\n"
		if art.Kind == "bom" {
			kt, gr = "implementation(platform(\""+coord+gv+"\"))", "implementation platform('"+coord+gv+"')"
			mdep = "<dependencyManagement>\n  <dependencies>\n    <dependency>\n      <groupId>" + group + "</groupId>\n      <artifactId>" + id +
				"</artifactId>\n      <version>" + mv + "</version>\n      <type>pom</type>\n      <scope>import</scope>\n    </dependency>\n  </dependencies>\n</dependencyManagement>\n"
		}
		s.add("gradle-kotlin", "build.gradle.kts", "kotlin", "dependencies {\n    "+kt+"\n}\n", note)
		s.add("gradle-groovy", "build.gradle", "groovy", "dependencies {\n    "+gr+"\n}\n", note)
		s.out = order(s.out)
		s.mavenRepos()
		s.add("maven", "pom.xml (dependency)", "xml", mdep, note)
	}
	return &format.ArtifactUsage{Artifact: art, Version: version, Mode: mode, Snippets: s.out}, nil
}

// order puts the snippets of each client together, in the order the clients first appear.
func order(in []format.Snippet) []format.Snippet {
	var clients []string
	by := map[string][]format.Snippet{}
	for _, s := range in {
		if _, ok := by[s.Client]; !ok {
			clients = append(clients, s.Client)
		}
		by[s.Client] = append(by[s.Client], s)
	}
	out := make([]format.Snippet, 0, len(in))
	for _, c := range clients {
		out = append(out, by[c]...)
	}
	return out
}
