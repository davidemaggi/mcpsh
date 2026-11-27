// Package ipc is the local channel between the mcpsh CLI and its per-user daemon: a Unix domain
// socket (Linux, macOS) or a named pipe (Windows), reachable only by the current user, plus the
// lock file that guarantees a single daemon per state directory.
package ipc

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"

	"github.com/davidemaggi/mcpsh/internal/platform"
)

// instanceID distinguishes daemons of different state directories (tests, MCPSH_STATE_DIR).
func instanceID() (string, error) {
	dir, err := platform.StateDir()
	if err != nil {
		return "", err
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	sum := sha256.Sum256([]byte(filepath.Clean(dir)))
	return hex.EncodeToString(sum[:])[:12], nil
}

// LockPath is the file the running daemon keeps locked.
func LockPath() (string, error) {
	dir, err := platform.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "daemon.lock"), nil
}
