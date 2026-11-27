#!/usr/bin/env node
// Launcher of the @davidemaggi/mcpsh npm package: runs the native mcpsh binary that npm installed
// from the platform package (@davidemaggi/mcpsh-<os>-<cpu>) and exits with its exit code.
'use strict';

const { spawnSync } = require('node:child_process');

const pkg = `@davidemaggi/mcpsh-${process.platform}-${process.arch}`;
const exe = process.platform === 'win32' ? 'mcpsh.exe' : 'mcpsh';

let bin;
try {
  bin = require.resolve(`${pkg}/bin/${exe}`);
} catch {
  console.error(
    `mcpsh: ${pkg} is not installed, so there is no mcpsh binary for ${process.platform}-${process.arch}.\n` +
      'Reinstall without --no-optional / --omit=optional, or install mcpsh another way: ' +
      'https://github.com/davidemaggi/mcpsh#-quickstart-5-minutes',
  );
  process.exit(2);
}

// Ctrl+C reaches mcpsh directly (same console / process group); the launcher only waits for it.
for (const sig of ['SIGINT', 'SIGTERM', 'SIGHUP']) {
  try {
    process.on(sig, () => {});
  } catch {
    // not supported on this platform
  }
}

const result = spawnSync(bin, process.argv.slice(2), {
  stdio: 'inherit',
  windowsHide: false,
  env: { ...process.env, MCPSH_INSTALL_METHOD: process.env.MCPSH_INSTALL_METHOD || 'npm' },
});
if (result.error) {
  console.error(`mcpsh: cannot run ${bin}: ${result.error.message}`);
  process.exit(3);
}
if (result.signal) {
  // Die the same way, so the caller sees the signal
  process.removeAllListeners(result.signal);
  process.kill(process.pid, result.signal);
}
process.exit(result.status ?? 1);
