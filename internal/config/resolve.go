package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// Resolved is a server ready to connect to: interpolated, with defaults applied.
// It may hold secrets (env, headers): never log it as a whole, use Describe.
type Resolved struct {
	Name      string
	Scope     Scope
	File      string
	Transport string

	Command string
	Args    []string
	Env     map[string]string // only the configured variables; the child also inherits the parent environment
	Cwd     string
	URL     string
	Headers map[string]string
	Roots   []string // absolute directories advertised as MCP roots

	CallTimeout    time.Duration
	StartupTimeout time.Duration
	IdleTimeout    time.Duration

	AllowTools, DenyTools, ConfirmTools, NoConfirmTools []string

	// OAuth is set for Streamable HTTP servers that may use OAuth: not disabled and without a
	// static Authorization header.
	OAuth *ResolvedOAuth
	// AuthStamp changes when the stored OAuth login changes (login, logout), so that a session
	// started with the old credentials is replaced. Set it with SetAuthStamp.
	AuthStamp string

	// ID identifies the connection: two Resolved with the same ID can share a session.
	ID string
}

// ResolvedOAuth is the interpolated OAuth configuration.
type ResolvedOAuth struct {
	ClientID     string
	ClientSecret string
	Scopes       []string
	RedirectPort int
}

// SetAuthStamp records the state of the stored login and updates the ID.
func (r *Resolved) SetAuthStamp(stamp string) {
	r.AuthStamp = stamp
	r.ID = r.computeID()
}

// UnknownServerError is a server name that no config file defines.
type UnknownServerError struct{ Name string }

func (e *UnknownServerError) Error() string {
	return fmt.Sprintf("unknown server %q; run \"mcpsh list\" to see the configured servers", e.Name)
}

// DisabledServerError is a server turned off with "disabled": true.
type DisabledServerError struct {
	Name string
	File string
}

func (e *DisabledServerError) Error() string {
	return fmt.Sprintf("server %q is disabled in %s", e.Name, e.File)
}

// InvalidServerError is a server whose definition cannot be used.
type InvalidServerError struct {
	Name string
	File string
	Err  error
}

func (e *InvalidServerError) Error() string {
	return fmt.Sprintf("server %q in %s: %v", e.Name, e.File, e.Err)
}
func (e *InvalidServerError) Unwrap() error { return e.Err }

// Resolve interpolates the named server with lookup and applies defaults.
func (c *Config) Resolve(name string, lookup Lookup) (*Resolved, error) {
	e, ok := c.Servers[name]
	if !ok {
		return nil, &UnknownServerError{Name: name}
	}
	if e.Server.Disabled {
		return nil, &DisabledServerError{Name: name, File: e.File}
	}
	s := e.Server
	if err := s.Validate(); err != nil {
		return nil, &InvalidServerError{Name: name, File: e.File, Err: err}
	}
	exp := func(v, field string) (string, error) { return Expand(v, lookup, field) }
	r := &Resolved{
		Name:           name,
		Scope:          e.Scope,
		File:           e.File,
		Transport:      s.TransportType(),
		CallTimeout:    s.Timeout.Or(c.Settings.CallTimeout.Or(DefaultCallTimeout)),
		StartupTimeout: s.StartupTimeout.Or(c.Settings.StartupTimeout.Or(DefaultStartupTimeout)),
		IdleTimeout:    s.IdleTimeout.Or(c.Settings.IdleTimeout.Or(DefaultIdleTimeout)),
		AllowTools:     s.AllowTools,
		DenyTools:      s.DenyTools,
		ConfirmTools:   s.ConfirmTools,
		NoConfirmTools: s.NoConfirmTools,
	}
	var err error
	wrap := func(err error) error { return &InvalidServerError{Name: name, File: e.File, Err: err} }
	if r.Command, err = exp(s.Command, name+".command"); err != nil {
		return nil, wrap(err)
	}
	for i, a := range s.Args {
		v, err := exp(a, fmt.Sprintf("%s.args[%d]", name, i))
		if err != nil {
			return nil, wrap(err)
		}
		r.Args = append(r.Args, v)
	}
	if r.Env, err = expandMap(s.Env, lookup, name+".env"); err != nil {
		return nil, wrap(err)
	}
	if r.Headers, err = expandMap(s.Headers, lookup, name+".headers"); err != nil {
		return nil, wrap(err)
	}
	if r.URL, err = exp(s.URL, name+".url"); err != nil {
		return nil, wrap(err)
	}
	r.Cwd = c.WorkspaceRoot
	if s.Cwd != "" {
		cwd, err := exp(s.Cwd, name+".cwd")
		if err != nil {
			return nil, wrap(err)
		}
		if !filepath.IsAbs(cwd) {
			cwd = filepath.Join(filepath.Dir(e.File), cwd)
		}
		r.Cwd = filepath.Clean(cwd)
	}
	if r.Transport == TransportHTTP && (s.OAuth == nil || !s.OAuth.Disabled) && !hasHeader(r.Headers, "Authorization") {
		r.OAuth = &ResolvedOAuth{}
		if o := s.OAuth; o != nil {
			r.OAuth.Scopes, r.OAuth.RedirectPort = o.Scopes, o.RedirectPort
			if r.OAuth.ClientID, err = exp(o.ClientID, name+".oauth.clientId"); err != nil {
				return nil, wrap(err)
			}
			if r.OAuth.ClientSecret, err = exp(o.ClientSecret, name+".oauth.clientSecret"); err != nil {
				return nil, wrap(err)
			}
		}
	}
	if s.Roots == nil || *s.Roots {
		r.Roots = []string{c.WorkspaceRoot}
	}
	r.ID = r.computeID()
	return r, nil
}

func hasHeader(h map[string]string, name string) bool {
	for k := range h {
		if strings.EqualFold(k, name) {
			return true
		}
	}
	return false
}

func expandMap(m map[string]string, lookup Lookup, field string) (map[string]string, error) {
	if len(m) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		x, err := Expand(v, lookup, field+"."+k)
		if err != nil {
			return nil, err
		}
		out[k] = x
	}
	return out, nil
}

func (r *Resolved) computeID() string {
	// encoding/json sorts map keys, so this is deterministic.
	b, _ := json.Marshal(struct {
		T       string
		Cmd     string
		Args    []string
		Env     map[string]string
		Cwd     string
		URL     string
		Headers map[string]string
		Roots   []string
		OAuth   *ResolvedOAuth
		Stamp   string
	}{r.Transport, r.Command, r.Args, r.Env, r.Cwd, r.URL, r.Headers, r.Roots, r.OAuth, r.AuthStamp})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}

// Describe is a secret-free one-line description, for listings and logs.
func (r *Resolved) Describe() string {
	if r.Transport == TransportStdio {
		return r.Command
	}
	return r.URL
}

// DefinitionHash hashes the raw (uninterpolated) definition; trust decisions are bound to it.
func DefinitionHash(s *Server) string {
	b, _ := json.Marshal(s)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
