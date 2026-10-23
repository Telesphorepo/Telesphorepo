package docker

import (
	"github.com/Telesphorepo/Telesphorepo/internal/assets"
	"github.com/Telesphorepo/Telesphorepo/internal/format"
)

// carry is how a merge takes the objects of another docker repo (format.Spec Carry): each image
// in one commit, so a manifest commits with the links and the child manifests it names and a
// tag with its manifest, every write marked as an object of that commit. The links and the
// manifests are content-addressed (their path is their digest), so a path holds the same bytes
// in both repos or in one; a tag that both hold with other digests is a conflict.
func carry() *format.Carry {
	return &format.Carry{
		Unit: func(p string) string {
			if op, ok := parsePath(p); ok {
				return op.image
			}
			return ""
		},
		Prepare: func(ws []assets.Write) {
			batch := make(map[string]bool, len(ws))
			for i := range ws {
				batch[string(ws[i].Path)] = true
			}
			for i := range ws {
				if op, ok := parsePath(string(ws[i].Path)); ok {
					ws[i].Meta = &writeMeta{kind: op.kind, batch: batch}
				}
			}
		},
		Pinned: func(p string) bool {
			op, ok := parsePath(p)
			return ok && op.kind == kindTag
		},
	}
}
