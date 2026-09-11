# cc-mimicry

A CLIProxyAPI (CPA) plugin that makes Claude (Anthropic) OAuth requests look like
they came from the native **Claude Code CLI**, so shared/pooled subscriptions are
less likely to be flagged as third-party clients.

[![CI](https://github.com/Arkptz/cc-mimicry/actions/workflows/ci.yml/badge.svg)](https://github.com/Arkptz/cc-mimicry/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](#license)

It ports the request transforms from sub2api's `gateway_tool_rewrite.go` /
`cc_mimicry` (originally the [Parrot](https://github.com/danger-dream/Parrot) /
[cc-proxy](https://github.com/danger-dream/cc-proxy) lineage) into a native CPA
plugin, so you keep CPA's high-throughput multi-account gateway and only add the
mimicry on the request path.

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
2. **System prompt 3-block rewrite** — rebuilds `system` into the CLI 2.1.268 shape:
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
   `anthropic-dangerous-direct-browser-access` to CLI 2.1.206 values via the
   CPA EgressHeaderInterceptor ABI hook (post-auth, pre-send). Strips
   CPA-injected headers absent from real CLI captures.

Each transform is individually toggleable (see [Config](#config)).

## Requirements

> [!IMPORTANT]
> The plugin is a native CGO `c-shared` object. The CPA host that loads it **must
> be built with `CGO_ENABLED=1`**. The stock `eceasy/cli-proxy-api` image is built
> without CGO and will silently ignore this (and every other) `.so` plugin.

- Go 1.26+
- A C compiler (gcc/clang) for CGO
- A local CLIProxyAPI checkout on the matching version at `../CLIProxyAPI` (the
  `go.mod` `replace` target). A gitignored `go.work` can override the path for a
  different local layout.

## GHCR artifact image

The compiled `.so` is published as a distroless artifact image to GHCR. Any CPA
Dockerfile can copy it in directly — no builder service, no volume mounts:

```dockerfile
COPY --from=ghcr.io/arkptz/cc-mimicry:v0.1.0 /plugin/cc-mimicry.so /plugins/linux/amd64/cc-mimicry.so
```

> [!WARNING]
> **ABI lock — always pin a version tag, never `:latest`.** The `.so` is compiled
> against a specific CLIProxyAPI SDK version (see `.cpa-version` / `.cpa-commit`).
> The CPA host loading it must be built against the **same** SDK version, or the
> plugin is rejected / silently misbehaves at load time. `:latest` may resolve to
> a different SDK version than your host. Match the plugin image tag to your CPA
> release. The image carries `cc-mimicry.cpa-sdk-version` / `cc-mimicry.cpa-sdk-commit`
> OCI labels so you can verify what a given tag was built against.

## Build

```sh
nix develop          # enter the dev shell (Go 1.26 + CGO)
./build.sh           # -> dist/cc-mimicry.so
make build           # same, via Makefile
VERSION=0.2.0 ./build.sh   # stamp a version
```

The plugin ID is the filename without extension (`cc-mimicry`).

## Install

Drop `cc-mimicry.so` into the host's plugin directory. CPA scans, in order:

```text
<plugins-dir>/<goos>/<goarch>-<variant>/   e.g. plugins/linux/amd64-v3/
<plugins-dir>/<goos>/<goarch>/             e.g. plugins/linux/amd64/
<plugins-dir>/                             (flat fallback)
```

Then enable it in `config.yaml`:

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    cc-mimicry:
      enabled: true
      priority: 100
      obfuscate_tool_names: true
      inject_system_prompt: true
      cache_breakpoints: true
      fill_fingerprint: true
      surface: cli            # "cli" (default) or "sdk-cli"
```

## Config

| Key | Default | Effect |
|-----|---------|--------|
| `obfuscate_tool_names` | `true` | Rename tools + reverse on responses/stream chunks. |
| `inject_system_prompt` | `true` | 4-block surface-aware system rewrite; relocate original system into messages. |
| `cache_breakpoints` | `true` | Ephemeral `cache_control` on the last tool. |
| `fill_fingerprint` | `true` | Fill `temperature`/`max_tokens`/`context_management`. |
| `surface` | `cli` | CLI entrypoint to impersonate: `cli` (interactive TUI, 11 betas) or `sdk-cli` (-p print, 10 betas). Unknown values are rejected at config load. |

An omitted key keeps its default (`true`); set to `false` to disable a stage.

## Development

```sh
make build           # build the .so
make test            # go test -race
make lint            # golangci-lint (zero-warning)
gofumpt -l .         # format check
make vuln            # govulncheck
./scripts/ci-local.sh   # replay CI jobs locally via act
```

## SDK version pin

The CLIProxyAPI SDK version is centralized in two files:

- `.cpa-version` — the git tag (e.g. `v7.2.51`)
- `.cpa-commit` — the pinned commit SHA, verified at Docker/CI clone time so a
  moved upstream tag cannot silently swap the SDK baked into the `.so`.

To bump: update both files. The Dockerfile, CI, and Makefile all derive from them.

## How the reverse pass is correlated

The C ABI has no shared per-request context across interceptor calls, so the
plugin correlates the forward `request.intercept_before` hook (which builds the
tool-name rewrite map) with the `response.intercept_after` /
`response.intercept_stream_chunk` hooks via a stable signature over the request
body (plus a couple of request-id headers). The map lives in a bounded in-process
FIFO; entries for requests that never reach the response side age out.

## Decisions

This project tracks decisions with `dg` under `docs/`. See
[ADR-002](docs/architecture/adr-002-cgo-c-shared-plugin-build-and-ghcr-artifact-image.md)
for the CGO c-shared build + GHCR artifact-image design, and [AGENTS.md](AGENTS.md)
for the contributor workflow.

## License

MIT
