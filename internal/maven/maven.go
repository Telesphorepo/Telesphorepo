// Package maven serves Maven repos at /maven/{repo}/{path...}: hosted repos that Gradle and
// Maven publish to, proxies of remote Maven repositories and groups of both. It mounts the
// standard format routes; its hooks describe the Maven files, keep releases immutable, merge
// maven-metadata.xml in a group and write it again after a delete.
package maven

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/module"
	"github.com/Telesphorepo/Telesphorepo/internal/repopath"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// Handler serves the Maven format.
type Handler struct {
	assets *assets.Pipeline
	host   *module.Host
}

// Module returns the Maven module: the Maven format with proxies, groups, uploads, imports,
// demo data and seeds. The Telesphorepo CLI publishes to it ("telesphorepo publish").
func Module() module.Module {
	h := &Handler{}
	return module.Module{
		ID: "maven", Title: "Maven / Gradle",
		Register: func(x *module.Extender) {
			x.Init(func(ctx context.Context, host *module.Host) error {
				h.assets, h.host = host.Assets, host
				return nil
			})
			x.Format(format.Spec{
				Name: "maven", Title: "Maven / Gradle",
				Description: "Java libraries and Gradle plugins, for Maven and Gradle builds.",
				Icon:        icon, KindIcons: map[string][]byte{"gradle-plugin": gradleIcon},
				Kinds:         []string{repos.KindHosted, repos.KindProxy, repos.KindGroup},
				Config:        format.ConfigSpec{Default: json.RawMessage(`{}`), Validate: validateConfig},
				Describe:      describe,
				Admit:         admit,
				AfterRemove:   h.afterRemove,
				Classify:      classify,
				ProxyProbe:    func(json.RawMessage) string { return "" },
				Merge:         &assets.Merger{Match: matchMerged, Merge: mergeParts},
				Versions:      h.Versions,
				Builds:        h.Builds,
				Usage:         h.Usage,
				ArtifactUsage: h.ArtifactUsage,
				Tools:         tools(),
				Publish:       format.PublishCLI,
				Upload:        h.uploadSpec(),
				Importer:      &format.Importer{Sources: []string{"maven2"}, Target: format.SourcePath},
				UI:            ui,
			})
			x.Demo(module.DemoData{Repos: demoRepos, Fill: h.Demo})
			x.Seed(seed)
		},
	}
}

// validateConfig accepts only an empty JSON object: Maven repos have no options.
func validateConfig(raw json.RawMessage) (json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return nil, errors.New("maven config must be a JSON object")
	}
	if len(m) > 0 {
		return nil, errors.New("maven repos take no config options")
	}
	return json.RawMessage(`{}`), nil
}

// contentTypes are the content types of Maven files by extension.
var contentTypes = map[string]string{
	".pom": "application/xml", ".xml": "application/xml",
	".jar": "application/java-archive", ".war": "application/java-archive", ".ear": "application/java-archive",
	".module": "application/json",
	".md5":    "text/plain", ".sha1": "text/plain", ".sha256": "text/plain", ".sha512": "text/plain",
	".asc": "application/pgp-signature",
}

// describe gives a Maven path its content type by extension, and the kind metadata to
// maven-metadata.xml and its checksums.
func describe(p repopath.Path) (assets.Describe, bool) {
	d := assets.Describe{ContentType: contentTypes[strings.ToLower(path.Ext(string(p)))], Kind: assets.KindArtifact}
	if d.ContentType == "" {
		d.ContentType = assets.FallbackContentType
	}
	if strings.HasPrefix(p.Base(), "maven-metadata.xml") {
		d.Kind = assets.KindMetadata
	}
	return d, true
}

// errImmutable is the refusal of different bytes at a release path.
const errImmutable = "release artifacts are immutable"

// admit keeps a release file immutable: the same bytes are kept, different bytes are refused
// with 409. maven-metadata.xml* and the files of a -SNAPSHOT version are replaced.
func admit(_ context.Context, w *assets.Write, existing *assets.Asset) (assets.Decision, error) {
	switch {
	case existing == nil:
		return assets.Store, nil
	case existing.SHA256 == w.Body.Digest():
		return assets.Keep, nil
	case mutable(string(w.Path)):
		return assets.Store, nil
	}
	return 0, assets.Refuse(http.StatusConflict, "%s: %s", w.Path, errImmutable)
}

func mutable(p string) bool {
	return strings.HasPrefix(path.Base(p), "maven-metadata.xml") || strings.HasSuffix(path.Base(path.Dir(p)), "-SNAPSHOT")
}

// ui is what the web UI shows of Maven repos.
var ui = format.UIDescriptor{
	DeleteNotes: []format.DeleteNote{{Match: "**/*.pom", One: "version", Many: "versions",
		Text: "maven-metadata.xml stops listing the deleted versions."}},
	FileDetails: []format.FileDetail{{Match: "**/*.pom", Part: "maven-artifact"}, {Match: "**/*.jar", Part: "maven-artifact"}},
	RemotePresets: []format.RemotePreset{
		{Label: "Maven Central", URL: "https://repo1.maven.org/maven2/"},
		{Label: "Gradle Plugin Portal", URL: "https://plugins.gradle.org/m2/"},
	},
	ArtifactKinds: map[string]string{"library": "Library", "bom": "Bill of materials", "parent": "Parent POM", "gradle-plugin": "Gradle plugin"},
	ArtifactName:  map[string]string{"library": "Coordinates", "bom": "Coordinates", "parent": "Coordinates", "gradle-plugin": "Plugin ID"},
	SnippetTools:  map[string]string{"gradle-kotlin": "Gradle (Kotlin)", "gradle-groovy": "Gradle (Groovy)", "maven": "Maven", "telesphorepo": "Telesphorepo CLI"},
	BuildFiles: []format.BuildFileLabel{
		{Extension: "pom", Label: "Project description (POM)"},
		{Extension: "module", Label: "Gradle module metadata"},
		{Extension: "jar", Label: "Library (JAR)"},
		{Extension: "*", Classifier: "sources", Label: "Sources"},
		{Extension: "*", Classifier: "javadoc", Label: "Javadoc"},
	},
}

// uploadSpec is the web UI upload of Maven repos (plan.go).
func (h *Handler) uploadSpec() *format.UploadSpec {
	u := *uploadSpec
	u.Plan, u.Store = h.Plan, h.Store
	return &u
}
