# mcpsh — design and delivery plan

mcpsh is a single, dependency-free Go binary that acts as an MCP client towards
one or more MCP servers and exposes their tools as CLI subcommands, so coding
agents (Claude Code, Codex, GitHub Copilot) can use any MCP server through a
shell plus a Skill.

## Decisions

| Topic | Decision |
| --- | --- |
| Module | `github.com/davidemaggi/mcpsh`, MIT. Dependencies must be permissive (MIT/BSD/Apache-2.0/ISC); CI rejects others; release archives carry third-party notices. |
| SDK | `github.com/modelcontextprotocol/go-sdk` pinned to **v1.8.0** (v1.9.0-pre changes redirect handling and number decoding). |
| CLI | cobra. Server and tool commands are built at runtime from discovery, only for the server being invoked. |
| Language | Code, CLI messages, docs and Skills in English. |
| Config | Standard `mcpServers` JSON. User file (`~/.config/mcpsh/config.json`, `%APPDATA%\mcpsh\config.json` on Windows) + project file `.mcpsh.json`, found by walking up from the cwd (stops at a git root or the home directory). Same server name: the project entry replaces the user entry entirely. `"disabled": true` hides a server. |
| Interpolation | `${VAR}` and `${VAR:-default}` in command, args, env, cwd, url, headers. A missing variable without default is a usage error (exit 2) naming the variable. |
| Secrets | `add --scope project` refuses literal secret-looking env/header values (keys containing token, secret, key, password, auth, credential, cookie) unless they use `${VAR}`; loading such a file prints a warning. Env and header values are never logged. |
| Trust | Project servers run only after `mcpsh trust`; trust is stored per project file and bound to a hash of the raw (uninterpolated) definition. Untrusted use exits 2 with the command to run. `add`/`import` into a project trust what they write. |
| Workspace root | Directory of `.mcpsh.json`, else the git root, else the cwd. It is the default cwd of stdio servers and the root advertised to servers (roots are deprecated by spec 2026-07-28 but still supported; `"roots": false` turns them off). |
| Reserved names | Built-in command names cannot be server names. `mcpsh call <server> <tool>` is the explicit form. Tool parameters that collide with global flags are exposed as `--param-<name>`. |
| Destructive tools | Confirmation only when `destructiveHint` is explicitly `true`, plus per-server `confirmTools` / `noConfirmTools` globs. TTY: interactive prompt; otherwise `--yes` is required (exit 2). |
| Output | Text content to stdout; `isError` content to stderr. Only `structuredContent`: indented JSON. Images/audio/blobs: written to a temp file, path printed. `--output json`: full MCP result. |
| Exit codes | 0 ok · 1 tool error (`isError`, or a server-side JSON-RPC error) · 2 usage/validation/config/trust · 3 connection, transport, timeout, daemon. |
| Daemon ↔ CLI | The CLI resolves and interpolates the config with its own environment and sends the resolved definition to the daemon over user-only IPC. Sessions are keyed by `hash(resolved definition + cwd)`; a changed definition for the same (file, name) closes the old session. No file watcher. |
| Processes | Windows: each stdio server starts suspended in its own job object (kill-on-close), then resumes; `.cmd`/`.bat` shims run through `cmd.exe` with proper escaping. Unix: own process group, SIGTERM then SIGKILL to the group. Closing a server always kills its whole tree. |
| Skills | Paths verified against official docs (2026-11-27): Claude Code `~/.claude/skills`, `.claude/skills`; Codex `~/.agents/skills`, `.agents/skills`; Copilot `~/.copilot/skills`, `.github/skills` (also reads `.claude/skills`, `.agents/skills`). `--target all` writes `.claude` + `.agents`. `allowed-tools: Bash(mcpsh *)` only for Claude Code. |
| Release | GoReleaser on tag `v*`: 6 targets, checksums, build attestations (`actions/attest`), dependency licenses in every archive (`go-licenses save`). Homebrew cask in `davidemaggi/homebrew-tap`, Scoop manifest in `davidemaggi/scoop-bucket`, npm `@davidemaggi/mcpsh` (launcher + per-platform packages, no install scripts), all written by `scripts/package-managers.mjs` after signing and tested by installing them before publishing; CI builds a snapshot and tries every installer on every OS. Windows signing as in Hodor: built on GitHub runners, signed on a self-hosted `codesign` runner that never checks out code, `-signed` assets added to the release. macOS: no notarization yet; README documents removing the quarantine attribute. |

