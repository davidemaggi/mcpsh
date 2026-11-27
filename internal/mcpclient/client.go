// Package mcpclient connects to one MCP server described by a config.Resolved, using the
// official go-sdk, and classifies failures so the CLI can map them to exit codes.
package mcpclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/davidemaggi/mcpsh/internal/auth"
	"github.com/davidemaggi/mcpsh/internal/config"
	"github.com/davidemaggi/mcpsh/internal/proc"
	"github.com/davidemaggi/mcpsh/internal/version"
)

// Options tune a connection.
type Options struct {
	Logger *slog.Logger
	// Stderr receives a copy of a stdio server's stderr (logs, --debug); may be nil.
	Stderr io.Writer
	// OnToolsChanged is called when the server sends notifications/tools/list_changed.
	OnToolsChanged func()
	// Auth provides OAuth for remote servers; nil means static headers only.
	Auth auth.Provider
}

// Conn is a live session with one server.
type Conn struct {
	Server  *config.Resolved
	Session *mcp.ClientSession
	proc    *proc.Process
	done    chan struct{}

	connCancel context.CancelFunc
}

// closeGrace is how long a stdio server gets to exit after its stdin closes.
const closeGrace = 2 * time.Second

// Connect starts (stdio) or contacts (http) the server and performs the MCP handshake,
// bounded by the server's startup timeout and ctx.
func Connect(ctx context.Context, rs *config.Resolved, opts Options) (*Conn, error) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	copts := &mcp.ClientOptions{Logger: logger}
	if len(rs.Roots) == 0 {
		copts.Capabilities = &mcp.ClientCapabilities{}
	}
	if opts.OnToolsChanged != nil {
		f := opts.OnToolsChanged
		copts.ToolListChangedHandler = func(context.Context, *mcp.ToolListChangedRequest) { f() }
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "mcpsh", Version: version.Version}, copts)
	for _, r := range rs.Roots {
		//lint:ignore SA1019 roots are deprecated by spec 2026-07-28 but still supported; mcpsh offers them on request
		client.AddRoots(&mcp.Root{URI: FileURI(r), Name: filepath.Base(r)})
	}

	c := &Conn{Server: rs, done: make(chan struct{})}
	var transport mcp.Transport
	switch rs.Transport {
	case config.TransportStdio:
		p, err := proc.Start(proc.Spec{
			Command: rs.Command,
			Args:    rs.Args,
			Env:     MergeEnv(os.Environ(), rs.Env),
			Dir:     rs.Cwd,
			Stderr:  opts.Stderr,
		})
		if err != nil {
			return nil, &ConnectError{Server: rs.Name, Err: err}
		}
		c.proc = p
		transport = &mcp.IOTransport{Reader: p.Stdout, Writer: p.Stdin}
	default:
		t, err := httpTransport(ctx, rs, opts.Auth)
		if err != nil {
			return nil, &ConnectError{Server: rs.Name, Err: err}
		}
		transport = t
	}

	// Some transports (legacy SSE) tie the connection to the context given to Connect, so it
	// must live as long as the session: cancellation and the startup timeout are enforced
	// here instead, and connCancel runs on Close.
	connCtx, connCancel := context.WithCancel(context.WithoutCancel(ctx))
	c.connCancel = connCancel
	type result struct {
		s   *mcp.ClientSession
		err error
	}
	ch := make(chan result, 1)
	go func() {
		s, err := client.Connect(connCtx, transport, nil)
		ch <- result{s, err}
	}()
	timer := time.NewTimer(rs.StartupTimeout)
	defer timer.Stop()
	var res result
	select {
	case res = <-ch:
	case <-timer.C:
		res.err = fmt.Errorf("no answer within the startup timeout (%s); raise \"startupTimeout\" if the server is slow to start", rs.StartupTimeout)
	case <-ctx.Done():
		res.err = ctx.Err()
	case <-c.procDone():
		// The process died during the handshake: no need to wait for the timeout.
		select {
		case res = <-ch:
		case <-time.After(500 * time.Millisecond):
			res.err = errors.New("server exited during startup")
		}
	}
	if res.err != nil {
		connCancel()
		go func() { // a handshake that completes after we gave up must not leak
			if late := <-ch; late.s != nil {
				_ = late.s.Close()
			}
		}()
		err := res.err
		ce := &ConnectError{Server: rs.Name, Err: err}
		if c.proc != nil {
			_ = c.proc.Close(closeGrace)
			ce.Stderr = lastLines(c.proc.StderrTail(), 15)
			if exitErr := c.proc.ExitErr(); exitErr != nil && !strings.Contains(err.Error(), "exit") {
				ce.Err = fmt.Errorf("%w (process: %v)", err, exitErr)
			}
		}
		return nil, ce
	}
	c.Session = res.s
	go func() {
		_ = c.Session.Wait()
		close(c.done)
	}()
	return c, nil
}

