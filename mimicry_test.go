package main

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

	chunk := []byte(`{"type":"tool_use","name":"sessions_list"}`)
	restored := restoreToolNamesInBytes(chunk, rw)
	if !strings.Contains(string(restored), "sessions_list") {
		t.Fatalf("reverse did not restore real name: %s", restored)
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

func TestSystemRewriteThreeBlocks(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4","system":"Be helpful.","messages":[{"role":"user","content":"hi"}]}`)
	out := rewriteSystemForClaudeCode(body, defaultConfig())
	sys := gjson.GetBytes(out, "system")
	if !sys.IsArray() || len(sys.Array()) != 3 {
		t.Fatalf("expected 3 system blocks, got: %s", sys.Raw)
	}
	if !strings.Contains(sys.Array()[1].Get("text").String(), "You are Claude Code") {
		t.Fatalf("missing identity block: %s", sys.Raw)
	}
	if sys.Array()[2].Get("cache_control.type").String() != "ephemeral" {
		t.Fatalf("expected cache breakpoint on last block: %s", sys.Raw)
	}
	// Original system relocated into messages head.
	first := gjson.GetBytes(out, "messages.0")
	if first.Get("role").String() != "user" || !strings.Contains(first.Get("content.0.text").String(), "Be helpful.") {
		t.Fatalf("original system not relocated into messages: %s", first.Raw)
	}
}

func TestSystemRewriteSkipsExistingClaudeCode(t *testing.T) {
	body := []byte(`{"system":"You are Claude Code, Anthropic's official CLI for Claude.","messages":[]}`)
	out := rewriteSystemForClaudeCode(body, defaultConfig())
	if gjson.GetBytes(out, "system").IsArray() {
		t.Fatal("must not rewrap an already-Claude-Code system")
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

func TestMergeClaudeCodeBetaPreservesContextManagement(t *testing.T) {
	merged := mergeClaudeCodeBeta("context-management-2025-06-27,some-other")
	if !betaTokensContain(merged, anthropicBetaContextManagementToken) {
		t.Fatalf("client context-management token dropped: %s", merged)
	}
	absent := mergeClaudeCodeBeta("oauth-2025-04-20")
	if betaTokensContain(absent, anthropicBetaContextManagementToken) {
		t.Fatalf("context-management token wrongly added: %s", absent)
	}
}

func TestCacheBreakpointRespectsClientTTL(t *testing.T) {
	body := []byte(`{"tools":[{"name":"a"},{"name":"b","cache_control":{"type":"ephemeral","ttl":"5m"}}]}`)
	out := applyToolsLastCacheBreakpoint(body)
	if gjson.GetBytes(out, "tools.1.cache_control.ttl").String() != "5m" {
		t.Fatalf("client ttl overwritten: %s", out)
	}
}
