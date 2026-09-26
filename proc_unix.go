//go:build linux || darwin

package main

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

func setSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// processCommandContains reports whether pid is alive and its command line
// contains want. PIDs read back from state.json can outlive their process and
// be reused (a reboot, a crashed helper), so check this before signalling one.
func processCommandContains(pid int, want string) bool {
	if pid <= 1 || want == "" {
		return false
	}
	out, err := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), want)
}