## Package layout

```text
cmd/mcpsh/               entry point
cmd/mcpsh-testserver/    MCP test server (stdio | http | sse) used by integration tests
internal/
  cli/         cobra commands, dynamic server/tool commands, global flags, exit codes
  config/      paths, load, merge, interpolation, add/remove/import, secret checks, trust store
  platform/    per-OS directories, TTY detection
  proc/        child processes: job objects (Windows), process groups (Unix), stderr tail
  mcpclient/   go-sdk wrapper: resolved server -> session; stdio/http/sse transports; header RoundTripper; auth seam
  schema/      JSON Schema -> flags, flags/--json -> arguments, validation, kebab-case aliases
  toolcache/   on-disk tools/list cache with TTL
  engine/      Engine interface; direct (in-process) and remote (daemon) implementations
  daemon/      session manager, lazy start, idle timeouts, restart with backoff, list_changed
  ipc/         Unix socket / named pipe with ACL, framing, version handshake
  render/      result rendering, binary content to files
  skill/       embedded Skill templates, install targets, generate, agents-md
  logging/     slog, rotating file, secret redaction
  version/     build information
e2e/           integration tests (build mcpsh + test server, run as subprocesses)
```

The `engine.Engine` seam is what keeps `--no-daemon` and daemon mode identical: parsing,
validation, confirmation and rendering run in the CLI for both; only the engine differs.
The e2e suite runs every scenario against both engines.

## Phases

Each phase lands as one PR; CI (Linux, macOS, Windows) must be green before the next starts.

1. **Direct mode.** Scaffolding and CI; config (merge, interpolation, trust, add/remove/list/import);
   process tree management; stdio transport; roots; schema-to-flags and validation;
   server/tool help, `--schema`, call; rendering and exit codes; discovery cache and `refresh`
   (moved from phase 3: help must not connect); `destructiveHint` (moved from phase 3);
   test server; unit and stdio e2e tests; first version of an eye-catching README (hero,
   badges, terminal demo, "why", quickstart, roadmap that is honest about what ships).
2. **Daemon.** IPC with ACLs, auto-start, version handshake, session manager, per-server and
   global idle timeouts, restart with backoff, concurrent calls, `--no-daemon` / `MCPSH_NO_DAEMON`,
   `daemon start|stop|status|restart|logs`, rotating logs with redaction; lifecycle tests.
3. **HTTP.** Streamable HTTP, legacy SSE, custom headers, auth provider seam (no OAuth yet),
   `tools/list_changed` refreshing the cache; HTTP/SSE e2e on both engines.
4. **Skills.** `skill install`, `skill generate <server>`, `agents-md`. Two Skills: `mcpsh`
   (use configured servers, progressive disclosure) and `mcpsh-setup` (given an MCP package, URL,
   README or `.mcp.json` snippet, guide the agent through `mcpsh add` with `${VAR}` secrets, scope,
   trust, tool filters, `list --check`, and optionally `skill generate`). Both also live in the repo
   under `skills/<name>/SKILL.md`, generated from the same templates (a test keeps them in sync), so
   `npx skills add davidemaggi/mcpsh` installs them; the Skills offer to install the `mcpsh`
   binary when it is missing (with the user's consent).
5. **Release.** GoReleaser, signing, Homebrew, Scoop, install.sh / install.ps1, npm package
   `@davidemaggi/mcpsh` (launcher + per-platform `optionalDependencies`, no postinstall, trusted
   publishing with provenance if available, published only after the Windows binaries are signed),
   README final pass (real install commands, demo recording).
6. **Later.** OAuth (SDK `auth.AuthorizationCodeHandler`, client-side support is experimental),
   resources and prompts, `self-update`, winget.

## Risks to check early

- `.cmd` shims on Windows (`npx.cmd`): argument escaping through `cmd.exe` (phase 1 test).
- The SDK caches `tools/list` per session under protocol 2026-07-28, but only when the server
  sends a positive `ttlMs`, and drops it on `tools/list_changed`: `refresh` honours that hint.
- Agents' terminals or CI may run mcpsh inside a job object that forbids breakaway, so the
  daemon dies with the terminal; the CLI restarts it transparently and the daemon logs it.
