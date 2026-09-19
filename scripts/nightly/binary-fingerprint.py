#!/usr/bin/env python3
"""Extract the static fingerprint of a Claude Code CLI release tarball.

Reads the sha512-verified tarball of @anthropic-ai/claude-code-linux-x64,
unpacks the native ELF, and greps its `strings` output for the tokens the
nightly drift check tracks: the `claude-cli/<ver>` User-Agent pattern and the
beta tokens the binary references. The binary is never executed.

Output JSON (stdout):
    {"version": ..., "user_agent_template": ..., "ua_embedded_version": ...,
     "beta_tokens": [...], "x_app_values": [...]}

Usage: binary-fingerprint.py --tarball <native.tgz> --version <ver>
"""

from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
import tarfile
import tempfile
from pathlib import Path

# The UA is built at runtime from a template literal, e.g.
# `claude-cli/${...VERSION} (external, ${a.CLAUDE_CODE_ENTRYPOINT??"cli"}...)`.
# No `claude-cli/2.1.278 (...)` literal exists in the binary, so the
# fingerprint records the normalized TEMPLATE with every ${...} hole replaced
# by <...>, plus the VERSION string embedded in the same template.
UA_TEMPLATE_ANCHOR = "return`claude-cli/"

# VERSION:"2.1.278" inside the first UA hole (a config-object lookup).
VERSION_IN_HOLE_RE = re.compile(r'VERSION:"([0-9]+\.[0-9]+\.[0-9]+)"')

# Dated beta tokens: <name>-YYYY-MM-DD, at least 5 chars of name.
DATED_BETA_RE = re.compile(r"[a-z0-9][a-z0-9-]{4,}-(?:2024|2025|2026)-[0-9]{2}-[0-9]{2}")

# Well-known non-dated beta names the CLI has referenced across versions.
NAMED_BETAS = (
    "computer-use",
    "extended-cache-ttl",
    "files-api",
    "fine-grained-tool-streaming",
    "prompt-caching",
    "redact-thinking",
    "structured-outputs",
    "token-efficient-tools",
    "context-management",
    "reasoning-effort",
)

X_APP_VALUES = ("cli", "sdk-cli", "external")


def elf_strings(tarball: Path) -> str:
    """Extract the largest ELF from the package and return `strings` output."""
    with tempfile.TemporaryDirectory(prefix="fp-") as tmp:
        with tarfile.open(tarball, "r:gz") as tar:
            members = [m for m in tar.getmembers() if m.isfile()]
            if not members:
                raise SystemExit(f"error: no files in {tarball}")
            tar.extractall(tmp, filter="data")
        # The native package ships exactly one executable: claude. Pick the
        # largest file defensively in case the layout grows.
        best = None
        best_size = -1
        for path in Path(tmp).rglob("*"):
            if path.is_file() and path.stat().st_size > best_size:
                best = path
                best_size = path.stat().st_size
        if best is None:
            raise SystemExit(f"error: no file found in {tarball}")
        # `file`-style ELF check via magic bytes.
        with open(best, "rb") as handle:
            if handle.read(4) != b"\x7fELF":
                raise SystemExit(
                    f"error: largest file in {tarball} is not an ELF: {best.name}"
                )
        return subprocess.run(
            ["strings", "-a", str(best)],
            check=True,
            stdout=subprocess.PIPE,
            text=True,
        ).stdout


def ua_template(text: str) -> dict:
    """Extract the normalized User-Agent template and its embedded version.

    The template literal lives in a single `strings` line. Holes (`${...}`)
    can nest object literals (`${{...}.VERSION}`), so a hole's end is found by
    counting ALL braces inside it — a regex cannot do this.
    """
    start = text.find(UA_TEMPLATE_ANCHOR)
    if start == -1:
        return {"user_agent_template": None, "ua_embedded_version": None}
    i = start + len(UA_TEMPLATE_ANCHOR)
    out: list[str] = []
    version: str | None = None
    in_hole = False
    depth = 0
    while i < len(text):
        ch = text[i]
        if not in_hole:
            if ch == "`":
                break  # template literal closed
            if ch == "$" and text[i + 1 : i + 2] == "{":
                out.append("<>")
                in_hole = True
                depth = 1
                i += 2
                continue
            out.append(ch)
        elif ch == "{":
            depth += 1
        elif ch == "}":
            depth -= 1
            if depth == 0:
                in_hole = False
        elif version is None:
            m = VERSION_IN_HOLE_RE.match(text, i)
            if m:
                version = m.group(1)
        i += 1
    template = "".join(out)
    if not template:
        return {"user_agent_template": None, "ua_embedded_version": None}
    return {
        "user_agent_template": "claude-cli/" + template,
        "ua_embedded_version": version,
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--tarball", required=True)
    parser.add_argument("--version", required=True)
    args = parser.parse_args()

    text = elf_strings(Path(args.tarball))

    ua = ua_template(text)
    dated = DATED_BETA_RE.findall(text)
    named = [b for b in NAMED_BETAS if b in text]
    x_app = [v for v in X_APP_VALUES if v in text]

    fingerprint = {
        "version": args.version,
        "user_agent_template": ua["user_agent_template"],
        "ua_embedded_version": ua["ua_embedded_version"],
        "beta_tokens": sorted(set(dated) | set(named)),
        "x_app_values": x_app,
    }
    json.dump(fingerprint, sys.stdout, sort_keys=True, indent=2)
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
