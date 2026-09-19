# Fingerprint baselines

Baseline for the nightly drift detection workflow
(`.github/workflows/fingerprint-nightly.yml`). All logic lives in
`scripts/nightly/` — the workflow is glue only.

## What is tracked

- `baseline/fingerprint-binary.json` — CHECK 1 (cheap): version,
  User-Agent template, beta tokens, and `x-app` values extracted from
  `strings` on the `@anthropic-ai/claude-code-linux-x64` tarball. The binary
  is **never executed** at this stage; the tarball sha512 is verified against
  the npm registry before extraction.
- `testdata/captures/v<version>/{cli,sdk-cli}-body.json` — CHECK 2
  (authoritative): the committed wire captures. The nightly run re-captures
  both surfaces with a well-formed-but-invalid fake credential against a
  local always-401 upstream (`scripts/nightly/fake-upstream.py`) and compares
  the normalized fingerprints via `scripts/nightly/stage-drift.py`.

There are no separate wire baselines: the committed captures ARE the wire
baseline. Same-version drift is reported in the nightly log / PR body;
existing fixtures are not auto-overwritten (they carry real-credential
provenance, PROC-001). A NEW CLI version gets its captures staged under
`testdata/captures/v<version>/` by the drift PR.

## Drift PRs

One PR per nightly run (label `fingerprint-drift`) carries the updated
baseline, any newly staged captures, and the Go test verdict. PRs opened with
the default `GITHUB_TOKEN` do not trigger `pull_request` workflows (GitHub
limitation), which is why the test run happens inside the nightly workflow
itself.

**Do NOT commit raw mitmproxy flow files.** They contain the full system
prompt and the auth header. Only normalized fixture JSON is committed.
