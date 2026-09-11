---
author: arkptz
code_paths:
- internal/mimicry/config.go
- internal/mimicry/toolrewrite.go
- internal/mimicry/mimicry.go
date: 2026-09-11
related:
- SPEC-001
- ADR-004
status: accepted
tags:
- fingerprint
- cache-control
- evidence
---
# Emitting fields the real CLI omits defeats the fingerprint

<!-- Brief introduction: what this document is about -->

## Context

cc-mimicry impersonates the Claude Code CLI's request fingerprint. A dual-model review plus mutation testing (commit 7b9681c, "fix: close fingerprint and test-strength gaps found by dual-model review") found the plugin was unconditionally attaching `cache_control` to `tools[-1]` while the real CLI 2.1.268 sends `cache_control` on none of its tools — verified against the live captures `testdata/captures/v2.1.268/{cli,sdk-cli}-body.json` (`tools_shape.with_cache_control: 0` of 221 tools for `cli`, 0 of 212 for `sdk-cli`; captured through a local mitmproxy reverse proxy, `scripts/recon/capture-live.sh`). An extra field the real client never sends is a POSITIVE discriminator: it does not merely fail to hide the plugin, it actively singles the request out as non-native traffic, which is worse than not transforming the field at all.

The same commit that retargeted the fingerprint to CLI 2.1.268 (7a2ac1e, "feat: impersonate CLI 2.1.268 — 3-block system, bare ephemeral cache_control") found the identical pattern in system[]: the 2.1.206-era blocks carried `cache_control: {type: ephemeral, ttl: 1h, scope: global}`, but 2.1.268 sends a bare `{"type":"ephemeral"}` with no `ttl`/`scope` on either cached block (see ADR-004, SPEC-001). This is not a one-off fix; it is the same class of defect recurring in two independent places, which is what motivates stating it as a standing principle here rather than leaving it as two isolated commit messages.
## Decision

`cache_breakpoints` defaults to **false** (`internal/mimicry/config.go` `defaultConfig()`). The tool-level ephemeral cache_control breakpoint on `tools[-1]` (`internal/mimicry/toolrewrite.go` `applyToolsLastCacheBreakpoint`) is now opt-in only.

Generalized principle: never emit a request field the real CLI does not send, even when emitting it would be beneficial to the operator — here, prompt-caching cost savings. Fidelity to the observed wire shape takes precedence over any local optimization that changes that shape. This principle already governs the system[] cache_control qualifiers (ADR-004, SPEC-001): `ttl` and `scope` were dropped from both cached blocks in the 2.1.268 retarget for exactly this reason, making this ADR a codification of an existing, consistently-applied rule rather than a new one-off carve-out.

The `cache_breakpoints` toggle itself is NOT removed. Operators who understand the tradeoff can still set `cache_breakpoints: true` to recover the tool-level cache breakpoint and its token savings, explicitly accepting the fingerprint cost.
## Consequences


### Positive

The default wire shape (system[] cache_control qualifiers and tools[] cache_control presence) now matches the observed CLI 2.1.268 traffic with no positive discriminators. A future field this plugin might be tempted to add for its own benefit is now covered by an explicit standing rule instead of requiring a fresh review each time.
### Negative

Operators running with defaults lose the `tools[-1]` cache breakpoint and its associated prompt-caching token savings unless they explicitly opt in via `cache_breakpoints: true`. Prompt caching is a real, non-trivial cost saving — that is precisely why the toggle survives instead of being deleted outright; the negative consequence is a conscious default-off tradeoff, not a capability loss.