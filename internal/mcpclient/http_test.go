package mcpclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/davidemaggi/mcpsh/internal/config"
	"github.com/davidemaggi/mcpsh/internal/testserver"
)

func remote(t *testing.T, transport string) *config.Resolved {
	t.Helper()
	exe, _ := os.Executable()
	server := testserver.New(exe)
	var h http.Handler
	path := "/mcp"
	if transport == config.TransportSSE {
		h = mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return server }, nil)
		path = "/sse"
	} else {
		h = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	}
	mux := http.NewServeMux()
	mux.Handle(path, h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &config.Resolved{
		Name: "remote", Transport: transport, URL: srv.URL + path,
		Headers:        map[string]string{"X-Test": "hello"},
		StartupTimeout: 10 * time.Second, CallTimeout: 10 * time.Second,
	}
}

func TestRemoteTransports(t *testing.T) {
	for _, tr := range []string{config.TransportHTTP, config.TransportSSE} {
		t.Run(tr, func(t *testing.T) {
			ctx := context.Background()
			c, err := Connect(ctx, remote(t, tr), Options{})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			tools, err := c.ListTools(ctx)
			if err != nil || len(tools) < 10 {
				t.Fatalf("list tools: %d %v", len(tools), err)
			}
			res, err := c.CallTool(ctx, "echo", map[string]any{"text": "hi"})
			if err != nil || res.IsError || res.Content[0].(*mcp.TextContent).Text != "hi" {
				t.Fatalf("echo: %+v %v", res, err)
			}
			if tr == config.TransportHTTP {
				res, err = c.CallTool(ctx, "header", map[string]any{"name": "X-Test"})
				if err != nil || res.Content[0].(*mcp.TextContent).Text != "hello" {
					t.Errorf("custom header not received: %+v %v", res, err)
				}
				res, _ = c.CallTool(ctx, "header", map[string]any{"name": "User-Agent"})
				if ua := res.Content[0].(*mcp.TextContent).Text; !strings.HasPrefix(ua, "mcpsh/") {
					t.Errorf("user agent = %q", ua)
				}
			}
		})
	}
}
