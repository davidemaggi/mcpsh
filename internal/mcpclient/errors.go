package mcpclient

import "fmt"

// ConnectError: the server could not be started or did not complete the handshake.
type ConnectError struct {
	Server string
	Err    error
	Stderr string // last lines of a stdio server's stderr
}

func (e *ConnectError) Error() string {
	msg := fmt.Sprintf("cannot connect to server %q: %v", e.Server, e.Err)
	if e.Stderr != "" {
		msg += "\nserver stderr:\n" + indent(e.Stderr)
	}
	return msg
}
func (e *ConnectError) Unwrap() error { return e.Err }

// TransportError: the connection broke during a request.
type TransportError struct {
	Server string
	Err    error
	Stderr string
}

func (e *TransportError) Error() string {
	msg := fmt.Sprintf("connection to server %q failed: %v", e.Server, e.Err)
	if e.Stderr != "" {
		msg += "\nserver stderr:\n" + indent(e.Stderr)
	}
	return msg
}
func (e *TransportError) Unwrap() error { return e.Err }

// TimeoutError: the request did not complete within the call timeout.
type TimeoutError struct{ Server string }

func (e *TimeoutError) Error() string {
	return fmt.Sprintf("server %q did not answer in time; raise it with --timeout or the server's \"timeout\" setting", e.Server)
}

// ServerError: the server answered with a JSON-RPC error.
type ServerError struct {
	Server  string
	Code    int64
	Message string
}

func (e *ServerError) Error() string {
	return fmt.Sprintf("server %q returned an error: %s (code %d)", e.Server, e.Message, e.Code)
}

func indent(s string) string {
	out := "  "
	for _, r := range s {
		out += string(r)
		if r == '\n' {
			out += "  "
		}
	}
	return out
}
