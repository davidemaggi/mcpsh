// Package proc starts MCP stdio servers as child processes and guarantees that closing one
// terminates its whole process tree (npx/uvx wrappers, shells, grandchildren): a job object on
// Windows, a process group on Unix.
package proc

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// Spec describes the process to start.
type Spec struct {
	Command string
	Args    []string
	Env     []string  // full environment ("K=V"); nil inherits the current one
	Dir     string    // working directory; "" is the current one
	Stderr  io.Writer // optional copy of the child's stderr (logs, debug)
}

// Process is a running child with its stdio pipes.
type Process struct {
	Stdin  io.WriteCloser // write end of the child's stdin
	Stdout io.ReadCloser  // read end of the child's stdout

	cmd    *exec.Cmd
	tree   tree
	stderr *tail
	done   chan struct{}
	err    error

	closeOnce sync.Once
	closeErr  error
}

// tree is the OS-specific handle on the child's process tree.
type tree interface {
	// attach is called right after the child started (suspended on Windows).
	attach(p *os.Process) error
	// kill terminates every process in the tree.
	kill() error
	// signal asks the tree to stop gracefully, where the OS has a way to do it.
	signal() error
	release()
}

// Start launches the process with its own process tree.
func Start(s Spec) (*Process, error) {
	if s.Command == "" {
		return nil, errors.New("empty command")
	}
	cmd, err := command(s)
	if err != nil {
		return nil, err
	}
	cmd.Env = s.Env
	cmd.Dir = s.Dir

	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		return nil, err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		outR.Close()
		outW.Close()
		return nil, err
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, errW

	t, err := newTree()
	if err != nil {
		for _, f := range []*os.File{inR, inW, outR, outW, errR, errW} {
			f.Close()
		}
		return nil, err
	}
	prepare(cmd)
	startErr := cmd.Start()
	// The child has its own copies now.
	inR.Close()
	outW.Close()
	errW.Close()
	if startErr != nil {
		inW.Close()
		outR.Close()
		errR.Close()
		t.release()
		return nil, fmt.Errorf("starting %s: %w", s.Command, startErr)
	}
	if err := t.attach(cmd.Process); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		inW.Close()
		outR.Close()
		errR.Close()
		t.release()
		return nil, fmt.Errorf("starting %s: %w", s.Command, err)
	}

	p := &Process{
		Stdin:  inW,
		Stdout: outR,
		cmd:    cmd,
		tree:   t,
		stderr: newTail(8 << 10),
		done:   make(chan struct{}),
	}
	go func() {
		var w io.Writer = p.stderr
		if s.Stderr != nil {
			w = io.MultiWriter(p.stderr, s.Stderr)
		}
		_, _ = io.Copy(w, errR)
		errR.Close()
	}()
	go func() {
		p.err = cmd.Wait()
		close(p.done)
	}()
	return p, nil
}

// Pid is the child's process ID.
func (p *Process) Pid() int { return p.cmd.Process.Pid }

// Done is closed when the child exits.
func (p *Process) Done() <-chan struct{} { return p.done }

// ExitErr is the child's exit status; valid after Done.
func (p *Process) ExitErr() error { return p.err }

// StderrTail returns the last lines the child wrote to stderr.
func (p *Process) StderrTail() string { return p.stderr.String() }

// Close stops the child: it closes stdin (the MCP way to ask a stdio server to exit), waits up
// to grace, asks the tree to stop, waits again, then kills the whole tree. Leftover
// grandchildren are killed even when the child exited on its own.
func (p *Process) Close(grace time.Duration) error {
	p.closeOnce.Do(func() {
		p.Stdin.Close()
		if !p.wait(grace) {
			if p.tree.signal() == nil {
				p.wait(grace)
			}
		}
		if err := p.tree.kill(); err != nil && !p.exited() {
			p.closeErr = err
		}
		if !p.wait(5 * time.Second) {
			p.closeErr = errors.New("process did not exit after being killed")
		}
		p.Stdout.Close()
		p.tree.release()
	})
	return p.closeErr
}

func (p *Process) wait(d time.Duration) bool {
	select {
	case <-p.done:
		return true
	case <-time.After(d):
		return false
	}
}

func (p *Process) exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// tail keeps the last n bytes written to it.
type tail struct {
	mu  sync.Mutex
	buf []byte
	n   int
}

func newTail(n int) *tail { return &tail{n: n} }

func (t *tail) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, b...)
	if len(t.buf) > t.n {
		t.buf = append([]byte(nil), t.buf[len(t.buf)-t.n:]...)
	}
	return len(b), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
