// Package engine is the seam between the CLI and the MCP sessions. The CLI parses, validates,
// confirms and renders; an Engine only talks to servers. Direct runs sessions in-process
// (--no-daemon); the daemon client (phase 2) implements the same interface, which keeps the
// visible behaviour identical in both modes.
package engine

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/davidemaggi/mcpsh/internal/config"
	"github.com/davidemaggi/mcpsh/internal/mcpclient"
	"github.com/davidemaggi/mcpsh/internal/toolcache"
)

// Engine runs MCP requests for the CLI.
type Engine interface {
	// ListTools fetches the server's tools (unfiltered) and refreshes the discovery cache.
	ListTools(ctx context.Context, rs *config.Resolved) ([]*mcp.Tool, error)
	// CallTool calls a tool; a result with IsError is not an error.
	CallTool(ctx context.Context, rs *config.Resolved, tool string, args map[string]any) (*mcp.CallToolResult, error)
	// Close releases the sessions this engine opened for the current command.
	Close() error
}

// Direct connects to servers from the CLI process and closes them when the command ends.
type Direct struct {
	Logger *slog.Logger
	Stderr io.Writer // copy of stdio servers' stderr, for debugging; may be nil

	mu    sync.Mutex
	conns map[string]*mcpclient.Conn
}

// NewDirect returns an in-process engine.
func NewDirect(logger *slog.Logger, stderr io.Writer) *Direct {
	return &Direct{Logger: logger, Stderr: stderr, conns: map[string]*mcpclient.Conn{}}
}

func (d *Direct) conn(ctx context.Context, rs *config.Resolved) (*mcpclient.Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if c, ok := d.conns[rs.ID]; ok {
		return c, nil
	}
	// Starting the server is bounded by its startup timeout, not by the deadline of the
	// request that triggered it; cancellation (Ctrl+C) still applies.
	sctx, cancel := withoutDeadline(ctx)
	defer cancel()
	c, err := mcpclient.Connect(sctx, rs, mcpclient.Options{Logger: d.Logger, Stderr: d.Stderr})
	if err != nil {
		return nil, err
	}
	d.conns[rs.ID] = c
	return c, nil
}

// withoutDeadline keeps ctx's values and explicit cancellation but drops its deadline.
func withoutDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	nctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stop := context.AfterFunc(ctx, func() {
		if errors.Is(ctx.Err(), context.Canceled) {
			cancel()
		}
	})
	return nctx, func() { stop(); cancel() }
}

func (d *Direct) ListTools(ctx context.Context, rs *config.Resolved) ([]*mcp.Tool, error) {
	c, err := d.conn(ctx, rs)
	if err != nil {
		return nil, err
	}
	tools, err := c.ListTools(ctx)
	if err != nil {
		return nil, err
	}
	if err := toolcache.Put(rs.ID, rs.Name, tools); err != nil && d.Logger != nil {
		d.Logger.Warn("cannot write the tool cache", "server", rs.Name, "error", err)
	}
	return tools, nil
}

func (d *Direct) CallTool(ctx context.Context, rs *config.Resolved, tool string, args map[string]any) (*mcp.CallToolResult, error) {
	c, err := d.conn(ctx, rs)
	if err != nil {
		return nil, err
	}
	return c.CallTool(ctx, tool, args)
}

func (d *Direct) Close() error {
	d.mu.Lock()
	conns := d.conns
	d.conns = map[string]*mcpclient.Conn{}
	d.mu.Unlock()
	var wg sync.WaitGroup
	for _, c := range conns {
		wg.Add(1)
		go func() { defer wg.Done(); _ = c.Close() }()
	}
	wg.Wait()
	return nil
}
