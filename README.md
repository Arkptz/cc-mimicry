<div align="center">

# cc-mimicry

**A CLIProxyAPI plugin that makes Anthropic OAuth traffic indistinguishable from
the native Claude Code CLI — so pooled subscriptions are not flagged as
third-party clients.**

Currently impersonating Claude Code CLI <!-- cc-target-version:start -->**2.1.278**<!-- cc-target-version:end -->,
kept current by a nightly drift check against the real binary.

[![CI](https://github.com/Arkptz/cc-mimicry/actions/workflows/ci.yml/badge.svg)](https://github.com/Arkptz/cc-mimicry/actions/workflows/ci.yml)
[![Nightly fingerprint drift](https://github.com/Arkptz/cc-mimicry/actions/workflows/fingerprint-nightly.yml/badge.svg)](https://github.com/Arkptz/cc-mimicry/actions/workflows/fingerprint-nightly.yml)
[![Release](https://img.shields.io/github/v/release/Arkptz/cc-mimicry?sort=semver)](https://github.com/Arkptz/cc-mimicry/releases)
[![GHCR](https://img.shields.io/badge/ghcr.io-cc--mimicry-2496ed)](https://github.com/Arkptz/cc-mimicry/pkgs/container/cc-mimicry)
[![Go](https://img.shields.io/github/go-mod/go-version/Arkptz/cc-mimicry)](go.mod)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

</div>

## Why

[CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) (CPA) is a
high-throughput multi-account gateway, but its outbound Anthropic requests do
not look like the Claude Code CLI: different system prompt, different tool
names, different headers. Every one of those is a discriminator.

cc-mimicry ports the request transforms from sub2api's `gateway_tool_rewrite.go`
/ `cc_mimicry` (originally the [Parrot](https://github.com/danger-dream/Parrot)
/ [cc-proxy](https://github.com/danger-dream/cc-proxy) lineage) into a native
CPA plugin — no sidecar and no rewriting proxy in front of the gateway.

> [!IMPORTANT]
> **The host must be the [`Arkptz/CLIProxyAPI`](https://github.com/Arkptz/CLIProxyAPI)
> fork, not upstream.** Upstream's plugin ABI can rewrite the request body but not
> the final outbound headers: `applyClaudeHeaders` overwrites whatever an
> interceptor set. The fork adds the `EgressHeaderInterceptor` hook (P4) that runs
> post-auth and pre-send, which is what lets the plugin own `user-agent`,
> `anthropic-beta` and the `x-stainless-*` set. Without it the body is disguised
> and the headers are not — a worse fingerprint than doing nothing. See
> [ADR-004](docs/architecture/adr-004-plugin-owned-fingerprint-via-p4-egressheaderinterceptor.md).

Fidelity is not hand-maintained. The nightly workflow downloads the current
Claude Code release, captures both entrypoints through mitmproxy against a local
upstream, diffs the wire shape against the committed captures, retargets the
plugin constants and the embedded system prompt, and opens a PR — so a CLI
release does not silently break the disguise.

## Quick start

The `cpa-mimicry` image is the whole stack: the CLIProxyAPI fork built with
`CGO_ENABLED=1` and `cc-mimicry.so` already baked in at
`/CLIProxyAPI/plugins/linux/amd64/`. Host and plugin are compiled from the same
SDK tree in one build, so they cannot drift apart.

<!-- x-release-please-start-version -->

```bash
docker run --rm -p 8317:8317 \
  -v "$PWD/config.yaml:/CLIProxyAPI/config.yaml:ro" \
  ghcr.io/arkptz/cpa-mimicry:0.3.0
```

<!-- x-release-please-end -->

Enable the plugin in that `config.yaml`:

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    cc-mimicry:
      enabled: true
      priority: 100
      surface: cli          # "cli" (default) or "sdk-cli"
```

`docker-compose.yml` builds and runs the same image locally from source.

### Bringing your own host

If you already build the CLIProxyAPI fork yourself, take the plugin alone from
the artifact image:

<!-- x-release-please-start-version -->

```dockerfile
COPY --from=ghcr.io/arkptz/cc-mimicry:0.3.0 /plugin/cc-mimicry.so /plugins/linux/amd64/cc-mimicry.so
```

<!-- x-release-please-end -->

That host must be built with `CGO_ENABLED=1` — the plugin is a native
`c-shared` object, and a non-CGO binary silently ignores every `.so` plugin.

> [!WARNING]
> **ABI lock — always pin a version tag, never `:latest`.** The `.so` is compiled
> against a specific CLIProxyAPI SDK version (see `.cpa-version` / `.cpa-commit`).
> The host loading it must be built against the **same** SDK version, or the
> plugin is rejected / silently misbehaves at load time. The image carries
> `cc-mimicry.cpa-sdk-version` / `cc-mimicry.cpa-sdk-commit` OCI labels so you can
> verify what a given tag was built against.

## What it does

Applied to Anthropic `/v1/messages` requests (source format `claude`/`anthropic`):

1. **Tool-name obfuscation** — renames `tools[*].name`, `tool_choice.name` and
   historical `tool_use.name` to Claude-Code-like aliases, then reverses them on
   the response and on every stream chunk.
   - *Static prefix map* (`sessions_` → `sessions_`, `session_` → `session_`).
   - *Dynamic map* when a request declares more than 5 renameable tools: a
     stable-per-toolset FNV-seeded shuffle picks a readable prefix and builds
     `<prefix><name[:3]><NN>`. Not md5 — the aliases stay human-readable.
   - *Server tools* (`web_search_20250305`, `computer_20250124`, …) are never
     renamed, since those names are Anthropic protocol semantics.
2. **System prompt 3-block rewrite** — rebuilds `system` into the CLI shape:
   `[0]` billing attribution (`cch=00000` placeholder for CPA signing),
   `[1]` surface-specific agent identity (cache ephemeral),
   `[2]` shared intro/security/tone bundle, which since 2.1.268 also carries the
   `# Text output` section that used to be a fourth block (cache ephemeral).
   The original system prompt is relocated into a `user`/`assistant` message pair
   so the model still receives the caller's instructions. Skipped if `system`
   already looks like Claude Code.
3. **Cache breakpoint** — injects `cache_control: {type: ephemeral, ttl: 1h}` on
   the last tool (client-provided `ttl` is preserved).
4. **Fingerprint fill** — sets `temperature`, `max_tokens`, and
   `context_management` (only when the `anthropic-beta` header enables it) to
   match the real CLI payload when absent.
5. **Egress header override** (P4 hook) — overrides `user-agent`, `x-app`,
   `anthropic-beta`, `anthropic-version`, `x-stainless-*`, and
   `anthropic-dangerous-direct-browser-access` to the targeted CLI's values via the
   CPA EgressHeaderInterceptor ABI hook (post-auth, pre-send). Strips
   CPA-injected headers absent from real CLI captures.

Each transform is individually toggleable (see [Config](#config)).

## Config

| Key | Default | Effect |
|-----|---------|--------|
| `obfuscate_tool_names` | `true` | Rename tools + reverse on responses/stream chunks. |
| `inject_system_prompt` | `true` | 3-block surface-aware system rewrite; relocate original system into messages. |
| `cache_breakpoints` | `false` | Ephemeral `cache_control` on the last tool. OFF by default: the real CLI sends none, so emitting one is a positive fingerprint discriminator. |
| `fill_fingerprint` | `true` | Fill `temperature`/`max_tokens`/`context_management`. |
| `surface` | `cli` | CLI entrypoint to impersonate: `cli` (interactive TUI, 11 betas) or `sdk-cli` (-p print, 10 betas). Unknown values are rejected at config load. |

An omitted key keeps its default (`true`); set to `false` to disable a stage.

## Building from source

Requirements: Go 1.26+, a C compiler (gcc/clang) for CGO, and the pinned
CLIProxyAPI SDK checked out at the `go.mod` `replace` target — which sits
OUTSIDE the repository, so a fresh clone cannot build until it is there:

```bash
scripts/setup-sdk.sh   # clones the fork at .cpa-version and verifies .cpa-commit
nix develop            # dev shell (Go 1.26 + CGO); optional but matches CI
./build.sh             # -> dist/cc-mimicry.so
make build             # same, via Makefile
VERSION=$(git describe --tags) ./build.sh   # stamp a version
```

The SDK comes from the `Arkptz/CLIProxyAPI` fork, not upstream: the plugin ABI
commits the build needs exist only there. A gitignored `go.work` can override
the path for a different local layout. The plugin ID is the filename without
extension (`cc-mimicry`).

## Manual install

Drop `cc-mimicry.so` into the host's plugin directory. CPA scans, in order:

```text
<plugins-dir>/<goos>/<goarch>-<variant>/   e.g. plugins/linux/amd64-v3/
<plugins-dir>/<goos>/<goarch>/             e.g. plugins/linux/amd64/
<plugins-dir>/                             (flat fallback)
```

## How it stays current

<details>
<summary>Nightly drift check, automatic retarget, and the SDK version pin</summary>

`.github/workflows/fingerprint-nightly.yml` runs every night: it resolves the
latest Claude Code release, fingerprints the binary against `baseline/`, captures
the `cli` and `sdk-cli` surfaces through mitmproxy, stages any new version under
`testdata/captures/v<version>/`, runs `scripts/nightly/retarget.py` to rewrite
`cliTargetVersion`, `cliVersion`, `stainlessPackageVersion` and the embedded
`shared_intro.txt`, runs the test suite, and opens a PR. Beta sets and agent
identifiers are reported but never auto-changed — those are judgment calls.

The CLIProxyAPI SDK version is centralized in two files:

- `.cpa-version` — the git tag (currently `v7.2.157-plugin3`)
- `.cpa-commit` — the pinned commit SHA, verified at Docker/CI clone time so a
  moved upstream tag cannot silently swap the SDK baked into the `.so`

To bump: update both files. The Dockerfile, CI, and Makefile all derive from them.

Releases are automatic (`.github/workflows/release-please.yml`): conventional
commits (`feat:` → minor, `fix:` → patch) accumulate in a Release PR that
release-please keeps up to date. Merge it, and the `vX.Y.Z` tag, the GitHub
Release with changelog notes, and the GHCR image follow — nothing to run by hand.

</details>

<details>
<summary>How the reverse pass is correlated</summary>

The C ABI has no shared per-request context across interceptor calls, so the
plugin correlates the forward `request.intercept_before` hook (which builds the
tool-name rewrite map) with the `response.intercept_after` /
`response.intercept_stream_chunk` hooks via a stable signature over the request
body (plus a couple of request-id headers). The map lives in a bounded in-process
FIFO; entries for requests that never reach the response side age out.

</details>

## Development

```sh
make build           # build the .so
make test            # go test -race
make lint            # golangci-lint (zero-warning)
gofumpt -l .         # format check
make vuln            # govulncheck
./scripts/ci-local.sh   # replay CI jobs locally via act
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for the full local setup and quality
gates, and [AGENTS.md](AGENTS.md) for the project conventions.

## Decisions

This project tracks decisions with `dg` under `docs/`. See
[ADR-002](docs/architecture/adr-002-cgo-c-shared-plugin-build-and-ghcr-artifact-image.md)
for the CGO c-shared build + GHCR artifact-image design, and
[ADR-005](docs/architecture/adr-005-emitting-fields-the-real-cli-omits-defeats-the-fingerprint.md)
for why the plugin never emits a field the real CLI omits.

## Security

Report vulnerabilities privately — see [SECURITY.md](SECURITY.md). Participation
is governed by the [Code of Conduct](CODE_OF_CONDUCT.md).

## License

[MIT](LICENSE)
