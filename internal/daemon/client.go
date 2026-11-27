package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/davidemaggi/mcpsh/internal/config"
	"github.com/davidemaggi/mcpsh/internal/engine"
	"github.com/davidemaggi/mcpsh/internal/ipc"
	"github.com/davidemaggi/mcpsh/internal/mcpclient"
)

// Error is a failure of the daemon itself (cannot start, protocol mismatch): exit code 3.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

const (
	startWait = 10 * time.Second // how long the CLI waits for a daemon it started
	slack     = 10 * time.Second // extra local wait beyond the daemon's own timeouts
)

// Client talks to the daemon and implements engine.Engine.
type Client struct {
	// DaemonIdle is sent with each request (from the config's daemonIdleTimeout).
	DaemonIdle time.Duration
	self       ipc.BuildInfo
}

// NewClient returns a client for the current binary.
func NewClient(daemonIdle time.Duration) *Client {
	return &Client{DaemonIdle: daemonIdle, self: ipc.Self()}
}

// Close implements engine.Engine; sessions belong to the daemon.
func (c *Client) Close() error { return nil }

// ListTools implements engine.Engine.
func (c *Client) ListTools(ctx context.Context, rs *config.Resolved) ([]*mcp.Tool, error) {
	resp, err := c.request(ctx, rs, &ipc.Request{Op: ipc.OpListTools, Server: rs})
	if err != nil {
		return nil, err
	}
	var tools []*mcp.Tool
	if err := json.Unmarshal(resp.Tools, &tools); err != nil {
		return nil, &Error{Msg: "daemon sent an unreadable tool list: " + err.Error()}
	}
	return tools, nil
}

// CallTool implements engine.Engine.
func (c *Client) CallTool(ctx context.Context, rs *config.Resolved, tool string, args map[string]any) (*mcp.CallToolResult, error) {
	resp, err := c.request(ctx, rs, &ipc.Request{Op: ipc.OpCallTool, Server: rs, Tool: tool, Args: args})
	if err != nil {
		return nil, err
	}
	var res mcp.CallToolResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		return nil, &Error{Msg: "daemon sent an unreadable result: " + err.Error()}
	}
	return &res, nil
}

