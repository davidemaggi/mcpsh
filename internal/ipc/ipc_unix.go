//go:build !windows

package ipc

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// Address is the socket path: $XDG_RUNTIME_DIR/mcpsh/<id>.sock, else <tmp>/mcpsh-<uid>/<id>.sock.
// Both directories are private to the user (0700, owned by us), which is what protects the socket.
func Address() (string, error) {
	id, err := instanceID()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("mcpsh-%d", os.Getuid()))
	if rd := os.Getenv("XDG_RUNTIME_DIR"); rd != "" && filepath.IsAbs(rd) {
		dir = filepath.Join(rd, "mcpsh")
	}
	return filepath.Join(dir, id+".sock"), nil
}

func privateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !fi.IsDir() || !ok || int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s is not a directory owned by the current user; refusing to use it", dir)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// Listen creates the socket. Call it only while holding the daemon lock: any existing socket
// file is then stale and is removed.
func Listen(addr string) (net.Listener, error) {
	if err := privateDir(filepath.Dir(addr)); err != nil {
		return nil, err
	}
	if err := os.Remove(addr); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	l, err := net.Listen("unix", addr)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(addr, 0o600); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}

// Dial connects to the daemon.
func Dial(ctx context.Context, addr string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", addr)
}

// Lock is an exclusive, non-blocking lock on a file, held for the daemon's lifetime.
type Lock struct{ f *os.File }

// TryLock takes the lock or reports ErrLocked.
func TryLock(path string) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, err
	}
	return &Lock{f: f}, nil
}

// Release drops the lock.
func (l *Lock) Release() { l.f.Close() }
