#!/usr/bin/env bash
# Builds release archives into dist/: teamsesh_<os>_<arch>.tar.gz (zip on Windows) and checksums.txt.
set -euo pipefail
VERSION="${1:?usage: build-release.sh <version>}"
cd "$(dirname "$0")/.."
rm -rf dist && mkdir -p dist
for target in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64 windows/arm64; do
  os="${target%/*}"; arch="${target#*/}"
  name="teamsesh_${os}_${arch}"
  work="$(mktemp -d)"
  bin="teamsesh"; [ "$os" = windows ] && bin="teamsesh.exe"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$work/$bin" ./cmd/teamsesh
  cp README.md LICENSE "$work/"
  if [ "$os" = windows ]; then
    (cd "$work" && zip -q "$OLDPWD/dist/$name.zip" "$bin" README.md LICENSE)
  else
    tar -C "$work" -czf "dist/$name.tar.gz" "$bin" README.md LICENSE
  fi
  rm -rf "$work"
done
(cd dist && shasum -a 256 teamsesh_* > checksums.txt)
cat dist/checksums.txt
