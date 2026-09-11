---
author: arkptz
code_paths:
- go.mod
- Makefile
- lefthook.yml
date: 2026-07-09
implements:
- ADR-002
owner: arkptz
related:
- ADR-001
review_date: 2027-07-09
status: active
tags: []
---
# Go toolchain is pinned by go directive + Nix + GOTOOLCHAIN=local; no redundant toolchain directive

<!-- Brief introduction: what this document is about -->

## Purpose
The Go 1.26 toolchain is pinned by three mechanisms that actually work: the go.mod `go 1.26.7` directive, the Nix `go_1_26` package in the dev shell/CI, and `GOTOOLCHAIN=local` (which forbids Go from silently downloading a different toolchain). A redundant `toolchain go1.26.7` directive MUST NOT be added: Go treats a toolchain directive equal to the go directive as redundant, strips it on `go mod tidy`, and `go build`/`go test` then refuse to run until it is removed — an unbreakable tidy/build loop. The toolchain directive is only meaningful when it names a version NEWER than the go directive.
## Policy
1. The go.mod `go` directive is the single in-file toolchain version pin. Do NOT add a `toolchain` directive equal to it. 2. When bumping the Go version, update the go.mod `go` directive, the Nix `go_1_XX` package in flake.nix, and keep `GOTOOLCHAIN=local` in the dev shell env — and update ADR-002's toolchain clause. 3. A `toolchain` directive is only added when intentionally pinning a PATCH/newer toolchain above the `go` directive floor.
## Scope
All contributors running `go mod tidy`, `make tidy`, or bumping the Go version.
## Compliance
`GOTOOLCHAIN=local` in the Nix dev shell prevents accidental toolchain drift. CI runs on the Nix-pinned `go_1_26`. The go.mod `go` directive is visible in review. No separate toolchain-directive grep is needed (and would be actively wrong).
## Roles

<!-- Roles and responsibilities -->


## Enforcement

<!-- Consequences of non-compliance -->


## Review History

| Date | Reviewer | Changes |
|---|---|---|