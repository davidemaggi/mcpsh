---
name: mcpsh-setup
description: "Configure an MCP server so agents can use it through the mcpsh CLI. Use when the user wants to add, install, connect or set up an MCP server from an npm or PyPI package, a Docker image, a URL, a README, or a .mcp.json / Claude Desktop config snippet, or asks why an mcpsh server does not start or connect."
metadata:
  generated-by: mcpsh
---

# Configuring an MCP server for mcpsh

Turn whatever the user gives you (an npm or PyPI package name, a Docker image, a URL, a README,
or a `.mcp.json` / `claude_desktop_config.json` snippet) into a working mcpsh server. Explain
each step briefly and ask before anything that installs software or touches credentials.

## 1. Check mcpsh

```sh
mcpsh version
```

If it is missing, ask the user, then install it with the first option that fits: macOS with
Homebrew `brew install --cask davidemaggi/tap/mcpsh`; Windows with Scoop
`scoop bucket add davidemaggi https://github.com/davidemaggi/scoop-bucket; scoop install mcpsh`;
Linux or macOS `curl -fsSL https://raw.githubusercontent.com/davidemaggi/mcpsh/main/install.sh | sh`;
Windows PowerShell `irm https://raw.githubusercontent.com/davidemaggi/mcpsh/main/install.ps1 | iex`;
with Node.js `npm install -g @davidemaggi/mcpsh`. A new terminal may be needed before `mcpsh` is
on the PATH.

## 2. Work out how the server runs

| The user gave you | Transport | Command |
| --- | --- | --- |
| npm package (`@scope/server-x`, "run with npx") | stdio | `npx -y <package> [args]`; needs Node.js (`node --version`) |
| Python package ("run with uvx") | stdio | `uvx <package> [args]`; needs uv (`uv --version`) |
| Docker image | stdio | `docker run -i --rm [-e NAME] <image>`; needs Docker |
| Local binary or script | stdio | its path and arguments |
| `https://.../mcp` URL | Streamable HTTP | `--url <url>` |
| URL ending in `/sse` or documented as SSE | legacy SSE | `--url <url> --transport sse` |
| A JSON config snippet | as written | import it (step 4) |

Read the server's README for required arguments, environment variables and HTTP headers. If a
prerequisite is missing, tell the user what to install; do not install it silently.

## 3. Handle secrets safely

- Never write a token, password or API key into a command or a config file.
- Reference an environment variable instead: `--env API_KEY='${API_KEY}'` or
  `--header 'Authorization: Bearer ${SERVICE_TOKEN}'`. Keep the single quotes so the shell does
  not expand the variable now.
- Tell the user which variables to set and where (their shell profile, or the system
  environment on Windows). Do not ask them to paste the value into the chat.

Remote servers that use OAuth (their docs mention signing in, or the first call fails with
"requires authorization") need no token: after adding the server, ask the user to run
`mcpsh auth login <name>` in a terminal, which opens the browser. Never handle their password.

## 4. Add the server

Pick a short lowercase name (letters, digits, `-`). Choose the scope:

- `--scope project`: the server belongs to this repository and teammates can use it (the file
  is `.mcpsh.json`, usually committed; secrets must be `${VAR}` references, mcpsh enforces it).
- `--scope user` (default): personal servers and anything that should not be shared.

```sh
mcpsh add <name> [--scope project] [--env NAME='${NAME}'] -- <command> [args...]
mcpsh add <name> --url <url> [--transport sse] [--header 'Name: ${VAR}']
```

For a JSON snippet, write it to a temporary file shaped like `{"mcpServers": {...}}` and run
`mcpsh import <file> [--scope user|project]`. Replace clear-text secrets with `${VAR}` first.

Project servers you add or import are trusted automatically. If a teammate's `.mcpsh.json`
defines servers, `mcpsh trust` shows them: let the user review before `mcpsh trust <name>`.

## 5. Verify

```sh
mcpsh list --check
mcpsh <name>
```

- Exit code 2 mentioning an environment variable: the user still has to set it.
- Exit code 3: read the error; it includes the server's own stderr. A first `npx`/`uvx` run can
  be slow: add `"startupTimeout": "3m"` to the server in the config file, or retry.
- `mcpsh daemon logs` shows what the background daemon saw.

## 6. Harden (suggest, let the user decide)

In the config entry (`mcpsh list` shows the file):

- `"denyTools": ["delete_*"]` or `"allowTools": ["get_*", "list_*"]` to limit what is exposed.
- `"confirmTools": ["write_*"]` to require `--yes` for risky tools the server did not mark as
  destructive.

## 7. Optional: a dedicated skill

```sh
mcpsh skill generate <name>
```

writes a skill that tells agents when to use this server. Finish with a short summary: the
server name, scope, variables the user must set, and an example call.
