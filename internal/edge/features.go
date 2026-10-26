package edge

import (
	"slices"
	"strings"

	"github.com/Telesphorepo/Telesphorepo/internal/settings"
)

// The edge contract (SPEC.md "Edge delivery", The contract): the Worker serves by the features it
// implements (edge/src/features.ts), never by the server version, so a server deploy needs no
// Worker deploy unless it uses a feature that the deployed Worker lacks.

// workerFeature is one thing that a Worker must implement to read what the server writes. Text
// ends the sentence "this server needs it to ..." ("serve OCI referrers").
type workerFeature struct {
	Name string
	Text string
}

// featureRepoDomains maps a request on a repo domain (config.json `domains`) to the format route
// of its repo. A Worker without it would read /raw/x/... on a domain as repo x.
var featureRepoDomains = workerFeature{Name: "repo-domains", Text: "serve the repository domains"}

// requires returns the features that config.json requires now: those of the objects that the
// server writes that a Worker must implement to serve at all. A feature that only some objects
// use goes in their `needs`, not here. repo-domains while a repo domain is listed.
func (m *mod) requires() []workerFeature {
	if len(settings.DomainsOf(m.h.Settings.Current())) > 0 {
		return []workerFeature{featureRepoDomains}
	}
	return nil
}

func featureNames(fs []workerFeature) []string {
	out := []string{}
	for _, f := range fs {
		out = append(out, f.Name)
	}
	return out
}

// outdated says why the deployed Worker sends every request to the server, or "" when it serves:
// features are the ones it reported.
func (m *mod) outdated(features []string) string {
	var lacks []string
	for _, f := range m.requires() {
		if !slices.Contains(features, f.Name) {
			lacks = append(lacks, f.Text)
		}
	}
	if len(lacks) == 0 {
		return ""
	}
	return "The deployed Worker sends every request to the server: this server needs it to " + strings.Join(lacks, ", ") + "."
}