func (c *Conn) procDone() <-chan struct{} {
	if c.proc == nil {
		return nil
	}
	return c.proc.Done()
}

// Pid returns the PID of a stdio server, 0 otherwise.
func (c *Conn) Pid() int {
	if c.proc == nil {
		return 0
	}
	return c.proc.Pid()
}

// Done is closed when the session ends (server exit or Close).
func (c *Conn) Done() <-chan struct{} { return c.done }

// StderrTail returns the last lines a stdio server wrote to stderr.
func (c *Conn) StderrTail() string {
	if c.proc == nil {
		return ""
	}
	return lastLines(c.proc.StderrTail(), 15)
}

// ListTools returns every tool, following pagination.
func (c *Conn) ListTools(ctx context.Context) ([]*mcp.Tool, error) {
	var tools []*mcp.Tool
	for t, err := range c.Session.Tools(ctx, nil) {
		if err != nil {
			return nil, c.classify(ctx, err)
		}
		tools = append(tools, t)
	}
	return tools, nil
}

// CallTool calls a tool. A result with IsError is not an error here.
func (c *Conn) CallTool(ctx context.Context, name string, args map[string]any) (*mcp.CallToolResult, error) {
	if args == nil {
		args = map[string]any{}
	}
	res, err := c.Session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return nil, c.classify(ctx, err)
	}
	return res, nil
}

// Close ends the session and terminates a stdio server's whole process tree.
func (c *Conn) Close() error {
	var err error
	if c.Session != nil {
		done := make(chan struct{})
		go func() { err = c.Session.Close(); close(done) }()
		select {
		case <-done:
		case <-time.After(closeGrace):
		}
	}
	if c.proc != nil {
		if perr := c.proc.Close(closeGrace); perr != nil {
			err = perr
		}
	}
	if c.connCancel != nil {
		c.connCancel()
	}
	return err
}

func (c *Conn) classify(ctx context.Context, err error) error {
	var we *jsonrpc.Error
	if errors.As(err, &we) {
		return &ServerError{Server: c.Server.Name, Code: we.Code, Message: we.Message}
	}
	if errors.Is(err, context.DeadlineExceeded) || ctx.Err() == context.DeadlineExceeded {
		return &TimeoutError{Server: c.Server.Name}
	}
	te := &TransportError{Server: c.Server.Name, Err: err}
	if c.proc != nil {
		// A broken pipe usually means the process died; give it a moment to be reaped.
		select {
		case <-c.proc.Done():
			te.Err = fmt.Errorf("server process exited (%v)", c.proc.ExitErr())
			te.Stderr = c.StderrTail()
		case <-time.After(time.Second):
		}
	}
	return te
}

// FileURI turns an absolute path into a file:// URI (file:///C:/x on Windows).
func FileURI(path string) string {
	p := filepath.ToSlash(path)
	if runtime.GOOS == "windows" && !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}

// MergeEnv overlays extra on base ("K=V" entries). Keys compare case-insensitively on Windows.
func MergeEnv(base []string, extra map[string]string) []string {
	if len(extra) == 0 {
		return base
	}
	norm := func(k string) string {
		if runtime.GOOS == "windows" {
			return strings.ToUpper(k)
		}
		return k
	}
	over := map[string]bool{}
	for k := range extra {
		over[norm(k)] = true
	}
	out := make([]string, 0, len(base)+len(extra))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if !over[norm(k)] {
			out = append(out, kv)
		}
	}
	for k, v := range extra {
		out = append(out, k+"="+v)
	}
	return out
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\r\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
