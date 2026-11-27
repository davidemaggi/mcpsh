//go:build !windows

package proc

import (
	"errors"
	"syscall"
)

// Alive reports whether a process with this PID exists.
func Alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
