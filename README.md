<div align="center">

# cc-mimicry

**A CLIProxyAPI plugin that makes Anthropic OAuth traffic indistinguishable from
the native Claude Code CLI — so pooled subscriptions are not flagged as
third-party clients.**

Currently impersonating Claude Code CLI <!-- cc-target-version:start -->**2.1.282**<!-- cc-target-version:end -->,
kept current by a nightly drift check against the real binary.

[![CI](https://github.com/Arkptz/cc-mimicry/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/Arkptz/cc-mimicry/actions/workflows/ci.yml?query=branch%3Amain)
[![Nightly fingerprint drift](https://github.com/Arkptz/cc-mimicry/actions/workflows/fingerprint-nightly.yml/badge.svg?branch=main)](https://github.com/Arkptz/cc-mimicry/actions/workflows/fingerprint-nightly.yml?query=branch%3Amain)
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
  ghcr.io/arkptz/cpa-mimicry:0.5.0
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

# Required: CPA's own Claude Code disguise runs after the plugin and rewrites
# system[] with its own pinned cc_version, undoing the mimicry. See below.
disable-claude-cloak-mode: true
```

`docker-compose.yml` builds and runs the same image locally from source.

### Turn CPA's own cloaking off

`disable-claude-cloak-mode: true` is the setting that matters. CPA ships a
Claude Code disguise of its own (`applyCloaking`) that rewrites `system[]` for
any request it does not recognise as native Claude Code. It runs *after* the
plugin's request interceptor and overwrites the billing block with CPA's
`DefaultClaudeVersion`, which is pinned to an older CLI — so the plugin does the
work and the host throws it away. The symptom is an HTTP 400 from Anthropic,
`Claude Code <old version> does not support this model; version <newer> or newer
is required`, while the outbound `User-Agent` already shows the version this
plugin targets: the headers come from the plugin, the body from the cloak.

With the cloak off the plugin owns every `/v1/messages` request end to end.
A `cloak_mode: always` on an auth or API key turns the cloak back on for that
credential, so leave it unset.

Turning the cloak off also turns off CPA's MCP tool-name aliasing, which only
runs inside the cloak. The plugin takes over that job: Anthropic rejects any
request whose tool names match `^mcp_[a-z0-9]` with a 400 "Third-party apps now
draw from your extra usage", so the plugin renames such tools to `cc_mcp_…` on
the way up and restores the original names in the response. This covers
`/v1/messages`, `/v1/messages/count_tokens` and the translated routes below,
except models served from a `claude-api-key` entry with `is-compat: true`: CPA
translates those requests outside the translator registry, so the plugin never
sees them.

#### If you also serve `/v1/chat/completions` or `/v1/responses`

The plugin's request interceptor is gated on the `claude`/`anthropic` source
format, so OpenAI-format requests never reach it and go upstream without the
Claude Code system block. They still get the plugin's tool-name rewrite, which
runs as a request normalizer after CPA translates them into the Claude format,
and the plugin's egress headers, which apply to every request the Claude
executor sends. What they do not get from the plugin is the `cc_version` in the
billing block: CPA derives it from the `user-agent` in `claude-header-defaults`
and falls back to an older pinned CLI version without it. If you route that
traffic to Anthropic, point the defaults at the version the plugin targets,
otherwise skip this block entirely:

```yaml
claude-header-defaults:
  user-agent: "claude-cli/2.1.282 (external, cli)"
  package-version: "0.112.1"
  runtime-version: "v26.3.0"
  os: "Linux"
  arch: "x64"
```

The nightly retarget rewrites `user-agent` and `package-version` there along
with the plugin's own constants. `runtime-version`, `os` and `arch` are not
retargeted; the plugin's egress headers override them whenever it is loaded.

#### Home/worker clusters

When CPA runs as a Home control plane with workers, the worker is started with
`-home-jwt` and no `-config`: it fetches its entire configuration from Home and
never reads a local `config.yaml`. The settings above, and the `plugins` block,
therefore belong in the *Home* config. A worker with `cc-mimicry.so` present on
disk but `plugins.enabled: false` in the Home config never loads it, and logs
nothing about the plugin at all.

### Bringing your own host

If you already build the CLIProxyAPI fork yourself, take the plugin alone from
the artifact image:

<!-- x-release-please-start-version -->

```dockerfile
COPY --from=ghcr.io/arkptz/cc-mimicry:0.5.0 /plugin/cc-mimicry.so /plugins/linux/amd64/cc-mimicry.so
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

Applied to Anthropic `/v1/messages` requests (source format `claude`/`anthropic`).
Requests CPA translates into the Claude format from other formats get only the
static tool-name rules, as a request normalizer, with the reverse applied to
each Claude response line before CPA translates it back.

1. **Tool-name obfuscation** — renames `tools[*].name`, `tool_choice.name` and
   historical `tool_use.name` to Claude-Code-like aliases, then reverses them on
   the response and on every stream chunk.
   - *Static prefix map*, applied at any tool count (`sessions_` → `cc_sess_`, `session_` → `cc_ses_`, and `mcp_` → `cc_mcp_` when the next character is a lowercase letter or digit).
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
