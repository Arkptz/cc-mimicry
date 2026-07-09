package main

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	// defaultCacheControlTTL is the ephemeral cache breakpoint TTL, matching the
	// real Claude Code CLI ("1h").
	defaultCacheControlTTL = "1h"

	// claudeCodeSystemPrompt is the identity block the real CLI always sends.
	claudeCodeSystemPrompt = "You are Claude Code, Anthropic's official CLI for Claude."

	// defaultSystemExpansion is a neutral expansion paragraph appended so the
	// system block count/volume approaches the real CLI without polluting the
	// proxied user's behavior with tool-specific instructions.
	defaultSystemExpansion = "You are an interactive CLI tool that helps users with software engineering tasks. Use the instructions below and the tools available to you to assist the user."

	// claudeCodeUserAgent is the CLI user-agent fingerprint.
	claudeCodeUserAgent = "claude-cli/2.1.92 (external, cli)"
)

// claudeCodeBetas is the set of Claude Code beta flags the CLI sends.
var claudeCodeBetas = []string{
	"claude-code-20250219",
	"oauth-2025-04-20",
	"interleaved-thinking-2025-05-14",
	"fine-grained-tool-streaming-2025-05-14",
}

// anthropicBetaContextManagementToken gates the context_management request field.
const anthropicBetaContextManagementToken = "context-management-2025-06-27"

// betaTokensContain reports whether a comma-separated anthropic-beta header
// contains token (whitespace-tolerant, case-sensitive as Anthropic tokens are).
func betaTokensContain(header, token string) bool {
	if header == "" || token == "" {
		return false
	}
	for part := range strings.SplitSeq(header, ",") {
		if strings.TrimSpace(part) == token {
			return true
		}
	}
	return false
}

// applyRequestMimicry runs the full forward transform pipeline on an Anthropic
// /v1/messages request body. Order matters (mirrors sub2api transform_request):
//  1. system rewrite (3-block + relocate original system into messages)
//  2. fingerprint fill (temperature/max_tokens/context_management)
//  3. tool-name obfuscation + last-tool cache breakpoint
//
// contextMgmtEnabled reports whether the effective anthropic-beta header carries
// the context-management token, which gates the context_management field
// (Anthropic returns 400 "Extra inputs are not permitted" otherwise).
//
// The tool rewrite map is returned so the caller can stash it for the reverse
// pass. rw is nil when nothing was renamed.
func applyRequestMimicry(body []byte, cfg pluginConfig, contextMgmtEnabled bool) ([]byte, *toolNameRewrite) {
	if len(body) == 0 {
		return body, nil
	}

	if cfg.InjectSystemPrompt {
		body = rewriteSystemForClaudeCode(body, cfg)
	}
	if cfg.FillFingerprint {
		body = fillRequestFingerprint(body, contextMgmtEnabled)
	}

	var rw *toolNameRewrite
	if cfg.ObfuscateToolNames {
		rw = buildToolNameRewriteFromBody(body)
		body = applyToolNameRewriteToBody(body, rw, cfg.CacheBreakpoints)
	} else if cfg.CacheBreakpoints {
		body = applyToolsLastCacheBreakpoint(body)
	}
	return body, rw
}

// rewriteSystemForClaudeCode rebuilds system into the CLI's 3-block form and
// relocates the original system prompt into a user/assistant message pair.
//
//	[0] billing attribution block (cc_entrypoint=cli; billing marker)
//	[1] "You are Claude Code..." identity block
//	[2] neutral expansion block with a cache_control breakpoint
//
// If the system already looks like Claude Code (identity prefix present) it is
// left untouched — we must not double-wrap.
func rewriteSystemForClaudeCode(body []byte, cfg pluginConfig) []byte {
	systemResult := gjson.GetBytes(body, "system")
	originalSystemText := extractSystemText(systemResult)

	if hasClaudeCodePrefix(originalSystemText) {
		return body
	}

	expansion := cfg.SystemExpansion
	if strings.TrimSpace(expansion) == "" {
		expansion = defaultSystemExpansion
	}

	blocks := buildClaudeCodeSystemBlocks(expansion)
	next, err := sjson.SetRawBytes(body, "system", blocks)
	if err != nil {
		return body
	}
	body = next

	// Relocate the original system prompt into the message history so the model
	// still receives the user's instructions.
	ccTrimmed := strings.TrimSpace(claudeCodeSystemPrompt)
	if originalSystemText != "" && originalSystemText != ccTrimmed {
		body = prependSystemAsMessages(body, originalSystemText)
	}
	return body
}

// extractSystemText flattens a string or array system field into plain text.
func extractSystemText(system gjson.Result) string {
	if !system.Exists() {
		return ""
	}
	if system.Type == gjson.String {
		return strings.TrimSpace(system.String())
	}
	if system.IsArray() {
		parts := make([]string, 0)
		system.ForEach(func(_, item gjson.Result) bool {
			if text := item.Get("text").String(); strings.TrimSpace(text) != "" {
				parts = append(parts, text)
			}
			return true
		})
		return strings.Join(parts, "\n\n")
	}
	return ""
}

