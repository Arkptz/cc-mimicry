#!/usr/bin/env bash
# check-binary.sh — Compare the plugin's current source of truth against the
# installed Claude Code binary. Run after every Claude Code upgrade.
#
# All plugin values are parsed directly from src/*.ts — not hardcoded in this
# script — so the report always reflects what the code actually does.
#
# Usage:
#   ./scripts/check-binary.sh                  # auto-detect claude binary
#   ./scripts/check-binary.sh /path/to/binary  # explicit binary path
#
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# ─── Locate binary ───────────────────────────────────────────────────────────
if [[ -n "${1:-}" ]]; then
  BIN="$1"
else
  CLAUDE_PATH="$(command -v claude 2>/dev/null || true)"
  if [[ -z "$CLAUDE_PATH" ]]; then
    echo "ERROR: 'claude' not in PATH. Pass binary path as argument." >&2
    exit 1
  fi
  # Resolve nix wrappers: claude → .claude-wrapped (actual ELF with strings)
  CLAUDE_DIR="$(dirname "$(readlink -f "$CLAUDE_PATH")")"
  if [[ -f "$CLAUDE_DIR/.claude-wrapped" ]]; then
    BIN="$CLAUDE_DIR/.claude-wrapped"
  else
    BIN="$(readlink -f "$CLAUDE_PATH")"
  fi
fi

[[ -f "$BIN" ]] || { echo "ERROR: binary not found: $BIN" >&2; exit 1; }

BIN_VERSION="$(claude --version 2>/dev/null | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1 || echo "?")"

# ─── Output helpers ──────────────────────────────────────────────────────────
GREEN=$'\033[32m'; RED=$'\033[31m'; YELLOW=$'\033[33m'
DIM=$'\033[2m'; BOLD=$'\033[1m'; RESET=$'\033[0m'

ok()   { printf '  %s✓%s %s\n' "$GREEN" "$RESET" "$1"; }
warn() { printf '  %s⚠%s %s\n' "$YELLOW" "$RESET" "$1"; }
fail() { printf '  %s✗%s %s\n' "$RED" "$RESET" "$1"; }
dim()  { printf '  %s%s%s\n' "$DIM" "$1" "$RESET"; }
hdr()  { printf '\n%s── %s ──%s\n' "$BOLD" "$1" "$RESET"; }
kv()   { printf '  %-28s %s\n' "$1" "$2"; }

# ─── Extract code values via Python parser ─────────────────────────────────
# Single python pass dumps every plugin constant as shell-safe KEY=VALUE
# lines and arrays as newline-delimited strings to $CODE_TMPDIR/*.txt.
CODE_TMPDIR="$(mktemp -d)"
trap 'rm -rf "$CODE_TMPDIR"' EXIT

REPO_ROOT="$REPO_ROOT" CODE_TMPDIR="$CODE_TMPDIR" python3 <<'PYEOF'
import re, json, os
root = os.environ["REPO_ROOT"]
out  = os.environ["CODE_TMPDIR"]

def read(path):
    with open(os.path.join(root, path), encoding="utf-8") as f:
        return f.read()

model_config = read("src/model-config.ts")
signing      = read("src/signing.ts")
xxhash       = read("src/xxhash64.ts")
credentials  = read("src/credentials.ts")
index_ts     = read("src/index.ts")

env = {}

# --- model-config.ts ---
m = re.search(r'ccVersion:\s*"([^"]+)"', model_config)
env["CODE_CC_VERSION"] = m.group(1) if m else ""

def extract_string_array(source, name):
    m = re.search(name + r":\s*\[([^\]]*)\]", source, re.S)
    if not m: return []
    return re.findall(r'"([^"]+)"', m.group(1))

base_betas = extract_string_array(model_config, "baseBetas")
long_betas = extract_string_array(model_config, "longContextBetas")

