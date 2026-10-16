package main

import (
	"os"
	"strconv"
	"strings"
)

// processStart returns the start time of process pid in clock ticks after boot (field 22 of
// /proc/<pid>/stat), or "" when it cannot read it.
func processStart(pid int) string {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return ""
	}
	// The command name (field 2) is in parentheses and can hold spaces; field 3 follows the last ")".
	i := strings.LastIndexByte(string(b), ')')
	if i < 0 {
		return ""
	}
	f := strings.Fields(string(b[i+1:]))
	if len(f) < 20 {
		return ""
	}
	return f[19]
}
