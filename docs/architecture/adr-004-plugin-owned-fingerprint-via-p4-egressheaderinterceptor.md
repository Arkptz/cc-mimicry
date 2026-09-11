---
author: arkptz
code_paths:
- internal/mimicry/surface.go
- internal/mimicry/egress_headers.go
- internal/mimicry/interceptors.go
- internal/mimicry/mimicry.go
date: 2026-07-10
status: accepted
tags:
- fingerprint
- plugin-abi
- p4-hook
---
# Plugin-owned fingerprint via P4 EgressHeaderInterceptor

<!-- Brief introduction: what this document is about -->

## Context
CPA `applyClaudeHeaders` builds outbound headers from gin inbound + device-profile constants, overwriting plugin interceptor header edits. `signAnthropicMessagesBody` signs `cch` after cloaking, independent of `ShouldCloak`. For relay traffic (claude-cli inbound, `cloakMode=auto`), `applyCloaking` skips entirely. Pure-plugin ownership (zero CPA change) was evaluated: headers only ownable via auth-attrs (clauberus scope, rejected) or P4 hook. ProviderExecutor plugin (P3) rejected: requires reimplementing auth/signing/streaming. Cross-model review (gpt-5.5 + opus-4.8) confirmed P4 as the minimal correct architecture.
## Decision
Add `EgressHeaderInterceptor` to CPA plugin ABI. Post-`applyClaudeHeaders` hook, pre-`RecordAPIRequest`, at `Execute`/`ExecuteStream`/`CountTokens` send sites. Headers-only (no body mutation, `cch` preserved). JSON-serializable contract mirroring `RequestInterceptor`. Plugin owns body `system[]` (3-block surface-aware with billing prefix + `cch=00000` placeholder) via interceptor + headers via P4 hook. CPA owns `cch` (xxHash64, untouched). Two surfaces: `cli` (default, 11 betas) and `sdk-cli` (10 betas). Surface config in plugin drives both atomically.
## Consequences


### Positive
Plugin owns complete fingerprint coherently. Evidence-backed: `testdata/captures/v2.1.268/{cli,sdk-cli}-body.json`, captured live through a local mitmproxy reverse proxy (`scripts/recon/capture-live.sh`), cover both surfaces (supersedes the earlier 2.1.206 evidence base). Nightly CI both surfaces. CPA skip-guard prevents double billing injection. Atomic surface tuple prevents half-mix.
### NegativeCPA fork requires one ABI addition (Track A). Signing gate requires upstream OAuth token as deployment invariant. `count_tokens` does not flow through CPA (theoretical, guarded defensively). `buildhash` algorithm drifted (CI-stripped, documented residual).