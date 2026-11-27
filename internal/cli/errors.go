package cli

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"

	"github.com/davidemaggi/mcpsh/internal/config"
	"github.com/davidemaggi/mcpsh/internal/mcpclient"
)

// Exit codes, documented in the README and in "mcpsh --help".
const (
	ExitOK        = 0 // success
	ExitTool      = 1 // the tool reported an error (isError) or the server rejected the call
	ExitUsage     = 2 // bad command line, arguments, configuration or trust
	ExitTransport = 3 // the server could not be reached, crashed or timed out; daemon errors
)

// UsageError is a mistake the caller can fix by changing the command or the config.
type UsageError struct {
	Msg  string
	Hint string // e.g. "see mcpsh github create_issue --help"
}

func (e *UsageError) Error() string {
	if e.Hint == "" {
		return e.Msg
	}
	return e.Msg + " (" + e.Hint + ")"
}

func usagef(hint, msg string) error { return &UsageError{Msg: msg, Hint: hint} }

// toolFailed is returned after an isError result has been printed.
var toolFailed = errors.New("tool returned an error")

// silentError carries an exit code for an error that has already been reported.
type silentError struct{ code int }

func (e *silentError) Error() string { return "already reported" }

// ExitCode maps an error to the process exit code.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	var se *silentError
	if errors.As(err, &se) {
		return se.code
	}
	if errors.Is(err, toolFailed) {
		return ExitTool
	}
	var (
		ue  *UsageError
		ce  *mcpclient.ConnectError
		te  *mcpclient.TransportError
		toe *mcpclient.TimeoutError
		sv  *mcpclient.ServerError
		fe  *config.FileError
		mv  *config.MissingVarError
		unk *config.UnknownServerError
		dis *config.DisabledServerError
		inv *config.InvalidServerError
		unt *config.UntrustedError
	)
	switch {
	case errors.As(err, &ue), errors.As(err, &fe), errors.As(err, &mv), errors.As(err, &unk),
		errors.As(err, &dis), errors.As(err, &inv), errors.As(err, &unt):
		return ExitUsage
	case errors.As(err, &sv):
		if sv.Code == jsonrpc.CodeInvalidParams || sv.Code == jsonrpc.CodeMethodNotFound {
			return ExitUsage
		}
		return ExitTool
	case errors.As(err, &ce), errors.As(err, &te), errors.As(err, &toe),
		errors.Is(err, context.DeadlineExceeded):
		return ExitTransport
	case errors.Is(err, context.Canceled):
		return 130
	}
	return ExitUsage
}
