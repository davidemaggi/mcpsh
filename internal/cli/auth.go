package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/davidemaggi/mcpsh/internal/auth"
	"github.com/davidemaggi/mcpsh/internal/config"
	"github.com/davidemaggi/mcpsh/internal/mcpclient"
)

// loginTimeout bounds the whole interactive login, browser included.
const loginTimeout = 10 * time.Minute

func (a *App) authCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Log in to remote servers that use OAuth",
		Long: `Log in to remote (Streamable HTTP) servers that use OAuth.

  mcpsh auth login <server>    authorize mcpsh in the browser and store the token
  mcpsh auth logout <server>   forget the stored token
  mcpsh auth status            which servers are logged in

Calls never open a browser: a server that needs authorization fails with exit code 2 and
asks for "mcpsh auth login". Tokens are refreshed automatically and stored in the user's
state directory (encrypted with DPAPI on Windows), never in config files.`,
	}
	cmd.AddCommand(a.authLoginCmd(), a.authLogoutCmd(), a.authStatusCmd())
	return cmd
}

// oauthServer resolves a server that can use OAuth.
func (a *App) oauthServer(name string) (*config.Resolved, error) {
	rs, err := a.resolve(name)
	if err != nil {
		return nil, err
	}
	if rs.Transport != config.TransportHTTP {
		return nil, usagef("", fmt.Sprintf("server %q uses %s: OAuth works with Streamable HTTP servers only", name, rs.Transport))
	}
	if rs.OAuth == nil {
		return nil, usagef("", fmt.Sprintf("OAuth is off for server %q: it has a static Authorization header or \"oauth\": {\"disabled\": true}", name))
	}
	return rs, nil
}

func (a *App) authLoginCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "login <server>",
		Short: "Authorize mcpsh for a server in the browser",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rs, err := a.oauthServer(args[0])
			if err != nil {
				return err
			}
			login, err := auth.NewLogin(rs, a.Stderr)
			if err != nil {
				return err
			}
			defer login.Close()
			// The handshake waits for the user in the browser: give it the login's time.
			lrs := *rs
			lrs.StartupTimeout = loginTimeout
			ctx, cancel := context.WithTimeout(cmd.Context(), loginTimeout)
			defer cancel()
			conn, err := mcpclient.Connect(ctx, &lrs, mcpclient.Options{Logger: a.logger, Auth: login})
			if err != nil {
				if strings.Contains(err.Error(), "failed to register client") && rs.OAuth.ClientID == "" {
					// Some providers (Figma, for one) only register clients they have approved.
					return usagef("", fmt.Sprintf("the authorization server of %q refused to register mcpsh as a client (%v). "+
						"It only accepts clients it knows: ask the provider to allow mcpsh, or configure a client it gave you with "+
						"\"oauth\": {\"clientId\": \"...\", \"clientSecret\": \"${SECRET}\"}", rs.Name, lastPart(err.Error())))
				}
				return err
			}
			tools, err := conn.ListTools(ctx)
			_ = conn.Close()
			if err != nil {
				return err
			}
			if !login.Saved() {
				fmt.Fprintf(a.Stdout, "Server %q did not ask for authorization: nothing to do.\n", rs.Name)
				return nil
			}
			fmt.Fprintf(a.Stdout, "Logged in to %q (%d tools).\n", rs.Name, len(tools))
			return nil
		},
	}
}

// lastPart keeps the end of a chain of wrapped errors ("a: b: c" -> "c").
func lastPart(s string) string {
	if i := strings.LastIndex(s, "registration failed"); i >= 0 {
		return s[i:]
	}
	return s
}

func (a *App) authLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout <server>",
		Short: "Forget the stored login of a server",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rs, err := a.oauthServer(args[0])
			if err != nil {
				return err
			}
			had, err := auth.Delete(rs.URL)
			if err != nil {
				return err
			}
			if had {
				fmt.Fprintf(a.Stdout, "Logged out of %q.\n", rs.Name)
			} else {
				fmt.Fprintf(a.Stdout, "Not logged in to %q.\n", rs.Name)
			}
			return nil
		},
	}
}

// authStatus is one line of "mcpsh auth status".
type authStatus struct {
	Server   string    `json:"server"`
	URL      string    `json:"url"`
	LoggedIn bool      `json:"loggedIn"`
	Expires  time.Time `json:"expires,omitzero"`
	Refresh  bool      `json:"refreshable,omitempty"`
	Error    string    `json:"error,omitempty"`
}

func (a *App) authStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show which OAuth servers are logged in",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := a.config()
			if err != nil {
				return err
			}
			names := make([]string, 0, len(cfg.Servers))
			for n := range cfg.Servers {
				names = append(names, n)
			}
			sort.Strings(names)
			out := []authStatus{}
			for _, n := range names {
				rs, err := cfg.Resolve(n, a.Lookup)
				if err != nil || rs.OAuth == nil {
					continue
				}
				st := authStatus{Server: n, URL: rs.URL}
				rec, err := auth.Load(rs.URL)
				switch {
				case err != nil:
					st.Error = err.Error()
				case rec != nil:
					st.LoggedIn, st.Expires, st.Refresh = true, rec.Token.Expiry, rec.Token.RefreshToken != ""
				}
				out = append(out, st)
			}
			if a.jsonOutput() {
				return writeJSON(a.Stdout, out)
			}
			if len(out) == 0 {
				fmt.Fprintln(a.Stdout, "No server can use OAuth (only Streamable HTTP servers without a static Authorization header can).")
				return nil
			}
			for _, st := range out {
				state := "not logged in"
				switch {
				case st.Error != "":
					state = "error: " + st.Error
				case st.LoggedIn && st.Refresh:
					state = "logged in (refreshed automatically)"
				case st.LoggedIn && !st.Expires.IsZero() && time.Now().After(st.Expires):
					state = "expired: run \"mcpsh auth login " + st.Server + "\""
				case st.LoggedIn:
					state = "logged in"
				}
				fmt.Fprintf(a.Stdout, "  %-20s %s  (%s)\n", st.Server, state, st.URL)
			}
			return nil
		},
	}
}
