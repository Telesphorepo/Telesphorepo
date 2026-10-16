//go:build !linux && !darwin && !windows

package main

// processStart returns "": this system does not tell the start time of a process.
func processStart(int) string { return "" }
