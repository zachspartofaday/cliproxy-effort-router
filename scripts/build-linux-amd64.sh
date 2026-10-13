#!/bin/sh
# Run from the repository root. Zig is used only to cross-compile the plugin.
set -eu
version=${1:-0.0.0-whiterock-shadow}
mkdir -p dist
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 CC="$PWD/scripts/zig-linux-amd64-cc" \
 go build -buildvcs=false -trimpath -buildmode=c-shared -ldflags "-s -w -X main.pluginVersion=$version" -o dist/effort-router.so .
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -buildvcs=false -trimpath -ldflags '-s -w' -o dist/jev-ocg-adapter ./adapter
(cd dist && shasum -a 256 effort-router.so jev-ocg-adapter > SHA256SUMS)
