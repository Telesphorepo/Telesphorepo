package rpm

import (
	"encoding/json"
	"errors"

	"github.com/Telesphorepo/Telesphorepo/internal/format"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
	"github.com/Telesphorepo/Telesphorepo/internal/signing"
)

// ValidateConfig checks that the config is a JSON object without signing_key: the signing key of
// a repo is the repo field signing_key. Other keys are kept. An RPM repo has no options: one
// repodata holds every architecture, and dnf takes the packages its machine can install.
func ValidateConfig(raw json.RawMessage) (json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return nil, errors.New("config must be a JSON object")
	}
	if _, ok := obj[signing.Field]; ok {
		return nil, errors.New("the signing key is the repository field signing_key, not a config key")
	}
	return json.Marshal(obj)
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

// ui is what the web UI shows of RPM repos.
var ui = format.UIDescriptor{
	DeleteNotes: []format.DeleteNote{{Match: "Packages/**/*.rpm", One: "package", Many: "packages",
		Text: "dnf stops offering them after the repodata rebuilds."}},
	FileDetails: []format.FileDetail{{Match: "Packages/**/*.rpm", Part: "rpm-package"}},
	RemotePresets: []format.RemotePreset{
		{Label: "Fedora", URL: "https://dl.fedoraproject.org/pub/fedora/linux/"},
		{Label: "Rocky Linux", URL: "https://dl.rockylinux.org/pub/rocky/"},
		{Label: "AlmaLinux", URL: "https://repo.almalinux.org/almalinux/"},
	},
	ProxyNote:      "Signed by the remote's packagers. Clients use the keys they trust for it, e.g. the distribution's keys in /etc/pki/rpm-gpg.",
	PublicKey:      "key.asc",
	KeyChange:      "Writes every package of {repo} again with a signature of the new key ({size}), and edge delivery copies each one to its regions again",
	KeyNote:        "Rocky, Alma and RHEL 8 (rpm 4.14) check only RSA keys: an Ed25519 key, the default, works on Fedora and on RHEL 9 and later.",
	ArtifactKinds:  map[string]string{"package": "RPM package"},
	ArtifactName:   map[string]string{"package": "Package"},
	SnippetTools:   map[string]string{"dnf": "dnf", "telesphorepo": "Telesphorepo CLI"},
	InstallHeading: "Install it",
	BuildFiles: []format.BuildFileLabel{
		{Extension: "*", Classifier: "noarch", Label: "Package, any architecture"},
		{Extension: "*", Classifier: "*", Label: "Package, {classifier}"},
	},
}
