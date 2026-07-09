---
author: arkptz
code_paths:
- Dockerfile
- Makefile
- .github/workflows/docker-build.yml
- .github/workflows/ci.yml
- .cpa-version
- .cpa-commit
date: 2026-07-09
status: accepted
tags:
- cgo
- plugin
- ghcr
- docker
- ci
---
# CGO c-shared plugin build and GHCR artifact image

<!-- Brief introduction: what this document is about -->

## Context
cc-mimicry is a CLIProxyAPI plugin compiled as a CGO c-shared shared object (.so). The previous build required a multi-repo Docker context (cc-mimicry + CLIProxyAPI side by side) plus a builder-service and named-volume hack in the dev compose stack — fragile, non-reproducible, and it blocked independent plugin CI. The plugin also requires Go 1.26.4 and CGO, which conflicts with the Go 1.24.13 toolchain standardized in ADR-001.
## Decision
1. Self-contained Dockerfile: the build stage clones CLIProxyAPI at the pinned SDK version (.cpa-version tag) inside the image, verifies the commit SHA against .cpa-commit (CWE-494 mitigation for a mutable tag), rewrites the go.mod replace directive, and builds the .so. No external build-context dependency.

2. GHCR artifact image: the final stage is a distroless carrier (gcr.io/distroless/static-debian12:nonroot) carrying only the compiled .so at /plugin/cc-mimicry.so, published to ghcr.io/arkptz/cc-mimicry. Consumers pin a version tag (COPY --from=ghcr.io/arkptz/cc-mimicry:vX.Y.Z ...), never :latest, because the plugin is ABI-locked to a specific SDK version.

3. SDK version pin: .cpa-version (tag) + .cpa-commit (SHA) are the dual source of truth, read by Makefile, CI, and Dockerfile. Bumping the SDK changes only those two files.

4. Go 1.26 toolchain pin: the go.mod `go 1.26.4` directive + Nix `go_1_26` in the dev shell/CI + `GOTOOLCHAIN=local` (forbids silent toolchain downloads). A redundant `toolchain go1.26.4` directive is deliberately NOT used — Go strips a toolchain directive equal to the go directive on `go mod tidy` and then refuses to build until it is gone (see POL-002). This supersedes ADR-001's Go 1.24.13 standardization.
## Consequences
### Positive
Plugin repo is fully self-contained; any CPA Dockerfile can COPY --from the GHCR image with no builder service; the SDK version is centralized and SHA-verified.

### Negative
CGO requires gcc in CI (ubuntu-latest provides it). The .so is glibc-coupled: both plugin and host must use debian bookworm. Consumers pulling :latest onto a mismatched CPA host get a rejected plugin — mitigated by README guidance to pin version tags.