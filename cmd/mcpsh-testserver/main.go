// Command mcpsh-testserver is the MCP server used by mcpsh's integration tests.
//
//	mcpsh-testserver                       serve over stdio
//	mcpsh-testserver --http 127.0.0.1:0    serve Streamable HTTP (prints the URL on stdout)
//	mcpsh-testserver --sse 127.0.0.1:0     serve legacy SSE (prints the URL on stdout)
//	mcpsh-testserver --sleep               just sleep (grandchild for process-tree tests)
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/davidemaggi/mcpsh/internal/testserver"
)

func main() {
	httpAddr := flag.String("http", "", "serve Streamable HTTP on this address")
	sseAddr := flag.String("sse", "", "serve legacy SSE on this address")
	sleep := flag.Bool("sleep", false, "sleep for an hour and exit")
	delay := flag.Duration("startup-delay", 0, "wait before serving (slow servers)")
	token := flag.String("require-token", "", "HTTP/SSE: answer 401 unless \"Authorization: Bearer <token>\" is sent")
	flag.Parse()

	if *sleep {
		time.Sleep(time.Hour)
		return
	}
	time.Sleep(*delay)
	// Tests make the server unstartable to prove that help is served from the cache.
	if p := os.Getenv("MCPSH_TESTSERVER_FAIL_IF_EXISTS"); p != "" {
		if _, err := os.Stat(p); err == nil {
			fmt.Fprintln(os.Stderr, "refusing to start: "+p+" exists")
			os.Exit(1)
		}
	}
	exe, _ := os.Executable()
	server := testserver.New(exe)

	switch {
	case *httpAddr != "":
		h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
		serve(*httpAddr, "/mcp", requireToken(*token, h))
	case *sseAddr != "":
		h := mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return server }, nil)
		serve(*sseAddr, "/sse", requireToken(*token, h))
	default:
		if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
			log.Fatal(err)
		}
	}
}

func serve(addr, path string, h http.Handler) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("http://%s%s\n", ln.Addr(), path)
	mux := http.NewServeMux()
	mux.Handle(path, h)
	log.Fatal(http.Serve(ln, mux))
}

func requireToken(token string, h http.Handler) http.Handler {
	if token == "" {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "missing or wrong bearer token", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	})
}
