//go:build !windows

package proc

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func command(s Spec) (*exec.Cmd, error) {
	return exec.Command(s.Command, s.Args...), nil
}

// prepare puts the child in a new process group, so the whole tree can be signalled and
// terminal signals (Ctrl+C) do not reach it directly.
func prepare(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

type groupTree struct{ pgid int }

func newTree() (tree, error) { return &groupTree{}, nil }

func (g *groupTree) attach(p *os.Process) error {
	g.pgid = p.Pid // Setpgid with Pgid 0 makes the child the group leader
	return nil
}

func (g *groupTree) signal() error {
	return groupKill(g.pgid, syscall.SIGTERM)
}

func (g *groupTree) kill() error {
	return groupKill(g.pgid, syscall.SIGKILL)
}

func (g *groupTree) release() {}

func groupKill(pgid int, sig syscall.Signal) error {
	if pgid <= 0 {
		return errors.New("no process group")
	}
	err := syscall.Kill(-pgid, sig)
	if errors.Is(err, syscall.ESRCH) {
		return nil // nothing left
	}
	return err
}
