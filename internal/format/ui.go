package format

import (
	"encoding/json"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"
)

// Field is one value of a form that the web UI builds from a format: a repo config option
// (ConfigSpec.Fields) or an upload field (UploadSpec.Fields).
type Field struct {
	Key      string          `json:"key"`
	Label    string          `json:"label"`
	Help     string          `json:"help,omitempty"`
	Type     string          `json:"type"` // "string", "bool", "list", "enum" or "secret"
	Options  []string        `json:"options,omitempty"`
	Required bool            `json:"required,omitempty"`
	Default  json.RawMessage `json:"default,omitempty"`
	// Placeholder is an example value that the empty control shows.
	Placeholder string `json:"placeholder,omitempty"`
	// ChangeNote is shown before a change of a saved value is saved, e.g. what a change of the
	// Debian architectures does.
	ChangeNote string `json:"change_note,omitempty"`
}

// Field types.
const (
	FieldString = "string"
	FieldBool   = "bool"
	FieldList   = "list"
	FieldEnum   = "enum"
	FieldSecret = "secret"
)

// UIDescriptor is what the web UI shows of a format beyond its specs: the words and the choices
// that differ from format to format, so no shared UI code names a format.
type UIDescriptor struct {
	// DeleteNotes say what a delete of matching files does besides the delete.
	DeleteNotes []DeleteNote
	// FileDetails name the format's part of the details of a matching file (the first match).
	FileDetails []FileDetail
	// RemotePresets are well-known remotes of a proxy.
	RemotePresets []RemotePreset
	// ProxyNote is one sentence about the files of a proxy, e.g. who signs them.
	ProxyNote string
	// PublicKey is the path under the repo where the format serves the public signing key of a
	// hosted repo ("" for none), e.g. "key.asc".
	PublicKey string
	// ArtifactKinds are the words for the Kind of an Artifact, e.g. "bom" -> "Bill of materials".
	ArtifactKinds map[string]string
	// ArtifactName is the word for the title of an Artifact of each kind, e.g. "Coordinates",
	// "Plugin ID", "Package". "" for a kind: "Name".
	ArtifactName map[string]string
	// SnippetTools are the words for the snippet tools of the format, e.g. "gradle-kotlin" ->
	// "Gradle (Kotlin)".
	SnippetTools map[string]string
	// InstallHeading is the heading of the artifact snippets, e.g. "Install it"; "" for "Use it in
	// a build".
	InstallHeading string
	// BuildFiles are the words for the files of a build (BuildFile), the first match wins.
	BuildFiles []BuildFileLabel
}

// BuildFileLabel is the words for the files of a build with an extension and a classifier.
// Extension "*" matches any extension; Classifier "*" any classifier and "" none. Label may hold
// "{classifier}" and "{extension}".
type BuildFileLabel struct {
	Extension  string `json:"extension"`
	Classifier string `json:"classifier"`
	Label      string `json:"label"`
}

// DeleteNote is a fact about a delete of files that match the glob Match: the delete plan counts
// them, and the UI says "<count> <one|many>: <text>".
type DeleteNote struct {
	Match string `json:"match"`
	One   string `json:"one"`
	Many  string `json:"many"`
	Text  string `json:"text"`
}

// FileDetail names the part (a component of the format's UI module) that the details of a file
// that matches the glob Match show.
type FileDetail struct {
	Match string `json:"match"`
	Part  string `json:"part"`
}

// RemotePreset is a well-known remote that a proxy of the format can keep.
type RemotePreset struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// Glob reports whether the repo path p matches pattern: "*" matches any run of characters
// except "/", "?" one character except "/", and "**/" any number of folders (none included).
// Matching is case-sensitive.
func Glob(pattern, p string) bool {
	return globRE(pattern).MatchString(p)
}

var globCache sync.Map // pattern -> *regexp.Regexp

func globRE(pattern string) *regexp.Regexp {
	if re, ok := globCache.Load(pattern); ok {
		return re.(*regexp.Regexp)
	}
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); {
		if strings.HasPrefix(pattern[i:], "**/") {
			b.WriteString("(?:[^/]*/)*")
			i += 3
			continue
		}
		r, n := utf8.DecodeRuneInString(pattern[i:])
		switch r {
		case '*':
			b.WriteString("[^/]*")
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
		i += n
	}
	b.WriteString("$")
	re := regexp.MustCompile(b.String())
	globCache.Store(pattern, re)
	return re
}
