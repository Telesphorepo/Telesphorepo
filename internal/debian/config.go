package debian

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/signing"
)

// defaultArchitectures is the architecture list of a repo whose config has none.
var defaultArchitectures = []string{"amd64", "arm64"}

// defaultConfig is {"architectures":["amd64","arm64"]}.
var defaultConfig = json.RawMessage(`{"architectures":["amd64","arm64"]}`)

// ValidateConfig checks "architectures": a non-empty list of distinct names matching
// ^[a-z0-9-]+$, other than "all". A missing list gets the default. The optional "distribution"
// is a repo name-style string. Other keys are kept. The signing key of a repo is the repo field
// signing_key, not a config key.
func (h *Handler) ValidateConfig(raw json.RawMessage) (json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return nil, errors.New("config must be a JSON object")
	}
	if _, ok := obj[signing.Field]; ok {
		return nil, errors.New("the signing key is the repository field signing_key, not a config key")
	}
	if v, ok := obj["distribution"]; ok {
		var d string
		if err := json.Unmarshal(v, &d); err != nil || !repos.NameRE.MatchString(d) {
			return nil, errors.New("distribution must be a valid distribution name")
		}
	}
	archs := defaultArchitectures
	if v, ok := obj["architectures"]; ok {
		archs = nil // Unmarshal would otherwise write into the default's array
		if err := json.Unmarshal(v, &archs); err != nil || len(archs) == 0 {
			return nil, errors.New("architectures must be a non-empty list of strings")
		}
	}
	for i, a := range archs {
		if !archRE.MatchString(a) || a == "all" {
			return nil, fmt.Errorf("invalid architecture %q", a)
		}
		if slices.Contains(archs[:i], a) {
			return nil, fmt.Errorf("architecture %q is listed twice", a)
		}
	}
	obj["architectures"], _ = json.Marshal(archs)
	return json.Marshal(obj)
}

// architectures returns the configured architectures of repo. The config was validated when
// it was stored.
func architectures(repo *repos.Repo) []string {
	var c struct {
		Architectures []string `json:"architectures"`
	}
	if json.Unmarshal(repo.Config, &c) != nil || len(c.Architectures) == 0 {
		return defaultArchitectures
	}
	return c.Architectures
}

// signingKey returns the name of the signing key of repo's indexes: the repo field signing_key;
// "" when the repo has none.
func signingKey(repo *repos.Repo) string {
	var name string
	if json.Unmarshal(repo.Fields[signing.Field], &name) != nil {
		return ""
	}
	return name
}

// importDistribution returns the "distribution" of repo's config: the distribution that
// imported packages go into (the import plan sets it from the source repo).
func importDistribution(repo *repos.Repo) (string, error) {
	if d := importDistributionOf(repo); d != "" {
		return d, nil
	}
	return "", errors.New("the repo config has no distribution for imported packages")
}

// importDistributionOf is the "distribution" of repo's config, "" when it has none.
func importDistributionOf(repo *repos.Repo) string {
	var c struct {
		Distribution string `json:"distribution"`
	}
	_ = json.Unmarshal(repo.Config, &c)
	return c.Distribution
}

// configFields are the repo config options that the web UI edits.
var configFields = []format.Field{
	{Key: "architectures", Label: "Architectures", Type: format.FieldList, Required: true, Placeholder: "arm64",
		Help:       "The processor types that apt sees in this repository.",
		Default:    json.RawMessage(`["amd64","arm64"]`),
		ChangeNote: "The package lists of an added architecture appear, those of a removed one go, and every list is signed again. apt on a machine of a removed architecture stops seeing updates."},
	{Key: "distribution", Label: "Distribution of imports", Type: format.FieldString, Placeholder: "stable",
		Help: "The distribution that packages copied from Nexus go into, and that the check of a proxy's remote reads."},
}

// ui is what the web UI shows of Debian repos.
var ui = format.UIDescriptor{
	DeleteNotes: []format.DeleteNote{{Match: "pool/**/*.deb", One: "package", Many: "packages",
		Text: "apt stops offering them after the indexes of their distributions rebuild."}},
	FileDetails:    []format.FileDetail{{Match: "pool/**/*.deb", Part: "deb-package"}},
	RemotePresets:  []format.RemotePreset{{Label: "Debian", URL: "https://deb.debian.org/debian/"}},
	ProxyNote:      "Signed by the remote archive. Clients use its key.",
	PublicKey:      "key.asc",
	ArtifactKinds:  map[string]string{"package": "Debian package"},
	ArtifactName:   map[string]string{"package": "Package"},
	SnippetTools:   map[string]string{"apt": "apt", "curl": "curl"},
	InstallHeading: "Install it",
	BuildFiles: []format.BuildFileLabel{
		{Extension: "*", Classifier: "all", Label: "Package, all architectures"},
		{Extension: "*", Classifier: "*", Label: "Package, {classifier}"},
	},
}
