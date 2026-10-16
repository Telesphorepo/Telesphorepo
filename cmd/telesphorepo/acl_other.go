//go:build !windows

package main

import (
	"fmt"
	"os"
)

// restrictDir gives dir mode 700: only the owner opens it.
func restrictDir(dir string) error { return os.Chmod(dir, 0o700) }

// restrictFile gives f mode 600: only the owner reads and writes it.
func restrictFile(f string) error { return os.Chmod(f, 0o600) }

// checkOwnerOnly returns an error when the group or other users can open f.
func checkOwnerOnly(f string) error {
	st, err := os.Stat(f)
	if err != nil {
		return err
	}
	if st.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s must be readable by its owner only. Run: chmod 600 %s", f, f)
	}
	return nil
}
