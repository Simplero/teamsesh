//go:build !windows

package daemon

import "syscall"

func processAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }
