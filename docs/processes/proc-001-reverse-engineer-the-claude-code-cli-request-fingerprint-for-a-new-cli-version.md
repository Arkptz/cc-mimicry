---
author: arkptz
date: 2026-07-10
enables:
- SPEC-001
- POL-001
owner: arkptz
review_date: 2027-01-10
status: active
tags:
- recon
- fingerprint
- reverse-engineering
- cli
- anthropic
---
# Reverse-engineer the Claude Code CLI request fingerprint for a new CLI version

<!-- Brief introduction: what this document is about -->

## Overview
The Claude Code CLI ships as a native, un-stripped ELF binary since approximately v2.1.88 (no more `cli.js` JS bundle). Periodically — whenever Anthropic ships a new CLI version — the cc-mimicry plugin and the CLIProxyAPI (CPA) stack must stay aligned with the CLI's exact **request fingerprint**: the precise set of HTTP headers, `anthropic-beta` tokens, `user-agent` string, `x-stainless-*` headers, billing header shape, and system-prompt layout that the real CLI sends to `api.anthropic.com/v1/messages`.

This PROC is the repeatable recipe for extracting that fingerprint from any new CLI release using a combination of static binary analysis and live traffic capture. It was first executed against **v2.1.206** (2026-07-10) and all commands below are verified against that version.

**Scope:** extracting the fingerprint only. Deciding what to change in the plugin or CPA based on the delta is out of scope (see SPEC-001 and POL-001 for correctness invariants).
## Inputs and Prerequisites
**Trigger:** a new `@anthropic-ai/claude-code` npm version is published and the team needs to verify the fingerprint has not drifted.

**Checklist:**

- Node.js >= 22 installed (verified: v24.15.0) — required for `node --experimental-strip-types`
- `npm` available — used only for `npm view` registry queries
- `strings` (GNU binutils), `file`, `python3`, `pgrep`, `ss` available on PATH
- Live OAuth access token (`sk-ant-oat01-…`) obtained from CPA container `/root/.cli-proxy-api/*.json` (pick one with `expired` > now and high tier e.g. `max_20x`)
- `CLAUDE_CODE_OAUTH_TOKEN` exported in the shell
- Repo scripts present: `scripts/recon/capture.ts`, `scripts/recon/check-binary.sh`, `scripts/recon/extract-cch.ts`, `scripts/recon/xxhash64.ts`
- Working directory is `/home/arkptz/git/mine/cc-mimicry`

**Obtaining the OAuth token from the CPA container:**

```bash
docker exec -it clauberus-cliproxy_cliproxyapi_1 sh
ls /root/.cli-proxy-api/*.json
# Pick a non-expired, high-tier token:
cat /root/.cli-proxy-api/<file>.json | python3 -c \
  "import sys,json; d=json.load(sys.stdin); print(d['access_token'], d['expired'])"
```

**Required repo files (scripts/recon/):**

| File | Purpose |
|---|---|
| `scripts/recon/capture.ts` | Slim standalone Node proxy (port 18899) — the primary capture tool. No build artifacts needed. |
| `scripts/recon/check-binary.sh` | Quick ELF sanity check. |
| `scripts/recon/intercept-claude.ts` | Legacy intercept helper (retained for reference). |
| `scripts/recon/extract-cch.ts` | Extracts cch-related strings from ELF (legacy — cch is now a static placeholder). |
| `scripts/recon/xxhash64.ts` | xxHash64 port (legacy — kept in case Anthropic re-enables dynamic cch). |
## Outputs
At the end of this process the operator has:

| Artifact | Location | Description |
|---|---|---|
| Sanitized capture fixture | `testdata/captures/v<VER>-<model>.json` | On-the-wire POST /v1/messages dump with all headers and body; secrets stripped. |
| Static strings excerpt | `/tmp/cc-strings.txt` (ephemeral) | Full `strings -n 6` output from the ELF; not committed. |
| Delta summary | Mental note / ticket | List of fields that changed vs the previous version, scoped to fields the plugin or CPA actually control. |

**Definition of done:**

1. `testdata/captures/v<VER>-<model>.json` exists and passes the secret-sanity grep (zero hits for token, device_id, session_id).
2. The operator can state for each fingerprint field: its current value, whether it is static or runtime-computed, and which layer (plugin vs CPA executor) owns it on the wire.
3. Port 18899 is free and no `capture.ts` node process is running.
## Roles and Responsibilities
| Role | Responsibility | Owner |
|---|---|---|
| Operator (R) | Executes Steps A–E; produces and commits the sanitized fixture; writes the delta summary. | @arkptz |
| Reviewer (C) | Spot-checks the fixture for completeness and secret hygiene before merge. | @arkptz |
| Plugin maintainer (I) | Receives delta summary and decides whether plugin constants need updating (per SPEC-001 / POL-001). | @arkptz |

