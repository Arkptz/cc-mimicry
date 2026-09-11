#!/usr/bin/env bash
# capture-live.sh — capture a real Claude Code request fingerprint per surface.
#
# Implements PROC-001 Step C without TLS interception: the CLI is pointed at a
# local mitmproxy running in reverse-proxy mode, which forwards to the real
# upstream. The CLI talks plain HTTP to localhost, so no CA trust is needed.
#
# Usage:
#   scripts/recon/capture-live.sh --version 2.1.268 [--surface cli|sdk-cli|both]
#
# Requirements: nix (provides mitmproxy), jq, curl, a Claude Code auth token.
# Auth is taken from ANTHROPIC_AUTH_TOKEN, or read from the path in
# CC_TOKEN_FILE (default: /var/lib/bifrost-vk/claude-code). Upstream defaults to
# ANTHROPIC_BASE_URL, matching the local wrapper.
#
# Output: testdata/captures/v<VERSION>/<surface>-body.json, redacted.

set -euo pipefail

REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
VERSION=""
SURFACES="both"
PORT="${CC_CAPTURE_PORT:-18080}"
UPSTREAM="${ANTHROPIC_BASE_URL:-https://api.anthropic.com}"
TOKEN_FILE="${CC_TOKEN_FILE:-/var/lib/bifrost-vk/claude-code}"
MODEL="${CC_CAPTURE_MODEL:-claude-sonnet-4-5-20250929}"
PROMPT="${CC_CAPTURE_PROMPT:-Reply with the single word: ok}"

while [ $# -gt 0 ]; do
  case "$1" in
    --version) VERSION="$2"; shift 2 ;;
    --surface) SURFACES="$2"; shift 2 ;;
    --port)    PORT="$2"; shift 2 ;;
    --upstream) UPSTREAM="$2"; shift 2 ;;
    --binary)  CLAUDE_BIN="$2"; shift 2 ;;
    -h|--help) sed -n '2,18p' "${BASH_SOURCE[0]}"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

if [ -z "$VERSION" ]; then
  echo "error: --version is required (e.g. --version 2.1.268)" >&2
  exit 2
fi

# The binary must match the version being captured: the CLI stamps its own
# VERSION into the billing header and User-Agent.
CLAUDE_BIN="${CLAUDE_BIN:-}"
if [ -z "$CLAUDE_BIN" ]; then
  CLAUDE_BIN="$REPO_ROOT/_work/claude-$VERSION/claude"
fi
if [ ! -x "$CLAUDE_BIN" ]; then
  echo "error: no executable CLI at $CLAUDE_BIN" >&2
  echo "hint: scripts/recon/fetch-binary.sh $VERSION" >&2
  exit 1
fi

ACTUAL_VERSION=$("$CLAUDE_BIN" --version 2>/dev/null | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1 || true)
if [ "$ACTUAL_VERSION" != "$VERSION" ]; then
  echo "error: $CLAUDE_BIN reports '$ACTUAL_VERSION', expected '$VERSION'" >&2
  exit 1
fi

if [ -z "${ANTHROPIC_AUTH_TOKEN:-}" ] && [ -r "$TOKEN_FILE" ]; then
  ANTHROPIC_AUTH_TOKEN=$(cat "$TOKEN_FILE")
fi
if [ -z "${ANTHROPIC_AUTH_TOKEN:-}" ]; then
  echo "error: no auth token (set ANTHROPIC_AUTH_TOKEN or make $TOKEN_FILE readable)" >&2
  exit 1
fi

# Split the upstream into origin (what mitmdump proxies to) and path prefix
# (what the CLI must keep in its base URL).
UPSTREAM_ORIGIN=$(printf '%s' "$UPSTREAM" | sed -E 's#^(https?://[^/]+).*#\1#')
UPSTREAM_PREFIX=$(printf '%s' "$UPSTREAM" | sed -E 's#^https?://[^/]+##; s#/$##')

WORK_DIR="$REPO_ROOT/_work/capture-$VERSION"
# Fixtures are grouped per CLI version, with identical file names inside each.
CAPTURE_DIR="$REPO_ROOT/testdata/captures/v$VERSION"
mkdir -p "$WORK_DIR" "$CAPTURE_DIR"

# A python that can import mitmproxy, for the flow -> fixture conversion.
PYTHON_MITM_EXPR=(--impure --expr "(import <nixpkgs> {}).python3.withPackages (ps: [ ps.mitmproxy ])")

case "$SURFACES" in
  both) SURFACE_LIST="cli sdk-cli" ;;
  *)    SURFACE_LIST="$SURFACES" ;;
esac

