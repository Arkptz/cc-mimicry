package mimicry

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Translated routes (/v1/chat/completions, /v1/responses) never reach
// interceptRequestBefore, which only mimics native Anthropic requests, and with
// CPA's cloak disabled the host does not alias their tool names either. The
// host runs request.normalize after it translates such a request into the
// Claude format, and response.normalize_before on each Claude-format response
// line before translating it back, so these two hooks rename and restore the
// tool names there.
//
// request.normalize has no per-request context, so only the static prefix
// rules apply: their reverse needs no per-request map. The dynamic aliases
// stay on the native path, which has the lifecycle RequestID to correlate hooks with.

const claudeFormat = "claude"

// normalizeRequest applies the static tool-name rules to a request the host
// translated into the Claude format. Native Claude requests pass through: the
// host also calls this hook on claude->claude after interceptRequestBefore has
// already rewritten the body.
func normalizeRequest(raw []byte) ([]byte, error) {
	var req pluginapi.RequestTransformRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	if !isTranslatedToClaude(req.FromFormat, req.ToFormat) || !currentConfig().ObfuscateToolNames {
		return okEnvelope(pluginapi.PayloadResponse{})
	}
	rw := buildToolNameRewrite(req.Body, false)
	if rw.empty() {
		return okEnvelope(pluginapi.PayloadResponse{})
	}
	return okEnvelope(pluginapi.PayloadResponse{Body: applyToolNameRewriteToBody(req.Body, rw, false)})
}

// normalizeResponseBefore restores the static tool-name aliases in a Claude
// response before the host translates it for a non-Claude client, so the
// translator's tool lookups against the client request see the real names.
// Here the provider format is FromFormat and the client format is ToFormat.
//
// The host calls this once per SSE line with both request bodies attached, so
// the envelope is read with gjson and the request bodies are only decoded for
// a line that carries a tool_use block with an alias name.
func normalizeResponseBefore(raw []byte) ([]byte, error) {
	fields := gjson.GetManyBytes(raw, "FromFormat", "ToFormat")
	if !isTranslatedToClaude(fields[1].String(), fields[0].String()) {
		return okEnvelope(pluginapi.PayloadResponse{})
	}
	body, err := decodeBytesField(raw, "Body")
	if err != nil {
		return nil, err
	}
	if !bytes.Contains(body, []byte(`"tool_use"`)) || !containsStaticAlias(body) {
		return okEnvelope(pluginapi.PayloadResponse{})
	}
	original, err := decodeBytesField(raw, "OriginalRequest")
	if err != nil {
		return nil, err
	}
	restored := restoreToolUseNames(body, original)
	if bytesEqual(restored, body) {
		return okEnvelope(pluginapi.PayloadResponse{})
	}
	return okEnvelope(pluginapi.PayloadResponse{Body: restored})
}

// decodeBytesField reads a []byte field of a JSON-encoded host request, which
// encoding/json carries as a base64 string.
func decodeBytesField(raw []byte, field string) ([]byte, error) {
	value := gjson.GetBytes(raw, field)
	if !value.Exists() || value.Type == gjson.Null {
		return nil, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(value.String())
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", field, err)
	}
	return decoded, nil
}

func containsStaticAlias(data []byte) bool {
	for _, alias := range staticToolNameRewrites {
		if bytes.Contains(data, []byte(alias)) {
			return true
		}
	}
	return false
}

// restoreToolUseNames rewrites the name of each tool_use block in a Claude
// response from a static alias back to the real name. The body is a JSON
// message, one SSE line, or (for a non-stream call the executor served from an
// upstream stream) a whole SSE transcript. Only the name field is touched:
// text, thinking and tool input stay as the model wrote them. A name the
// client itself sent in its original request is a real tool that happens to
// look like an alias and is kept.
func restoreToolUseNames(body, originalRequest []byte) []byte {
	if gjson.ValidBytes(body) {
		return restoreToolUseNamesInJSON(body, originalRequest)
	}
	lines := bytes.SplitAfter(body, []byte("\n"))
	changed := false
	for i, line := range lines {
		restored := restoreToolUseNamesInSSELine(line, originalRequest)
		if !bytesEqual(restored, line) {
			lines[i] = restored
			changed = true
		}
	}
	if !changed {
		return body
	}
	return bytes.Join(lines, nil)
}

// restoreToolUseNamesInSSELine handles one "data: {...}" line, keeping its
// prefix and line terminator; any other line is returned unchanged.
func restoreToolUseNamesInSSELine(line, originalRequest []byte) []byte {
	trimmed := bytes.TrimRight(line, "\r\n")
	eol := line[len(trimmed):]
	rest, ok := bytes.CutPrefix(trimmed, []byte("data:"))
	if !ok {
		return line
	}
	payload := bytes.TrimLeft(rest, " \t")
	restored := restoreToolUseNamesInJSON(payload, originalRequest)
	if bytesEqual(restored, payload) {
		return line
	}
	prefixLen := len(trimmed) - len(payload)
	out := make([]byte, 0, prefixLen+len(restored)+len(eol))
	out = append(out, trimmed[:prefixLen]...)
	out = append(out, restored...)
	return append(out, eol...)
}

func restoreToolUseNamesInJSON(payload, originalRequest []byte) []byte {
	if !gjson.ValidBytes(payload) {
		return payload
	}
	root := gjson.ParseBytes(payload)
	out := payload
	restore := func(path string, block gjson.Result) {
		if block.Get("type").String() != "tool_use" {
			return
		}
		name := block.Get("name").String()
		realName := unaliasToolName(name)
		if realName == name || bytes.Contains(originalRequest, mustJSON(name)) {
			return
		}
		if next, err := sjson.SetBytes(out, path, realName); err == nil {
			out = next
		}
	}
	if block := root.Get("content_block"); block.Exists() {
		restore("content_block.name", block)
	}
	root.Get("content").ForEach(func(key, block gjson.Result) bool {
		restore(fmt.Sprintf("content.%d.name", key.Int()), block)
		return true
	})
	return out
}

// unaliasToolName maps a static alias back to its real name; any other name
// is returned unchanged.
func unaliasToolName(name string) string {
	for prefix, alias := range staticToolNameRewrites {
		if strings.HasPrefix(name, alias) {
			return prefix + name[len(alias):]
		}
	}
	return name
}

// isTranslatedToClaude reports whether a payload goes from a non-Claude client
// format to the Claude provider format.
func isTranslatedToClaude(clientFormat, providerFormat string) bool {
	if !strings.EqualFold(strings.TrimSpace(providerFormat), claudeFormat) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(clientFormat)) {
	case claudeFormat, "anthropic", "":
		return false
	default:
		return true
	}
}
