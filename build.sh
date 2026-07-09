#!/usr/bin/env bash
# Build the cc-mimicry CLIProxyAPI plugin as a native c-shared library.
#
# CLIProxyAPI plugins are CGO c-shared objects. The plugin ID is the output
# filename without extension, so we always emit `cc-mimicry.so`.
#
# The host that loads this .so MUST be built with CGO_ENABLED=1 (the stock
# eceasy image is NOT — use ../CLIProxyAPI/Dockerfile.plugins).
#
# Usage:
#   ./build.sh                      # build for the host platform into ./dist
#   OUT_DIR=/path ./build.sh        # override output dir
#   VERSION=0.2.0 ./build.sh        # stamp a plugin version
set -euo pipefail

cd "$(dirname "$0")"

ID="cc-mimicry"
VERSION="${VERSION:-0.1.0}"
OUT_DIR="${OUT_DIR:-dist}"

GOOS="${GOOS:-$(go env GOOS)}"
GOARCH="${GOARCH:-$(go env GOARCH)}"

case "$GOOS" in
	linux | freebsd) EXT="so" ;;
	darwin) EXT="dylib" ;;
	windows) EXT="dll" ;;
	*)
		echo "unsupported GOOS: $GOOS" >&2
		exit 1
		;;
esac

mkdir -p "$OUT_DIR"
OUT="${OUT_DIR}/${ID}.${EXT}"

echo "building ${OUT} (GOOS=${GOOS} GOARCH=${GOARCH} VERSION=${VERSION})"
CGO_ENABLED=1 GOOS="$GOOS" GOARCH="$GOARCH" go build \
	-buildvcs=false \
	-buildmode=c-shared \
	-ldflags="-s -w -X 'main.pluginVersion=${VERSION}'" \
	-o "$OUT" .

# c-shared also emits a .h header we do not need for distribution.
rm -f "${OUT%.*}.h"

echo "done: ${OUT}"