R = Responsible, C = Consulted, I = Informed.
## Process Flow
```mermaid
flowchart LR
    A[A: Fetch binary\nnpm/curl + verify ELF] --> B[B: Static extraction\nstrings + grep\nbeta table / cch strategy]
    B --> C[C: Live capture\ncapture.ts proxy\nruntime headers + body]
    C --> D[D: CPA-overlap check\ngit show executor\nCPA-owned vs plugin-owned]
    D --> E[E: Diff and decide\ndelta summary\ncommit sanitized fixture]
```

Five sequential stages; each has a clear pass/fail gate before proceeding to the next.
## Steps
1. **Step A — Fetch the Binary.** Download the official platform tarball and confirm it is an un-stripped ELF.

   ```bash
   npm view @anthropic-ai/claude-code dist-tags
   # Example: { latest: '2.1.206', ... }

   VER=2.1.206   # set to the version you want to recon

   curl -L "https://registry.npmjs.org/@anthropic-ai/claude-code-linux-x64/-/claude-code-linux-x64-${VER}.tgz" \
     -o /tmp/claude-${VER}.tgz
   mkdir -p /tmp/claude-${VER}
   tar -xzf /tmp/claude-${VER}.tgz -C /tmp/claude-${VER}

   # Binary is at package/claude — NOT package/cli (common mistake)
   CLAUDE_BIN=/tmp/claude-${VER}/package/claude
   chmod +x "$CLAUDE_BIN"
   file "$CLAUDE_BIN"          # Expected: ELF 64-bit … not stripped
   "$CLAUDE_BIN" --version     # Expected: 2.1.206
   ```

   **Gate:** `file` says "not stripped"; `--version` matches `$VER`.

2. **Step B — Static Extraction** (source of truth for fixed fingerprint fields).

   ```bash
   strings -n 6 "$CLAUDE_BIN" > /tmp/cc-strings.txt

   grep -i "billing" /tmp/cc-strings.txt
   # Look for: x-anthropic-billing-header
   # Value template: cc_version=…; cc_entrypoint=…; cch=…

   grep -i "cch=" /tmp/cc-strings.txt | head -20
   # v2.1.206: cch=00000  ← STATIC placeholder, NOT recomputed at runtime

   # (Conditional) Dynamic cch detection — only if cch is NOT 00000:
   grep -E "6e52736ac806831e|59cf53e54c78|padStart|0xfffff" /tmp/cc-strings.txt
   # seed, salt, padding, mask from old xxHash64. If found → port xxhash64.ts.

   grep -E "claude-cli/" /tmp/cc-strings.txt
   # Expected: claude-cli/${VERSION} (external, sdk-cli)

   grep "anthropic-version" /tmp/cc-strings.txt    # Expected: 2023-06-01
   grep '"x-app"' /tmp/cc-strings.txt              # Expected: cli
   grep "x-stainless-package-version" /tmp/cc-strings.txt

   # Full beta-token table
   grep -aoE '"[a-z][a-z0-9-]*-202[0-9]-[0-9]{2}-[0-9]{2}"' /tmp/cc-strings.txt | sort -u
   # v2.1.206 expected (9 tokens):
   #   "advisor-tool-2026-03-01"         "claude-code-20250219"
   #   "context-management-2025-06-27"   "effort-2025-11-24"
   #   "interleaved-thinking-2025-05-14" "mid-conversation-system-2026-04-07"
   #   "prompt-caching-scope-2026-01-05" "structured-outputs-2025-12-15"
   #   "thinking-token-count-2026-05-13"
   ```

   **KEY LEARNING (v2.1.206):** `cch` is a **static `cch=00000` placeholder** — the old xxHash64+seed+salt machinery is dead/legacy. No crypto port needed unless the conditional grep above shows activity.

   **Gate:** billing header key found; beta token list non-empty; cch strategy determined.

