// Package repopath parses the paths of files in a repo. It is pure: routes, file operations,
// imports and uploads all check a path with Parse or Folder, so one rule set holds everywhere.
//
// A file path is valid UTF-8, has no control character and no backslash, does not start with
// "/", has no empty, "." or ".." segment, and is at most 1024 bytes long with at most 255 bytes
// per segment. Paths use forward slashes on every OS.
package repopath

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// ErrInvalid is a path that breaks the rules.
var ErrInvalid = errors.New("invalid path")

// Max is the longest path in bytes; MaxSegment the longest segment.
const (
	Max        = 1024
	MaxSegment = 255
)

// Path is a file path in a repo, e.g. "com/acme/lib/1.0/lib-1.0.jar", or a folder that ends in
// "/" (see Folder).
type Path string

func (p Path) String() string { return string(p) }

// Parse checks s as a file path.
func Parse(s string) (Path, error) {
	if !valid(s) {
		return "", ErrInvalid
	}
	return Path(s), nil
}

// Folder checks s as a folder: "" (the repo root) or a path, with or without a trailing "/". It
// returns "" or the folder with one trailing "/".
func Folder(s string) (Path, error) {
	s = strings.TrimSuffix(s, "/")
	if s == "" {
		return "", nil
	}
	if !valid(s) || len(s)+1 > Max {
		return "", ErrInvalid
	}
	return Path(s + "/"), nil
}

// Join returns folder dir ("" or ending in "/") joined with the relative path rel, checked.
func Join(dir Path, rel string) (Path, error) { return Parse(string(dir) + rel) }

// Dir returns the folder of p with a trailing "/", or "" for a file at the root.
func (p Path) Dir() Path {
	s := strings.TrimSuffix(string(p), "/")
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		return Path(s[:i+1])
	}
	return ""
}

// Base returns the last segment of p.
func (p Path) Base() string {
	s := strings.TrimSuffix(string(p), "/")
	return s[strings.LastIndexByte(s, '/')+1:]
}

func valid(s string) bool {
	if s == "" || len(s) > Max || !utf8.ValidString(s) {
		return false
	}
	for seg := range strings.SplitSeq(s, "/") {
		if seg == "" || seg == "." || seg == ".." || len(seg) > MaxSegment {
			return false
		}
		for i := 0; i < len(seg); i++ {
			if c := seg[i]; c < 0x20 || c == 0x7F || c == '\\' {
				return false
			}
		}
	}
	return true
}
