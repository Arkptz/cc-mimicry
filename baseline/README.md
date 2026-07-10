# Fingerprint baselines

Fingerprint baselines for the nightly drift detection workflow
(`.github/workflows/fingerprint-nightly.yml`).

Three fingerprints are tracked:

- `fingerprint-binary.json` — CHECK 1 (cheap): version, User-Agent pattern, and
  beta tokens extracted from `strings` on the `@anthropic-ai/claude-code` npm
  tarball. The binary is **never executed**; sha512 is verified against the
  registry before extraction.
- `fingerprint-wire-cli.json` — CHECK 2 (authoritative): normalized headers +
  `system[0]` billing prefix captured by mitmdump when driving the interactive
  TUI (`cli` surface) with a well-formed-but-invalid fake OAuth credential. The
  runner is ephemeral and no real credentials are used.
- `fingerprint-wire-sdk.json` — same as above for the `-p` print /
  `sdk-cli` surface.

On the first successful run, the CI job commits real baselines here via
auto-PR. The placeholders below intentionally omit content so the diff on the
first run is unambiguous.

**Do NOT commit raw mitmproxy flow files.** They contain the full system
prompt. Only normalized fingerprint JSON is committed.