# modelOverrides.*.add — flatten all add arrays across every override entry
override_add_raw = []
for m in re.finditer(r'add:\s*\[([^\]]*)\]', model_config):
    override_add_raw += re.findall(r'"([^"]+)"', m.group(1))
# Deduplicate while preserving order (multiple overrides can add the same beta)
seen = set()
override_add = []
for b in override_add_raw:
    if b not in seen:
        seen.add(b)
        override_add.append(b)

all_code_betas = sorted(set(base_betas + long_betas + override_add))

def dump(name, items):
    with open(os.path.join(out, name), "w") as f:
        f.write("\n".join(items))

dump("base_betas.txt",     base_betas)
dump("long_betas.txt",     long_betas)
dump("override_add.txt",   override_add)
dump("all_code_betas.txt", all_code_betas)

# --- signing.ts ---
m = re.search(r'BILLING_SALT\s*=\s*"([^"]+)"', signing)
env["CODE_BILLING_SALT"] = m.group(1) if m else ""

m = re.search(r'\[\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)\s*\]\s*\.map', signing)
env["CODE_SAMPLING_INDICES"] = f"[{m.group(1)},{m.group(2)},{m.group(3)}]" if m else ""

m = re.search(r'cch=(\w+);`', signing)
env["CODE_CCH_PLACEHOLDER"] = m.group(1) if m else ""

# --- xxhash64.ts ---
m = re.search(r'CCH_SEED\s*=\s*(0x[0-9a-fA-F]+)n', xxhash)
env["CODE_CCH_SEED"] = m.group(1) if m else ""

m = re.search(r'hash\s*&\s*(0x[0-9a-fA-F]+)n', xxhash)
env["CODE_CCH_MASK"] = m.group(1) if m else ""

# --- credentials.ts ---
m = re.search(r'OAUTH_CLIENT_ID\s*=\s*"([^"]+)"', credentials)
env["CODE_OAUTH_CLIENT_ID"] = m.group(1) if m else ""

m = re.search(r'OAUTH_TOKEN_URL\s*=\s*"([^"]+)"', credentials)
env["CODE_OAUTH_TOKEN_URL"] = m.group(1) if m else ""

# --- index.ts ---
m = re.search(r'`claude-cli/\$\{[^}]+\}\s*\((external,\s*[^)]+)\)`', index_ts)
env["CODE_USER_AGENT_PARENS"] = m.group(1) if m else ""

m = re.search(r'"anthropic-version",\s*"([^"]+)"', index_ts)
env["CODE_API_VERSION"] = m.group(1) if m else ""

m = re.search(r'x-stainless-package-version"\s*:\s*"([^"]+)"', index_ts)
env["CODE_STAINLESS_VER"] = m.group(1) if m else ""

m = re.search(r'SYSTEM_IDENTITY_PREFIX\s*=\s*\n?\s*"([^"]+)"', index_ts)
env["CODE_SYSTEM_IDENTITY"] = m.group(1) if m else ""

with open(os.path.join(out, "env.sh"), "w") as f:
    for k, v in env.items():
        f.write(f"{k}={json.dumps(v)}\n")
PYEOF

# shellcheck source=/dev/null
source "$CODE_TMPDIR/env.sh"

mapfile -t CODE_BASE_BETAS   < "$CODE_TMPDIR/base_betas.txt"
mapfile -t CODE_LONG_BETAS   < "$CODE_TMPDIR/long_betas.txt"
mapfile -t CODE_OVERRIDE_ADD < "$CODE_TMPDIR/override_add.txt"
mapfile -t CODE_ALL_BETAS    < "$CODE_TMPDIR/all_code_betas.txt"

# ─── Extract binary values ──────────────────────────────────────────────────
BIN_STRINGS="$CODE_TMPDIR/bin_strings.txt"
strings -n 8 "$BIN" > "$BIN_STRINGS"

