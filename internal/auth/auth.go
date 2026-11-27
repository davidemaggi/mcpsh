// Package auth is the seam for authenticating to remote MCP servers.
//
// Today remote servers authenticate with static headers from the config (for example
// "Authorization": "Bearer ${TOKEN}"), applied by mcpclient. OAuth (phase 6) plugs in here: a
// Provider returns the SDK's auth.OAuthHandler for a server, which the Streamable HTTP
// transport calls on 401/403 and uses as its token source. Tokens will live in the user's
// state directory, never in config files.
package auth

import (
	"context"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/davidemaggi/mcpsh/internal/config"
)

// Provider returns the OAuth handler for a remote server, or nil when it needs none.
type Provider interface {
	OAuthHandler(ctx context.Context, rs *config.Resolved) (sdkauth.OAuthHandler, error)
}

// None is the provider used until OAuth support lands: static headers only.
type None struct{}

// OAuthHandler implements Provider.
func (None) OAuthHandler(context.Context, *config.Resolved) (sdkauth.OAuthHandler, error) {
	return nil, nil
}
