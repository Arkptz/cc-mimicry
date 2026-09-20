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
//   - buildClaudeCodeSystemBlocks(profile) — the 3-block system[] body
//   - buildEgressHeaderResponse(profile)   — the P4 egress header override
//
// and asserts the cross-surface invariants F3 demands:
//   - system[] is 3 blocks with the right types and cache_control shapes;
//   - system[0] carries the "x-anthropic-billing-header:" prefix, the CPA
//     cch=00000 placeholder, and the surface's cc_entrypoint token;
//   - system[1] is the surface's exact agent identifier;
//   - system[2] is the shared 10676-byte intro (surface-invariant);
//   - system[2] carries the "# Text output" section, which 2.1.268 folded into
//     the intro block;
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
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
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
			capture:     "cli-body.json",
			betaCount:   11,
			betaMarker:  "redact-thinking-2026-02-12",
			betaAntiTok: "",
		},
		{
			name:        "sdk-cli",
			profile:     SDKCLISurface,
			capture:     "sdk-cli-body.json",
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
			assertToolsShape(t, tc, capt)
		})
	}
}

// authGatedBetas are the tokens the real CLI withholds when the session is not
// firstParty OAuth, so a capture taken through a relay legitimately lacks them.
// They are NOT unverified: each one is present in the 2.1.268 ELF (PROC-001
// Step B). Anything outside this list must match the capture exactly — keeping
// the list short and explicit is what makes a dropped token fail the test.
var authGatedBetas = []string{
	"oauth-2025-04-20",
	"advisor-tool-2026-03-01",
	"advanced-tool-use-2025-11-20",
	"extended-cache-ttl-2025-04-11",
	"cache-diagnosis-2026-04-07",
}

// capture models the subset of the golden JSON this test reads. Fields absent
// from the sdk capture (ua/betas) decode to the zero value and are skipped
// downstream — see assertHeaderPipeline.
type capture struct {
	UA         string            `json:"ua"`
	Betas      string            `json:"betas"`
	Headers    map[string]string `json:"headers"`
	ToolsShape toolsShape        `json:"tools_shape"`
	Sys        []captureSlot     `json:"system_blocks"`
}

// toolsShape records what the real CLI's tools[] looked like, without the
// caller-specific tool names.
type toolsShape struct {
	Count            int             `json:"count"`
	WithCacheControl int             `json:"with_cache_control"`
	LastCacheControl json.RawMessage `json:"last_cache_control"`
}

type captureSlot struct {
	Idx          int             `json:"idx"`
	Len          int             `json:"len"`
	Text         string          `json:"text"`
	CacheControl json.RawMessage `json:"cache_control"`
}

