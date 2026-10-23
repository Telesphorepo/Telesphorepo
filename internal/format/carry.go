package format

import (
	"context"

	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/repos"
)

// Carry is what a merge of two hosted repos of the format (the gradual move of the Nexus
// migration) needs beyond a commit of each file by its digest at its path. Every field is
// optional; a format without Carry, or without Prepare, is merged as SPEC.md "Nexus sources",
// Gradual move, says.
type Carry struct {
	// Unit names the files that one commit of a merge holds together, so a file commits with the
	// files it names (the image of a Docker path); "" keeps a file to itself. The files of one unit
	// are next to each other in path order.
	Unit func(path string) string
	// Prepare sets the format data (Write.Meta) of the writes of one commit of a merge, in path
	// order. A format with Prepare takes the files by their digests, also when its Importer has
	// Store.
	Prepare func(ws []assets.Write)
	// Pinned reports whether a path that both repos hold with other bytes is a conflict of the merge
	// although the format's Admit replaces the file (a Docker tag).
	Pinned func(path string) bool
	// Labels lists the named values of repo from that are not files (npm dist-tags), each with the
	// value that into has. A merge sets each in into after the files: a label of something into does
	// not have, or that into has no value for, takes from's value; a different value of something
	// into has is a conflict with the choices of a file.
	Labels func(ctx context.Context, from, into *repos.Repo) ([]Label, error)
	// SetLabel gives into the value of l in from. A label whose value into cannot take (its
	// version is missing) changes nothing.
	SetLabel func(ctx context.Context, into *repos.Repo, l Label) error
}

// Label is one named value of Carry.Labels.
type Label struct {
	Key    string // unique in the repo, e.g. "lib beta"
	Title  string // for people, e.g. "lib: dist-tag beta"
	Value  string // the value in from
	Target string // the value in into; "" when into has none
	// Owned is true when into has what the label belongs to (the package).
	Owned bool
}

// Conflict reports whether l is a conflict of a merge: into has it with another value.
func (l Label) Conflict() bool { return l.Owned && l.Target != "" && l.Target != l.Value }
