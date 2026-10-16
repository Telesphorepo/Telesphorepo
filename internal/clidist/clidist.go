// Package clidist holds the archives of the Telesphorepo CLI that the server binary serves at
// /cli/ and that its install scripts download. "go tool task build", "dev", "cli" and "release"
// write them into dist/ before they compile the server, so a server serves the CLI of its own
// build. A build without them has only dist/.keep.
package clidist

import (
	"bufio"
	"bytes"
	"embed"
	"io/fs"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// SumsFile is the file that lists the SHA-256 of every archive, in the format of sha256sum.
const SumsFile = "SHA256SUMS"

// Target is a platform of the CLI.
type Target struct{ OS, Arch string }

// Targets are the platforms of the CLI archives of a full build.
var Targets = []Target{
	{"linux", "amd64"}, {"linux", "arm64"},
	{"darwin", "amd64"}, {"darwin", "arm64"},
	{"windows", "amd64"}, {"windows", "arm64"},
}

// Archive returns the archive name of the CLI for os and arch: telesphorepo_<os>_<arch>.tar.gz,
// or .zip on Windows.
func Archive(os, arch string) string {
	if os == "windows" {
		return "telesphorepo_" + os + "_" + arch + ".zip"
	}
	return "telesphorepo_" + os + "_" + arch + ".tar.gz"
}

// FS returns the archives and SumsFile.
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}

// Sums returns the SHA-256 (lower-case hex) of each archive by name, from SumsFile. It is empty
// for a build without archives.
func Sums() map[string]string {
	out := map[string]string{}
	b, err := fs.ReadFile(FS(), SumsFile)
	if err != nil {
		return out
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		sum, name, ok := strings.Cut(strings.TrimSpace(sc.Text()), "  ")
		if ok && len(sum) == 64 {
			out[strings.TrimSpace(name)] = sum
		}
	}
	return out
}
