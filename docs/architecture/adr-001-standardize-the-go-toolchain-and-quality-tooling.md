---
author: arkptz
code_paths:
- go.mod
- .golangci.yml
- Makefile
- lefthook.yml
- flake.nix
date: 2026-07-08
status: accepted
superseded_by: ADR-002
tags:
- go
- toolchain
---
# Standardize the Go toolchain and quality tooling

Which Go toolchain and quality tools this project standardizes on.

## Context

Toolchain drift (different Go versions across contributors and CI) causes "works on
my machine" build failures and inconsistent vet/lint results. This ADR documents the
12-dimension toolchain checklist from POL-013 as applied to Go.

## Decision

(a) **Language**: Go 1.24.13. The `go.mod` `go 1.24` directive sets the language-version
    floor; `toolchain go1.24.13` (Go 1.21+) pins the exact toolchain. The patch-level
    `toolchain` line is preserved by `go mod tidy` precisely because it is newer than the
    `go` floor — a redundant `go 1.24.13` + `toolchain go1.24.13` pair would be normalised
    down to a bare `go 1.24.13`, silently dropping the explicit toolchain pin.

(b) **Package manager**: `go mod` (built-in). No alternative module proxy required.
    `go.sum` is committed and non-empty (seeded with `github.com/stretchr/testify v1.9.0`).

(c) **Formatter**: `gofumpt` (strict superset of `gofmt`). Enforced on commit via
    lefthook `fmt-check` command; CI `fmt` job validates.

(d) **Linter**: `golangci-lint` v2 with `default: standard` plus `gosec`, `gocritic`,
    `errorlint`, `modernize`, `revive`. Config in `.golangci.yml`. Zero-warning policy.

(e) **Type system**: Go's built-in static type checker (no additional tools needed).

(f) **Lockfile**: `go.sum` (content-addressed hash file). Seeded with
    `github.com/stretchr/testify v1.9.0` as the sole test dependency.

(g) **Test runner**: `go test -race` (built-in race detector). `gotestsum` available in
    the dev shell for better output formatting.

(h) **Build tool**: `go build` (built-in). `Makefile` provides `build`, `test`, `lint`,
    `tidy`, `vuln` targets as shortcuts.

(i) **README-gen**: **No cargo-rdme analog exists for Go**. Go module documentation is
    generated via `godoc` / pkg.go.dev from package-level `//` comments, but there is no
    standard tool that auto-syncs `//!` docs into README.md as `cargo-rdme` does for Rust.
    The README is maintained manually.

(j) **Renovate datasource**: `gomod` (auto-detects `go.mod` dependencies). Configured in
    `renovate.json5` with minor+patch grouping.

(k) **CI actions**: All GitHub Actions SHA-pinned to 40-char commit SHAs with `# vX.Y.Z`
    comments per POL-013(k). See `.github/workflows/ci.yml` and `docker-build.yml`.

(l) **Version pinning**: Three-layer strategy:
    - `go.mod` `go 1.24` floor + `toolchain go1.24.13` — pins the exact Go toolchain version
    - Nix `pkgs.go_1_24` on `nixos-25.11` — pins the dev shell toolchain
    - `GOTOOLCHAIN=local` env var — ensures the Nix-provided Go is used, not auto-downloaded

## Consequences

### Positive

- One pinned toolchain; identical lint/format results locally and in CI.
- `gofumpt` catches gofmt-incompatible formatting that `gofmt` misses.
- `govulncheck` scans the dependency graph for known CVEs on every CI run.
- Nix dev shell means zero manual toolchain installation for contributors.

### Negative

- Bumping the pinned toolchain requires updating `go.mod`, `flake.nix`, and this ADR.
- `golangci-lint` v2 configuration differs from v1; migrating existing linter configs
  requires re-reading the v2 docs.