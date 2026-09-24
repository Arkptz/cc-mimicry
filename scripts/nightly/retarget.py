#!/usr/bin/env python3
"""Retarget the plugin's impersonated CLI version from fresh captures.

Reads the captures staged by stage-drift.py (testdata/captures/v<version>/)
and mechanically updates every version-pinned surface in the plugin source:

  - internal/mimicry/mimicry.go   cliTargetVersion constant
  - internal/mimicry/surface.go   cliVersion constant
  - internal/mimicry/egress_headers.go
                                  stainlessPackageVersion (from the capture's
                                  x-stainless-package-version header)
  - internal/mimicry/surfacedata/shared_intro.txt
                                  system[2] static prefix (everything before
                                  the "# Session-specific guidance" marker)
  - README.md                     the impersonated version between the
                                  <!-- cc-target-version:start/end --> markers

What is deliberately NOT auto-updated, with the reason:
  - beta sets (surface.go cliBetas/sdkCLIBetas): the wire set is a subset of
    the binary's strings, and membership (auth-gated vs not) is a manual
    judgment — the nightly compare reports wire drift instead;
  - surface agent identifiers: "You are Claude Code ..." changes are rare and
    semantic; the compare reports them;
  - buildhash: per-build value; the plugin derives a deterministic one.

Fails (exit 1, no partial writes) when the captures disagree with each other
or a pinned source location cannot be found — a retarget that cannot be done
mechanically must be done by hand, not half-way.

Exit code 0 also prints a one-line summary per touched file.

Usage: retarget.py --version <ver> --repo-root <dir>
"""

from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path

SESSION_GUIDANCE_MARKER = "# Session-specific guidance"

# The README states the impersonated CLI version exactly once, between these
# markers, so the nightly retarget keeps the front page honest without a human
# remembering to edit it.
README_VERSION_RE = re.compile(
    r"(<!-- cc-target-version:start -->).*?(<!-- cc-target-version:end -->)",
    re.DOTALL,
)

# The host-settings snippet spells out a claude-header-defaults block whose
# user-agent and package-version must track the same target as the plugin
# constants, or copy-pasting it reintroduces the version-gate 400.
README_HEADER_UA_RE = re.compile(
    r'(^[ \t]*claude-header-defaults:[ \t]*\n(?:[ \t]+.*\n)*?[ \t]+user-agent:\s*"claude-cli/)'
    r'[0-9]+(?:\.[0-9]+)*(\s\(external,\s*cli\)")',
    re.MULTILINE,
)
README_HEADER_PKG_RE = re.compile(
    r'(^[ \t]*claude-header-defaults:[ \t]*\n(?:[ \t]+.*\n)*?[ \t]+package-version:\s*")[^"]*(")',
    re.MULTILINE,
)


def die(msg: str) -> None:
    print(f"retarget: error: {msg}", file=sys.stderr)
    raise SystemExit(1)


def load_capture(repo: Path, version: str, surface: str) -> dict:
    path = repo / "testdata" / "captures" / f"v{version}" / f"{surface}-body.json"
    if not path.is_file():
        die(f"missing capture {path}")
    with open(path, encoding="utf-8") as handle:
        return json.load(handle)


def static_intro(blocks: list) -> str:
    for block in blocks:
        if block.get("idx") == 2:
            text = block.get("text") or ""
            before, _, found = text.partition(SESSION_GUIDANCE_MARKER)
            if not found:
                die("capture system[2] has no '# Session-specific guidance' marker")
            return before
    die("capture has no system[2] block")
    raise AssertionError  # unreachable


def replace_const(source: str, name: str, new_value: str, path: Path) -> str:
    # Matches a Go const / var declaration, with or without the keyword
    # (`const x = "v"` or plain `x = "v"` inside a const block), indented or
    # not, with an optional trailing comment.
    pattern = re.compile(
        rf'(^[\t ]*(?:const\s+)?{name}\s*=\s*)"[^"]*"(\s*(?://.*)?$)', re.MULTILINE
    )
    if not pattern.search(source):
        die(f"{path}: cannot find `{name} = \"...\"` to retarget")
    return pattern.sub(rf'\g<1>"{new_value}"\g<2>', source, count=1)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--version", required=True)
    parser.add_argument("--repo-root", required=True)
    args = parser.parse_args()
    repo = Path(args.repo_root)
    version = args.version

    cli = load_capture(repo, version, "cli")
    sdk = load_capture(repo, version, "sdk-cli")

    # Cross-surface coherence gate: the intro and stainless values must agree.
    intro_cli = static_intro(cli["system_blocks"])
    intro_sdk = static_intro(sdk["system_blocks"])
    if intro_cli != intro_sdk:
        die("cli and sdk-cli captures disagree on the system[2] static intro")
    pkg_cli = (cli.get("headers") or {}).get("x-stainless-package-version")
    pkg_sdk = (sdk.get("headers") or {}).get("x-stainless-package-version")
    if pkg_cli != pkg_sdk or not pkg_cli:
        die(f"captures disagree on x-stainless-package-version: {pkg_cli!r} vs {pkg_sdk!r}")

    # Every output is computed and validated before the first write, so a
    # missing pin leaves the tree untouched.
    mimicry_dir = repo / "internal" / "mimicry"
    outputs: dict[Path, str] = {}

    path = mimicry_dir / "mimicry.go"
    outputs[path] = replace_const(path.read_text(encoding="utf-8"), "cliTargetVersion", version, path)

    path = mimicry_dir / "surface.go"
    outputs[path] = replace_const(path.read_text(encoding="utf-8"), "cliVersion", version, path)

    path = mimicry_dir / "egress_headers.go"
    outputs[path] = replace_const(path.read_text(encoding="utf-8"), "stainlessPackageVersion", pkg_cli, path)

    outputs[mimicry_dir / "surfacedata" / "shared_intro.txt"] = intro_cli

    readme = repo / "README.md"
    src = readme.read_text(encoding="utf-8")
    if not README_VERSION_RE.search(src):
        die("README.md: cc-target-version markers are missing")
    src = README_VERSION_RE.sub(rf"\g<1>**{version}**\g<2>", src, count=1)
    if not README_HEADER_UA_RE.search(src):
        die("README.md: claude-header-defaults user-agent line is missing")
    src = README_HEADER_UA_RE.sub(rf"\g<1>{version}\g<2>", src, count=1)
    if not README_HEADER_PKG_RE.search(src):
        die("README.md: claude-header-defaults package-version line is missing")
    src = README_HEADER_PKG_RE.sub(rf"\g<1>{pkg_cli}\g<2>", src, count=1)
    outputs[readme] = src

    touched: list[str] = []
    for path, content in outputs.items():
        path.write_text(content, encoding="utf-8")
        touched.append(str(path.relative_to(repo)))

    for name in touched:
        print(f"retarget: updated {name}")
    print(f"retarget: plugin now impersonates CLI {version} "
          f"(x-stainless-package-version {pkg_cli})")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
