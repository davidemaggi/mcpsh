---
name: mcpsh
description: "Use MCP (Model Context Protocol) servers through the mcpsh CLI: discover the configured servers and their tools, read a tool's parameters and call it from the shell. Use when a task needs an external tool or data source that may be available as an MCP server (issue trackers, databases, browsers, cloud and SaaS APIs, internal services), or when the user mentions MCP, an MCP server or mcpsh."
metadata:
  generated-by: mcpsh
---

# Using MCP servers with mcpsh

`mcpsh` exposes the tools of MCP (Model Context Protocol) servers as shell commands. Discover
progressively: do not guess tool names or parameters, ask mcpsh.

## Workflow

1. **Which servers exist?**
   ```sh
   mcpsh list
   ```
   If there is none, or none fits the task, tell the user. To add one, follow the
   `mcpsh-setup` skill.
2. **Which tools does a server have?** One line per tool:
   ```sh
   mcpsh <server>
   ```
3. **How is a tool called?** Description, annotations and every parameter as a flag:
   ```sh
   mcpsh <server> <tool> --help
   ```
4. **Call it:**
   ```sh
   mcpsh <server> <tool> --param value --list-param a --list-param b
   ```
   - Nested objects or awkward values: `--json '{"key": {"nested": 1}}'`, `--json @args.json`,
     or `--json -` to read from stdin. Flags override keys of `--json`.
   - Add `--output json` when you need to process the result: you get the full MCP result
     (`content`, `structuredContent`, `isError`).
   - Images and other binary content are saved to a file; the path is printed.

## Rules

- **Destructive tools** (marked `[destructive]` in `mcpsh <server>`) refuse to run without
  `--yes`. Only pass `--yes` after the user explicitly agreed to that exact action.
- Long operations: raise the limit with `--timeout 5m`.
- Read error messages: they say what to fix and which `--help` to look at.
- Never print, log or echo secrets. Credentials live in environment variables referenced by the
  mcpsh config; do not ask the user to paste tokens into commands.

## Exit codes

| Code | Meaning | What to do |
| --- | --- | --- |
| 0 | Success | |
| 1 | The tool reported an error | Read stderr; fix the arguments or report the error |
| 2 | Usage, validation, config or trust error | Fix the command (see `--help`) or the config; `mcpsh trust` for project servers |
| 3 | Connection, timeout or daemon error | The server is down or slow; `mcpsh daemon status`, `mcpsh daemon logs` |

## If mcpsh is not installed

`mcpsh` is a single executable with no runtime. Ask the user before installing anything, then
use the first option that fits:

| Platform | Command |
| --- | --- |
| macOS with Homebrew | `brew install --cask davidemaggi/tap/mcpsh` |
| Windows with Scoop | `scoop bucket add davidemaggi https://github.com/davidemaggi/scoop-bucket; scoop install mcpsh` |
| Linux, macOS | `curl -fsSL https://raw.githubusercontent.com/davidemaggi/mcpsh/main/install.sh \| sh` |
| Windows (PowerShell) | `irm https://raw.githubusercontent.com/davidemaggi/mcpsh/main/install.ps1 \| iex` |
| Node.js available | `npm install -g @davidemaggi/mcpsh` |

The install scripts put mcpsh in `~/.local/bin` or `%LOCALAPPDATA%\Programs\mcpsh`; a new
terminal may be needed before it is on the PATH. More options:
https://github.com/davidemaggi/mcpsh#-quickstart-5-minutes.
