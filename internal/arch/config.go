package arch

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/signing"
)

// defaultArchitectures is the architecture list of a repo whose config has none: the $arch of
// Arch Linux and of Arch Linux ARM.
var defaultArchitectures = []string{"x86_64", "aarch64"}

var defaultConfig = json.RawMessage(`{"architectures":["x86_64","aarch64"]}`)

// ValidateConfig checks "architectures": a non-empty list of distinct names matching
// ^[a-z0-9_]+$, other than "any" and "pool" (the folder of the packages). A missing list gets the default. Other keys are kept. The
// signing key of a repo is the repo field signing_key, not a config key.
func ValidateConfig(raw json.RawMessage) (json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return nil, errors.New("config must be a JSON object")
	}
	if _, ok := obj[signing.Field]; ok {
		return nil, errors.New("the signing key is the repository field signing_key, not a config key")
	}
	archs := defaultArchitectures
	if v, ok := obj["architectures"]; ok {
		archs = nil // Unmarshal would otherwise write into the default's array
		if err := json.Unmarshal(v, &archs); err != nil || len(archs) == 0 {
			return nil, errors.New("architectures must be a non-empty list of strings")
		}
	}
	for i, a := range archs {
		if !archRE.MatchString(a) || a == "any" || a == "pool" {
			return nil, fmt.Errorf("invalid architecture %q", a)
		}
		if slices.Contains(archs[:i], a) {
			return nil, fmt.Errorf("architecture %q is listed twice", a)
		}
	}
	obj["architectures"], _ = json.Marshal(archs)
	return json.Marshal(obj)
}

// architectures returns the configured architectures of repo. The config was validated when it
// was stored.
func architectures(repo *repos.Repo) []string {
	var c struct {
		Architectures []string `json:"architectures"`
	}
	if json.Unmarshal(repo.Config, &c) != nil || len(c.Architectures) == 0 {
		return defaultArchitectures
	}
	return c.Architectures
}

// targets are the architectures whose indexes list a package of architecture arch: arch itself,
// or every configured one for "any".
func targets(repo *repos.Repo, arch string) []string {
	if arch == "any" {
		return architectures(repo)
	}
	return []string{arch}
}

// signingKey returns the name of the signing key of repo: the repo field signing_key; "" when
// the repo has none.
func signingKey(repo *repos.Repo) string {
	var name string
	if json.Unmarshal(repo.Fields[signing.Field], &name) != nil {
		return ""
	}
	return name
}

// configFields are the repo config options that the web UI edits.
var configFields = []format.Field{
	{Key: "architectures", Label: "Architectures", Type: format.FieldList, Required: true, Placeholder: "aarch64",
		Help:       "The values of $arch that pacman sees in this repository.",
		Default:    json.RawMessage(`["x86_64","aarch64"]`),
		ChangeNote: "The index of an added architecture appears with the any packages and the packages uploaded for it; the index of a removed one goes, and its packages stay in pool/. pacman on a machine of a removed architecture stops seeing updates."},
}

// ui is what the web UI shows of Arch repos.
var ui = format.UIDescriptor{
	DeleteNotes: []format.DeleteNote{{Match: "pool/**/*.pkg.tar.*", One: "package", Many: "packages",
		Text: "pacman stops offering them after the indexes rebuild; the newest older version of each takes its place."}},
	FileDetails:    []format.FileDetail{{Match: "pool/**/*.pkg.tar.*", Part: "arch-package"}},
	RemotePresets:  []format.RemotePreset{{Label: "Arch Linux", URL: "https://geo.mirror.pkgbuild.com/"}},
	ProxyNote:      "Signed by the remote's packagers. Clients use the keys they trust for it, e.g. archlinux-keyring.",
	PublicKey:      "key.asc",
	ArtifactKinds:  map[string]string{"package": "Arch package"},
	ArtifactName:   map[string]string{"package": "Package"},
	SnippetTools:   map[string]string{"pacman": "pacman", "telesphorepo": "Telesphorepo CLI"},
	InstallHeading: "Install it",
	BuildFiles: []format.BuildFileLabel{
		{Extension: "*", Classifier: "any", Label: "Package, any architecture"},
		{Extension: "*", Classifier: "*", Label: "Package, {classifier}"},
	},
}
