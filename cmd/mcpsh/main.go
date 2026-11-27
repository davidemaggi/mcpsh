// Command mcpsh exposes the tools of MCP servers as shell commands.
package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/davidemaggi/mcpsh/internal/cli"
	"github.com/davidemaggi/mcpsh/internal/update"
)

func main() {
	if exe, err := os.Executable(); err == nil {
		update.CleanupOld(exe) // left by a self-update on Windows
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := cli.NewApp().Run(ctx, os.Args[1:])
	stop()
	os.Exit(code)
}
