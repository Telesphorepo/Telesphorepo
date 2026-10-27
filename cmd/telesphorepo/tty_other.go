//go:build !linux && !darwin && !windows

package main

import "os"

// isTerminal reports whether f is a character device.
func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// enableANSI reports whether the terminal of f takes escape sequences.
func enableANSI(*os.File) bool { return true }
