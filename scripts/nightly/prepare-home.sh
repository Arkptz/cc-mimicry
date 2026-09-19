#!/usr/bin/env bash
# prepare-home.sh — pre-seed the CLI's onboarding state for a capture run.
#
# A fresh runner HOME has no ~/.claude.json, so the TUI would boot into
# onboarding dialogs (theme, trust) that steal the scripted keystrokes and
# can swallow the prompt. Seeding the documented state keys skips straight
# to the session. Config flags that reduce nondeterministic background
# traffic are exported for the caller to inherit.

set -euo pipefail

CLAUDE_JSON="$HOME/.claude.json"

if [ ! -f "$CLAUDE_JSON" ]; then
  cat > "$CLAUDE_JSON" <<'JSON'
{
  "hasCompletedOnboarding": true,
  "hasTrustDialogAccepted": true,
  "bypassPermissionsModeAccepted": true,
  "theme": "dark"
}
JSON
  echo "seeded $CLAUDE_JSON"
else
  echo "$CLAUDE_JSON already present, leaving it as is"
fi

# Keep the capture to /v1/messages only: no telemetry, no update pings.
# Exported for same-shell callers (source this script); the nightly workflow
# sets the same vars in its own step env instead.
export CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1
export DISABLE_TELEMETRY=1
export DISABLE_AUTOUPDATER=1
export DISABLE_BUG_COMMAND=1
export DISABLE_COST_WARNINGS=1
export DISABLE_ERROR_REPORTING=1

echo "onboarding state ready"
