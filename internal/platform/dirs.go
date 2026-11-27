// Package platform hides per-OS differences: where files live and whether we talk to a terminal.
package platform

import (
	"os"
	"path/filepath"
	"runtime"

	"golang.org/x/term"
)

// Environment variables that override the default locations (used by tests and for isolation).
const (
	EnvConfigDir = "MCPSH_CONFIG_DIR"
	EnvStateDir  = "MCPSH_STATE_DIR"
)

// ConfigDir is where the user config file lives:
// $MCPSH_CONFIG_DIR, else %APPDATA%\mcpsh on Windows, else $XDG_CONFIG_HOME/mcpsh or ~/.config/mcpsh.
func ConfigDir() (string, error) {
	if d := os.Getenv(EnvConfigDir); d != "" {
		return d, nil
	}
	if runtime.GOOS == "windows" {
		if d := os.Getenv("APPDATA"); d != "" {
			return filepath.Join(d, "mcpsh"), nil
		}
	}
	return xdg("XDG_CONFIG_HOME", ".config")
}

// StateDir holds trust decisions, logs and daemon files:
// $MCPSH_STATE_DIR, else %LOCALAPPDATA%\mcpsh\state on Windows, else $XDG_STATE_HOME/mcpsh or ~/.local/state/mcpsh.
func StateDir() (string, error) {
	if d := os.Getenv(EnvStateDir); d != "" {
		return d, nil
	}
	if runtime.GOOS == "windows" {
		if d := os.Getenv("LOCALAPPDATA"); d != "" {
			return filepath.Join(d, "mcpsh", "state"), nil
		}
	}
	return xdg("XDG_STATE_HOME", filepath.Join(".local", "state"))
}

// CacheDir holds the discovery cache: $MCPSH_STATE_DIR/cache when that is set,
// else %LOCALAPPDATA%\mcpsh\cache on Windows, else $XDG_CACHE_HOME/mcpsh or ~/.cache/mcpsh.
func CacheDir() (string, error) {
	if d := os.Getenv(EnvStateDir); d != "" {
		return filepath.Join(d, "cache"), nil
	}
	if runtime.GOOS == "windows" {
		if d := os.Getenv("LOCALAPPDATA"); d != "" {
			return filepath.Join(d, "mcpsh", "cache"), nil
		}
	}
	return xdg("XDG_CACHE_HOME", ".cache")
}

func xdg(env, fallback string) (string, error) {
	if d := os.Getenv(env); d != "" && filepath.IsAbs(d) {
		return filepath.Join(d, "mcpsh"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, fallback, "mcpsh"), nil
}

// IsTerminal reports whether f is an interactive terminal.
func IsTerminal(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}
