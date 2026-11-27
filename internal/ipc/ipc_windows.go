//go:build windows

package ipc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

func userSID() (string, error) {
	tok := windows.GetCurrentProcessToken()
	u, err := tok.GetTokenUser()
	if err != nil {
		return "", err
	}
	return u.User.Sid.String(), nil
}

// Address is the pipe name: \\.\pipe\mcpsh-<user SID>-<id>.
func Address() (string, error) {
	id, err := instanceID()
	if err != nil {
		return "", err
	}
	sid, err := userSID()
	if err != nil {
		return "", err
	}
	return `\\.\pipe\mcpsh-` + sid + "-" + id, nil
}

// Listen creates the pipe. Its DACL is protected (no inheritance), allows only the current
// user, and denies network logons, so other users and remote clients cannot connect.
func Listen(addr string) (net.Listener, error) {
	sid, err := userSID()
	if err != nil {
		return nil, err
	}
	sd := fmt.Sprintf("D:P(D;;GA;;;NU)(A;;GA;;;%s)", sid)
	return winio.ListenPipe(addr, &winio.PipeConfig{SecurityDescriptor: sd})
}

// Dial connects to the daemon.
func Dial(ctx context.Context, addr string) (net.Conn, error) {
	return winio.DialPipeContext(ctx, addr)
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
	ol := new(windows.Overlapped)
	err = windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ol)
	if err != nil {
		f.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, ErrLocked
		}
		return nil, err
	}
	return &Lock{f: f}, nil
}

// Release drops the lock.
func (l *Lock) Release() { l.f.Close() }
