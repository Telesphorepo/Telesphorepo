// Package edgedist holds the edge Worker folder that the server hands out at
// GET /api/edge/worker.zip: the bundled Worker, package.json, bun.lock, the provision script and
// the README of edge/. "go tool task build", "dev", "edge" and "release" write it into dist/
// before they compile the server, so a server hands out the Worker of its own build. A build
// without it has only dist/.keep.
package edgedist

import (
	"embed"
	"io/fs"
	"strings"
	"sync"
)

//go:embed all:dist
var dist embed.FS

const (
	// ZipFile is the zip of the Worker folder, without wrangler.jsonc: the server adds its own.
	ZipFile = "telesphorepo-edge.zip"
	// VersionFile holds the version that the Worker was bundled with.
	VersionFile = "VERSION"
	// Dir is the folder that every file of the zip sits in.
	Dir = "telesphorepo-edge/"
)

// Zip returns the zip of the Worker folder and the version of its bundle, or nil and "" for a
// build without it. The caller must not change the bytes.
var Zip = sync.OnceValues(func() ([]byte, string) {
	b, err := fs.ReadFile(dist, "dist/"+ZipFile)
	if err != nil {
		return nil, ""
	}
	v, err := fs.ReadFile(dist, "dist/"+VersionFile)
	if err != nil {
		return nil, ""
	}
	return b, strings.TrimSpace(string(v))
})