3. **Step C — Live Capture** (runtime-computed values + on-the-wire truth).

   ```bash
   echo "$CLAUDE_CODE_OAUTH_TOKEN" | head -c 20   # must start with sk-ant-oat01-

   CLAUDE_CODE_OAUTH_TOKEN="$CLAUDE_CODE_OAUTH_TOKEN" \
   CLAUDE_BIN="$CLAUDE_BIN" \
   node --experimental-strip-types scripts/recon/capture.ts \
     --model claude-fable-5 \
     --output testdata/captures/v${VER}-fable5.json
   ```

   **GOTCHA — title-generation sidecar:** CLI sends a secondary request with a dummy `x-api-key` → 401 from api.anthropic.com. Primary fingerprint is still fully captured; the 401 is expected and harmless.

   **GOTCHA — CPA pipeline order** (fields the plugin CANNOT control):
   ```
   DD-model-decode → plugin intercept_before → CPA executor transforms:
       beta header merge/override   ← OVERWRITES plugin-set Anthropic-Beta
       User-Agent override          ← OVERWRITES plugin-set User-Agent
       x-app override               ← OVERWRITES plugin-set x-app
       anthropic-version override   ← OVERWRITES plugin-set anthropic-version
       cch signing (no-op: cch=00000)
   ```

   **Sanitize before committing:**
   ```bash
   python3 -c "
   import json
   with open('testdata/captures/v${VER}-fable5.json') as f:
       d = json.load(f)
   for h in list(d.get('headers', {})):
       if h.lower() in ('x-api-key', 'authorization'):
           d['headers'][h] = 'REDACTED'
   for key in ('device_id', 'session_id'):
       if key in d:
           d[key] = 'REDACTED'
   with open('testdata/captures/v${VER}-fable5.json', 'w') as f:
       json.dump(d, f, indent=2)
   "
   grep -c "sk-ant-oat01" testdata/captures/v${VER}-fable5.json   # must be 0
   grep -c "device_id"    testdata/captures/v${VER}-fable5.json   # must be 0
   ```

   **Gate:** fixture exists; `user-agent`, `anthropic-beta`, billing header non-empty; secret grep counts = 0.

4. **Step D — CPA-Overlap Check** (avoids patching dead code). Determine which fingerprint fields CPA's executor already hardcodes and overwrites.

   ```bash
   CPA_DIR=/path/to/cliproxyapi
   EXECUTOR=internal/runtime/executor/claude_executor.go

   # Option 1: local checkout
   grep -n "applyClaudeHeaders\|baseBetas\|Header.Set\|Anthropic-Beta\|User-Agent\|x-app\|anthropic-version\|x-stainless" \
     "${CPA_DIR}/${EXECUTOR}" | head -60

   # Option 2: specific tag from git history
   git -C "$CPA_DIR" show "v0.9.x:${EXECUTOR}" | \
     grep -n "applyClaudeHeaders\|baseBetas\|Header.Set" | head -60
   ```

   Look for `applyClaudeHeaders` (~lines 1050–1170 in v0.9.x). `baseBetas` string literal + `Header.Set("Anthropic-Beta", …)` prove CPA clobbers the header. Record **CPA-owned** vs **plugin-owned** fields.

   **Gate:** every field from Steps B/C categorized as CPA-owned or plugin-owned.

5. **Step E — Diff and Decide.** Produce a concise delta summary.

   ```bash
   PREV=testdata/captures/v<PREV_VER>-fable5.json
   NEW=testdata/captures/v${VER}-fable5.json

   python3 -c "
   import json
   with open('$PREV') as f: prev = json.load(f)
   with open('$NEW')  as f: new  = json.load(f)
   pb = set(prev.get('betas', [])); nb = set(new.get('betas', []))
   print('Added:',   nb - pb)
   print('Removed:', pb - nb)
   pu = prev.get('headers', {}).get('user-agent', '')
   nu = new.get('headers',  {}).get('user-agent', '')
   if pu != nu: print(f'UA changed: {pu!r} → {nu!r}')
   "
   ```

   For each delta: (a) CPA-owned → note but no plugin action; (b) check SPEC-001/POL-001 coverage; (c) open ticket only for plugin-owned fields that actually changed. Commit the fixture.

