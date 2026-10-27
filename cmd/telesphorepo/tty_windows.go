package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// isTerminal reports whether f is a console.
func isTerminal(f *os.File) bool {
	var mode uint32
	return windows.GetConsoleMode(windows.Handle(f.Fd()), &mode) == nil
}

// enableANSI turns on the escape sequences of the console of f (Windows 10 and later) and reports
// whether the console takes them.
func enableANSI(f *os.File) bool {
	h := windows.Handle(f.Fd())
	var mode uint32
	if windows.GetConsoleMode(h, &mode) != nil {
		return false
	}
	return windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) == nil
}
