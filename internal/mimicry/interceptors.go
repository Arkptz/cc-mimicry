package mimicry

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// rewriteStore carries the per-request tool-name rewrite map from the request
// interceptor to the response/stream interceptors. The plugin C ABI has no
// shared context object across interceptor calls, so we key the map by a stable
// per-request signature derived from the request body + headers.
//
// Entries are single-use-ish: a bounded FIFO (insertion-order eviction, not
// recency-promoting) keeps memory flat even if some requests never reach the
// response side (e.g. upstream errors before body).
var rewriteStore = newBoundedRewriteStore(2048)

// interceptRequestBefore applies the full forward mimicry before credential
// selection. This is the only forward hook we use; it runs on the Anthropic
// source format only.
func interceptRequestBefore(raw []byte) ([]byte, error) {
	var req pluginapi.RequestInterceptRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	cfg := currentConfig()

	if !isAnthropicClaudeRequest(req.SourceFormat, req.Body) {
		return okEnvelope(pluginapi.RequestInterceptResponse{})
	}

	var headerOverrides http.Header
	var clearHeaders []string
	if cfg.NormalizeHeaders {
		headerOverrides, clearHeaders = claudeCodeHeaderOverrides(req.Headers)
	}

	// The effective anthropic-beta is what we actually send: our override when
	// normalizing, otherwise the client's incoming header. context_management is
	// only valid when that header carries the context-management token.
	effectiveBeta := headerValue(req.Headers, "anthropic-beta")
	if headerOverrides != nil {
		if v := headerOverrides.Get("anthropic-beta"); v != "" {
			effectiveBeta = v
		}
	}
	contextMgmtEnabled := betaTokensContain(effectiveBeta, anthropicBetaContextManagementToken)

	newBody, rw := applyRequestMimicry(req.Body, cfg, contextMgmtEnabled)
	newBody = stripContextManagementIfUnsupported(newBody, contextMgmtEnabled)

	resp := pluginapi.RequestInterceptResponse{}
	if len(newBody) > 0 && !bytesEqual(newBody, req.Body) {
		resp.Body = newBody
	}
	resp.Headers = headerOverrides
	resp.ClearHeaders = clearHeaders

	if cfg.ObfuscateToolNames && !rw.empty() {
		rewriteStore.put(requestSignature(newBody, req.Headers), rw)
	}
	return okEnvelope(resp)
}

// interceptRequestAfter is a no-op: all transforms happen before auth. Declared
// because RequestInterceptor requires both methods; returning an empty response
// leaves the request unchanged.
func interceptRequestAfter(_ []byte) ([]byte, error) {
	return okEnvelope(pluginapi.RequestInterceptResponse{})
}

// interceptResponse reverses tool-name obfuscation on a non-streaming response.
func interceptResponse(raw []byte) ([]byte, error) {
	var req pluginapi.ResponseInterceptRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	rw := rewriteStore.get(requestSignature(req.RequestBody, req.RequestHeaders))
	// A stored map means the matching forward request WAS obfuscated, so its
	// response must be restored even if obfuscation was disabled mid-flight.
	// Only the static-prefix fallback (rw == nil) is gated on the current config.
	if rw == nil && !currentConfig().ObfuscateToolNames {
		return okEnvelope(pluginapi.ResponseInterceptResponse{})
	}
	restored := restoreToolNamesInBytes(req.Body, rw)
	resp := pluginapi.ResponseInterceptResponse{}
	if !bytesEqual(restored, req.Body) {
		resp.Body = restored
	}
	return okEnvelope(resp)
}

