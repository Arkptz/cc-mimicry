#!/usr/bin/env bash
# resolve-latest.sh — resolve and download the latest Claude Code CLI tarballs.
#
# Queries the npm registry for the latest @anthropic-ai/claude-code version,
# then downloads both the thin wrapper package and the native linux-x64
# package, verifying each against the registry sha512. Writes:
#   <out-dir>/version        — the resolved version string
#   <out-dir>/wrapper.tgz    — the thin installer package
#   <out-dir>/native.tgz     — the native linux-x64 binary package
#
# Usage: resolve-latest.sh <out-dir>
# Env:  NPM_REGISTRY (default https://registry.npmjs.org)

set -euo pipefail

OUT_DIR="${1:-}"
if [ -z "$OUT_DIR" ]; then
  echo "usage: $0 <out-dir>" >&2
  exit 2
fi
if [ "$OUT_DIR" = "-h" ] || [ "$OUT_DIR" = "--help" ]; then
  sed -n '2,14p' "${BASH_SOURCE[0]}"
  exit 0
fi

REGISTRY="${NPM_REGISTRY:-https://registry.npmjs.org}"
PKG="@anthropic-ai/claude-code"

mkdir -p "$OUT_DIR"

fetch_verified() {
  # fetch_verified <pkg> <version> <dest>: metadata → tarball → sha512 gate.
  local pkg="$1" ver="$2" dest="$3"
  local meta="$dest.meta.json"
  curl -fsSL "$REGISTRY/$pkg/$ver" -o "$meta"
  local tarball integrity alg expected actual
  tarball=$(jq -r .dist.tarball "$meta")
  integrity=$(jq -r .dist.integrity "$meta")
  curl -fsSL "$tarball" -o "$dest"
  alg=${integrity%%-*}
  expected=${integrity#*-}
  if [ "$alg" != "sha512" ]; then
    echo "error: $pkg: unexpected integrity algorithm: $alg" >&2
    exit 1
  fi
  actual=$(openssl dgst -sha512 -binary "$dest" | base64 -w0)
  if [ "$actual" != "$expected" ]; then
    echo "error: $pkg sha512 mismatch: registry=$expected actual=$actual" >&2
    exit 1
  fi
}

VERSION=$(curl -fsSL "$REGISTRY/$PKG/latest" | jq -r .version)
if ! [[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "error: registry returned a non-semantic version: $VERSION" >&2
  exit 1
fi

fetch_verified "$PKG" "$VERSION" "$OUT_DIR/wrapper.tgz"
fetch_verified "$PKG-linux-x64" "$VERSION" "$OUT_DIR/native.tgz"
printf '%s\n' "$VERSION" > "$OUT_DIR/version"

echo "resolved $PKG@$VERSION (sha512-verified wrapper + native linux-x64)"
