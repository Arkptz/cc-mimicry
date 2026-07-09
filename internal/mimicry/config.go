package mimicry

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

// pluginConfig is the plugins.configs.cc-mimicry YAML subtree.
type pluginConfig struct {
	// ObfuscateToolNames toggles the static+dynamic tool-name rename (and its
	// reverse on responses/stream chunks). Default true.
	ObfuscateToolNames bool `yaml:"obfuscate_tool_names"`
	// InjectSystemPrompt toggles the Claude Code 3-block system prompt rewrite.
	// Default true.
	InjectSystemPrompt bool `yaml:"inject_system_prompt"`
	// CacheBreakpoints toggles the ephemeral cache_control breakpoint on the last
	// tool. Default true.
	CacheBreakpoints bool `yaml:"cache_breakpoints"`
	// FillFingerprint toggles request-body fingerprint fill (temperature,
	// max_tokens, context_management). Default true.
	FillFingerprint bool `yaml:"fill_fingerprint"`
	// NormalizeHeaders toggles Claude Code request header normalization. Default true.
	NormalizeHeaders bool `yaml:"normalize_headers"`
	// SystemExpansion overrides the neutral system-prompt expansion block text.
	// Empty uses the built-in default.
	SystemExpansion string `yaml:"system_expansion"`
}

func defaultConfig() pluginConfig {
	return pluginConfig{
		ObfuscateToolNames: true,
		InjectSystemPrompt: true,
		CacheBreakpoints:   true,
		FillFingerprint:    true,
		NormalizeHeaders:   true,
	}
}

var (
	cfgMu     sync.RWMutex
	activeCfg = defaultConfig()
)

func currentConfig() pluginConfig {
	cfgMu.RLock()
	defer cfgMu.RUnlock()
	return activeCfg
}

// configure decodes the lifecycle request and stores the active config. Absent
// or empty config keeps the all-on defaults.
func configure(raw []byte) error {
	cfg := defaultConfig()
	if len(raw) > 0 {
		var req lifecycleRequest
		if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
			return errUnmarshal
		}
		if len(req.ConfigYAML) > 0 {
			// Decode into a presence-tracking map first so that an omitted key
			// keeps its default-true rather than being zeroed to false.
			if errDecode := decodeConfigYAML(req.ConfigYAML, &cfg); errDecode != nil {
				return errDecode
			}
		}
	}
	cfgMu.Lock()
	activeCfg = cfg
	cfgMu.Unlock()
	return nil
}

// decodeConfigYAML applies only the keys explicitly present in the YAML subtree
// onto cfg (which starts from defaults), so unset booleans stay true.
func decodeConfigYAML(configYAML []byte, cfg *pluginConfig) error {
	var probe map[string]yaml.Node
	if errUnmarshal := yaml.Unmarshal(configYAML, &probe); errUnmarshal != nil {
		return errUnmarshal
	}
	for _, f := range []struct {
		key string
		dst *bool
	}{
		{"obfuscate_tool_names", &cfg.ObfuscateToolNames},
		{"inject_system_prompt", &cfg.InjectSystemPrompt},
		{"cache_breakpoints", &cfg.CacheBreakpoints},
		{"fill_fingerprint", &cfg.FillFingerprint},
		{"normalize_headers", &cfg.NormalizeHeaders},
	} {
		if err := decodeBool(probe, f.key, f.dst); err != nil {
			return fmt.Errorf("config key %q: %w", f.key, err)
		}
	}
	if node, ok := probe["system_expansion"]; ok {
		if err := node.Decode(&cfg.SystemExpansion); err != nil {
			return fmt.Errorf("config key %q: %w", "system_expansion", err)
		}
	}
	return nil
}

// decodeBool decodes a present key into dst, returning an error on a type
// mismatch so a mistyped toggle is surfaced instead of silently keeping the
// default. A missing key leaves dst untouched.
func decodeBool(m map[string]yaml.Node, key string, dst *bool) error {
	node, ok := m[key]
	if !ok {
		return nil
	}
	return node.Decode(dst)
}

func configFields() []pluginapi.ConfigField {
	return []pluginapi.ConfigField{
		{Name: "obfuscate_tool_names", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Rename tool names to Claude-Code-like aliases (reversed on responses)."},
		{Name: "inject_system_prompt", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Rewrite system into the Claude Code 3-block form; relocate original system into messages."},
		{Name: "cache_breakpoints", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Inject an ephemeral cache_control breakpoint on the last tool."},
		{Name: "fill_fingerprint", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Fill temperature/max_tokens/context_management to match the real CLI payload."},
		{Name: "normalize_headers", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Normalize request headers to Claude Code CLI values (user-agent, anthropic-beta, x-app, x-stainless-*)."},
		{Name: "system_expansion", Type: pluginapi.ConfigFieldTypeString, Description: "Override the neutral system-prompt expansion block text."},
	}
}
