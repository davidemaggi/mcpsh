// Package cli implements the mcpsh command line: built-in commands, and server/tool commands
// built at runtime from discovery.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/davidemaggi/mcpsh/internal/config"
	"github.com/davidemaggi/mcpsh/internal/daemon"
	"github.com/davidemaggi/mcpsh/internal/engine"
	"github.com/davidemaggi/mcpsh/internal/platform"
)

// Environment variables.
const (
	EnvNoDaemon = "MCPSH_NO_DAEMON" // "1" runs every request in-process
	EnvDebug    = "MCPSH_DEBUG"     // "1" prints debug logs and server stderr
)

// App holds what a command needs; tests build it with their own streams.
type App struct {
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	Cwd            string
	Lookup         config.Lookup
	// Interactive reports whether we may prompt the user (stdin and stderr are terminals).
	Interactive bool

	// Global flags.
	output       string
	timeout      time.Duration
	yes          bool
	noDaemon     bool // effective: flag or environment
	noDaemonFlag bool

	logger *slog.Logger
	debug  bool
	cfg    *config.Config
	trust  *config.TrustStore
	eng    engine.Engine
}

// NewApp returns an App wired to the process environment.
func NewApp() *App {
	cwd, _ := os.Getwd()
	return &App{
		Stdin:       os.Stdin,
		Stdout:      os.Stdout,
		Stderr:      os.Stderr,
		Cwd:         cwd,
		Lookup:      os.LookupEnv,
		Interactive: platform.IsTerminal(os.Stdin) && platform.IsTerminal(os.Stderr),
	}
}

// Run executes the command line and returns the exit code.
func (a *App) Run(ctx context.Context, args []string) int {
	a.debug = envTrue(a.Lookup, EnvDebug)
	a.noDaemon = envTrue(a.Lookup, EnvNoDaemon)
	if a.debug {
		a.logger = slog.New(slog.NewTextHandler(a.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	} else {
		a.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	err := a.execute(ctx, args)
	if a.eng != nil {
		_ = a.eng.Close()
	}
	code := ExitCode(err)
	var se *silentError
	if err != nil && !errors.Is(err, errToolFailed) && !errors.As(err, &se) {
		fmt.Fprintln(a.Stderr, "mcpsh: "+strings.TrimSpace(err.Error()))
	}
	return code
}

// config loads the merged config once per command and prints its warnings.
func (a *App) config() (*config.Config, error) {
	if a.cfg != nil {
		return a.cfg, nil
	}
	cfg, err := config.Load(a.Cwd)
	if err != nil {
		return nil, err
	}
	for _, w := range cfg.Warnings {
		fmt.Fprintln(a.Stderr, "mcpsh: warning: "+w)
	}
	a.cfg = cfg
	return cfg, nil
}

func (a *App) trustStore() (*config.TrustStore, error) {
	if a.trust != nil {
		return a.trust, nil
	}
	ts, err := config.LoadTrust()
	if err != nil {
		return nil, err
	}
	a.trust = ts
	return ts, nil
}

// engine returns the engine for this command: the daemon client, or in-process sessions with
// --no-daemon / MCPSH_NO_DAEMON=1.
func (a *App) engine() engine.Engine {
	if a.eng != nil {
		return a.eng
	}
	if a.noDaemon {
		var serverStderr io.Writer
		if a.debug {
			serverStderr = a.Stderr
		}
		a.eng = engine.NewDirect(a.logger, serverStderr)
		return a.eng
	}
	a.eng = daemon.NewClient(a.daemonIdle())
	return a.eng
}

func (a *App) daemonIdle() time.Duration {
	if cfg, err := a.config(); err == nil {
		return cfg.Settings.DaemonIdleTimeout.Or(config.DefaultDaemonIdleTimeout)
	}
	return config.DefaultDaemonIdleTimeout
}

// resolve returns a usable, trusted server definition.
func (a *App) resolve(name string) (*config.Resolved, error) {
	cfg, err := a.config()
	if err != nil {
		return nil, err
	}
	e, ok := cfg.Servers[name]
	if !ok {
		return nil, &config.UnknownServerError{Name: name}
	}
	ts, err := a.trustStore()
	if err != nil {
		return nil, err
	}
	if err := ts.Check(e); err != nil {
		return nil, err
	}
	return cfg.Resolve(name, a.Lookup)
}

func (a *App) jsonOutput() bool { return a.output == "json" }

func envTrue(lookup config.Lookup, name string) bool {
	v, _ := lookup(name)
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
