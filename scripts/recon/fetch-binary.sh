#!/usr/bin/env bash
# fetch-binary.sh — download and verify the native Claude Code CLI for a version.
#
# Since 2.1.x the @anthropic-ai/claude-code npm package is a thin installer; the
# native binary ships in a per-platform package. This fetches the linux-x64 one,
# verifies its npm sha512, and unpacks it to _work/claude-<version>/claude.
#
# Usage: scripts/recon/fetch-binary.sh 2.1.268 [--tgz <path>]
#   --tgz  install from an already-downloaded tarball instead of the registry.
#          The bytes are still verified against the registry sha512, so a CI
#          artifact cannot smuggle a different binary past the gate.

set -euo pipefail

VERSION="${1:-}"
if [ -z "$VERSION" ] || [ "$VERSION" = "--tgz" ]; then
  echo "usage: $0 <version> [--tgz <path>]   (e.g. $0 2.1.268)" >&2
  echo "hint: npm view @anthropic-ai/claude-code dist-tags" >&2
  exit 2
fi
shift

TGZ=""
if [ "${1:-}" = "--tgz" ]; then
  if [ $# -lt 2 ] || [ -z "$2" ]; then
    echo "error: --tgz needs a path" >&2
    exit 2
  fi
  TGZ="$2"
  shift 2
fi
if [ $# -gt 0 ]; then
  echo "error: unknown argument: $1" >&2
  exit 2
fi

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
REGISTRY="${NPM_REGISTRY:-https://registry.npmjs.org}"
PKG="@anthropic-ai/claude-code-linux-x64"
DEST="$REPO_ROOT/_work/claude-$VERSION"

if [ -x "$DEST/claude" ]; then
  echo "already present: $DEST/claude"
  "$DEST/claude" --version
  exit 0
fi

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

echo "fetching $PKG@$VERSION"
curl -fsSL "$REGISTRY/$PKG/$VERSION" -o "$TMP/meta.json"
TARBALL=$(jq -r .dist.tarball "$TMP/meta.json")
INTEGRITY=$(jq -r .dist.integrity "$TMP/meta.json")

if [ -n "$TGZ" ]; then
  # Verify the artifact bytes are exactly the registry's tarball before use.
  cp "$TGZ" "$TMP/pkg.tgz"
else
  curl -fsSL "$TARBALL" -o "$TMP/pkg.tgz"
fi

ALG=${INTEGRITY%%-*}
EXPECTED=${INTEGRITY#*-}
if [ "$ALG" != "sha512" ]; then
  echo "error: unexpected integrity algorithm: $ALG" >&2
  exit 1
fi
ACTUAL=$(openssl dgst -sha512 -binary "$TMP/pkg.tgz" | base64 -w0)
if [ "$ACTUAL" != "$EXPECTED" ]; then
  echo "error: sha512 mismatch: registry=$EXPECTED actual=$ACTUAL" >&2
  exit 1
fi
echo "sha512 OK"

mkdir -p "$DEST"
tar -xzf "$TMP/pkg.tgz" -C "$DEST" --strip-components=1
chmod +x "$DEST/claude"

file "$DEST/claude"
"$DEST/claude" --version
echo "binary ready: $DEST/claude"
