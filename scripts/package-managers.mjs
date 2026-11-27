#!/usr/bin/env node
// Writes the package-manager files for a release from the files GoReleaser (and the signing
// jobs) produced. Nothing is rebuilt: every package points at, or contains, a release file.
//
//   node scripts/package-managers.mjs --version 1.2.3 --dist dist --out out [--only homebrew,scoop,npm]
//
// --dist holds the release files: mcpsh_<v>_<os>_<arch>.tar.gz|zip, checksums.txt and, when
// Windows signing ran, mcpsh_<v>_windows_<arch>-signed.zip and checksums-signed.txt.
// For npm the archives must be present; Homebrew and Scoop only need the checksum files.
//
// Output:
//   out/homebrew/Casks/mcpsh.rb
//   out/scoop/bucket/mcpsh.json
//   out/npm/<package>/...      one directory per npm package, ready for `npm publish`
import { execFileSync } from 'node:child_process';
import { copyFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync, chmodSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { parseArgs } from 'node:util';

const REPO = 'davidemaggi/mcpsh';
const HOMEPAGE = `https://github.com/${REPO}`;
const DESCRIPTION = 'Use any MCP server from the shell: MCP tools as CLI commands for coding agents';
const NPM_SCOPE = '@davidemaggi';

const { values } = parseArgs({
  options: {
    version: { type: 'string' },
    dist: { type: 'string', default: 'dist' },
    out: { type: 'string', default: 'out' },
    only: { type: 'string', default: 'homebrew,scoop,npm' },
  },
});
if (!values.version) fail('--version is required');
const version = values.version.replace(/^v/, '');
const dist = resolve(values.dist);
const out = resolve(values.out);
const only = new Set(values.only.split(',').map((s) => s.trim()).filter(Boolean));

function fail(msg) {
  console.error(`package-managers: ${msg}`);
  process.exit(1);
}

function readSums(name) {
  const file = join(dist, name);
  if (!existsSync(file)) return null;
  const sums = new Map();
  for (const line of readFileSync(file, 'utf8').split(/\r?\n/)) {
    const m = line.match(/^([0-9a-f]{64})\s+\*?(.+)$/i);
    if (m) sums.set(m[2].trim(), m[1].toLowerCase());
  }
  return sums;
}

const sums = readSums('checksums.txt') ?? fail(`no checksums.txt in ${dist}`);
const signedSums = readSums('checksums-signed.txt') ?? new Map();

const archive = (os, arch) => `mcpsh_${version}_${os}_${arch}.${os === 'windows' ? 'zip' : 'tar.gz'}`;
const signedArchive = (arch) => `mcpsh_${version}_windows_${arch}-signed.zip`;
const downloadUrl = (file) => `https://github.com/${REPO}/releases/download/v${version}/${file}`;

function sha(file) {
  const s = sums.get(file) ?? signedSums.get(file);
  if (!s) fail(`${file} is not listed in the checksums`);
  return s;
}

// The Windows file a package should use: the signed zip when the release has it.
function windowsFile(arch) {
  const signed = signedArchive(arch);
  return signedSums.has(signed) ? signed : archive('windows', arch);
}

function write(path, content) {
  mkdirSync(join(path, '..'), { recursive: true });
  writeFileSync(path, content);
  console.error(`wrote ${path}`);
}

// ── Homebrew ─────────────────────────────────────────────────────────────────────────────────
function homebrew() {
  const arm = sha(archive('darwin', 'arm64'));
  const intel = sha(archive('darwin', 'amd64'));
  const cask = `cask "mcpsh" do
  arch arm: "arm64", intel: "amd64"

  version "${version}"
  sha256 arm:   "${arm}",
         intel: "${intel}"

  url "${HOMEPAGE}/releases/download/v#{version}/mcpsh_#{version}_darwin_#{arch}.tar.gz"
  name "mcpsh"
  desc "${DESCRIPTION.replace(/: .*/, '')}"
  homepage "${HOMEPAGE}"

  livecheck do
    url :url
    strategy :github_latest
  end

  binary "mcpsh"

  # Not notarized by Apple: without this, macOS refuses to run it the first time
  postflight_steps do
    run "/usr/bin/xattr", args: ["-d", "com.apple.quarantine", "{{staged_path}}/mcpsh"]
  end

  zap trash: [
    "~/.cache/mcpsh",
    "~/.config/mcpsh",
    "~/.local/state/mcpsh",
  ]
end
`;
  write(join(out, 'homebrew', 'Casks', 'mcpsh.rb'), cask);
}

// ── Scoop ────────────────────────────────────────────────────────────────────────────────────
function scoop() {
  const entry = (arch) => {
    const file = windowsFile(arch);
    return { url: downloadUrl(file), hash: sha(file) };
  };
  const signed = signedSums.has(signedArchive('amd64'));
  const suffix = signed ? '-signed' : '';
  const manifest = {
    version,
    description: DESCRIPTION,
    homepage: HOMEPAGE,
    license: 'MIT',
    architecture: { '64bit': entry('amd64'), arm64: entry('arm64') },
    bin: 'mcpsh.exe',
    checkver: { github: HOMEPAGE },
    autoupdate: {
      architecture: {
        '64bit': { url: `${HOMEPAGE}/releases/download/v$version/mcpsh_$version_windows_amd64${suffix}.zip` },
        arm64: { url: `${HOMEPAGE}/releases/download/v$version/mcpsh_$version_windows_arm64${suffix}.zip` },
      },
      hash: { url: `$baseurl/checksums${suffix}.txt` },
    },
  };
  write(join(out, 'scoop', 'bucket', 'mcpsh.json'), JSON.stringify(manifest, null, 4) + '\n');
}

// ── npm ──────────────────────────────────────────────────────────────────────────────────────
// One package per platform holding the native binary (selected by npm through os/cpu), and the
// main package with a small launcher that runs it. No install scripts.
const NPM_PLATFORMS = [
  { os: 'linux', arch: 'amd64', npmOs: 'linux', npmCpu: 'x64' },
  { os: 'linux', arch: 'arm64', npmOs: 'linux', npmCpu: 'arm64' },
  { os: 'darwin', arch: 'amd64', npmOs: 'darwin', npmCpu: 'x64' },
  { os: 'darwin', arch: 'arm64', npmOs: 'darwin', npmCpu: 'arm64' },
  { os: 'windows', arch: 'amd64', npmOs: 'win32', npmCpu: 'x64' },
  { os: 'windows', arch: 'arm64', npmOs: 'win32', npmCpu: 'arm64' },
];

const common = {
  version,
  license: 'MIT',
  homepage: HOMEPAGE,
  repository: { type: 'git', url: `git+https://github.com/${REPO}.git` },
  bugs: { url: `${HOMEPAGE}/issues` },
};

function extract(file, dir) {
  // bsdtar (Windows, macOS) reads zip and tar.gz; GNU tar (Linux) only tar.gz.
  if (file.endsWith('.zip') && process.platform === 'linux') {
    execFileSync('unzip', ['-q', '-o', file, '-d', dir], { stdio: 'inherit' });
  } else if (process.platform === 'win32') {
    // Windows' own bsdtar: a GNU tar from Git Bash earlier in PATH reads "C:" as a remote host
    const tar = join(process.env.SystemRoot || 'C:\\Windows', 'System32', 'tar.exe');
    execFileSync(tar, ['-xf', file, '-C', dir], { stdio: 'inherit' });
  } else {
    execFileSync('tar', ['-xf', file, '-C', dir], { stdio: 'inherit' });
  }
}

function npm() {
  const root = join(out, 'npm');
  rmSync(root, { recursive: true, force: true });
  const optionalDependencies = {};
  const tmp = mkdtempSync(join(tmpdir(), 'mcpsh-npm-'));
  try {
    for (const p of NPM_PLATFORMS) {
      const name = `${NPM_SCOPE}/mcpsh-${p.npmOs}-${p.npmCpu}`;
      optionalDependencies[name] = version;
      const file = p.os === 'windows' ? windowsFile(p.arch) : archive(p.os, p.arch);
      const src = join(dist, file);
      if (!existsSync(src)) fail(`${file} is missing from ${dist}`);
      const x = join(tmp, `${p.os}-${p.arch}`);
      mkdirSync(x, { recursive: true });
      extract(src, x);

      const dir = join(root, `mcpsh-${p.npmOs}-${p.npmCpu}`);
      const exe = p.os === 'windows' ? 'mcpsh.exe' : 'mcpsh';
      mkdirSync(join(dir, 'bin'), { recursive: true });
      copyFileSync(join(x, exe), join(dir, 'bin', exe));
      if (p.os !== 'windows') chmodSync(join(dir, 'bin', exe), 0o755);
      copyFileSync(join(x, 'LICENSE'), join(dir, 'LICENSE'));
      write(
        join(dir, 'package.json'),
        JSON.stringify(
          {
            name,
            ...common,
            description: `The mcpsh binary for ${p.npmOs}-${p.npmCpu}. Install ${NPM_SCOPE}/mcpsh instead.`,
            os: [p.npmOs],
            cpu: [p.npmCpu],
            files: ['bin/', 'LICENSE'],
            preferUnplugged: true,
          },
          null,
          2,
        ) + '\n',
      );
      write(
        join(dir, 'README.md'),
        `# ${name}\n\nThe \`mcpsh\` ${p.npmOs}-${p.npmCpu} binary, used by [\`${NPM_SCOPE}/mcpsh\`](https://www.npmjs.com/package/${NPM_SCOPE}/mcpsh). See ${HOMEPAGE}.\n`,
      );
    }
  } finally {
    rmSync(tmp, { recursive: true, force: true });
  }

  const main = join(root, 'mcpsh');
  const here = new URL('.', import.meta.url);
  mkdirSync(join(main, 'bin'), { recursive: true });
  copyFileSync(new URL('../npm/mcpsh.js', here), join(main, 'bin', 'mcpsh.js'));
  copyFileSync(new URL('../LICENSE', here), join(main, 'LICENSE'));
  copyFileSync(new URL('../README.md', here), join(main, 'README.md'));
  write(
    join(main, 'package.json'),
    JSON.stringify(
      {
        name: `${NPM_SCOPE}/mcpsh`,
        ...common,
        description: DESCRIPTION,
        keywords: ['mcp', 'model-context-protocol', 'cli', 'agents', 'claude-code', 'codex', 'copilot'],
        bin: { mcpsh: 'bin/mcpsh.js' },
        files: ['bin/', 'LICENSE', 'README.md'],
        engines: { node: '>=18' },
        optionalDependencies,
      },
      null,
      2,
    ) + '\n',
  );
  // Publish order: the platform packages first, so the main package never points at a missing one.
  const order = [...Object.keys(optionalDependencies).map((n) => n.split('/')[1]), 'mcpsh'];
  write(join(root, 'publish-order.txt'), order.join('\n') + '\n');
}

if (only.has('homebrew')) homebrew();
if (only.has('scoop')) scoop();
if (only.has('npm')) npm();
