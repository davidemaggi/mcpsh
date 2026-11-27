# Handoff: where mcpsh stands

Updated 2026-11-27 so that work can continue on another machine (and by an agent that has none
of the previous conversation). Read this, then [plan.md](plan.md).

## Working agreements

- Talk to the owner (Davide) in **Italian**; everything in the repository is in **English**.
- Only dependencies with permissive licenses (MIT, BSD, Apache-2.0, ISC): mcpsh is released as
  MIT. CI enforces it (`licenses` job).
- One phase at a time, on a branch `phase-N-...`, one PR per phase, green tests before moving on.
- When the PR's CI is green, **squash-merge it yourself** (`gh pr merge N --squash --delete-branch`).
  If CI is red and the fix is not obvious, go back to the owner.
- **No attribution**: no `Co-Authored-By` trailers or "Generated with" lines in commits or PR
  bodies. Commit as `Davide Maggi <davide.maggi@live.com>`: run
  `git config user.email davide.maggi@live.com` in every new clone (the owner's work PC has a
  corporate address in its global git config, which must never appear in this repository).
- Never commit secrets; never put secret values in logs. Publishing a release (pushing a tag) is
  outward-facing: ask first.
- Verify SDK and tool APIs against current docs / source, not memory. go-sdk is pinned to
  **v1.8.0** (v1.9.0 pre-releases have breaking redirect / json.Number changes).

## State

Phases 1 to 6 are merged into `main`. See the roadmap in the README. Phase 6 was split in four
PRs: 6a resources and prompts, 6b OAuth (`mcpsh auth`), 6c `mcpsh self-update`, 6d winget.

Branch protection on `main` requires: `test (ubuntu-latest)`, `test (macos-latest)`,
`test (windows-latest)`, `lint`, `licenses`, `snapshot`, `installers (ubuntu-latest)`,
`installers (macos-latest)`, `installers (windows-latest)`; admins included, no force pushes.

**No release has been published yet.** README and skills already show the real install commands,
which work only after the first tag.

### Release prerequisites

1. The self-hosted signing runner (label `codesign`) is registered and online. Without it a
   release ships unsigned Windows zips only, and winget is skipped.
2. npm: secret `NPM_TOKEN` (granular token able to publish under `@davidemaggi`) for the first
   publish. Afterwards configure a trusted publisher on npmjs.com for all 7 packages
   (`@davidemaggi/mcpsh` and `@davidemaggi/mcpsh-{linux,darwin,win32}-{x64,arm64}`; workflow
   `release.yml`; tick "Allowed actions → npm publish", otherwise configs created after
   2026-09-03 only allow staged publishing), set the repository variable
   `NPM_TRUSTED_PUBLISHING=true` and delete the token. Without either, the npm job is skipped.
3. winget: secret `WINGET_TOKEN` (classic PAT with `public_repo`, used by `wingetcreate submit` to
   open a PR on microsoft/winget-pkgs from the owner's fork). Without it the job builds, tests and
   skips the submission. The first submission is reviewed by Microsoft before
   `winget install DavideMaggi.mcpsh` works.
4. `PACKAGE_REPOS_TOKEN` must be able to push to `davidemaggi/homebrew-tap` and
   `davidemaggi/scoop-bucket`. `RUNNER_STATUS_TOKEN` exists (Administration: read).
5. Then: `git tag v0.1.0 && git push origin v0.1.0` (only after the owner says so).

### Machine notes

- On the owner's corporate Windows PC an EDR scans every unsigned exe start (~2.3 s; a freshly
  copied exe can take ~20 s the first time). Do not mistake it for an mcpsh slowness. Measure
  again with signed builds.
- Claude Code's auto mode there denied `go run/go install <tool>@version` (staticcheck,
  go-licenses, goreleaser) and changes to branch protection: those run in CI or are done by the
  owner. Do not work around such denials.

## Where things are (phase 6)

- Resources and prompts: `internal/cli/resources.go`; engine methods in `internal/engine`, IPC
  protocol 2 ops in `internal/ipc/protocol.go`, daemon `Manager.Do`.
- OAuth: `internal/auth` (store with DPAPI on Windows, `Stored` provider used by every
  connection, `Login` for `mcpsh auth login` with the SDK's `AuthorizationCodeHandler`),
  `internal/cli/auth.go`. The stored login is part of `Resolved.ID` (`SetAuthStamp`) so the
  daemon replaces sessions after login/logout. Test authorization server:
  `internal/testserver/oauth.go` (`mcpsh-testserver --http ... --oauth`; the test server binary
  doubles as `$BROWSER`).
- self-update: `internal/update`, `internal/cli/selfupdate.go`; `MCPSH_UPDATE_API` and
  `MCPSH_UPDATE_BASE_URL` point it at a fake release in tests.
- winget: `scripts/package-managers.mjs --only winget` (portable zip manifest, schema 1.10.0,
  signed builds only) and the `winget` job in `release.yml`.

## Ideas for later

- Store OAuth tokens in the OS keychain (macOS Keychain, Secret Service) instead of a 0600 file.
- `resources/subscribe` and completions for prompt arguments.
- macOS notarization (needs an Apple Developer account).