# All date-suffixed betas (YYYY-MM-DD) in the binary.
# Digits in body allow ids like "context-1m-2025-08-07".
mapfile -t BIN_DATED_BETAS < <(
  grep -E '^[a-z][a-z0-9-]+-20[0-9]{2}-[0-9]{2}-[0-9]{2}$' "$BIN_STRINGS" | sort -u
)

# Non-dated betas: claude-code-YYYYMMDD format (no dashes between date parts)
mapfile -t BIN_NONDATED_BETAS < <(
  grep -E '^claude-code-[0-9]{8}$' "$BIN_STRINGS" | sort -u
)

BIN_ALL_BETAS=( "${BIN_DATED_BETAS[@]}" "${BIN_NONDATED_BETAS[@]}" )

in_array() {
  local needle="$1"; shift
  local item
  for item in "$@"; do [[ "$item" == "$needle" ]] && return 0; done
  return 1
}

bin_count() { grep -cF -- "$1" "$BIN_STRINGS" || true; }

# ─── Report ─────────────────────────────────────────────────────────────────
echo "═══════════════════════════════════════════════════════════════"
echo "  opencode-claude-auth  ↔  Claude Code binary audit"
echo "═══════════════════════════════════════════════════════════════"
kv "Binary"            "$BIN"
kv "Binary version"    "$BIN_VERSION"
kv "Plugin ccVersion"  "$CODE_CC_VERSION"
kv "Report time"       "$(date -Iseconds)"

# ─── 1. CLI version ────────────────────────────────────────────────────────
hdr "CLI version"
if [[ "$BIN_VERSION" == "$CODE_CC_VERSION" ]]; then
  ok "match: $BIN_VERSION"
else
  fail "MISMATCH — plugin=$CODE_CC_VERSION  binary=$BIN_VERSION"
  dim "fix: set ccVersion = \"$BIN_VERSION\" in src/model-config.ts"
fi

# ─── 2. Base betas ──────────────────────────────────────────────────────────
hdr "baseBetas — sent on every request"
for beta in "${CODE_BASE_BETAS[@]}"; do
  [[ -z "$beta" ]] && continue
  if in_array "$beta" "${BIN_ALL_BETAS[@]}"; then
    ok "$beta"
  else
    fail "$beta — NOT in binary"
  fi
done

# ─── 3. Long context betas ─────────────────────────────────────────────────
hdr "longContextBetas — added when ANTHROPIC_ENABLE_1M_CONTEXT=true"
for beta in "${CODE_LONG_BETAS[@]}"; do
  [[ -z "$beta" ]] && continue
  if in_array "$beta" "${BIN_ALL_BETAS[@]}"; then
    ok "$beta"
  else
    fail "$beta — NOT in binary"
  fi
done

