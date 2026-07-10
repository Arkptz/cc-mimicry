---
author: arkptz
code_paths:
- internal/mimicry/mimicry.go
- internal/mimicry/interceptors.go
- internal/mimicry/egress_headers.go
- internal/mimicry/toolrewrite.go
- internal/mimicry/config.go
date: 2026-07-10
depends_on:
- ADR-002
implemented_by:
- ADR-003
priority: must
status: approved
tags: []
---
# Request/response transform behavior contract for /v1/messages mimicry

<!-- Brief introduction: what this document is about -->

## Story
As a CPA operator, I want the plugin to transform Anthropic /v1/messages requests to match the native Claude Code CLI fingerprint — rewriting system[] to a 4-block layout, signing the body with CPA's xxHash64 cch field, and injecting surface-correct egress headers — so that pooled/shared subscription accounts are not flagged as third-party clients. The reverse pass transparently restores obfuscated tool names so clients see their real names.
## Scenarios
- Forward pipeline order is: (1) system 4-block rewrite via plugin interceptor, (2) fingerprint fill, (3) tool-name obfuscation + last-tool cache breakpoint — mirroring applyRequestMimicry.
- System rewrite produces exactly 4 blocks in system[]: [0] billing block with `x-anthropic-billing-header:` prefix and `cch=00000` placeholder; [1] surface-specific agent identifier block; [2] shared intro+security+System+DoingTasks+Tone content (~10676 chars) with `cache_control: {type: ephemeral, ttl: 1h, scope: global}`; [3] surface-specific `# Text output` block with `cache_control: {type: ephemeral, ttl: 1h}`.
- System rewrite is owned by the plugin interceptor (body transform); header injection is owned by the new EgressHeaderInterceptor (P4 hook). Both are driven by the surface config in the plugin. Surface coherence is atomic under stable config; a reconfigure during in-flight requests may cause a one-request divergence (body surface A with header surface B). Per-request surface pinning (R9) is not implemented — Metadata correlation is not populated by CPA.
- System rewrite is skipped (no-op) when the system field already begins with the "You are Claude Code" identity prefix (no double-wrap).
- System rewrite relocates the original system text into a messages[0] user + messages[1] assistant pair so the model still receives the caller's instructions.
- Two surfaces are supported: `cli` (default — 11 beta tokens including redact-thinking-2026-02-12) and `sdk-cli` (10 beta tokens, omits redact-thinking). Surface config in plugin drives both body blocks and egress headers. The Anthropic-Beta header is wholesale-replaced with the surface's exact token set; client-requested betas outside the set are intentionally dropped for fingerprint fidelity.
- CPA xxHash64 signing (cch field): CPA's signer runs independently of ShouldCloak, over the final request body after the plugin has written the cch=00000 placeholder in block [0]. The skip-guard at CPA:1865 defers to the plugin billing prefix so no double-injection occurs.
- Dead code removed: claudeCodeHeaderOverrides, mergeClaudeCodeBeta, headerValue, and NormalizeHeaders are no longer part of the pipeline; the normalize_headers config toggle is obsolete.
- Tool-name obfuscation uses a static prefix map for 5 or fewer mimicable tools, and a dynamic FNV-seeded readable-alias shuffle for more than 5.
- Server tools (type not in {"", "function", "custom"}, e.g. web_search_20250305, computer_20250124) are NEVER renamed — those names are Anthropic protocol semantics.
- context_management is injected only when the effective anthropic-beta header carries context-management-2025-06-27; a client-provided context_management is stripped when that beta token is absent.
- Reverse pass: both response.intercept_after and response.intercept_stream_chunk apply restoreToolNamesInBytes using the LRU-retrieved rewrite map; the stream header-init chunk (ChunkIndex == -1, empty body) is skipped.
- Cache breakpoint: injected on tools[-1].cache_control; a client-provided ttl is preserved; a bare cache_control gets ttl "1h" added; an absent one gets the full {"type":"ephemeral","ttl":"1h"}.
- Source-format filter: only "claude"/"anthropic"/"" source formats are processed; translated OpenAI/Gemini paths pass through unchanged.
- Design note: tool-name aliases are human-readable and intentionally NON-cryptographic — FNV-64a is used only as a stable seed for a deterministic shuffle, not as a security primitive. Switching to a cryptographic hash would break cache-key stability and is explicitly out of scope.
## Acceptance Criteria
- Each toggle (obfuscate_tool_names, inject_system_prompt, cache_breakpoints, fill_fingerprint) independently disables its stage; normalize_headers is no longer a valid toggle (dead code removed).
- An omitted config key defaults to true (presence-tracking YAML decode).
- temperature=1 and max_tokens=128000 are filled only when absent.
- system[] after rewrite has exactly 4 blocks in the order: billing (cch=00000 placeholder), agent-id, shared-intro (cache scope=global ttl=1h), text-output (cache ttl=1h).
- EgressHeaderInterceptor injects the correct anthropic-beta token list for the active surface: 11 tokens for cli, 10 tokens for sdk-cli.
- CPA signer detects the billing prefix from block [0] and does not inject a second cch field.
- Surfaces cli and sdk-cli are the only recognized surface values; any unknown surface MUST cause an error at config load time.