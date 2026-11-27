package ipc

import (
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/davidemaggi/mcpsh/internal/config"
	"github.com/davidemaggi/mcpsh/internal/version"
)

// ProtocolVersion changes whenever the messages below change incompatibly.
const ProtocolVersion = 2

// ErrLocked: another daemon holds the lock.
var ErrLocked = errors.New("another mcpsh daemon is running")

// Operations.
const (
	OpStatus    = "status"
	OpListTools = "list_tools"
	OpCallTool  = "call_tool"
	OpShutdown  = "shutdown"
	// Since protocol 2.
	OpListResources = "list_resources"
	OpReadResource  = "read_resource"
	OpListPrompts   = "list_prompts"
	OpGetPrompt     = "get_prompt"
)

// Request is one call from the CLI. One request and one response per connection; the CLI
// closes the connection to cancel.
type Request struct {
	Proto  int              `json:"proto"`
	Op     string           `json:"op"`
	Client BuildInfo        `json:"client"`
	Server *config.Resolved `json:"server,omitempty"` // may hold secrets: never log it
	// Key identifies the definition's origin (file + name): a new definition for the same key
	// replaces the old session.
	Key          string            `json:"key,omitempty"`
	Tool         string            `json:"tool,omitempty"`
	Args         map[string]any    `json:"args,omitempty"`
	URI          string            `json:"uri,omitempty"`
	Prompt       string            `json:"prompt,omitempty"`
	PromptArgs   map[string]string `json:"promptArgs,omitempty"`
	TimeoutMs    int64             `json:"timeoutMs,omitempty"`
	DaemonIdleMs int64             `json:"daemonIdleMs,omitempty"`
}

// Response answers a Request.
type Response struct {
	Proto  int             `json:"proto"`
	Daemon DaemonInfo      `json:"daemon"`
	Error  *WireError      `json:"error,omitempty"`
	Tools  json.RawMessage `json:"tools,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Status *Status         `json:"status,omitempty"`
}

// Error kinds.
const (
	KindConnect   = "connect"
	KindTransport = "transport"
	KindTimeout   = "timeout"
	KindServer    = "server"
	KindAuth      = "auth"    // the server needs "mcpsh auth login"
	KindUpgrade   = "upgrade" // the client is newer: the daemon is shutting down to be replaced
	KindVersion   = "version" // the daemon is newer than the client
	KindInternal  = "internal"
)

// WireError carries a classified error.
type WireError struct {
	Kind    string `json:"kind"`
	Server  string `json:"server,omitempty"`
	Message string `json:"message"`
	Code    int64  `json:"code,omitempty"`
	Stderr  string `json:"stderr,omitempty"`
}

// BuildInfo identifies a binary.
type BuildInfo struct {
	Version string `json:"version"`
	Exe     string `json:"exe"`
	ModTime int64  `json:"modTime"` // unix nanoseconds of the executable
}

// DaemonInfo describes the running daemon.
type DaemonInfo struct {
	BuildInfo
	Proto     int       `json:"proto"`
	Pid       int       `json:"pid"`
	StartedAt time.Time `json:"startedAt"`
}

// Status is the daemon's state, for "mcpsh daemon status" and "mcpsh list".
type Status struct {
	Daemon        DaemonInfo      `json:"daemon"`
	IdleTimeoutMs int64           `json:"idleTimeoutMs"`
	Sessions      []SessionStatus `json:"sessions"`
}

// SessionStatus is one server session.
type SessionStatus struct {
	Name      string    `json:"name"`
	ID        string    `json:"id"`
	File      string    `json:"file"`
	Target    string    `json:"target"`
	State     string    `json:"state"` // starting, running, crashed
	Pid       int       `json:"pid,omitempty"`
	StartedAt time.Time `json:"startedAt,omitzero"`
	LastUsed  time.Time `json:"lastUsed,omitzero"`
	Inflight  int       `json:"inflight"`
	Calls     int64     `json:"calls"`
	Restarts  int       `json:"restarts"`
	LastError string    `json:"lastError,omitempty"`
}

// Self describes the running binary.
func Self() BuildInfo {
	b := BuildInfo{Version: version.Version}
	if exe, err := os.Executable(); err == nil {
		b.Exe = exe
		if fi, err := os.Stat(exe); err == nil {
			b.ModTime = fi.ModTime().UnixNano()
		}
	}
	return b
}

// Newer reports whether client is a newer build than daemon: release versions compare by
// semantic version; otherwise (dev builds, equal versions) a different executable with a
// later modification time counts as newer.
func Newer(client, daemon BuildInfo) bool {
	cv, ok1 := parseSemver(client.Version)
	dv, ok2 := parseSemver(daemon.Version)
	if ok1 && ok2 && cv != dv {
		for i := range cv {
			if cv[i] != dv[i] {
				return cv[i] > dv[i]
			}
		}
	}
	if client.Exe == daemon.Exe && client.ModTime == daemon.ModTime {
		return false
	}
	return client.ModTime > daemon.ModTime
}

func parseSemver(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(v, "v")
	v, _, _ = strings.Cut(v, "-")
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
