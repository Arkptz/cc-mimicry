#!/usr/bin/env python3
"""Convert a mitmproxy flow file into a testdata/captures fixture.

Picks the /v1/messages request carrying the full system[] array (the CLI also
emits smaller sidecar requests) and emits the same shape the existing
v2.1.206/ fixtures use, with secrets stripped.

Run under `nix shell nixpkgs#mitmproxy` so `mitmproxy.io` is importable.
"""

from __future__ import annotations

import argparse
import json
import re
import sys
from datetime import date
from typing import Any

from mitmproxy import io as mitm_io
from mitmproxy import http

# Header values that must never land in a committed fixture.
SECRET_HEADERS = {"authorization", "x-api-key", "proxy-authorization", "cookie"}

# Per-request or per-machine values: recording them would make the fixture fail
# on the next capture for reasons that are not fingerprint drift.
VOLATILE_HEADERS = {
    "x-claude-code-session-id",
    "content-length",
    "host",
    "connection",
    "accept-encoding",
    "x-stainless-retry-count",
}

# cch is per-request and signed downstream; the fixtures redact it so byte
# comparisons stay stable across captures.
CCH_RE = re.compile(r"cch=[0-9a-f]+", re.IGNORECASE)


def load_messages_requests(path: str) -> list[http.HTTPFlow]:
    flows: list[http.HTTPFlow] = []
    with open(path, "rb") as handle:
        for flow in mitm_io.FlowReader(handle).stream():
            if not isinstance(flow, http.HTTPFlow) or flow.request is None:
                continue
            if "/v1/messages" not in flow.request.path:
                continue
            flows.append(flow)
    return flows


def pick_primary(flows: list[http.HTTPFlow]) -> http.HTTPFlow:
    """Return the flow whose body carries the full system[] array.

    The CLI fires auxiliary requests (title generation, quota probes) at the
    same path; the fingerprint lives in the one with the largest system block.
    """
    best: http.HTTPFlow | None = None
    best_len = -1
    for flow in flows:
        try:
            body = json.loads(flow.request.get_text() or "{}")
        except json.JSONDecodeError:
            continue
        system = body.get("system")
        if not isinstance(system, list):
            continue
        size = sum(len(block.get("text", "")) for block in system if isinstance(block, dict))
        if size > best_len:
            best_len = size
            best = flow
    if best is None:
        raise SystemExit("no /v1/messages request with a system[] array found in the flow")
    return best


def build_fixture(flow: http.HTTPFlow, surface: str, version: str) -> dict[str, Any]:
    body = json.loads(flow.request.get_text() or "{}")
    headers = {k.lower(): v for k, v in flow.request.headers.items()}

    system_blocks = []
    for idx, block in enumerate(body.get("system", [])):
        if not isinstance(block, dict):
            continue
        text = block.get("text", "")
        if idx == 0:
            text = CCH_RE.sub("cch=<DYNAMIC>", text)
        system_blocks.append(
            {
                "idx": idx,
                "text": text,
                "cache_control": block.get("cache_control"),
            }
        )

    entrypoint = "sdk-cli" if surface == "sdk-cli" else "cli"

    # Persist every non-secret request header. Recording only ua/betas is how
    # x-stainless-package-version drifted 0.94.0 -> 0.112.1 unnoticed: a field
    # the fixture never captured cannot be pinned by a test.
    safe_headers = {
        k.lower(): v
        for k, v in headers.items()
        if k.lower() not in SECRET_HEADERS and k.lower() not in VOLATILE_HEADERS
    }

    # The shape of tools[] is part of the fingerprint (2.1.268 sends no
    # cache_control on any tool); the names are the caller's, so keep only the
    # shape. The raw count varies with the machine's MCP servers (190+ tools
    # on a loaded workstation, ~31 on a clean runner), so the core count —
    # built-in tools only — is what the drift compare pins.
    tools = body.get("tools") or []
    core = [t for t in tools if isinstance(t, dict) and not str(t.get("name", "")).startswith("mcp__")]
    tools_shape = {
        "count": len(tools),
        "core_count": len(core),
        "with_cache_control": sum(
            1 for t in tools if isinstance(t, dict) and "cache_control" in t
        ),
        "last_cache_control": (
            tools[-1].get("cache_control") if tools and isinstance(tools[-1], dict) else None
        ),
    }

    return {
        "_note": (
            f"REAL Claude Code {version} ({entrypoint} entrypoint) FULL system[] body. "
            "Captured via mitmproxy reverse proxy against the live upstream. cch redacted."
        ),
        "_source": date.today().isoformat(),
        "_surface": surface,
        "ua": headers.get("user-agent"),
        "betas": headers.get("anthropic-beta"),
        "headers": safe_headers,
        "tools_shape": tools_shape,
        "system_blocks": system_blocks,
    }


def secret_values(flow: http.HTTPFlow) -> list[str]:
    """Return the credential values this request carried, for leak checking."""
    values = []
    for name, value in flow.request.headers.items():
        if name.lower() in SECRET_HEADERS and value:
            values.append(value)
            # Bearer tokens are worth matching without their scheme prefix too.
            if " " in value:
                values.append(value.split(" ", 1)[1])
    return values


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--flow", required=True)
    parser.add_argument("--surface", required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()

    flows = load_messages_requests(args.flow)
    if not flows:
        raise SystemExit(f"no /v1/messages requests in {args.flow}")

    primary = pick_primary(flows)
    fixture = build_fixture(primary, args.surface, args.version)

    # The wire tells us which surface was really captured. Writing a cli
    # fixture from an sdk-cli request would silently corrupt the golden data.
    billing = fixture["system_blocks"][0]["text"] if fixture["system_blocks"] else ""
    match = re.search(r"cc_entrypoint=([a-z-]+)", billing)
    if not match:
        raise SystemExit(f"no cc_entrypoint in billing block: {billing!r}")
    if match.group(1) != args.surface:
        raise SystemExit(
            f"captured cc_entrypoint={match.group(1)} but --surface={args.surface}; "
            "the CLI was driven in the wrong mode"
        )
    if args.version not in billing:
        raise SystemExit(f"billing block reports a different version: {billing!r}")

    # The fixture only ever carries ua/betas/system text, so a secret can only
    # appear as a credential *value*. Checking for header names would match the
    # system prompt's own prose (it discusses "authorization context").
    blob = json.dumps(fixture, indent=2, ensure_ascii=False)
    for secret in secret_values(primary):
        if secret and secret in blob:
            raise SystemExit("refusing to write: a credential value leaked into the fixture")

    with open(args.output, "w", encoding="utf-8") as handle:
        handle.write(blob + "\n")

    print(
        f"{args.output}: {len(fixture['system_blocks'])} system blocks, "
        f"ua={fixture['ua']!r}"
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
