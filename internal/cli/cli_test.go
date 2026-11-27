package cli

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"

	"github.com/davidemaggi/mcpsh/internal/config"
	"github.com/davidemaggi/mcpsh/internal/mcpclient"
)

func TestExitCode(t *testing.T) {
	cases := []struct {
		err  error
		code int
	}{
		{nil, ExitOK},
		{errToolFailed, ExitTool},
		{usagef("", "x"), ExitUsage},
		{fmt.Errorf("wrapped: %w", &config.MissingVarError{Var: "X"}), ExitUsage},
		{&config.UntrustedError{Name: "p"}, ExitUsage},
		{&config.UnknownServerError{Name: "p"}, ExitUsage},
		{&mcpclient.ConnectError{Server: "s", Err: errors.New("x")}, ExitTransport},
		{&mcpclient.TransportError{Server: "s", Err: errors.New("x")}, ExitTransport},
		{&mcpclient.TimeoutError{Server: "s"}, ExitTransport},
		{&mcpclient.ServerError{Code: jsonrpc.CodeInvalidParams}, ExitUsage},
		{&mcpclient.ServerError{Code: jsonrpc.CodeInternalError}, ExitTool},
		{context.Canceled, 130},
		{&silentError{code: 3}, 3},
	}
	for _, c := range cases {
		if got := ExitCode(c.err); got != c.code {
			t.Errorf("ExitCode(%v) = %d, want %d", c.err, got, c.code)
		}
	}
}

func TestFirstPositional(t *testing.T) {
	cases := []struct {
		args []string
		want int
	}{
		{[]string{"github", "x"}, 0},
		{[]string{"-o", "json", "github"}, 2},
		{[]string{"--output=json", "github"}, 1},
		{[]string{"--timeout", "5s", "--yes", "github"}, 3},
		{[]string{"--no-daemon"}, -1},
		{[]string{"--", "x"}, -1},
	}
	for _, c := range cases {
		if got := firstPositional(c.args); got != c.want {
			t.Errorf("firstPositional(%v) = %d, want %d", c.args, got, c.want)
		}
	}
}