# ─── 4. Model-override betas (effort-*, etc) ───────────────────────────────
hdr "modelOverrides.*.add — per-model extra betas"
if [[ ${#CODE_OVERRIDE_ADD[@]} -eq 0 ]] || [[ -z "${CODE_OVERRIDE_ADD[0]:-}" ]]; then
  dim "(none defined)"
else
  for beta in "${CODE_OVERRIDE_ADD[@]}"; do
    [[ -z "$beta" ]] && continue
    if in_array "$beta" "${BIN_ALL_BETAS[@]}"; then
      ok "$beta"
    else
      fail "$beta — NOT in binary"
    fi
  done
fi

# ─── 5. New betas in binary but not referenced by plugin ───────────────────
hdr "Betas present in binary but NOT used by plugin"
any_new=0
for beta in "${BIN_ALL_BETAS[@]}"; do
  if ! in_array "$beta" "${CODE_ALL_BETAS[@]}"; then
    dim "· $beta"
    any_new=1
  fi
done
if [[ $any_new -eq 0 ]]; then
  ok "none — plugin tracks every known beta in the binary"
fi

# ─── 6. Billing salt ───────────────────────────────────────────────────────
hdr "Billing salt (used in version-suffix SHA-256 input)"
COUNT="$(bin_count "$CODE_BILLING_SALT")"
kv "Plugin"         "$CODE_BILLING_SALT"
kv "In binary"      "$COUNT occurrence(s)"
if [[ "$COUNT" -gt 0 ]]; then
  ok "salt still present in binary"
else
  fail "salt not found — algorithm or constant likely changed"
fi

# ─── 7. Sampling indices ───────────────────────────────────────────────────
hdr "Version-suffix sampling indices"
COUNT="$(bin_count "$CODE_SAMPLING_INDICES")"
kv "Plugin"         "$CODE_SAMPLING_INDICES"
kv "In binary"      "$COUNT occurrence(s)"
if [[ "$COUNT" -gt 0 ]]; then
  ok "indices unchanged"
else
  fail "indices not found — Claude may sample different positions now"
fi

# ─── 8. CCH placeholder ────────────────────────────────────────────────────
hdr "CCH placeholder (replaced with xxHash64 output after body hash)"
PLACEHOLDER="cch=$CODE_CCH_PLACEHOLDER"
COUNT="$(bin_count "$PLACEHOLDER")"
kv "Plugin"     "$PLACEHOLDER"
kv "In binary"  "$COUNT occurrence(s)"
if [[ "$COUNT" -gt 0 ]]; then
  ok "placeholder format unchanged"
else
  fail "placeholder NOT found — binary may use a different token"
fi

# ─── 9. CCH seed & mask ────────────────────────────────────────────────────
hdr "CCH hash constants (xxHash64)"
kv "Plugin CCH_SEED"  "$CODE_CCH_SEED"
kv "Plugin mask"      "$CODE_CCH_MASK (low 20 bits → 5 hex chars)"
BUN_HASH="$(grep -cF 'Bun.hash(' "$BIN_STRINGS" || true)"
XXHASH="$(grep -cF 'xxhash' "$BIN_STRINGS" || true)"
WYHASH="$(grep -cF 'wyhash' "$BIN_STRINGS" || true)"
kv "Bun.hash refs"    "$BUN_HASH"
kv "xxhash refs"      "$XXHASH"
kv "wyhash refs"      "$WYHASH"
dim "Seed is Bun native bytecode, not a raw string — cannot grep."
dim "Run 'pnpm run extract:cch' to verify live hash equality."

# ─── 10. OAuth client ID ───────────────────────────────────────────────────
hdr "OAuth client ID (token refresh)"
COUNT="$(bin_count "$CODE_OAUTH_CLIENT_ID")"
kv "Plugin"     "$CODE_OAUTH_CLIENT_ID"
kv "In binary"  "$COUNT occurrence(s)"
if [[ "$COUNT" -gt 0 ]]; then
  ok "client ID unchanged"
else
  fail "client ID not found — OAuth app may have rotated"
fi

# ─── 11. OAuth token URL ───────────────────────────────────────────────────
hdr "OAuth token endpoint"
kv "Plugin"     "$CODE_OAUTH_TOKEN_URL"
HOST="$(echo "$CODE_OAUTH_TOKEN_URL" | sed 's|https\?://||;s|/.*||')"
PATH_FRAG="$(echo "$CODE_OAUTH_TOKEN_URL" | sed 's|.*://[^/]*||')"
HOST_COUNT="$(bin_count "$HOST")"
PATH_COUNT="$(bin_count "$PATH_FRAG")"
kv "host '$HOST'"      "$HOST_COUNT occurrence(s)"
kv "path '$PATH_FRAG'" "$PATH_COUNT occurrence(s)"
if [[ "$PATH_COUNT" -gt 0 ]]; then
  ok "token endpoint path present"
elif [[ "$HOST_COUNT" -gt 0 ]]; then
  warn "host found but not path — endpoint may have moved"
else
  warn "endpoint not found as a literal (may be built at runtime)"
fi

# ─── 12. User-Agent format ─────────────────────────────────────────────────
hdr "User-Agent parenthetical"
kv "Plugin"     "claude-cli/<ver> ($CODE_USER_AGENT_PARENS)"
COUNT="$(bin_count "($CODE_USER_AGENT_PARENS)")"
kv "In binary"  "$COUNT occurrence(s)"
if [[ "$COUNT" -gt 0 ]]; then
  ok "user-agent format unchanged"
else
  warn "format not literal-matched (string may be built from fragments)"
fi

# ─── 13. System identity prompt ────────────────────────────────────────────
hdr "System identity prefix"
COUNT="$(bin_count "$CODE_SYSTEM_IDENTITY")"
kv "Plugin"     "\"$CODE_SYSTEM_IDENTITY\""
kv "In binary"  "$COUNT occurrence(s)"
if [[ "$COUNT" -gt 0 ]]; then
  ok "identity string unchanged"
else
  fail "identity string not found — prompt wording may have changed"
fi

# ─── 14. API version header ────────────────────────────────────────────────
hdr "anthropic-version header value"
COUNT="$(grep -cE "^${CODE_API_VERSION}$" "$BIN_STRINGS" || true)"
kv "Plugin"                  "$CODE_API_VERSION"
kv "In binary (exact match)" "$COUNT occurrence(s)"
if [[ "$COUNT" -gt 0 ]]; then
  ok "API version unchanged"
else
  fail "API version not found as exact string"
fi

# ─── 15. Billing header format markers ─────────────────────────────────────
hdr "Billing header format markers"
for marker in "x-anthropic-billing-header" "cc_version=" "cc_entrypoint=" "cch="; do
  COUNT="$(bin_count "$marker")"
  if [[ "$COUNT" -gt 0 ]]; then
    ok "$marker  ($COUNT occurrences)"
  else
    fail "$marker  — NOT in binary"
  fi
done
COUNT="$(bin_count "cc_workload=")"
if [[ "$COUNT" -gt 0 ]]; then
  dim "note: binary also emits cc_workload=  ($COUNT occurrences) — plugin does not"
fi

# ─── 16. Stainless SDK package version ─────────────────────────────────────
hdr "Stainless SDK (Anthropic TypeScript SDK) package version"
kv "Plugin hardcoded"  "$CODE_STAINLESS_VER"
# The SDK bakes the version as `var <minified>="X.Y.Z"` right before the
# runtime-detection function ({typeof Deno<"u"...}). Anchor on that pattern.
BIN_SDK_VER="$(
  python3 - "$BIN" <<'PYEOF'
import re, sys
with open(sys.argv[1], "rb") as f:
    data = f.read()
m = re.search(
    rb'[A-Za-z_$][\w$]*="(\d+\.\d+\.\d+)";function\s+[A-Za-z_$][\w$]*\(\)\{if\(typeof Deno',
    data,
)
print(m.group(1).decode() if m else "")
PYEOF
)"
if [[ -n "$BIN_SDK_VER" ]]; then
  kv "In binary"  "$BIN_SDK_VER"
  if [[ "$BIN_SDK_VER" == "$CODE_STAINLESS_VER" ]]; then
    ok "SDK version matches"
  else
    warn "SDK version drift: plugin=$CODE_STAINLESS_VER  binary=$BIN_SDK_VER"
    dim "update x-stainless-package-version in src/index.ts to $BIN_SDK_VER"
  fi
else
  warn "couldn't extract SDK version from binary — verify manually"
fi

# ─── 17. Model alias map (info) ────────────────────────────────────────────
hdr "Known model aliases in binary (firstParty identifiers)"
grep -oE 'firstParty:"claude-[a-z0-9-]+' "$BIN_STRINGS" \
  | sort -u | sed 's/firstParty:"//' | while read -r model; do
  dim "· $model"
done

# ─── Summary ────────────────────────────────────────────────────────────────
echo
echo "═══════════════════════════════════════════════════════════════"
echo "  Audit complete. Review any ✗ (fail) or ⚠ (warn) items above."
echo "═══════════════════════════════════════════════════════════════"
