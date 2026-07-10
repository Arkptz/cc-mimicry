package mimicry

// integration_test.go is the CPA-own-emission gate (round-4 reviewer F3).
//
// Nightly CI captures the REAL Claude Code CLI's outbound wire (upstream drift
// detection). The per-surface golden tests (golden_test.go, egress_headers_test.go)
// each pin one half of the fingerprint in isolation. This test wires both halves
// together for both surfaces in one table, so a regression that would let the
// plugin body agree with the capture while the header hook drifts (or vice
// versa) fails here loudly before it can reach the wire.
//
// It calls the same two functions the plugin ABI dispatches to on the hot path:
//   - buildClaudeCodeSystemBlocks(profile) — the 4-block system[] body
//   - buildEgressHeaderResponse(profile)   — the P4 egress header override
//
// and asserts the cross-surface invariants F3 demands:
//   - system[] is 4 blocks with the right types and cache_control shapes;
//   - system[0] carries the "x-anthropic-billing-header:" prefix, the CPA
//     cch=00000 placeholder, and the surface's cc_entrypoint token;
//   - system[1] is the surface's exact agent identifier;
//   - system[2] is the shared 10676-byte intro (surface-invariant);
//   - system[3] starts with the static "# Text output" prefix;
//   - the header override sets the surface's User-Agent, the right beta count
//     (cli=11, sdk-cli=10) with the surface-discriminating token, all shared
//     x-stainless-* values, browser-access=true, and x-app=cli.
//
// For the cli surface, the capture ships the exact "ua" and "betas" strings
// the real CLI sent — we cross-check the plugin's header override against them
// byte-for-byte so upstream drift is caught by this test too. The sdk-cli
// capture only records the body (headers were not persisted in that recon
// run), so its header assertions use the constants derived from the recon.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// integrationCase pairs a surface profile with the capture file that pins the
// wire it must produce.
type integrationCase struct {
	name        string
	profile     SurfaceProfile
	capture     string
	betaCount   int
	betaMarker  string // token that MUST be present for this surface
	betaAntiTok string // token that MUST NOT be present (empty = no constraint)
}

// TestIntegrationFingerprintPipeline is the end-to-end smoke test of the
// plugin's fingerprint emission for both surfaces. See file header for scope.
func TestIntegrationFingerprintPipeline(t *testing.T) {
	cases := []integrationCase{
		{
			name:        "cli",
			profile:     CLISurface,
			capture:     "v2.1.206-cli-body.json",
			betaCount:   11,
			betaMarker:  "redact-thinking-2026-02-12",
			betaAntiTok: "",
		},
		{
			name:        "sdk-cli",
			profile:     SDKCLISurface,
			capture:     "v2.1.206-interactive-body.json",
			betaCount:   10,
			betaMarker:  "claude-code-20250219",
			betaAntiTok: "redact-thinking-2026-02-12",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			capt := loadCapture(t, tc.capture)
			assertBodyPipeline(t, tc, capt)
			assertHeaderPipeline(t, tc, capt)
		})
	}
}

// capture models the subset of the golden JSON this test reads. Fields absent
// from the sdk capture (ua/betas) decode to the zero value and are skipped
// downstream — see assertHeaderPipeline.
type capture struct {
	UA    string        `json:"ua"`
	Betas string        `json:"betas"`
	Sys   []captureSlot `json:"system_blocks"`
}

type captureSlot struct {
	Idx          int             `json:"idx"`
	Len          int             `json:"len"`
	Text         string          `json:"text"`
	CacheControl json.RawMessage `json:"cache_control"`
}

func loadCapture(t *testing.T, name string) capture {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "captures", name)
	raw, err := os.ReadFile(path) //nolint:gosec // test-only, paths are hardcoded test fixtures
	if err != nil {
		t.Fatalf("read capture %s: %v", path, err)
	}
	var c capture
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatalf("unmarshal capture %s: %v", path, err)
	}
	if len(c.Sys) != 4 {
		t.Fatalf("%s: capture has %d system blocks, want 4", name, len(c.Sys))
	}
	return c
}

