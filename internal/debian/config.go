package debian

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// defaultArchitectures is the architecture list of a repo whose config has none.
var defaultArchitectures = []string{"amd64", "arm64"}

// DefaultConfig is {"architectures":["amd64","arm64"]}.
func (h *Handler) DefaultConfig() json.RawMessage {
	return json.RawMessage(`{"architectures":["amd64","arm64"]}`)
}

// ValidateConfig checks "architectures": a non-empty list of distinct names matching
// ^[a-z0-9-]+$, other than "all". A missing list gets the default. The optional
// "distribution" is a repo name-style string. Other keys are kept.
func (h *Handler) ValidateConfig(raw json.RawMessage) (json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return nil, errors.New("config must be a JSON object")
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

// importDistribution returns the "distribution" of repo's config: the distribution that
// imported packages go into (spec §12; the import sets it from the source repo).
func importDistribution(repo *repos.Repo) (string, error) {
	var c struct {
		Distribution string `json:"distribution"`
	}
	if json.Unmarshal(repo.Config, &c) != nil || c.Distribution == "" {
		return "", errors.New("the repo config has no distribution for imported packages")
	}
	return c.Distribution, nil
}
