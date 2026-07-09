---
author: arkptz
code_paths:
- internal/mimicry/mimicry.go
- internal/mimicry/interceptors.go
- internal/mimicry/toolrewrite.go
- internal/mimicry/config.go
date: 2026-07-09
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
As a CPA operator, I want the plugin to transform Anthropic /v1/messages requests to match the native Claude Code CLI fingerprint, so that pooled/shared subscription accounts are not flagged as third-party clients — and to transparently reverse the tool-name obfuscation on the way back so clients see their real tool names.
## Scenarios
- Forward pipeline order is: (1) system 3-block rewrite, (2) fingerprint fill, (3) tool-name obfuscation + last-tool cache breakpoint — mirroring applyRequestMimicry.
- System rewrite is skipped (no-op) when the system field already begins with the "You are Claude Code" identity prefix (no double-wrap).
- System rewrite relocates the original system text into a messages[0] user + messages[1] assistant pair so the model still receives the caller's instructions.
- Tool-name obfuscation uses a static prefix map for 5 or fewer mimicable tools, and a dynamic FNV-seeded readable-alias shuffle for more than 5.
- Server tools (type not in {"", "function", "custom"}, e.g. web_search_20250305, computer_20250124) are NEVER renamed — those names are Anthropic protocol semantics.
- context_management is injected only when the effective anthropic-beta header carries context-management-2025-06-27; a client-provided context_management is stripped when that beta token is absent.
- Reverse pass: both response.intercept_after and response.intercept_stream_chunk apply restoreToolNamesInBytes using the LRU-retrieved rewrite map; the stream header-init chunk (ChunkIndex == -1, empty body) is skipped.
- Cache breakpoint: injected on tools[-1].cache_control; a client-provided ttl is preserved; a bare cache_control gets ttl "1h" added; an absent one gets the full {"type":"ephemeral","ttl":"1h"}.
- Source-format filter: only "claude"/"anthropic"/"" source formats are processed; translated OpenAI/Gemini paths pass through unchanged.
- Design note: tool-name aliases are human-readable and intentionally NON-cryptographic — FNV-64a is used only as a stable seed for a deterministic shuffle, not as a security primitive. Switching to a cryptographic hash would break cache-key stability and is explicitly out of scope.
## Acceptance Criteria
- Each toggle (obfuscate_tool_names, inject_system_prompt, cache_breakpoints, fill_fingerprint, normalize_headers) independently disables its stage.
- An omitted config key defaults to true (presence-tracking YAML decode).
- temperature=1 and max_tokens=128000 are filled only when absent.