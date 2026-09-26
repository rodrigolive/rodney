//go:build windows

package main

import "os/exec"

func setSysProcAttr(cmd *exec.Cmd) {
	// SysProcAttr.Setsid is not available on Windows; no-op on this platform
}

// processCommandContains can't read another process's command line without
// extra APIs on Windows, so it keeps the old behaviour: any positive PID is
// trusted.
func processCommandContains(pid int, want string) bool {
	return pid > 0
}