6. **QA / Verification Checklist** — run after all steps:

   ```bash
   file "$CLAUDE_BIN" | grep -q "not stripped" && echo "OK" || echo "WARN: stripped"
   "$CLAUDE_BIN" --version | grep -q "$VER" && echo "OK" || echo "FAIL: version mismatch"

   python3 -c "
   import json
   with open('testdata/captures/v${VER}-fable5.json') as f: d = json.load(f)
   assert d.get('headers', {}).get('user-agent'), 'missing user-agent'
   assert d.get('betas') or d.get('headers', {}).get('anthropic-beta'), 'missing betas'
   print('OK: fixture complete')
   "

   grep -c 'sk-ant-oat01' testdata/captures/v${VER}-fable5.json   # must be 0
   grep -c 'device_id'    testdata/captures/v${VER}-fable5.json   # must be 0
   pgrep -af "capture.ts" || echo "OK: no capture.ts procs"
   ss -ltn | grep -q ":18899" && echo "WARN: port 18899 bound" || echo "OK"
   ```

7. **Appendix — Known Values as of v2.1.206** (baseline for future diffs; update each run):

   | Field | Value | Static/Runtime | Owned by |
   |---|---|---|---|
   | `user-agent` | `claude-cli/2.1.206 (external, sdk-cli)` | Runtime (version substituted) | CPA executor |
   | `x-stainless-package-version` | `0.94.0` | Static | CPA executor |
   | `anthropic-version` | `2023-06-01` | Static | CPA executor |
   | `x-app` | `cli` | Static | CPA executor |
   | `x-anthropic-billing-header` | `cc_version=2.1.206.855; cc_entrypoint=sdk-cli;` | Runtime (`cch=` omitted on sdk-cli entrypoint) | CPA executor |
   | `cch` component | `cch=00000` (static placeholder, NOT recomputed) | Static | CPA executor |

   On-the-wire `anthropic-beta` tokens (9 total, all CPA-owned via `applyClaudeHeaders`):
   `claude-code-20250219`, `interleaved-thinking-2025-05-14`, `thinking-token-count-2026-05-13`, `context-management-2025-06-27`, `prompt-caching-scope-2026-01-05`, `mid-conversation-system-2026-04-07`, `advisor-tool-2026-03-01`, `effort-2025-11-24`, `structured-outputs-2025-12-15`.
## Exceptions and Escalation
| Condition | Symptom | Resolution |
|---|---|---|
| Binary is stripped | `file` output contains "stripped" instead of "not stripped" | Static extraction still works for string literals; symbol names absent. Note in delta summary. No escalation needed. |
| `cch` is no longer a static placeholder | Step B grep shows `6e52736ac806831e` / `59cf53e54c78` alongside a non-`00000` cch template | Port `scripts/recon/xxhash64.ts` logic; test against a live capture. Update this PROC's appendix. |
| capture.ts exits with no fixture | No file at `testdata/captures/v<VER>-fable5.json` after the script completes | Check Node version (`node --version` ≥ 22); confirm `CLAUDE_CODE_OAUTH_TOKEN` is exported and non-expired; check `ss -ltn | grep 18899` (port conflict); inspect script stderr. |
| All requests get 401 | Fixture has `status: 401` in every captured request | The OAuth token has expired. Re-obtain from CPA container (see Prerequisites). The fingerprint headers are still visible in the 401 request — capture is still valid for header extraction. |
| CPA source not available locally | No CPA git checkout for Step D | Use `git -C <cpa-dir> show <tag>:internal/runtime/executor/claude_executor.go` against a cached remote, or check the `.cpa-version` / `.cpa-commit` pinned in this repo and fetch that commit from the upstream CPA remote. |
| Port 18899 already in use | `capture.ts` exits immediately with `EADDRINUSE` | Kill the conflicting process: `fuser -k 18899/tcp`, then retry. |
| New beta token found that is not in SPEC-001 | Step E diff shows a new `*-202X-*` token | Add to the appendix of this PROC; open a ticket to update SPEC-001's beta invariant list and POL-001 if it introduces a new correctness constraint. |
## Metrics
| Metric | Target | Notes |
|---|---|---|
| Time to complete Steps A–E | < 30 min per CLI version | From `npm view` to committed sanitized fixture. Assumes token already obtained. |
| Secret-hygiene pass rate | 100% | Zero fixtures committed with live tokens, device_id, or session_id. |
| Coverage of fingerprint fields | 100% | Every field in the on-the-wire request must be categorized (static/runtime, plugin-owned/CPA-owned) before the process is considered done. |
| Lag from CLI release to recon completion | < 7 days | Run this process within one week of a new `@anthropic-ai/claude-code` npm publish. |
## Revision History
| Date | Author | Changes |
|---|---|---|
| 2026-07-10 | @arkptz | Initial version. Verified against CLI v2.1.206. Documents Steps A–E, CPA-overlap check, cch=static finding, pipeline order gotcha, and known values appendix. |