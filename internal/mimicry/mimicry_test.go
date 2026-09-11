package mimicry

import (
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestBuildDynamicToolMap_BelowThreshold(t *testing.T) {
	names := []string{"bash", "edit", "read", "write", "search"}
	if m := buildDynamicToolMap(names); m != nil {
		t.Fatalf("expected nil dynamic map at/below threshold, got %v", m)
	}
}

func TestBuildDynamicToolMap_AboveThresholdStable(t *testing.T) {
	names := []string{"alpha", "beta", "gamma", "delta", "epsilon", "zeta"}
	a := buildDynamicToolMap(names)
	b := buildDynamicToolMap(names)
	if a == nil {
		t.Fatal("expected non-nil dynamic map above threshold")
	}
	if len(a) != 6 {
		t.Fatalf("len = %d, want 6", len(a))
	}
	for _, n := range names {
		if a[n] != b[n] {
			t.Fatalf("unstable mapping for %q: %q vs %q", n, a[n], b[n])
		}
		if a[n] == n {
			t.Fatalf("name %q was not renamed", n)
		}
	}
}

func TestStaticPrefixRewriteRoundTrip(t *testing.T) {
	body := []byte(`{"tools":[{"name":"sessions_list"},{"name":"sessions_get"}]}`)
	rw := buildToolNameRewriteFromBody(body)
	if rw.empty() {
		t.Fatal("expected non-empty rewrite for sessions_ tools")
	}
	out := applyToolNameRewriteToBody(body, rw, false)
	if strings.Contains(string(out), `"name":"sessions_list"`) {
		t.Fatalf("forward rewrite did not rename sessions_list: %s", out)
	}
	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "cc_sess_list" {
		t.Fatalf("unexpected fake name: %s", got)
	}

	// Reverse the FAKE name the forward pass actually produced (not the real
	// name), so this proves static fake -> real restoration rather than a no-op.
	fake := gjson.GetBytes(out, "tools.0.name").String()
	chunk := []byte(`{"type":"tool_use","name":"` + fake + `"}`)
	restored := restoreToolNamesInBytes(chunk, rw)
	if got := gjson.GetBytes(restored, "name").String(); got != "sessions_list" {
		t.Fatalf("reverse did not restore real name: got %q from fake %q (%s)", got, fake, restored)
	}
}

func TestServerToolsNotRenamed(t *testing.T) {
	body := []byte(`{"tools":[{"type":"web_search_20250305","name":"web_search"},{"type":"function","name":"sessions_list"}]}`)
	rw := buildToolNameRewriteFromBody(body)
	if rw == nil {
		t.Fatal("expected rewrite for the function tool")
	}
	if _, ok := rw.forward["web_search"]; ok {
		t.Fatal("server tool web_search must not be renamed")
	}
	if _, ok := rw.forward["sessions_list"]; !ok {
		t.Fatal("function tool sessions_list must be renamed")
	}
}

func TestDynamicRewriteRoundTrip(t *testing.T) {
	body := []byte(`{"tools":[{"name":"read_file"},{"name":"write_file"},{"name":"list_dir"},{"name":"run_cmd"},{"name":"grep_text"},{"name":"apply_patch"}]}`)
	rw := buildToolNameRewriteFromBody(body)
	if rw.empty() {
		t.Fatal("expected dynamic rewrite for 6 tools")
	}
	out := applyToolNameRewriteToBody(body, rw, false)
	for i, real := range []string{"read_file", "write_file", "list_dir", "run_cmd", "grep_text", "apply_patch"} {
		if got := gjson.GetBytes(out, fmt.Sprintf("tools.%d.name", i)).String(); got == real {
			t.Fatalf("tool %d name %q was not renamed", i, real)
		}
	}
	// Simulate a streamed tool_use referencing a fake name; reverse restores it.
	fake := rw.forward["read_file"]
	if fake == "" || fake == "read_file" {
		t.Fatalf("read_file not renamed: %q", fake)
	}
	chunk := []byte(`{"name":"` + fake + `"}`)
	restored := restoreToolNamesInBytes(chunk, rw)
	if !strings.Contains(string(restored), "read_file") {
		t.Fatalf("reverse failed for dynamic name %q -> %s", fake, restored)
	}
}

// TestMultiToolUseReverseExact pins the response reverse path on a single body
// carrying SEVERAL tool_use blocks at once — the shape the real non-streaming
// response takes — so the length-desc reverseOrdered ordering is exercised and
// every fake alias is restored to its exact original.
func TestMultiToolUseReverseExact(t *testing.T) {
	body := []byte(`{"tools":[{"name":"read"},{"name":"read_file"},{"name":"read_file_lines"},{"name":"write"},{"name":"grep"},{"name":"patch"}]}`)
	rw := buildToolNameRewriteFromBody(body)
	if rw.empty() {
		t.Fatal("expected dynamic rewrite for 6 tools including substring names")
	}

	reals := []string{"read", "read_file", "read_file_lines", "write", "grep", "patch"}
	blocks := make([]string, 0, len(reals))
	for _, real := range reals {
		fake, ok := rw.forward[real]
		if !ok || fake == real {
			t.Fatalf("expected %q to be renamed, got %q (ok=%v)", real, fake, ok)
		}
		blocks = append(blocks, `{"type":"tool_use","name":"`+fake+`"}`)
	}
	respBody := `{"content":[` + strings.Join(blocks, ",") + `]}`

	restored := restoreToolNamesInBytes([]byte(respBody), rw)
	names := gjson.GetBytes(restored, "content.#.name").Array()
	if len(names) != len(reals) {
		t.Fatalf("name count drift: got %d want %d in %s", len(names), len(reals), restored)
	}
	for i, real := range reals {
		if got := names[i].String(); got != real {
			t.Fatalf("block %d: got %q want exact %q (%s)", i, got, real, restored)
		}
	}
}

// TestStaticReverseIsUnconditional documents the inherited behavior (verified
// byte-identical to the pre-refactor tree) that restoreToolNamesInBytes ALWAYS
// applies the static prefix reverse, even with a nil rw. This is a deliberate
// preservation pin, not an endorsement — flagged in SPEC-001 as a known edge.
func TestStaticReverseIsUnconditional(t *testing.T) {
	chunk := []byte(`{"type":"tool_use","name":"sessions_list"}`)
	restored := restoreToolNamesInBytes(chunk, nil)
	if got := gjson.GetBytes(restored, "name").String(); got != "sessions_list" {
		t.Fatalf("static reverse with nil rw: got %q want %q", got, restored)
	}
}

// TestSystemRewriteThreeBlocks pins the 3-block surface-aware system[] output:
// [0] billing block, [1] agent identity, [2] shared intro (which since 2.1.268
// carries the former TextOutputSection), and the original system relocated into
// messages head.
func TestSystemRewriteThreeBlocks(t *testing.T) {
	for _, tc := range []struct {
		name    string
		profile SurfaceProfile
	}{
		{"cli", CLISurface},
		{"sdk-cli", SDKCLISurface},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"model":"claude-sonnet-4","system":"Be helpful.","messages":[{"role":"user","content":"hi"}]}`)
			out := rewriteSystemForClaudeCode(body, tc.profile)
			sys := gjson.GetBytes(out, "system")
			if !sys.IsArray() || len(sys.Array()) != 3 {
				t.Fatalf("expected 3 system blocks, got: %s", sys.Raw)
			}
			billing := sys.Array()[0].Get("text").String()
			if !strings.HasPrefix(billing, "x-anthropic-billing-header: cc_version=2.1.268.") {
				t.Fatalf("[0] wrong billing prefix: %q", billing)
			}
			if !strings.Contains(billing, "cc_entrypoint="+tc.profile.Entrypoint+";") ||
				!strings.Contains(billing, "cch=00000;") {
				t.Fatalf("[0] wrong billing tail: %q", billing)
			}
			if sys.Array()[0].Get("cache_control").Exists() {
				t.Fatalf("[0] must not have cache_control: %s", sys.Array()[0].Raw)
			}
			if got := sys.Array()[1].Get("text").String(); got != tc.profile.AgentIdentifier {
				t.Fatalf("[1] agent identifier mismatch: %q vs %q", got, tc.profile.AgentIdentifier)
			}
			// 2.1.268 caches both the identity and the intro block, with a bare
			// ephemeral marker: the ttl/scope qualifiers of 2.1.206 are gone.
			for _, idx := range []int{1, 2} {
				block := sys.Array()[idx]
				if block.Get("cache_control.type").String() != "ephemeral" ||
					block.Get("cache_control.scope").Exists() ||
					block.Get("cache_control.ttl").Exists() {
					t.Fatalf("[%d] wrong cache_control: %s", idx, block.Raw)
				}
			}
			// Original system relocated into messages head.
			first := gjson.GetBytes(out, "messages.0")
			if first.Get("role").String() != "user" || !strings.Contains(first.Get("content.0.text").String(), "Be helpful.") {
				t.Fatalf("original system not relocated into messages: %s", first.Raw)
			}
		})
	}
}

func TestSystemRewriteSkipsExistingClaudeCode(t *testing.T) {
	body := []byte(`{"system":"You are Claude Code, Anthropic's official CLI for Claude.","messages":[]}`)
	out := rewriteSystemForClaudeCode(body, CLISurface)
	if gjson.GetBytes(out, "system").IsArray() {
		t.Fatal("must not rewrap an already-Claude-Code system (legacy identity)")
	}
}

func TestSystemRewriteSkipsExistingSDKIdentity(t *testing.T) {
	body := []byte(`{"system":"You are a Claude agent, built on Anthropic's Claude Agent SDK.","messages":[]}`)
	out := rewriteSystemForClaudeCode(body, SDKCLISurface)
	if gjson.GetBytes(out, "system").IsArray() {
		t.Fatal("must not rewrap an already-sdk-agent system")
	}
}

func TestFingerprintFill(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4","messages":[]}`)
	out := fillRequestFingerprint(body, false)
	if gjson.GetBytes(out, "temperature").Int() != 1 {
		t.Fatal("temperature not filled")
	}
	if gjson.GetBytes(out, "max_tokens").Int() != 128000 {
		t.Fatal("max_tokens not filled")
	}
}