// assertBodyPipeline runs the plugin's body-side transform (system[] rewrite)
// and verifies structure + per-block content against the capture. The
// byte-level compare of the shared intro and the static prefix of system[3]
// already lives in TestGoldenSystemBlocksMatchCaptures; here we assert the
// STRUCTURAL invariants F3 names (block count, types, billing prefix, agent id,
// intro length, "# Text output" prefix, cache_control shapes).
func assertBodyPipeline(t *testing.T, tc integrationCase, capt capture) {
	t.Helper()
	raw := buildClaudeCodeSystemBlocks(tc.profile)
	var blocks []map[string]any
	if err := json.Unmarshal(raw, &blocks); err != nil {
		t.Fatalf("unmarshal built blocks: %v", err)
	}
	if len(blocks) != 4 {
		t.Fatalf("built %d system blocks, want 4", len(blocks))
	}

	for i, b := range blocks {
		if b["type"] != "text" {
			t.Errorf("system[%d].type = %v, want %q", i, b["type"], "text")
		}
	}

	// system[0] — billing block: prefix, cch=00000 placeholder, entrypoint token.
	sys0, _ := blocks[0]["text"].(string)
	if !strings.HasPrefix(sys0, "x-anthropic-billing-header:") {
		t.Errorf("system[0] missing billing prefix: %q", snippet(sys0))
	}
	if !strings.Contains(sys0, "cch=00000;") {
		t.Errorf("system[0] missing cch=00000; placeholder (CPA signer relies on it): %q", sys0)
	}
	wantEntry := "cc_entrypoint=" + tc.profile.Entrypoint + ";"
	if !strings.Contains(sys0, wantEntry) {
		t.Errorf("system[0] missing %q: %q", wantEntry, sys0)
	}
	if cc := blocks[0]["cache_control"]; cc != nil {
		t.Errorf("system[0] cache_control = %v, want absent", cc)
	}

	// system[1] — surface agent identifier, no cache_control.
	sys1, _ := blocks[1]["text"].(string)
	if sys1 != tc.profile.AgentIdentifier {
		t.Errorf("system[1] = %q, want %q", sys1, tc.profile.AgentIdentifier)
	}
	if sys1 != capt.Sys[1].Text {
		t.Errorf("system[1] disagrees with capture: got %q want %q", sys1, capt.Sys[1].Text)
	}
	if cc := blocks[1]["cache_control"]; cc != nil {
		t.Errorf("system[1] cache_control = %v, want absent", cc)
	}

	// system[2] — shared intro with ephemeral/1h/global cache_control. Compare
	// against the capture's actual text length (the capture's "len" metadata
	// field is a stale recon note; the golden byte-compare in
	// TestGoldenSystemBlocksMatchCaptures is authoritative on the text itself).
	sys2, _ := blocks[2]["text"].(string)
	if len(sys2) != len(capt.Sys[2].Text) {
		t.Errorf("system[2] len = %d, want %d (shared intro, surface-invariant)", len(sys2), len(capt.Sys[2].Text))
	}
	assertEphemeral(t, blocks[2]["cache_control"], "global")

	// system[3] — surface TextOutputSection, starts with "# Text output".
	sys3, _ := blocks[3]["text"].(string)
	const sys3Prefix = "# Text output"
	if !strings.HasPrefix(sys3, sys3Prefix) {
		t.Errorf("system[3] does not start with %q: %q", sys3Prefix, snippet(sys3))
	}
	assertEphemeral(t, blocks[3]["cache_control"], "")
}

// assertEphemeral verifies the cache_control block shape the real CLI emits:
// {"type":"ephemeral","ttl":"1h"} for system[3], plus "scope":"global" for
// system[2]. wantScope="" means no scope key must be present.
func assertEphemeral(t *testing.T, ccAny any, wantScope string) {
	t.Helper()
	cc, ok := ccAny.(map[string]any)
	if !ok {
		t.Fatalf("cache_control not an object: %v", ccAny)
	}
	if cc["type"] != "ephemeral" {
		t.Errorf("cache_control.type = %v, want %q", cc["type"], "ephemeral")
	}
	if cc["ttl"] != "1h" {
		t.Errorf("cache_control.ttl = %v, want %q", cc["ttl"], "1h")
	}
	if wantScope == "" {
		if _, has := cc["scope"]; has {
			t.Errorf("cache_control.scope present, want absent: %v", cc["scope"])
		}
		return
	}
	if cc["scope"] != wantScope {
		t.Errorf("cache_control.scope = %v, want %q", cc["scope"], wantScope)
	}
}

// assertHeaderPipeline runs the plugin's P4 egress header hook and verifies
// the per-surface fingerprint. For cli, we also cross-check UA + betas against
// the capture's byte-for-byte "ua" / "betas" strings so upstream drift trips
// this test in addition to the CI wire-diff job.
func assertHeaderPipeline(t *testing.T, tc integrationCase, capt capture) {
	t.Helper()
	resp := buildEgressHeaderResponse(tc.profile)
	h := resp.Headers

	wantUA := "claude-cli/2.1.206 (external, " + tc.profile.Entrypoint + ")"
	if got := h.Get("User-Agent"); got != wantUA {
		t.Errorf("User-Agent = %q, want %q", got, wantUA)
	}
	if capt.UA != "" && h.Get("User-Agent") != capt.UA {
		t.Errorf("User-Agent disagrees with capture: got %q want %q", h.Get("User-Agent"), capt.UA)
	}

	beta := h.Get("Anthropic-Beta")
	tokens := strings.Split(beta, ",")
	if len(tokens) != tc.betaCount {
		t.Errorf("Anthropic-Beta token count = %d, want %d: %q", len(tokens), tc.betaCount, beta)
	}
	if tc.betaMarker != "" && !containsToken(tokens, tc.betaMarker) {
		t.Errorf("Anthropic-Beta missing surface marker %q: %v", tc.betaMarker, tokens)
	}
	if tc.betaAntiTok != "" && containsToken(tokens, tc.betaAntiTok) {
		t.Errorf("Anthropic-Beta MUST NOT contain %q on %s: %v", tc.betaAntiTok, tc.name, tokens)
	}
	if capt.Betas != "" && beta != capt.Betas {
		t.Errorf("Anthropic-Beta disagrees with capture:\n got: %q\nwant: %q", beta, capt.Betas)
	}

	// Shared x-stainless-* + browser-access + x-app. Same tuple for both
	// surfaces — reused so a regression on either surface fails here.
	assertSharedStainless(t, h)
}
