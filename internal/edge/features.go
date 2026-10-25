package edge

import (
	"fmt"
	"slices"
	"strings"
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

// requires returns the features that config.json requires now: those of the objects that the
// server writes that a Worker must implement to serve at all. A feature that only some objects
// use goes in their `needs`, not here. None yet.
func (m *mod) requires() []workerFeature { return nil }

func featureNames(fs []workerFeature) []string {
	out := []string{}
	for _, f := range fs {
		out = append(out, f.Name)
	}
	return out
}

// outdated says why the deployed Worker sends every request to the server, or "" when it serves:
// build and features are what it reported, reported is false for a Worker that reported no
// features (one built before the contract, which does not read config.json).
func (m *mod) outdated(build string, features []string, reported bool) string {
	if !reported {
		return fmt.Sprintf("The deployed Worker (%s) cannot read this server's edge objects and sends every request to the server.", build)
	}
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
