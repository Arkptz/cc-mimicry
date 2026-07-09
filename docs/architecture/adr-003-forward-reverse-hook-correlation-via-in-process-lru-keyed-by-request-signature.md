---
author: arkptz
code_paths:
- internal/mimicry/interceptors.go
- internal/mimicry/helpers.go
date: 2026-07-09
related:
- ADR-002
status: accepted
tags:
- cgo
- plugin
- interceptor
- concurrency
---
# Forward/reverse hook correlation via in-process bounded FIFO keyed by request signature

<!-- Brief introduction: what this document is about -->

## Context
The CLIProxyAPI plugin C ABI exposes three independent hook entry points — request.intercept_before (forward), response.intercept_after and response.intercept_stream_chunk (reverse) — with no shared call-frame or SDK-provided per-request context object. The tool-name rewrite map built during the forward pass must be available in both reverse passes to restore the original (real) tool names before the client sees them.
## Decision
Correlate the forward and reverse hooks via requestSignature — an FNV-64a hash over the request body plus the request-id headers X-Request-Id, X-Stainless-Request-Id, and Anthropic-Client-Request-Id — stored in a package-level bounded store (boundedRewriteStore, capacity 2048) keyed by that signature. The response-side hooks receive the same RequestBody and RequestHeaders fields the SDK provides, so the signature is reproducible on the reverse side without any additional cross-call state.
## Consequences
### Positive
No SDK-side changes are needed; the plugin stays stateless from the host's perspective; the bounded store keeps memory flat.

### Negative
1. Signature collision: two concurrent requests with byte-identical bodies AND no distinguishing request-id headers hash to the same key — one request's rewrite map overwrites the other's, causing incorrect tool-name restoration.
2. Eviction under load: the store holds at most 2048 entries; if more than 2048 requests are in flight simultaneously, the oldest entry is evicted. If a response arrives after its entry was evicted, get() returns nil and the tool names are returned to the caller still obfuscated (the fake alias leaks).
3. It is a slice-based FIFO, not a true LRU with promotion-on-get: a get() does not refresh recency, so a write-heavy burst can evict a still-live entry before its response arrives.
