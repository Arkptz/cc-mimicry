package mimicry

import (
	"encoding/json"
	"testing"
)

func lifecyclePayload(t *testing.T, yaml string) []byte {
	t.Helper()
	raw, err := json.Marshal(lifecycleRequest{ConfigYAML: []byte(yaml)})
	if err != nil {
		t.Fatalf("marshal lifecycle: %v", err)
	}
	return raw
}

func TestConfigureDefaultsAllOn(t *testing.T) {
	if err := configure(nil); err != nil {
		t.Fatalf("configure(nil): %v", err)
	}
	cfg := currentConfig()
	for name, got := range map[string]bool{
		"obfuscate_tool_names": cfg.ObfuscateToolNames,
		"inject_system_prompt": cfg.InjectSystemPrompt,
		"cache_breakpoints":    cfg.CacheBreakpoints,
		"fill_fingerprint":     cfg.FillFingerprint,
		"normalize_headers":    cfg.NormalizeHeaders,
	} {
		if !got {
			t.Fatalf("default %s = false, want true", name)
		}
	}
}

func TestConfigureOmittedKeyStaysDefaultTrue(t *testing.T) {
	// Only one key set to false; the rest must remain default-true.
	raw := lifecyclePayload(t, "obfuscate_tool_names: false\n")
	if err := configure(raw); err != nil {
		t.Fatalf("configure: %v", err)
	}
	cfg := currentConfig()
	if cfg.ObfuscateToolNames {
		t.Fatal("obfuscate_tool_names should be false")
	}
	if !cfg.InjectSystemPrompt || !cfg.CacheBreakpoints || !cfg.FillFingerprint || !cfg.NormalizeHeaders {
		t.Fatalf("omitted keys were zeroed instead of staying default-true: %+v", cfg)
	}
	// Restore defaults so this test's global mutation cannot leak under -shuffle.
	t.Cleanup(func() { _ = configure(nil) })
}

func TestConfigureAllDisabled(t *testing.T) {
	raw := lifecyclePayload(t, `obfuscate_tool_names: false
inject_system_prompt: false
cache_breakpoints: false
fill_fingerprint: false
normalize_headers: false
`)
	if err := configure(raw); err != nil {
		t.Fatalf("configure: %v", err)
	}
	cfg := currentConfig()
	if cfg.ObfuscateToolNames || cfg.InjectSystemPrompt || cfg.CacheBreakpoints || cfg.FillFingerprint || cfg.NormalizeHeaders {
		t.Fatalf("all-disabled config not applied: %+v", cfg)
	}
	// Restore defaults so later tests in this package are not polluted.
	t.Cleanup(func() { _ = configure(nil) })
}

func TestConfigureSystemExpansionOverride(t *testing.T) {
	raw := lifecyclePayload(t, "system_expansion: \"custom expansion\"\n")
	if err := configure(raw); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if got := currentConfig().SystemExpansion; got != "custom expansion" {
		t.Fatalf("system_expansion = %q, want %q", got, "custom expansion")
	}
	t.Cleanup(func() { _ = configure(nil) })
}

func TestConfigureRejectsBadLifecycleJSON(t *testing.T) {
	if err := configure([]byte("{not json")); err == nil {
		t.Fatal("expected error on malformed lifecycle JSON")
	}
}

func TestConfigureRejectsBadYAML(t *testing.T) {
	raw := lifecyclePayload(t, "key: [unterminated\n")
	if err := configure(raw); err == nil {
		t.Fatal("expected error on malformed config YAML")
	}
	t.Cleanup(func() { _ = configure(nil) })
}

func TestConfigureRejectsTypeMismatchedToggle(t *testing.T) {
	// A mistyped boolean must surface an error, not silently keep the default —
	// otherwise an operator's intent to disable a stage is dropped without signal.
	// ("yes"/"no" are YAML 1.1 booleans and decode cleanly; use a non-bool value.)
	raw := lifecyclePayload(t, "obfuscate_tool_names: maybe\n")
	if err := configure(raw); err == nil {
		t.Fatal("expected error on type-mismatched boolean toggle")
	}
	t.Cleanup(func() { _ = configure(nil) })
}

func TestConfigFieldsCoverEveryToggle(t *testing.T) {
	fields := configFields()
	want := map[string]bool{
		"obfuscate_tool_names": false,
		"inject_system_prompt": false,
		"cache_breakpoints":    false,
		"fill_fingerprint":     false,
		"normalize_headers":    false,
		"system_expansion":     false,
	}
	for _, f := range fields {
		if _, ok := want[f.Name]; !ok {
			t.Fatalf("unexpected config field %q", f.Name)
		}
		want[f.Name] = true
		if f.Description == "" {
			t.Fatalf("config field %q has empty description", f.Name)
		}
	}
	for name, seen := range want {
		if !seen {
			t.Fatalf("config field %q missing from configFields()", name)
		}
	}
}
