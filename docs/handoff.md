# Handoff: where mcpsh stands

Written 2026-11-27 so that work can continue on another machine (and by an agent that has none
of the previous conversation). Read this, then [plan.md](plan.md).

## Working agreements

- Talk to the owner (Davide) in **Italian**; everything in the repository is in **English**.
- Only dependencies with permissive licenses (MIT, BSD, Apache-2.0, ISC): mcpsh is released as
  MIT. CI enforces it (`licenses` job).
- One phase at a time, on a branch `phase-N-...`, one PR per phase, green tests before moving on.
- When the PR's CI is green, **squash-merge it yourself** (`gh pr merge N --squash --delete-branch`).
  If CI is red and the fix is not obvious, go back to the owner.
- Never commit secrets; never put secret values in logs. Publishing a release (pushing a tag) is
  outward-facing: ask first.
- Verify SDK and tool APIs against current docs / source, not memory. go-sdk is pinned to
  **v1.8.0** (v1.9.0 pre-releases have breaking redirect / json.Number changes).

## State

Phases 1 to 5 are merged into `main` (PRs #1 to #5). See the roadmap in the README.

Branch protection on `main` requires: `test (ubuntu-latest)`, `test (macos-latest)`,
`test (windows-latest)`, `lint`, `licenses`, `snapshot`, `installers (ubuntu-latest)`,
`installers (macos-latest)`, `installers (windows-latest)`.

**No release has been published yet.** README and skills already show the real install commands,
which work only after the first tag.

### Before the first release (owner actions)

1. Secret `NPM_TOKEN` (granular token able to publish under `@davidemaggi`) for the first npm
   publish. Afterwards: configure a trusted publisher on npmjs.com for all 7 packages
   (`@davidemaggi/mcpsh` and `@davidemaggi/mcpsh-{linux,darwin,win32}-{x64,arm64}`; workflow
   `release.yml`; tick "Allowed actions → npm publish", otherwise configs created after
   2026-09-03 only allow staged publishing), set repository variable
   `NPM_TRUSTED_PUBLISHING=true`, delete the token. Without either, the npm job is skipped.
2. Register the self-hosted signing runner with label `codesign` on `davidemaggi/mcpsh`
   (variables `CODESIGN_CERT_SUBJECT`, `CODESIGN_TIMESTAMP_URL` exist; optional
   `CODESIGN_CERT_SHA1`, `SIGNTOOL_PATH`, `SIGNING_RUNNER_LABEL`). Without it releases ship
   unsigned Windows zips only.
3. `PACKAGE_REPOS_TOKEN` exists; it must be able to push to `davidemaggi/homebrew-tap` and
   `davidemaggi/scoop-bucket`. `RUNNER_STATUS_TOKEN` exists (Administration: read).
4. Then: `git tag v0.1.0 && git push origin v0.1.0` (after the owner says so).

### Machine notes

- On the owner's corporate Windows PC an EDR scans every unsigned exe start (~2.3 s; a freshly
  copied exe can take ~20 s the first time). Do not mistake it for an mcpsh slowness. Measure
  again with signed builds.
- Claude Code's auto mode there denied `go run/go install <tool>@version` (staticcheck,
  go-licenses, goreleaser): those run in CI only. Do not work around such denials.

## Phase 6 (next): OAuth, resources and prompts, self-update, winget

Do it as up to four PRs (6a..6d) so each stays reviewable; order below.

### 6a. Resources and prompts

- Engine interface (`internal/engine`) gains `ListResources`, `ReadResource`,
  `ListResourceTemplates`, `ListPrompts`, `GetPrompt`; implement in `Direct` and in the daemon
  (`internal/ipc/protocol.go` new ops; bump `ProtocolVersion` to 2, the CLI already restarts an
  older daemon).
- CLI: `mcpsh <server> resources` (list, incl. templates), `mcpsh <server> read <uri>`
  (text to stdout, blobs saved to a temp file like images), `mcpsh <server> prompts`,
  `mcpsh <server> prompt <name> --arg value` (prompt arguments become flags; required ones checked).
  `resources`, `read`, `prompts`, `prompt` become reserved names: a tool with one of those names
  must stay reachable (`mcpsh <server> call <tool>` or `--tool`); check `internal/cli/server.go`
  and `root.go` (`call` rewrite) before choosing.
- Cache resource/prompt lists like tools (`internal/toolcache`), refresh on
  `resources/list_changed` and `prompts/list_changed` in the daemon.
- Test server: add a couple of resources (text + blob + template) and prompts; e2e in both modes.
- Update skill templates (`internal/skill/templates/*.md`, then `go test ./internal/skill -update`)
  and README.

### 6b. OAuth for remote servers

- Abstraction already there: `internal/auth.Provider` returns the SDK `auth.OAuthHandler`, wired
  into `StreamableClientTransport.OAuthHandler` in `internal/mcpclient/http.go`. Check what go-sdk
  v1.8.0's `auth` package offers on the client side (authorization code + PKCE, dynamic client
  registration, protected resource metadata discovery) before writing anything.
- Flow: `mcpsh auth login <server>` opens the browser (loopback redirect on 127.0.0.1, random
  port, PKCE, state check), prints the URL when no browser; `mcpsh auth logout <server>`,
  `mcpsh auth status`. Config: `"auth": {"type": "oauth", "clientId": ..., "scopes": [...]}`
  optional; default dynamic registration.
- Tokens: never in config files or logs. Store in the OS keychain if a permissive, CGO-free
  library allows it (Windows Credential Manager via x/sys, macOS Keychain via `security` CLI,
  Linux Secret Service); fall back to a 0600 file in the state dir. Refresh tokens automatically;
  the daemon must pick up new tokens (reload on login).
- Non-interactive calls (agents) never open a browser: fail with exit 2 and the message
  "run `mcpsh auth login <server>`".
- e2e: an in-process authorization server in the test server (`--oauth` flag) issuing tokens.

### 6c. self-update

- `mcpsh self-update [--version X] [--check]`: GitHub latest release, same asset names and
  checksum verification as `install.sh` / `install.ps1` (prefer `-signed` zips and
  `checksums-signed.txt` on Windows).
- Refuse and print the right command when installed by a package manager: npm (env
  `MCPSH_INSTALL_METHOD=npm`, set by `npm/mcpsh.js`), Homebrew (executable under the Caskroom),
  Scoop (under `scoop\apps`), winget (under the WinGet packages dir). `go install` builds: say
  so and suggest `go install ...@latest`.
- Replace in place: Unix write `.mcpsh.new` + rename; Windows rename the running exe to
  `mcpsh.exe.old` and copy (same trick as `install.ps1`), delete leftovers on next start. Then
  stop the old daemon (the CLI version check already does it on the next call).
- Unit tests against an `httptest` fake release.

### 6d. winget

- Portable zip manifest (InstallerType `zip`, NestedInstallerType `portable`,
  `PortableCommandAlias: mcpsh`) pointing at the **signed** zips only; package id
  `DavideMaggi.mcpsh`.
- Add to `scripts/package-managers.mjs` (`--only winget`) and a `winget` job in `release.yml`
  after `publish-signed`, skipped when signing was skipped. As in Hodor's
  `package-managers.yml`: `winget validate`, `winget install --manifest`, check
  `mcpsh version`, then `wingetcreate submit` with secret `WINGET_TOKEN` (skip with a notice
  when missing). Hodor: https://github.com/davidemaggi/Hodor/tree/main/.github/workflows
- README install table and skill templates: add winget.