func loadCapture(t *testing.T, name string) capture {
	t.Helper()
	path := filepath.Join(captureDir(), name)
	raw, err := os.ReadFile(path) //nolint:gosec // test-only, paths are hardcoded test fixtures
	if err != nil {
		t.Fatalf("read capture %s: %v", path, err)
	}
	var c capture
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatalf("unmarshal capture %s: %v", path, err)
	}
	if len(c.Sys) != 3 {
		t.Fatalf("%s: capture has %d system blocks, want 3", name, len(c.Sys))
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
	if len(blocks) != 3 {
		t.Fatalf("built %d system blocks, want 3", len(blocks))
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
	assertEphemeral(t, blocks[1]["cache_control"], "")

	// system[2] — shared intro, ephemeral cache_control. The capture continues
	// past the plugin's static text with the client-dynamic session tail, so
	// compare against that prefix; the golden byte-compare in
	// TestGoldenSystemBlocksMatchCaptures is authoritative on the text itself.
	sys2, _ := blocks[2]["text"].(string)
	wantIntro := staticIntroPrefix(capt.Sys[2].Text)
	if len(sys2) != len(wantIntro) {
		t.Errorf("system[2] len = %d, want %d (shared intro, surface-invariant)", len(sys2), len(wantIntro))
	}
	// Since 2.1.268 the "# Text output" section is part of this block.
	if !strings.Contains(sys2, "# Text output") {
		t.Errorf("system[2] missing the %q section: %q", "# Text output", snippet(sys2))
	}
	assertEphemeral(t, blocks[2]["cache_control"], "")
}

// assertEphemeral verifies the cache_control block shape the real CLI emits.
// Since 2.1.268 that is a bare {"type":"ephemeral"} — the ttl/scope qualifiers
// the 2.1.206 captures carried are gone. wantScope="" means no scope key.
func assertEphemeral(t *testing.T, ccAny any, wantScope string) {
	t.Helper()
	cc, ok := ccAny.(map[string]any)
	if !ok {
		t.Fatalf("cache_control not an object: %v", ccAny)
	}
	if cc["type"] != "ephemeral" {
		t.Errorf("cache_control.type = %v, want %q", cc["type"], "ephemeral")
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

	wantUA := "claude-cli/" + cliTargetVersion + " (external, " + tc.profile.Entrypoint + ")"
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
	// Pin the beta set by EQUALITY against the capture, modulo an explicit list
	// of tokens the CLI omits when the capture is not taken over firstParty
	// OAuth. A plain subset check would pass even if a token were dropped from
	// the profile — the exact drift direction this repo exists to catch.
	wantBetas := make([]string, 0, len(tokens))
	for token := range strings.SplitSeq(capt.Betas, ",") {
		if token = strings.TrimSpace(token); token != "" {
			wantBetas = append(wantBetas, token)
		}
	}
	for _, token := range authGatedBetas {
		if containsToken(tokens, token) && !containsToken(wantBetas, token) {
			wantBetas = append(wantBetas, token)
		}
	}
	sortedGot := slices.Sorted(slices.Values(tokens))
	sortedWant := slices.Sorted(slices.Values(wantBetas))
	if !slices.Equal(sortedGot, sortedWant) {
		t.Errorf("Anthropic-Beta disagrees with capture (auth-gated tokens allowed: %v):\n got: %v\nwant: %v",
			authGatedBetas, sortedGot, sortedWant)
	}

	// Shared x-stainless-* + browser-access + x-app. Same tuple for both
	// surfaces — reused so a regression on either surface fails here.
	assertSharedStainless(t, h)

	// Every static header the capture recorded must match byte-for-byte. This
	// is what pins values like x-stainless-package-version, which a constants-
	// only assertion cannot catch when upstream bumps it.
	for name, want := range capt.Headers {
		if slices.Contains(captureOnlyHeaders, name) {
			continue
		}
		if got := h.Get(name); got != want {
			t.Errorf("header %q disagrees with capture: got %q want %q", name, got, want)
		}
	}
}

// captureOnlyHeaders are recorded for reference but not emitted by the egress
// hook: the HTTP client or CPA owns them, not the plugin.
// anthropic-beta is excluded because it is auth-dependent and already compared
// above with the authGatedBetas allowance.
var captureOnlyHeaders = []string{"accept", "content-type", "x-stainless-timeout", "anthropic-beta"}

// assertToolsShape pins the tools[] shape against the capture. 2.1.268 sends no
// cache_control on any tool, so a breakpoint the plugin adds is a positive
// discriminator — the one thing an impersonator must never emit alone.
func assertToolsShape(t *testing.T, tc integrationCase, capt capture) {
	t.Helper()
	if capt.ToolsShape.Count == 0 {
		t.Skipf("%s: capture recorded no tools_shape", tc.name)
	}

	tools := make([]string, 0, capt.ToolsShape.Count)
	for i := range capt.ToolsShape.Count {
		tools = append(tools, fmt.Sprintf(`{"name":"tool_%d","input_schema":{"type":"object"}}`, i))
	}
	body := []byte(`{"model":"claude-sonnet-4","tools":[` + strings.Join(tools, ",") + `],"messages":[]}`)

	out := applyToolNameRewriteToBody(body, nil, defaultConfig().CacheBreakpoints)

	withCC := 0
	gjson.GetBytes(out, "tools").ForEach(func(_, tool gjson.Result) bool {
		if tool.Get("cache_control").Exists() {
			withCC++
		}
		return true
	})
	if withCC != capt.ToolsShape.WithCacheControl {
		t.Errorf("%s: %d tools carry cache_control, but the real CLI sent %d — "+
			"emitting one is a positive fingerprint discriminator",
			tc.name, withCC, capt.ToolsShape.WithCacheControl)
	}
}
