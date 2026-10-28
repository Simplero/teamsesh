#!/bin/sh
# Installs TeamSesh on macOS or Linux:
#   curl -fsSL https://raw.githubusercontent.com/Simplero/teamsesh/main/install.sh | sh
# Set TEAMSESH_NO_SERVICE=1 to skip starting the background service.
# Running it again updates TeamSesh.
set -eu

REPO="Simplero/teamsesh"
BIN_DIR="${TEAMSESH_BIN_DIR:-$HOME/.local/bin}"

say() { printf '%s\n' "$*"; }
fail() { printf 'teamsesh install: %s\n' "$*" >&2; exit 1; }

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) fail "unsupported system $(uname -s). On Windows, run in PowerShell: irm https://raw.githubusercontent.com/$REPO/main/install.ps1 | iex" ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) fail "unsupported processor $(uname -m)" ;;
esac

command -v curl >/dev/null || fail "curl is required"
if command -v sha256sum >/dev/null; then sha() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null; then sha() { shasum -a 256 "$1" | cut -d' ' -f1; }
else fail "sha256sum or shasum is required"; fi

name="teamsesh_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/latest/download"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

say "Downloading TeamSesh for $os/$arch..."
curl -fsSL "$base/$name" -o "$tmp/$name" || fail "download failed: $base/$name"
curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt" || fail "download failed: checksums.txt"
want="$(grep " $name\$" "$tmp/checksums.txt" | cut -d' ' -f1)"
[ -n "$want" ] || fail "no checksum for $name"
[ "$(sha "$tmp/$name")" = "$want" ] || fail "checksum mismatch for $name"

tar -xzf "$tmp/$name" -C "$tmp" teamsesh
mkdir -p "$BIN_DIR"
mv "$tmp/teamsesh" "$BIN_DIR/teamsesh.new"
chmod 755 "$BIN_DIR/teamsesh.new"
mv "$BIN_DIR/teamsesh.new" "$BIN_DIR/teamsesh"
say "Installed $("$BIN_DIR/teamsesh" version) to $BIN_DIR/teamsesh"

case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *) say ""; say "Add $BIN_DIR to your PATH, for example:"; say "  echo 'export PATH=\"$BIN_DIR:\$PATH\"' >> ~/.$(basename "${SHELL:-sh}")rc" ;;
esac

if ! command -v tailscale >/dev/null && [ ! -x /Applications/Tailscale.app/Contents/MacOS/Tailscale ]; then
  say ""
  say "TeamSesh needs Tailscale. Install it from https://tailscale.com/download and log in, then run:"
  say "  teamsesh service install"
  exit 0
fi

if [ "${TEAMSESH_NO_SERVICE:-}" = 1 ]; then
  say "Skipped the background service. Start it with: teamsesh service install"
else
  "$BIN_DIR/teamsesh" service install
fi

say ""
say "Next: add your teammates by their Tailscale login (they add you too):"
say "  teamsesh team add alice@example.com"
say "Then share a session with: teamsesh share --last"
