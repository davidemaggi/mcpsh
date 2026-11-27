// Package auth authenticates mcpsh to remote MCP servers with OAuth.
//
// Servers that take a static token use a header from the config ("Authorization":
// "Bearer ${TOKEN}") and need nothing from here. For OAuth servers, "mcpsh auth login <server>"
// runs the authorization code flow (PKCE, dynamic client registration or a pre-registered
// client) through the SDK and stores the result (Record). Every later connection, from the CLI
// or the daemon, uses the stored token and refreshes it; it never opens a browser on its own:
// a server that asks for authorization yields a LoginRequiredError.
package auth

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"golang.org/x/oauth2"

	"github.com/davidemaggi/mcpsh/internal/config"
)

// Provider returns the OAuth handler for a remote server, or nil when it needs none.
type Provider interface {
	OAuthHandler(ctx context.Context, rs *config.Resolved) (sdkauth.OAuthHandler, error)
}

// LoginRequiredError: the server asked for authorization and there is no valid stored login.
type LoginRequiredError struct{ Server string }

func (e *LoginRequiredError) Error() string {
	return fmt.Sprintf("server %q requires authorization: run \"mcpsh auth login %s\"", e.Server, e.Server)
}

// Stored is the provider for normal use: the login saved by "mcpsh auth login".
type Stored struct{}

// OAuthHandler implements Provider.
func (Stored) OAuthHandler(_ context.Context, rs *config.Resolved) (sdkauth.OAuthHandler, error) {
	if rs.OAuth == nil {
		return nil, nil
	}
	rec, err := Load(rs.URL)
	if err != nil {
		return nil, err
	}
	h := &storedHandler{server: rs.Name}
	if rec != nil {
		h.ts = newPersistingSource(context.Background(), rec)
	}
	return h, nil
}

type storedHandler struct {
	server string
	ts     oauth2.TokenSource
}

func (h *storedHandler) TokenSource(context.Context) (oauth2.TokenSource, error) {
	if h.ts == nil {
		return nil, nil
	}
	return h.ts, nil
}

func (h *storedHandler) Authorize(_ context.Context, _ *http.Request, resp *http.Response) error {
	resp.Body.Close()
	return &LoginRequiredError{Server: h.server}
}

// persistingSource refreshes the token when it expires and stores the new one, so that the
// next process starts from it.
type persistingSource struct {
	mu   sync.Mutex
	rec  *Record
	base oauth2.TokenSource
}

func newPersistingSource(ctx context.Context, rec *Record) *persistingSource {
	return &persistingSource{rec: rec, base: rec.Config().TokenSource(ctx, rec.Token)}
}

func (s *persistingSource) Token() (*oauth2.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.base.Token()
	if err != nil {
		return nil, err
	}
	if t.AccessToken != s.rec.Token.AccessToken {
		rec := *s.rec
		rec.Token = t
		// Only update the login this source came from: after a logout or a new login, an old
		// session (in the daemon) must not bring its token back.
		if cur, err := Load(rec.ServerURL); err == nil && cur != nil && cur.LoggedInAt == rec.LoggedInAt && cur.ClientID == rec.ClientID {
			_ = Save(&rec)
		}
		s.rec = &rec
	}
	return t, nil
}
