//go:build windows

package main

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"unsafe"
)

func setProcGroup(cmd *exec.Cmd) {}

// Windows has no SIGINT for other processes; console Ctrl+C already reaches every process
// attached to the console, so "interrupt" ends the whole tree (bun.exe → node/vite) at once.
func interruptTree(cmd *exec.Cmd) { killTree(cmd) }

func killTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	tk := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
	if tk.Run() != nil {
		_ = cmd.Process.Kill()
	}
}

// enableVT turns on ANSI escape processing for the console; colours are off if it fails.
func enableVT() bool {
	const enableVirtualTerminalProcessing = 0x0004
	k32 := syscall.NewLazyDLL("kernel32.dll")
	get, set := k32.NewProc("GetConsoleMode"), k32.NewProc("SetConsoleMode")
	h := syscall.Handle(os.Stdout.Fd())
	var mode uint32
	if r, _, _ := get.Call(uintptr(h), uintptr(unsafe.Pointer(&mode))); r == 0 {
		return false
	}
	if mode&enableVirtualTerminalProcessing != 0 {
		return true
	}
	r, _, _ := set.Call(uintptr(h), uintptr(mode|enableVirtualTerminalProcessing))
	return r != 0
}
