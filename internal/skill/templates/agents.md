## MCP tools (mcpsh)

MCP servers are available through the `mcpsh` CLI. Discover before calling, do not guess:

1. `mcpsh list` shows the configured servers.
2. `mcpsh <server>` lists its tools, one per line.
3. `mcpsh <server> <tool> --help` shows the tool's description and parameters (as flags).
4. Call it: `mcpsh <server> <tool> --param value`. Use `--json '{...}'` for nested input and
   `--output json` when you need to process the result.

Tools marked destructive need `--yes`: pass it only after the user agreed to that action.
Exit codes: 0 ok, 1 tool error, 2 usage/config error (see `--help`), 3 connection or timeout.
