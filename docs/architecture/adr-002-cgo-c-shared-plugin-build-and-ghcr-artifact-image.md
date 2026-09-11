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
cc-mimicry is a CLIProxyAPI plugin compiled as a CGO c-shared shared object (.so). The previous build required a multi-repo Docker context (cc-mimicry + CLIProxyAPI side by side) plus a builder-service and named-volume hack in the dev compose stack — fragile, non-reproducible, and it blocked independent plugin CI. The plugin also requires Go 1.26.7 and CGO, which conflicts with the Go 1.24.13 toolchain standardized in ADR-001. (The go.mod `go` directive started at 1.26.4 and was bumped to 1.26.7 via `nix flake update nixpkgs`, clearing GO-2026-5972 — unbounded recursion in `encoding/asn1`, fixed upstream in 1.26.6; POL-002 requires this paragraph be updated on every such bump.)
## Decision
1. Self-contained Dockerfile: the build stage clones CLIProxyAPI at the pinned SDK version (.cpa-version tag) inside the image, verifies the commit SHA against .cpa-commit (CWE-494 mitigation for a mutable tag), rewrites the go.mod replace directive, and builds the .so. No external build-context dependency. The SDK is cloned from the FORK `https://github.com/Arkptz/CLIProxyAPI.git`, not upstream `router-for-me/CLIProxyAPI`: the plugin-ABI commits this build needs (P4 EgressHeaderInterceptor, see ADR-004) exist only in the fork, and the pinned tag (`.cpa-version`, currently `v7.2.157-plugin3`) does not resolve on upstream at all. This was the root cause of CI being red on every push from 2026-07-10 to 2026-09-11, fixed by commit d1a7512 ("resolve the release SDK gate against the fork, not upstream"). Because a fresh `COPY . .` restores the repo's own go.mod — whose replace path (`../forks/CLIProxyAPI`) does not exist inside the image — the Dockerfile re-applies `go mod edit -replace` a second time AFTER the `COPY . .` step, pointing it back at the verified in-image clone; the pre-COPY `go mod edit -replace` only exists to let `go mod download` run against the correct module before the full context lands. Locally, a fresh clone needs `scripts/setup-sdk.sh` to place the SDK at the go.mod replace target before `./build.sh`/`make build` can succeed.

2. GHCR artifact image: the final stage is a distroless carrier (gcr.io/distroless/static-debian12:nonroot) carrying only the compiled .so at /plugin/cc-mimicry.so, published to ghcr.io/arkptz/cc-mimicry. Consumers pin a version tag (COPY --from=ghcr.io/arkptz/cc-mimicry:vX.Y.Z ...), never :latest, because the plugin is ABI-locked to a specific SDK version.

3. SDK version pin: .cpa-version (tag) + .cpa-commit (SHA) are the dual source of truth, read by Makefile, CI, and Dockerfile. Bumping the SDK changes only those two files.

4. Go 1.26 toolchain pin: the go.mod `go 1.26.7` directive + Nix `go_1_26` in the dev shell/CI + `GOTOOLCHAIN=local` (forbids silent toolchain downloads). A redundant `toolchain go1.26.7` directive is deliberately NOT used — Go strips a toolchain directive equal to the go directive on `go mod tidy` and then refuses to build until it is gone (see POL-002). This supersedes ADR-001's Go 1.24.13 standardization.
## Consequences
### Positive
Plugin repo is fully self-contained; any CPA Dockerfile can COPY --from the GHCR image with no builder service; the SDK version is centralized and SHA-verified.

### Negative
CGO requires gcc in CI (ubuntu-latest provides it). The .so is glibc-coupled: both plugin and host must use debian bookworm. Consumers pulling :latest onto a mismatched CPA host get a rejected plugin — mitigated by README guidance to pin version tags.