func hasClaudeCodePrefix(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), "You are Claude Code")
}

// buildClaudeCodeSystemBlocks returns the raw JSON array for the 3-block system.
func buildClaudeCodeSystemBlocks(expansion string) []byte {
	billing := fmt.Sprintf("cc_version=%s; cc_entrypoint=cli;", cliVersion())
	// [2] carries the ephemeral cache breakpoint (stable cache prefix).
	arr := `[]`
	arr, _ = sjsonSetRawString(arr, "-1", jsonTextBlock(billing, false))
	arr, _ = sjsonSetRawString(arr, "-1", jsonTextBlock(claudeCodeSystemPrompt, false))
	arr, _ = sjsonSetRawString(arr, "-1", jsonTextBlock(expansion, true))
	return []byte(arr)
}

// jsonTextBlock builds one Anthropic system text block, optionally with an
// ephemeral cache_control breakpoint.
func jsonTextBlock(text string, withCache bool) string {
	block := `{"type":"text"}`
	block, _ = sjson.SetRaw(block, "text", jsonString(text))
	if withCache {
		block, _ = sjson.SetRaw(block, "cache_control", fmt.Sprintf(`{"type":"ephemeral","ttl":%q}`, defaultCacheControlTTL))
	}
	return block
}

// prependSystemAsMessages inserts a user/assistant pair carrying the original
// system prompt at the head of messages[].
func prependSystemAsMessages(body []byte, originalSystemText string) []byte {
	instr := `{"role":"user"}`
	instr, _ = sjson.SetRaw(instr, "content", `[{"type":"text","text":`+jsonString("[System Instructions]\n"+originalSystemText)+`}]`)
	ack := `{"role":"assistant"}`
	ack, _ = sjson.SetRaw(ack, "content", `[{"type":"text","text":`+jsonString("Understood. I will follow these instructions.")+`}]`)

	// Prepend by setting the two head slots then shifting is awkward with sjson;
	// rebuild messages array with the pair in front.
	existing := gjson.GetBytes(body, "messages")
	rebuilt := "[" + instr + "," + ack
	if existing.IsArray() {
		existing.ForEach(func(_, msg gjson.Result) bool {
			rebuilt += "," + msg.Raw
			return true
		})
	}
	rebuilt += "]"
	if next, err := sjson.SetRawBytes(body, "messages", []byte(rebuilt)); err == nil {
		body = next
	}
	return body
}

// fillRequestFingerprint fills fields the real CLI always sends so the payload
// is byte-shape consistent with it.
//
// context_management is only injected when contextMgmtEnabled is true. Anthropic
// rejects the field with 400 "context_management: Extra inputs are not permitted"
// unless the request's anthropic-beta header carries context-management-2025-06-27.
func fillRequestFingerprint(body []byte, contextMgmtEnabled bool) []byte {
	if !gjson.GetBytes(body, "temperature").Exists() {
		if next, err := sjson.SetBytes(body, "temperature", 1); err == nil {
			body = next
		}
	}
	if !gjson.GetBytes(body, "max_tokens").Exists() {
		if next, err := sjson.SetBytes(body, "max_tokens", 128000); err == nil {
			body = next
		}
	}
	if contextMgmtEnabled && !gjson.GetBytes(body, "context_management").Exists() {
		thinkingType := gjson.GetBytes(body, "thinking.type").String()
		if thinkingType == "enabled" || thinkingType == "adaptive" {
			const cm = `{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]}`
			if next, err := sjson.SetRawBytes(body, "context_management", []byte(cm)); err == nil {
				body = next
			}
		}
	}
	return body
}

// stripContextManagementIfUnsupported removes a client-provided context_management
// field when the effective anthropic-beta header lacks the context-management
// token, mirroring sub2api sanitizeAnthropicBodyForBetaTokens. This prevents the
// 400 "Extra inputs are not permitted" when a caller (or an upstream model group
// like claude-opus-4-8) forwards the field to a model that does not accept it.
func stripContextManagementIfUnsupported(body []byte, contextMgmtEnabled bool) []byte {
	if contextMgmtEnabled || !gjson.GetBytes(body, "context_management").Exists() {
		return body
	}
	if next, err := sjson.DeleteBytes(body, "context_management"); err == nil {
		return next
	}
	return body
}

func cliVersion() string { return "2.1.92" }

// sjsonSetRawString appends a raw JSON value to an array at path "-1".
func sjsonSetRawString(arr, path, raw string) (string, error) {
	return sjson.SetRaw(arr, path, raw)
}

// jsonString safely JSON-encodes a string (with surrounding quotes).
func jsonString(s string) string {
	return string(mustJSON(s))
}