capture_one() {
  local surface="$1"
  local flow="$WORK_DIR/$surface.flow"
  local mitm_log="$WORK_DIR/$surface-mitm.log"
  local cli_log="$WORK_DIR/$surface-cli.log"

  echo "=== capturing surface: $surface ==="
  rm -f "$flow"

  # Reverse mode: the CLI hits http://127.0.0.1:$PORT, mitmproxy forwards to the
  # real upstream over TLS. No CA install, no cert pinning trouble.
  #
  # mitmdump's reverse spec accepts scheme://host:port only, so any path prefix
  # on the upstream (e.g. a relay's /anthropic) is kept on the client side and
  # re-attached to the CLI's base URL below.
  nix shell nixpkgs#mitmproxy --command \
    mitmdump --mode "reverse:$UPSTREAM_ORIGIN" --listen-port "$PORT" \
      --set flow_detail=0 --set termlog_verbosity=warn \
      -w "$flow" > "$mitm_log" 2>&1 &
  local mitm_pid=$!
  # shellcheck disable=SC2064  # expand now: the pid must be captured at trap time
  trap "kill $mitm_pid 2>/dev/null || true" RETURN

  local ready=""
  for _ in $(seq 1 60); do
    if curl -s -o /dev/null --max-time 2 "http://127.0.0.1:$PORT$UPSTREAM_PREFIX/" 2>/dev/null; then
      ready=1
      break
    fi
    if ! kill -0 "$mitm_pid" 2>/dev/null; then
      echo "error: mitmdump exited early; see $mitm_log" >&2
      cat "$mitm_log" >&2
      return 1
    fi
    sleep 0.5
  done
  if [ -z "$ready" ]; then
    echo "error: mitmdump did not become ready on port $PORT" >&2
    return 1
  fi

  # The surface is decided by how the CLI is driven, not by an env var: `-p`
  # (print) always reports cc_entrypoint=sdk-cli and sends the Output Style
  # prompt, while the interactive TUI reports cc_entrypoint=cli and sends the
  # software-engineering prompt plus the extra "# Text output" block. Driving
  # the TUI needs a pty, so the cli surface is captured through `script`.
  set +e
  if [ "$surface" = "cli" ]; then
    env -u ANTHROPIC_API_KEY \
      ANTHROPIC_BASE_URL="http://127.0.0.1:$PORT$UPSTREAM_PREFIX" \
      ANTHROPIC_AUTH_TOKEN="$ANTHROPIC_AUTH_TOKEN" \
      CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 \
      script -qefc "$(printf '%q' "$CLAUDE_BIN") --model $(printf '%q' "$MODEL")" /dev/null \
      < <(
          # A fresh workspace first shows the "do you trust this folder?"
          # prompt; answer it before typing, or the prompt text is swallowed.
          sleep 3; printf '\033[B\r'
          sleep 4; printf '%s\r' "$PROMPT"
          sleep 25; printf '\003\003'
        ) > "$cli_log" 2>&1
  else
    env -u ANTHROPIC_API_KEY \
      ANTHROPIC_BASE_URL="http://127.0.0.1:$PORT$UPSTREAM_PREFIX" \
      ANTHROPIC_AUTH_TOKEN="$ANTHROPIC_AUTH_TOKEN" \
      CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 \
      "$CLAUDE_BIN" -p "$PROMPT" --model "$MODEL" > "$cli_log" 2>&1
  fi
  local cli_rc=$?
  set -e
  if [ "$cli_rc" -ne 0 ]; then
    echo "warning: CLI exited $cli_rc (capture may still be usable); see $cli_log" >&2
  fi

  # Let mitmproxy flush the flow file before reading it.
  sleep 1
  kill "$mitm_pid" 2>/dev/null || true
  wait "$mitm_pid" 2>/dev/null || true

  if [ ! -s "$flow" ]; then
    echo "error: no flow captured for $surface; see $mitm_log / $cli_log" >&2
    return 1
  fi

  local out="$CAPTURE_DIR/$surface-body.json"
  # The mitmproxy package ships its modules next to its own interpreter rather
  # than on the ambient sys.path, so run the converter with both taken from the
  # package itself.
  # The mitmproxy binary package does not expose its modules to an ambient
  # python, so build an interpreter that has the library on its path.
  nix shell "${PYTHON_MITM_EXPR[@]}" --command \
    python3 "$REPO_ROOT/scripts/recon/flow-to-fixture.py" \
      --flow "$flow" --surface "$surface" --version "$VERSION" --output "$out"

  # Secrets must never reach testdata/. The converter already compares against
  # the request's real credential values; this is a second, value-shaped net.
  # Header *names* are deliberately not matched: the captured system prompt
  # itself talks about "authorization context" and Bearer tokens.
  local leak=0
  for pattern in 'sk-ant-' 'device_id'; do
    if grep -qF "$pattern" "$out"; then
      echo "error: '$pattern' found in $out — refusing to keep it" >&2
      leak=1
    fi
  done
  if [ -n "$ANTHROPIC_AUTH_TOKEN" ] && grep -qF "$ANTHROPIC_AUTH_TOKEN" "$out"; then
    echo "error: the auth token leaked into $out — refusing to keep it" >&2
    leak=1
  fi
  if [ "$leak" -ne 0 ]; then
    rm -f "$out"
    return 1
  fi

  echo "wrote $out"
}

for surface in $SURFACE_LIST; do
  capture_one "$surface"
done

echo
echo "captures written to testdata/captures/v$VERSION/"
echo "next: review the diff, then point golden_test.go at the new fixtures"
