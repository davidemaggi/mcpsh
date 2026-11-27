#!/bin/sh
# Install mcpsh on Linux or macOS.
#
#   curl -fsSL https://raw.githubusercontent.com/davidemaggi/mcpsh/main/install.sh | sh
#
# Environment:
#   MCPSH_VERSION      version to install, e.g. 1.2.3 (default: the latest release)
#   MCPSH_INSTALL_DIR  where to put the binary (default: ~/.local/bin)
#   MCPSH_BASE_URL     release download base (default: https://github.com/davidemaggi/mcpsh/releases)
#
# The archive is verified against the release's checksums.txt before anything is installed.
set -eu

REPO="davidemaggi/mcpsh"
BASE_URL="${MCPSH_BASE_URL:-https://github.com/$REPO/releases}"
INSTALL_DIR="${MCPSH_INSTALL_DIR:-$HOME/.local/bin}"

say() { printf '%s\n' "mcpsh-install: $*" >&2; }
die() { say "error: $*"; exit 1; }

if command -v curl >/dev/null 2>&1; then
  fetch() { curl -fsSL "$1" -o "$2"; }
  fetch_stdout() { curl -fsSL "$1"; }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget -q "$1" -O "$2"; }
  fetch_stdout() { wget -q -O - "$1"; }
else
  die "curl or wget is required"
fi

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) die "unsupported OS $(uname -s); on Windows use install.ps1" ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) die "unsupported architecture $(uname -m)" ;;
esac

version="${MCPSH_VERSION:-}"
if [ -z "$version" ]; then
  tag=$(fetch_stdout "https://api.github.com/repos/$REPO/releases/latest" |
    sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1)
  [ -n "$tag" ] || die "cannot find the latest release (set MCPSH_VERSION)"
  version="${tag#v}"
fi
version="${version#v}"

asset="mcpsh_${version}_${os}_${arch}.tar.gz"
url="$BASE_URL/download/v$version"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

say "downloading mcpsh $version for $os/$arch"
fetch "$url/$asset" "$tmp/$asset" || die "download failed: $url/$asset"
fetch "$url/checksums.txt" "$tmp/checksums.txt" || die "download failed: $url/checksums.txt"

expected=$(awk -v f="$asset" '$2 == f || $2 == "*"f { print $1 }' "$tmp/checksums.txt")
[ -n "$expected" ] || die "$asset is not listed in checksums.txt"
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$tmp/$asset" | awk '{print $1}')
else
  actual=$(shasum -a 256 "$tmp/$asset" | awk '{print $1}')
fi
[ "$expected" = "$actual" ] || die "checksum mismatch for $asset (expected $expected, got $actual)"

tar -xzf "$tmp/$asset" -C "$tmp"
[ -f "$tmp/mcpsh" ] || die "the archive does not contain mcpsh"
mkdir -p "$INSTALL_DIR"
# Replace atomically: a running daemon keeps using the old file until it restarts.
cp "$tmp/mcpsh" "$INSTALL_DIR/.mcpsh.new"
chmod 0755 "$INSTALL_DIR/.mcpsh.new"
if [ "$os" = darwin ]; then
  xattr -d com.apple.quarantine "$INSTALL_DIR/.mcpsh.new" 2>/dev/null || true
fi
mv -f "$INSTALL_DIR/.mcpsh.new" "$INSTALL_DIR/mcpsh"

say "installed $("$INSTALL_DIR/mcpsh" version)"
say "  -> $INSTALL_DIR/mcpsh"
case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *) say "add $INSTALL_DIR to your PATH, e.g.: echo 'export PATH=\"$INSTALL_DIR:\$PATH\"' >> ~/.profile" ;;
esac
say "next: mcpsh add <name> -- <command>   and   mcpsh skill install"