// interceptStreamChunk reverses tool-name obfuscation on each stream chunk.
func interceptStreamChunk(raw []byte) ([]byte, error) {
	var req pluginapi.StreamChunkInterceptRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	// The header-init call carries no payload; nothing to reverse.
	if req.ChunkIndex == pluginapi.StreamChunkHeaderInitIndex || len(req.Body) == 0 {
		return okEnvelope(pluginapi.StreamChunkInterceptResponse{})
	}
	rw := rewriteStore.get(requestSignature(req.RequestBody, req.RequestHeaders))
	// A stored map means the matching forward request WAS obfuscated, so its
	// chunks must be restored even if obfuscation was disabled mid-stream.
	if rw == nil && !currentConfig().ObfuscateToolNames {
		return okEnvelope(pluginapi.StreamChunkInterceptResponse{})
	}
	restored := restoreToolNamesInBytes(req.Body, rw)
	resp := pluginapi.StreamChunkInterceptResponse{}
	if !bytesEqual(restored, req.Body) {
		resp.Body = restored
	}
	return okEnvelope(resp)
}

// isAnthropicClaudeRequest reports whether a request is an Anthropic /v1/messages
// payload we should mimic. We only touch the native Anthropic source format so
// translated OpenAI/Gemini paths are left alone.
func isAnthropicClaudeRequest(sourceFormat string, body []byte) bool {
	switch strings.ToLower(strings.TrimSpace(sourceFormat)) {
	case "claude", "anthropic", "":
		// Empty source format is treated as native Anthropic: any non-empty body
		// is processed (per SPEC-001). Translated OpenAI/Gemini paths carry a
		// non-empty source format and fall through to the default case below.
		return len(body) > 0
	default:
		return false
	}
}

// claudeCodeHeaderOverrides returns the header replacements and header removals
// needed to look like the Claude Code CLI. It only sets values; the host merges
// these over the current request headers (ClearHeaders removes first).
func claudeCodeHeaderOverrides(current http.Header) (http.Header, []string) {
	out := http.Header{}
	out.Set("user-agent", claudeCodeUserAgent)
	out.Set("x-app", "cli")
	out.Set("anthropic-version", "2023-06-01")
	out.Set("anthropic-beta", mergeClaudeCodeBeta(headerValue(current, "anthropic-beta")))
	out.Set("x-stainless-lang", "js")
	out.Set("x-stainless-package-version", "0.60.0")
	out.Set("x-stainless-os", "Linux")
	out.Set("x-stainless-arch", "x64")
	out.Set("x-stainless-runtime", "node")
	out.Set("x-stainless-runtime-version", "v22.11.0")

	// Remove client SDK fingerprints that would contradict the CLI identity.
	stripped := []string{"x-stainless-retry-count", "x-stainless-timeout", "x-stainless-helper-method"}
	return out, stripped
}

// mergeClaudeCodeBeta builds the CLI beta header, preserving the client's
// context-management token when present so we never silently strip a caller's
// legitimate context_management capability.
func mergeClaudeCodeBeta(clientBeta string) string {
	tokens := append([]string(nil), claudeCodeBetas...)
	if betaTokensContain(clientBeta, anthropicBetaContextManagementToken) {
		tokens = append(tokens, anthropicBetaContextManagementToken)
	}
	return strings.Join(tokens, ",")
}

// headerValue returns the first value for key, tolerating a nil header.
func headerValue(h http.Header, key string) string {
	if h == nil {
		return ""
	}
	return h.Get(key)
}

// boundedRewriteStore is a tiny mutex-guarded bounded FIFO keyed by request
// signature. get() does not promote entries, so eviction is insertion-order.
type boundedRewriteStore struct {
	mu       sync.Mutex
	capacity int
	items    map[string]*toolNameRewrite
	order    []string
}

func newBoundedRewriteStore(capacity int) *boundedRewriteStore {
	return &boundedRewriteStore{capacity: capacity, items: make(map[string]*toolNameRewrite)}
}

func (s *boundedRewriteStore) put(key string, rw *toolNameRewrite) {
	if key == "" || rw.empty() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.items[key]; !ok {
		s.order = append(s.order, key)
	}
	s.items[key] = rw
	for len(s.order) > s.capacity {
		oldest := s.order[0]
		s.order = s.order[1:]
		delete(s.items, oldest)
	}
}

func (s *boundedRewriteStore) get(key string) *toolNameRewrite {
	if key == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.items[key]
}
