package mimicry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// billingDynamicsRE matches the two documented-dynamic segments of the billing
// block: the buildhash after cc_version=<ver>. (real CLI algorithm drifted for
// 2.1.206, U3) and the cch=<hex>; signed by CPA downstream. The plugin emits
// deterministic placeholders; the capture carries either the real values or the
// "<DYNAMIC>" redaction. We normalise both sides before comparison.
var billingDynamicsRE = regexp.MustCompile(
	`(cc_version=2\.1\.206\.)([A-Za-z0-9<>]+)(; cc_entrypoint=[a-z-]+; cch=)([A-Za-z0-9<>]+)`,
)

// TestGoldenSystemBlocksMatchCaptures pins the plugin's 4-block output against
// the on-disk mitmproxy captures of real Claude Code 2.1.206 traffic for both
// surfaces. Every static field must match byte-for-byte; the two documented
// dynamic tails are stripped before comparison:
//
//   - system[0].text: the "cch=<hex>" tail is CPA-signed downstream. The plugin
//     emits the placeholder "cch=00000;" and the capture's "cch=<DYNAMIC>;" is
//     the redacted per-request signature. We normalise both to the same value.
//   - system[3].text: the plugin owns only the STATIC prefix (through
//     "# Text output ..."); the capture continues with client-dynamic
//     "# Session-specific guidance" and later sections. We compare only the
//     shared static prefix.
func TestGoldenSystemBlocksMatchCaptures(t *testing.T) {
	for _, tc := range []struct {
		name    string
		profile SurfaceProfile
		file    string
	}{
		{"cli", CLISurface, "v2.1.206-cli-body.json"},
		{"sdk-cli", SDKCLISurface, "v2.1.206-interactive-body.json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join("..", "..", "testdata", "captures", tc.file)
			raw, err := os.ReadFile(path) //nolint:gosec // test-only, paths are hardcoded test fixtures
			if err != nil {
				t.Fatalf("read capture %s: %v", path, err)
			}

			blocks := buildClaudeCodeSystemBlocks(tc.profile)
			var got []map[string]any
			if err := json.Unmarshal(blocks, &got); err != nil {
				t.Fatalf("unmarshal built blocks: %v", err)
			}
			if len(got) != 4 {
				t.Fatalf("built %d system blocks, want 4", len(got))
			}

			gjson.GetBytes(raw, "system_blocks").ForEach(func(_, item gjson.Result) bool {
				idx := int(item.Get("idx").Int())
				if idx < 0 || idx >= len(got) {
					t.Fatalf("capture idx %d out of range", idx)
					return false
				}
				gotText, _ := got[idx]["text"].(string)
				wantText := item.Get("text").String()

				if idx == 0 {
					gotText = normaliseCchTail(gotText)
					wantText = normaliseCchTail(wantText)
				}
				if idx == 3 {
					wantText = staticSys3Prefix(wantText)
				}

				if gotText != wantText {
					t.Fatalf("system[%d].text mismatch (%s):\n  got:  %q\n  want: %q",
						idx, tc.name, snippet(gotText), snippet(wantText))
				}

				gotCC, _ := got[idx]["cache_control"].(map[string]any)
				wantCC := item.Get("cache_control")
				assertCacheControl(t, idx, tc.name, gotCC, wantCC)
				return true
			})
		})
	}
}

// normaliseCchTail replaces the buildhash and cch=<hex>; tail with fixed
// placeholders so the CPA signature (or the capture's <DYNAMIC> redaction) and
// the drifted 2.1.206 buildhash algorithm (U3) do not defeat the byte compare.
// Everything else on the billing line is compared as-is.
func normaliseCchTail(text string) string {
	text = billingDynamicsRE.ReplaceAllString(text, "${1}XXX${3}00000")
	if !strings.HasSuffix(text, ";") {
		text += ";"
	}
	return text
}

// staticSys3Prefix returns the STATIC prefix of system[3] up to (but excluding)
// the "# Session-specific guidance" client-dynamic tail. Whitespace before the
// marker is trimmed to keep the compare stable across newline drift.
func staticSys3Prefix(text string) string {
	before, _, found := strings.Cut(text, "# Session-specific guidance")
	if !found {
		return text
	}
	return strings.TrimRight(before, "\n")
}

func assertCacheControl(t *testing.T, idx int, surface string, got map[string]any, want gjson.Result) {
	t.Helper()
	// The capture's cache_control is null when the block has none.
	if !want.Exists() || want.Type == gjson.Null {
		if got != nil {
			t.Fatalf("system[%d] (%s) unexpected cache_control %v", idx, surface, got)
		}
		return
	}
	if got == nil {
		t.Fatalf("system[%d] (%s) missing cache_control (want %s)", idx, surface, want.Raw)
	}
	for _, k := range []string{"type", "ttl", "scope"} {
		wantV := want.Get(k)
		gotV, ok := got[k]
		if !wantV.Exists() {
			if ok {
				t.Fatalf("system[%d] (%s) cache_control unexpected key %q=%v", idx, surface, k, gotV)
			}
			continue
		}
		if !ok || gotV != wantV.String() {
			t.Fatalf("system[%d] (%s) cache_control.%s: got %v want %q", idx, surface, k, gotV, wantV.String())
		}
	}
}

func snippet(s string) string {
	const limit = 200
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "...(+" + itoa(len(s)-limit) + ")"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
