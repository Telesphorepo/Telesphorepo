//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// setProcGroup puts the child in its own process group so stopping it also stops its
// descendants (bun → vite, ...). Ctrl+C reaches only this tool, which then stops children.
func setProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func interruptTree(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
	}
}

func killTree(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

func enableVT() bool { return true }
