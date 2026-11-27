// Package config loads, merges, resolves and edits mcpsh configuration files.
//
// The file format is the standard "mcpServers" JSON used by Claude Desktop and Claude Code
// (.mcp.json), extended with optional mcpsh fields. Unknown fields are ignored on read and
// preserved when mcpsh edits a file.
package config

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Transport types.
const (
	TransportStdio = "stdio"
	TransportHTTP  = "http"
	TransportSSE   = "sse"
)

// Server is one entry of "mcpServers", as written in the file (not interpolated).
type Server struct {
	Type    string            `json:"type,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Cwd     string            `json:"cwd,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`

	AllowTools     []string `json:"allowTools,omitempty"`
	DenyTools      []string `json:"denyTools,omitempty"`
	ConfirmTools   []string `json:"confirmTools,omitempty"`
	NoConfirmTools []string `json:"noConfirmTools,omitempty"`

	Timeout        Duration `json:"timeout,omitempty"`
	StartupTimeout Duration `json:"startupTimeout,omitempty"`
	IdleTimeout    Duration `json:"idleTimeout,omitempty"`
	Roots          *bool    `json:"roots,omitempty"`
	Disabled       bool     `json:"disabled,omitempty"`

	OAuth *OAuthConfig `json:"oauth,omitempty"`
}

// OAuthConfig tunes OAuth for a remote (Streamable HTTP) server. OAuth needs no configuration:
// a server that answers 401 with OAuth metadata is supported through dynamic client
// registration. These fields are for servers that need a pre-registered client.
type OAuthConfig struct {
	// Disabled turns OAuth off (for example when the server takes a static token header).
	Disabled bool `json:"disabled,omitempty"`
	// ClientID and ClientSecret of a pre-registered client; the secret must be a ${VAR} reference.
	ClientID     string `json:"clientId,omitempty"`
	ClientSecret string `json:"clientSecret,omitempty"`
	// Scopes to request instead of the ones the server advertises.
	Scopes []string `json:"scopes,omitempty"`
	// RedirectPort fixes the loopback port of the login redirect (http://127.0.0.1:<port>/callback),
	// for clients registered with a fixed redirect URI. 0 picks a free port.
	RedirectPort int `json:"redirectPort,omitempty"`
}

// TransportType returns the effective transport: the explicit "type", else stdio when a
// command is set, else http when a url is set.
func (s *Server) TransportType() string {
	switch t := strings.ToLower(s.Type); t {
	case "streamable-http", "streamablehttp", "http":
		return TransportHTTP
	case "":
		if s.Command == "" && s.URL != "" {
			return TransportHTTP
		}
		return TransportStdio
	default:
		return t
	}
}

// Validate reports definition errors that make the server unusable.
func (s *Server) Validate() error {
	switch s.TransportType() {
	case TransportStdio:
		if s.Command == "" {
			return fmt.Errorf("stdio server needs a \"command\"")
		}
	case TransportHTTP, TransportSSE:
		if s.URL == "" {
			return fmt.Errorf("%s server needs a \"url\"", s.TransportType())
		}
	default:
		return fmt.Errorf("unknown transport type %q (use stdio, http or sse)", s.Type)
	}
	return nil
}

// Settings are global options; project values override user values key by key.
type Settings struct {
	CallTimeout       Duration `json:"callTimeout,omitempty"`
	StartupTimeout    Duration `json:"startupTimeout,omitempty"`
	CacheTTL          Duration `json:"cacheTTL,omitempty"`
	IdleTimeout       Duration `json:"idleTimeout,omitempty"`
	DaemonIdleTimeout Duration `json:"daemonIdleTimeout,omitempty"`
}

// Defaults applied when neither the server nor the settings give a value.
const (
	DefaultCallTimeout       = 60 * time.Second
	DefaultStartupTimeout    = 60 * time.Second
	DefaultCacheTTL          = 24 * time.Hour
	DefaultIdleTimeout       = 10 * time.Minute
	DefaultDaemonIdleTimeout = 30 * time.Minute
)

func (s Settings) merge(over Settings) Settings {
	pick := func(a, b Duration) Duration {
		if b != 0 {
			return b
		}
		return a
	}
	return Settings{
		CallTimeout:       pick(s.CallTimeout, over.CallTimeout),
		StartupTimeout:    pick(s.StartupTimeout, over.StartupTimeout),
		CacheTTL:          pick(s.CacheTTL, over.CacheTTL),
		IdleTimeout:       pick(s.IdleTimeout, over.IdleTimeout),
		DaemonIdleTimeout: pick(s.DaemonIdleTimeout, over.DaemonIdleTimeout),
	}
}

// Duration is a time.Duration written in JSON as a Go duration string ("90s", "10m")
// or as a number of seconds.
type Duration time.Duration

// D returns the value as a time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// Or returns d, or def when d is zero.
func (d Duration) Or(def time.Duration) time.Duration {
	if d == 0 {
		return def
	}
	return time.Duration(d)
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		v, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("invalid duration %q (examples: 30s, 10m, 1h)", s)
		}
		*d = Duration(v)
		return nil
	}
	f, err := strconv.ParseFloat(string(b), 64)
	if err != nil {
		return fmt.Errorf("invalid duration %s (use a string like \"30s\" or a number of seconds)", b)
	}
	*d = Duration(f * float64(time.Second))
	return nil
}
