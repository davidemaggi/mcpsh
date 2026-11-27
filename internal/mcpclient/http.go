package mcpclient

import (
	"context"
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/davidemaggi/mcpsh/internal/auth"
	"github.com/davidemaggi/mcpsh/internal/config"
	"github.com/davidemaggi/mcpsh/internal/version"
)

// httpTransport builds the Streamable HTTP or legacy SSE transport for a remote server.
func httpTransport(ctx context.Context, rs *config.Resolved, provider auth.Provider) (mcp.Transport, error) {
	client := &http.Client{Transport: &headerTransport{
		base:    http.DefaultTransport,
		headers: rs.Headers,
	}}
	switch rs.Transport {
	case config.TransportHTTP:
		t := &mcp.StreamableClientTransport{Endpoint: rs.URL, HTTPClient: client}
		if provider != nil {
			h, err := provider.OAuthHandler(ctx, rs)
			if err != nil {
				return nil, err
			}
			t.OAuthHandler = h
		}
		return t, nil
	case config.TransportSSE:
		return &mcp.SSEClientTransport{Endpoint: rs.URL, HTTPClient: client}, nil
	}
	return nil, fmt.Errorf("transport %q is not supported", rs.Transport)
}

// headerTransport adds the configured headers (and a User-Agent) to every request.
type headerTransport struct {
	base    http.RoundTripper
	headers map[string]string
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	for k, v := range t.headers {
		r.Header.Set(k, v)
	}
	if r.Header.Get("User-Agent") == "" {
		r.Header.Set("User-Agent", "mcpsh/"+version.Version)
	}
	return t.base.RoundTrip(r)
}