func TestContextManagementNotInjectedWithoutBeta(t *testing.T) {
	body := []byte(`{"model":"claude-opus-4-8","thinking":{"type":"enabled"},"messages":[]}`)
	out := fillRequestFingerprint(body, false)
	if gjson.GetBytes(out, "context_management").Exists() {
		t.Fatalf("context_management injected without the beta token: %s", out)
	}
}

func TestContextManagementInjectedWithBeta(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4","thinking":{"type":"enabled"},"messages":[]}`)
	out := fillRequestFingerprint(body, true)
	if !gjson.GetBytes(out, "context_management").Exists() {
		t.Fatalf("context_management not injected despite beta token: %s", out)
	}
}

func TestStripContextManagementWhenUnsupported(t *testing.T) {
	body := []byte(`{"model":"claude-opus-4-8","context_management":{"edits":[]},"messages":[]}`)
	out := stripContextManagementIfUnsupported(body, false)
	if gjson.GetBytes(out, "context_management").Exists() {
		t.Fatalf("client context_management not stripped when unsupported: %s", out)
	}
}

func TestKeepContextManagementWhenSupported(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4","context_management":{"edits":[]},"messages":[]}`)
	out := stripContextManagementIfUnsupported(body, true)
	if !gjson.GetBytes(out, "context_management").Exists() {
		t.Fatalf("context_management wrongly stripped when supported: %s", out)
	}
}

func TestCacheBreakpointRespectsClientTTL(t *testing.T) {
	body := []byte(`{"tools":[{"name":"a"},{"name":"b","cache_control":{"type":"ephemeral","ttl":"5m"}}]}`)
	out := applyToolsLastCacheBreakpoint(body)
	if gjson.GetBytes(out, "tools.1.cache_control.ttl").String() != "5m" {
		t.Fatalf("client ttl overwritten: %s", out)
	}
}