// ListResources implements engine.Engine.
func (c *Client) ListResources(ctx context.Context, rs *config.Resolved) (*engine.ResourceList, error) {
	var out engine.ResourceList
	if err := c.requestInto(ctx, rs, &ipc.Request{Op: ipc.OpListResources, Server: rs}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ReadResource implements engine.Engine.
func (c *Client) ReadResource(ctx context.Context, rs *config.Resolved, uri string) (*mcp.ReadResourceResult, error) {
	var out mcp.ReadResourceResult
	if err := c.requestInto(ctx, rs, &ipc.Request{Op: ipc.OpReadResource, Server: rs, URI: uri}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListPrompts implements engine.Engine.
func (c *Client) ListPrompts(ctx context.Context, rs *config.Resolved) ([]*mcp.Prompt, error) {
	var out []*mcp.Prompt
	if err := c.requestInto(ctx, rs, &ipc.Request{Op: ipc.OpListPrompts, Server: rs}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetPrompt implements engine.Engine.
func (c *Client) GetPrompt(ctx context.Context, rs *config.Resolved, name string, args map[string]string) (*mcp.GetPromptResult, error) {
	var out mcp.GetPromptResult
	if err := c.requestInto(ctx, rs, &ipc.Request{Op: ipc.OpGetPrompt, Server: rs, Prompt: name, PromptArgs: args}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// requestInto sends a request and decodes its result into out.
func (c *Client) requestInto(ctx context.Context, rs *config.Resolved, req *ipc.Request, out any) error {
	resp, err := c.request(ctx, rs, req)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(resp.Result, out); err != nil {
		return &Error{Msg: "daemon sent an unreadable result: " + err.Error()}
	}
	return nil
}

// request sends a server request. The ctx deadline becomes the daemon-side timeout of the
// request itself (server startup has its own timeout), exactly as in direct mode.
func (c *Client) request(ctx context.Context, rs *config.Resolved, req *ipc.Request) (*ipc.Response, error) {
	req.Key = rs.File + "\x00" + rs.Name
	wait := rs.StartupTimeout + slack
	if dl, ok := ctx.Deadline(); ok {
		left := time.Until(dl)
		if left <= 0 {
			return nil, &mcpclient.TimeoutError{Server: rs.Name}
		}
		req.TimeoutMs = max(left.Milliseconds(), 1)
		wait += left
	}
	lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), wait)
	defer cancel()
	stop := context.AfterFunc(ctx, func() {
		if errors.Is(ctx.Err(), context.Canceled) {
			cancel()
		}
	})
	defer stop()
	resp, err := c.Do(lctx, req, true)
	if err != nil {
		if ctx.Err() == context.Canceled {
			return nil, ctx.Err()
		}
		return nil, err
	}
	if resp.Error != nil {
		return nil, fromWire(resp.Error, rs.Name)
	}
	return resp, nil
}

// Do sends one request, starting (autostart) or replacing an outdated daemon as needed.
func (c *Client) Do(ctx context.Context, req *ipc.Request, autostart bool) (*ipc.Response, error) {
	req.Proto = ipc.ProtocolVersion
	req.Client = c.self
	if c.DaemonIdle > 0 {
		req.DaemonIdleMs = c.DaemonIdle.Milliseconds()
	}
	for attempt := 0; ; attempt++ {
		conn, err := c.dial(ctx, autostart)
		if err != nil {
			return nil, err
		}
		resp, err := roundTrip(ctx, conn, req)
		if err != nil {
			return nil, err
		}
		if resp.Error != nil && resp.Error.Kind == ipc.KindUpgrade && attempt == 0 {
			// The daemon is shutting down to make room for this newer binary.
			if err := waitStopped(ctx); err != nil {
				return nil, err
			}
			if req.Op == ipc.OpShutdown {
				return &ipc.Response{}, nil
			}
			continue
		}
		if resp.Error != nil && resp.Error.Kind == ipc.KindVersion {
			return nil, &Error{Msg: resp.Error.Message}
		}
		return resp, nil
	}
}

func roundTrip(ctx context.Context, conn net.Conn, req *ipc.Request) (*ipc.Response, error) {
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() }) // cancels the daemon side too
	defer stop()
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, &Error{Msg: "cannot talk to the mcpsh daemon: " + err.Error()}
	}
	var resp ipc.Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &Error{Msg: "the mcpsh daemon closed the connection: " + err.Error() + " (see \"mcpsh daemon logs\")"}
	}
	return &resp, nil
}

// dial connects, starting the daemon when allowed.
func (c *Client) dial(ctx context.Context, autostart bool) (net.Conn, error) {
	addr, err := ipc.Address()
	if err != nil {
		return nil, &Error{Msg: err.Error()}
	}
	if conn, err := dialOnce(ctx, addr); err == nil {
		return conn, nil
	}
	if !autostart {
		return nil, ErrNotRunning
	}
	if err := Start(); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(startWait)
	for {
		conn, err := dialOnce(ctx, addr)
		if err == nil {
			return conn, nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return nil, &Error{Msg: fmt.Sprintf("the mcpsh daemon did not start: %v (see \"mcpsh daemon logs\", or use --no-daemon)", err)}
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func dialOnce(ctx context.Context, addr string) (net.Conn, error) {
	dctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return ipc.Dial(dctx, addr)
}

// ErrNotRunning: no daemon answers and autostart was not requested.
var ErrNotRunning = errors.New("the mcpsh daemon is not running")

// Running reports whether a daemon answers.
func Running(ctx context.Context) bool {
	addr, err := ipc.Address()
	if err != nil {
		return false
	}
	conn, err := dialOnce(ctx, addr)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// waitStopped waits until the daemon released its lock (it exited).
func waitStopped(ctx context.Context) error {
	lockPath, err := ipc.LockPath()
	if err != nil {
		return err
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		if l, err := ipc.TryLock(lockPath); err == nil {
			l.Release()
			return nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return &Error{Msg: "the old mcpsh daemon did not stop in time; run \"mcpsh daemon stop\""}
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Start launches "mcpsh daemon run" in the background, detached from the terminal.
func Start() error {
	exe, err := os.Executable()
	if err != nil {
		return &Error{Msg: "cannot find the mcpsh executable: " + err.Error()}
	}
	p, err := startDetached(exe, []string{"daemon", "run"}, os.Environ())
	if err != nil {
		return &Error{Msg: "cannot start the mcpsh daemon: " + err.Error()}
	}
	_ = p.Release()
	return nil
}

// neutralDir is the daemon's working directory: not the caller's project, which the daemon
// would otherwise keep busy (on Windows a directory in use cannot be deleted).
func neutralDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return os.TempDir()
}

// Stop asks the daemon to exit and waits until it has.
func (c *Client) Stop(ctx context.Context) (bool, error) {
	if _, err := c.Do(ctx, &ipc.Request{Op: ipc.OpShutdown}, false); err != nil {
		if errors.Is(err, ErrNotRunning) {
			return false, nil
		}
		return false, err
	}
	return true, waitStopped(ctx)
}

// Status asks the running daemon for its status, without starting one.
func (c *Client) Status(ctx context.Context) (*ipc.Status, error) {
	resp, err := c.Do(ctx, &ipc.Request{Op: ipc.OpStatus}, false)
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, &Error{Msg: resp.Error.Message}
	}
	return resp.Status, nil
}

func fromWire(w *ipc.WireError, server string) error {
	if w.Server != "" {
		server = w.Server
	}
	switch w.Kind {
	case ipc.KindConnect:
		return &mcpclient.ConnectError{Server: server, Err: errors.New(w.Message), Stderr: w.Stderr}
	case ipc.KindTransport:
		return &mcpclient.TransportError{Server: server, Err: errors.New(w.Message), Stderr: w.Stderr}
	case ipc.KindTimeout:
		return &mcpclient.TimeoutError{Server: server}
	case ipc.KindServer:
		return &mcpclient.ServerError{Server: server, Code: w.Code, Message: w.Message}
	}
	return &Error{Msg: "mcpsh daemon: " + w.Message}
}
