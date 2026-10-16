package main

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// processStart returns the start time of process pid (seconds.microseconds), or "" when it
// cannot read it.
func processStart(pid int) string {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return ""
	}
	t := k.Proc.P_starttime
	return fmt.Sprintf("%d.%06d", t.Sec, t.Usec)
}
