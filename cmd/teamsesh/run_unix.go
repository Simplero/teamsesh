//go:build !windows

package main

import (
	"os"
	"os/exec"
	"syscall"
)

// run replaces teamsesh with the assistant, so it owns the terminal.
func run(dir, name string, args ...string) error {
	path, err := exec.LookPath(name)
	if err != nil {
		return err
	}
	if err := os.Chdir(dir); err != nil {
		return err
	}
	return syscall.Exec(path, append([]string{name}, args...), os.Environ())
}
