//go:build !windows

package main

import "os"

// restrictDir gives dir mode 700: only the owner opens it.
func restrictDir(dir string) error { return os.Chmod(dir, 0o700) }

// restrictFile gives f mode 600: only the owner reads and writes it.
func restrictFile(f string) error { return os.Chmod(f, 0o600) }
