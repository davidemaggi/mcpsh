package auth

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"sync"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"

	"github.com/davidemaggi/mcpsh/internal/config"
)

// Login is an interactive authorization: it is the Provider of the one connection that
// "mcpsh auth login" opens. The browser comes back to a loopback listener.
type Login struct {
	rs      *config.Resolved
	out     io.Writer
	ln      net.Listener
	srv     *http.Server
	handler *sdkauth.AuthorizationCodeHandler
	results chan *sdkauth.AuthorizationResult

	mu    sync.Mutex
	saved bool
}

// NewLogin prepares a login to rs; out receives the instructions for the user.
func NewLogin(rs *config.Resolved, out io.Writer) (*Login, error) {
	if rs.OAuth == nil {
		return nil, fmt.Errorf("OAuth is off for server %q", rs.Name)
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(rs.OAuth.RedirectPort)))
	if err != nil {
		return nil, fmt.Errorf("cannot listen for the login redirect: %v", err)
	}
	l := &Login{rs: rs, out: out, ln: ln, results: make(chan *sdkauth.AuthorizationResult, 1)}
	redirect := fmt.Sprintf("http://127.0.0.1:%d/callback", ln.Addr().(*net.TCPAddr).Port)

	cfg := &sdkauth.AuthorizationCodeHandlerConfig{
		RedirectURL:              redirect,
		AuthorizationCodeFetcher: l.fetch,
		RequestRefreshToken:      true,
		NewTokenSource:           l.store,
	}
	if o := rs.OAuth; o.ClientID != "" {
		cfg.PreregisteredClient = &oauthex.ClientCredentials{ClientID: o.ClientID}
		if o.ClientSecret != "" {
			cfg.PreregisteredClient.ClientSecretAuth = &oauthex.ClientSecretAuth{ClientSecret: o.ClientSecret}
		}
	} else {
		cfg.DynamicClientRegistrationConfig = &sdkauth.DynamicClientRegistrationConfig{
			Metadata: &oauthex.ClientRegistrationMetadata{
				RedirectURIs:            []string{redirect},
				ClientName:              "mcpsh",
				ClientURI:               "https://github.com/davidemaggi/mcpsh",
				GrantTypes:              []string{"authorization_code", "refresh_token"},
				ResponseTypes:           []string{"code"},
				TokenEndpointAuthMethod: "none",
			},
		}
	}
	if scopes := rs.OAuth.Scopes; len(scopes) > 0 {
		cfg.ScopeFilter = func([]string) []string { return scopes }
	}
	if l.handler, err = sdkauth.NewAuthorizationCodeHandler(cfg); err != nil {
		ln.Close()
		return nil, err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", l.callback)
	l.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = l.srv.Serve(ln) }()
	return l, nil
}

// OAuthHandler implements Provider.
func (l *Login) OAuthHandler(context.Context, *config.Resolved) (sdkauth.OAuthHandler, error) {
	return l.handler, nil
}

// Saved reports whether the login completed and was stored.
func (l *Login) Saved() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.saved
}

// Close stops the loopback listener.
func (l *Login) Close() error { return l.srv.Close() }

// fetch sends the user to the authorization page and waits for the redirect.
func (l *Login) fetch(ctx context.Context, args *sdkauth.AuthorizationArgs) (*sdkauth.AuthorizationResult, error) {
	fmt.Fprintf(l.out, "Opening the browser to authorize mcpsh for %q. If it does not open, visit:\n\n  %s\n\n", l.rs.Name, args.URL)
	if err := OpenBrowser(args.URL); err != nil {
		fmt.Fprintf(l.out, "(could not open a browser: %v)\n", err)
	}
	fmt.Fprintln(l.out, "Waiting for the authorization...")
	select {
	case r := <-l.results:
		if r.Code == "" {
			return nil, errors.New(r.State) // callback() puts the server's error in State
		}
		return r, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (l *Login) callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	res := &sdkauth.AuthorizationResult{Code: q.Get("code"), State: q.Get("state"), Iss: q.Get("iss")}
	title, msg := "mcpsh is authorized", "You can close this window and go back to the terminal."
	if res.Code == "" {
		reason := q.Get("error")
		if d := q.Get("error_description"); d != "" {
			reason += ": " + d
		}
		if reason == "" {
			reason = "the authorization server sent no code"
		}
		res.State = "authorization failed: " + reason
		title, msg = "Authorization failed", reason
	}
	select {
	case l.results <- res:
	default: // a repeated redirect: the first one counts
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, "<!doctype html><title>%s</title><body style=\"font-family:sans-serif;margin:3em\"><h1>%s</h1><p>%s</p>",
		html.EscapeString(title), html.EscapeString(title), html.EscapeString(msg))
}

// store saves the issued token with the client that obtained it.
func (l *Login) store(ctx context.Context, cfg *oauth2.Config, tok *oauth2.Token) (oauth2.TokenSource, error) {
	rec := &Record{
		ServerURL:    l.rs.URL,
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		AuthURL:      cfg.Endpoint.AuthURL,
		TokenURL:     cfg.Endpoint.TokenURL,
		AuthStyle:    int(cfg.Endpoint.AuthStyle),
		Scopes:       cfg.Scopes,
		Token:        tok,
		LoggedInAt:   time.Now().Unix(),
	}
	if err := Save(rec); err != nil {
		return nil, fmt.Errorf("cannot store the login: %v", err)
	}
	l.mu.Lock()
	l.saved = true
	l.mu.Unlock()
	return newPersistingSource(ctx, rec), nil
}

// OpenBrowser opens url with $BROWSER when set, else the system's default browser.
func OpenBrowser(url string) error {
	var cmd *exec.Cmd
	switch b := os.Getenv("BROWSER"); {
	case b != "":
		cmd = exec.Command(b, url)
	case runtime.GOOS == "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case runtime.GOOS == "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
