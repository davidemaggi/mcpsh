//go:build !windows

package daemon

import (
	"os"
	"os/exec"
	"syscall"
)

// startDetached starts the daemon in a new session, without a controlling terminal.
func startDetached(exe string, args, env []string) (*os.Process, error) {
	cmd := exec.Command(exe, args...)
	cmd.Env = env
	cmd.Dir = neutralDir()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd.Process, nil
}
