//go:build windows

package daemon

import (
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// startDetached starts the daemon without a console, in its own process group, and outside the
// caller's job object when that job allows it: agent terminals and CI runners often run
// commands in a job that is killed as a whole, which would take the daemon with it.
func startDetached(exe string, args, env []string) (*os.Process, error) {
	base := uint32(windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP)
	for _, flags := range []uint32{base | windows.CREATE_BREAKAWAY_FROM_JOB, base} {
		cmd := exec.Command(exe, args...)
		cmd.Env = env
		cmd.Dir = neutralDir()
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags, HideWindow: true}
		err := cmd.Start()
		if err == nil {
			return cmd.Process, nil
		}
		if flags == base {
			return nil, err
		}
		// The job forbids breakaway: start inside it (the daemon then lives as long as the job).
	}
	panic("unreachable")
}
