---
author: arkptz
code_paths:
- internal/mimicry/interceptors.go
- internal/mimicry/normalizers.go
- internal/mimicry/toolrewrite.go
date: 2026-09-23
related:
- SPEC-001
status: accepted
supersedes: ADR-003
tags:
- plugin
- interceptor
- tool-names
---

# Rewrite-map correlation breaks on stream chunks without a request body

The plugin renames client tool names on the way up and must restore them before
the client sees the response. This record covers how the forward and reverse
hooks find the same per-request rewrite map, and how translated routes, which
have no shared per-request state, restore names.

## Context

ADR-003 keyed the rewrite map by a hash of the request body plus a few request-id
headers, relying on the response hooks receiving the same `RequestBody`. That no
longer holds for streams: from plugin ABI schema v3 the host sends the request
body only with the header-init call, and every payload chunk of
`response.intercept_stream_chunk` arrives without it
(`sdk/api/handlers/handlers_stream.go` in the pinned CPA fork). The lookup missed
on every stream chunk, so a streaming client with more than five tools received
the dynamic aliases (for example `update_alp00`) instead of its own tool names;
this was confirmed live on 2026-09-23 while the non-stream call on the same body
restored correctly.

A second gap: Anthropic answers HTTP 400 "Third-party apps now draw from your
extra usage" to any request with a tool name matching `^mcp_[a-z0-9]`. With CPA's
cloak disabled (required, see the README), CPA no longer aliases tool names, and
`/v1/chat/completions` and `/v1/responses` never reach the plugin's request
interceptor because it is gated on the Anthropic source format.

## Decision

1. Key the rewrite map by the host's lifecycle `RequestID`, which the host passes
   unchanged to `request.intercept_before`, `response.intercept_after` and every
   `response.intercept_stream_chunk` call. The body signature is kept only as the
   fallback for a host that sends no `RequestID`.
2. Add a static rule `mcp_` → `cc_mcp_`, applied when the next byte is a
   lowercase letter or digit, at any tool count. The real CLI's
   `mcp__server__tool` names are left alone.
3. Cover translated routes with `request.normalize` (applies only the static
   rules, and only when the client format is not Claude and the provider format
   is) and `response.normalize_before` (restores them on each Claude response
   line before CPA translates it back). `request.normalize` carries no request
   id, so dynamic aliases, which need a per-request map, stay on the native path.
4. On translated routes the reverse rewrites only `tool_use` name fields, and
   skips a name that appears in the client's original request, so response text,
   tool input and a client tool genuinely named `cc_mcp_*` are not altered.

## Consequences

### Positive

Streaming responses restore dynamic aliases again. An `mcp_*` tool name is
renamed on native `/v1/messages`, on `/v1/messages/count_tokens` and on the
translated routes, with no host change.

### Negative

1. A host without a `RequestID` still has the ADR-003 failure modes (signature
   collision, and stream chunks that miss the map).
2. The store remains a bounded FIFO of 2048 entries; a response that arrives
   after its entry was evicted returns the aliases to the client. With a
   `RequestID` a missing entry is treated as "nothing was renamed" and the
   response passes through, because the static-prefix fallback would also
   un-alias a client tool that is really named like an alias. The native
   reverse replaces an alias only where it is a whole identifier, so a longer
   client name that starts with an alias is kept.
3. `response.normalize_before` runs once per SSE line and the host attaches both
   request bodies to every call, so its cost grows with request size. The plugin
   reads the envelope with gjson and decodes the request body only for a line
   that contains a `tool_use` alias.
4. Models served from a `claude-api-key` entry with `is-compat: true` are
   translated outside CPA's translator registry, so `request.normalize` never
   runs for them and their `mcp_*` names still go upstream unchanged.
