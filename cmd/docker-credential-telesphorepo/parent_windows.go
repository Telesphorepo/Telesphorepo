package main

import (
	"strconv"

	"golang.org/x/sys/windows"
)

// processStart returns the creation time of process pid in nanoseconds since 1970, or "" when it
// cannot read it.
func processStart(pid int) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return ""
	}
	return strconv.FormatInt(created.Nanoseconds(), 10)
}
