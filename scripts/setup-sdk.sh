#!/usr/bin/env bash
# setup-sdk.sh — put the pinned CLIProxyAPI SDK where go.mod expects it.
#
# The build needs the SDK checked out at the go.mod replace target, which lives
# outside the repository. A fresh clone therefore cannot build until this runs.
#
# Usage: scripts/setup-sdk.sh [--force]
#   --force  re-checkout even if the directory already exists

set -euo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$REPO_ROOT"

FORCE=0
if [ "${1:-}" = "--force" ]; then
  FORCE=1
fi

# Read the replace target straight from go.mod so this script cannot drift from it.
REPLACE_PATH=$(go mod edit -json | jq -r '.Replace[]? | select(.Old.Path=="github.com/router-for-me/CLIProxyAPI/v7") | .New.Path')
if [ -z "$REPLACE_PATH" ]; then
  echo "error: no replace directive for the CLIProxyAPI SDK in go.mod" >&2
  exit 1
fi
case "$REPLACE_PATH" in
  /*) SDK_DIR="$REPLACE_PATH" ;;
  *)  SDK_DIR="$REPO_ROOT/$REPLACE_PATH" ;;
esac

CPA_VERSION=$(cat .cpa-version)
CPA_COMMIT=$(cat .cpa-commit)
# The plugin-ABI commits live only in the fork; the upstream tag does not contain them.
SDK_REPO="${CPA_REPO:-https://github.com/Arkptz/CLIProxyAPI.git}"

if [ -d "$SDK_DIR/.git" ] && [ "$FORCE" -eq 0 ]; then
  ACTUAL=$(git -C "$SDK_DIR" rev-parse HEAD)
  if [ "$ACTUAL" = "$CPA_COMMIT" ]; then
    echo "SDK already at the pinned commit: $SDK_DIR ($CPA_VERSION)"
    exit 0
  fi
  echo "error: $SDK_DIR is at $ACTUAL, expected $CPA_COMMIT ($CPA_VERSION)" >&2
  echo "hint: commit or stash any work there, then re-run with --force" >&2
  exit 1
fi

mkdir -p "$(dirname "$SDK_DIR")"
if [ -d "$SDK_DIR" ]; then
  rm -rf "$SDK_DIR"
fi

echo "cloning $SDK_REPO at $CPA_VERSION into $SDK_DIR"
git clone --depth 1 --branch "$CPA_VERSION" "$SDK_REPO" "$SDK_DIR"

ACTUAL=$(git -C "$SDK_DIR" rev-parse HEAD)
if [ "$ACTUAL" != "$CPA_COMMIT" ]; then
  echo "error: tag $CPA_VERSION resolved to $ACTUAL, expected pinned $CPA_COMMIT" >&2
  exit 1
fi

echo "SDK ready at $SDK_DIR ($CPA_VERSION @ ${CPA_COMMIT:0:12})"
echo "next: ./build.sh"
