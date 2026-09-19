#!/usr/bin/env python3
"""Compare fresh nightly captures against the committed ones; stage drift.

The nightly workflow drives the real Claude Code CLI once per surface (cli and
sdk-cli) through mitmproxy and converts each flow to the same fixture shape
scripts/recon/flow-to-fixture.py writes (system_blocks, headers, tools_shape,
ua, betas). This script decides whether the fresh capture shows fingerprint
drift relative to the committed fixture of the same surface, and stages the
update for the drift PR.

What counts as drift (same-surface compare):
  - user-agent, beta list, any kept header value or kept-header set change;
  - tools_shape (count, with_cache_control, last_cache_control);
  - system[0] billing prefix outside the documented-dynamic segments
    (buildhash, cch) — cc_entrypoint / cc_version / new-or-dropped segments;
  - system[1] agent identifier text;
  - system[2] static prefix (everything up to the "# Session-specific
    guidance" marker; the client-dynamic tail after it is ignored).

Values known to vary per capture machine / run and therefore NOT drift:
  - cch= / buildhash (signed / build-id dynamic);
  - x-stainless-runtime-version, x-stainless-os, x-stainless-arch — platform
    dependent, not part of the CLI fingerprint;
  - request ids and session ids — never recorded by the fixture anyway;
  - the client-dynamic tail of system[2] after "# Session-specific guidance".

Exit code is 0 for both drift and no-drift; the caller reads the JSON verdict
from stdout. Non-zero exit is reserved for operational errors.

Committed fixtures are never overwritten: they carry real-credential
provenance (PROC-001), and refreshing them is a deliberate recon, not a
nightly action. A NEW version (no fixture dir yet) gets its captures staged
under testdata/captures/v<version>/ for the drift PR.

Usage: stage-drift.py --captures-dir <dir> --repo-root <dir> --version <ver>
"""

from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path

# Header values that vary with the machine the capture ran on, not with the
# CLI release. A change here is reported but never marks drift.
PLATFORM_HEADERS = {
    "x-stainless-runtime-version",
    "x-stainless-os",
    "x-stainless-arch",
}

# The client-dynamic tail of the intro block starts at this heading.
SESSION_GUIDANCE_MARKER = "# Session-specific guidance"

# cc_version buildhash (e.g. `cc_version=2.1.268.1af`) and the cch= tail are
# documented-dynamic; everything else in system[0] must match byte-for-byte.
BUILDHASH_RE = re.compile(r"(cc_version=[0-9.]+\.)[0-9a-f]+")
CCH_RE = re.compile(r"cch=[^;]*;?")


def load(path: Path) -> dict:
    with open(path, encoding="utf-8") as handle:
        return json.load(handle)


def normalise_billing(text: str) -> str:
    text = BUILDHASH_RE.sub(r"\1BUILDHASH", text)
    text = CCH_RE.sub("", text)
    text = re.sub(r";{2,}", ";", text)
    if not text.endswith(";"):
        text += ";"
    return text.strip()


def static_intro(text: str) -> str:
    """The plugin-owned prefix of the intro block, client tail removed."""
    before, _, found = text.partition(SESSION_GUIDANCE_MARKER)
    if not found:
        return text
    return before


def compare_fingerprints(committed: dict, fresh: dict) -> tuple[bool, list[str]]:
    """Return (drifted, findings) for a same-surface pair."""
    findings: list[str] = []

    for key in ("ua", "betas"):
        if committed.get(key) != fresh.get(key):
            findings.append(f"{key}: {committed.get(key)!r} -> {fresh.get(key)!r}")

    old_h = committed.get("headers") or {}
    new_h = fresh.get("headers") or {}
    for name in sorted(set(old_h) | set(new_h)):
        if name in PLATFORM_HEADERS:
            continue
        if old_h.get(name) != new_h.get(name):
            findings.append(f"header {name}: {old_h.get(name)!r} -> {new_h.get(name)!r}")

    # The raw tools count varies with the capture machine's MCP servers, so
    # the drift compare pins core_count (built-ins only) when both sides have
    # it; the committed 2.1.268 fixtures predate the field, hence the guard.
    old_t = committed.get("tools_shape") or {}
    new_t = fresh.get("tools_shape") or {}
    if "core_count" in old_t and "core_count" in new_t:
        if old_t.get("core_count") != new_t.get("core_count"):
            findings.append(
                f"tools_shape.core_count: {old_t.get('core_count')!r} -> {new_t.get('core_count')!r}"
            )
    for key in ("with_cache_control", "last_cache_control"):
        if old_t.get(key) != new_t.get(key):
            findings.append(f"tools_shape.{key}: {old_t.get(key)!r} -> {new_t.get(key)!r}")

    old_blocks = committed.get("system_blocks") or []
    new_blocks = fresh.get("system_blocks") or []
    if len(old_blocks) != len(new_blocks):
        findings.append(f"system blocks: {len(old_blocks)} -> {len(new_blocks)}")
    for old, new in zip(old_blocks, new_blocks):
        idx = old.get("idx")
        old_text = old.get("text") or ""
        new_text = new.get("text") or ""
        if idx == 0:
            if normalise_billing(old_text) != normalise_billing(new_text):
                findings.append("system[0] billing prefix changed (outside buildhash/cch)")
        elif idx == 2:
            if static_intro(old_text) != static_intro(new_text):
                findings.append("system[2] static prefix changed")
        elif old_text != new_text:
            findings.append(f"system[{idx}] text changed")

    return bool(findings), findings


def write_stable_json(path: Path, payload: dict) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with open(path, "w", encoding="utf-8") as handle:
        json.dump(payload, handle, indent=2, ensure_ascii=False)
        handle.write("\n")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--captures-dir", required=True, help="dir with fresh cli-body.json / sdk-cli-body.json")
    parser.add_argument("--repo-root", required=True)
    parser.add_argument("--version", required=True, help="CLI version the fresh captures came from")
    args = parser.parse_args()

    captures_dir = Path(args.captures_dir)
    repo_root = Path(args.repo_root)
    captures_root = repo_root / "testdata" / "captures"
    version_dir = captures_root / f"v{args.version}"

    verdict: dict = {"drifted": False, "version": args.version, "surfaces": {}}

    for surface, fixture in (("cli", "cli-body.json"), ("sdk-cli", "sdk-cli-body.json")):
        fresh_path = captures_dir / fixture
        committed_path = version_dir / fixture
        status: dict

        if not fresh_path.is_file():
            status = {
                "captured": False,
                "drifted": False,
                "findings": [f"missing fresh capture: {fresh_path}"],
            }
        elif not committed_path.is_file():
            # No fixture for this version yet: staging it IS the drift.
            target = version_dir / fixture
            write_stable_json(target, load(fresh_path))
            status = {
                "captured": True,
                "drifted": True,
                "findings": [f"first capture for v{args.version} ({surface})"],
                "staged": str(target.relative_to(repo_root)),
            }
        else:
            drifted, findings = compare_fingerprints(load(committed_path), load(fresh_path))
            status = {"captured": True, "drifted": drifted, "findings": findings}
            if drifted:
                status["note"] = "reported only — committed fixture kept; refresh via recon (PROC-001)"

        verdict["surfaces"][surface] = status
        if status["drifted"]:
            verdict["drifted"] = True

    json.dump(verdict, sys.stdout, indent=2)
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
