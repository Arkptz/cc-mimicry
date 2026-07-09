---
author: arkptz
code_paths:
- internal/mimicry/toolrewrite.go
- internal/mimicry/mimicry.go
- internal/mimicry/interceptors.go
- Dockerfile
- .cpa-version
- .cpa-commit
date: 2026-07-09
implements:
- ADR-002
owner: arkptz
related:
- SPEC-001
review_date: 2027-07-09
status: active
tags: []
---
# Correctness invariants for Anthropic protocol fields: server-tool names, context_management gate, and ABI version pinning

<!-- Brief introduction: what this document is about -->

## Purpose
Three correctness invariants are easy to accidentally break during feature work; this policy makes them explicit so they can be checked in review.
## Policy
1. Server-tool names MUST NOT be renamed. Tools whose type is outside {"", "function", "custom"} (e.g. web_search_20250305, computer_20250124, bash_20250124) are Anthropic protocol semantics; renaming them makes the upstream reject the request. The shouldMimicToolName guard implements this and any change to it must preserve this invariant.
2. The context_management field MUST be gated on the beta token. Injecting or forwarding context_management when the request's anthropic-beta header does not contain context-management-2025-06-27 makes Anthropic return HTTP 400 "Extra inputs are not permitted". The stripContextManagementIfUnsupported call in interceptRequestBefore enforces this and must not be removed or bypassed.
3. The plugin image tag MUST be pinned to a specific version; :latest MUST NOT be used in production. The .so is ABI-locked to the CLIProxyAPI SDK version in .cpa-version / .cpa-commit; a tag mismatch makes the host reject or silently misbehave on load. The OCI labels cc-mimicry.cpa-sdk-version / cc-mimicry.cpa-sdk-commit on each image enable verification.
## Scope
All contributors modifying internal/mimicry/, and all operators writing a CPA Dockerfile or config.yaml that references this plugin.
## Compliance
Invariants 1 and 2 are exercised by `make test` (-race). Invariant 3 is a deployment-time review gate with no automated check yet; a future CI lint grepping for :latest in consuming Dockerfiles is recommended. PR review must verify all three.
## Roles

<!-- Roles and responsibilities -->


## Enforcement

<!-- Consequences of non-compliance -->


## Review History

| Date | Reviewer | Changes |
|---|---|---|