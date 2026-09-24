package mimicry

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

// rewriteStore carries the per-request tool-name rewrite map from the request
// interceptor to the response/stream interceptors. The plugin C ABI has no
// shared context object across interceptor calls, so the map is keyed by
// rewriteKey.
//
// Entries are single-use-ish: a bounded FIFO (insertion-order eviction, not
// recency-promoting) keeps memory flat even if some requests never reach the
// response side (e.g. upstream errors before body).
var rewriteStore = newBoundedRewriteStore(2048)

// interceptRequestBefore applies the full forward mimicry before credential
// selection. This is the only forward hook we use; it runs on the Anthropic
// source format only.
//
// Header-side fingerprint (user-agent, x-stainless-*, anthropic-beta) is NOT
// applied here — that is the responsibility of the egress header hook (P4) which
// runs post-auth. On the body path we only rewrite the request body.
func interceptRequestBefore(raw []byte) ([]byte, error) {
	var req pluginapi.RequestInterceptRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	cfg := currentConfig()

	if !isAnthropicClaudeRequest(req.SourceFormat, req.Body) {
		return okEnvelope(pluginapi.RequestInterceptResponse{})
	}

	// Defensive guard: count_tokens payloads have no `messages` key. Skip the
	// system and fingerprint transforms so the plugin does not emit a billing
	// block on a non-/v1/messages call, but still apply the static tool-name
	// rules: the upstream rejects an ^mcp_[a-z0-9] tool name on any route.
	if !gjson.GetBytes(req.Body, "messages").Exists() {
		resp := pluginapi.RequestInterceptResponse{}
		if cfg.ObfuscateToolNames {
			if rw := buildToolNameRewrite(req.Body, false); !rw.empty() {
				resp.Body = applyToolNameRewriteToBody(req.Body, rw, false)
			}
		}
		return okEnvelope(resp)
	}

	// The effective anthropic-beta is what the client sent; context_management
	// is only valid when that header carries the context-management token.
	// (Header override lives in the P4 egress hook and does not run here.)
	effectiveBeta := headerValue(req.Headers, "anthropic-beta")
	contextMgmtEnabled := betaTokensContain(effectiveBeta, anthropicBetaContextManagementToken)

	profile := resolveSurface(cfg.Surface)
	newBody, rw := applyRequestMimicry(req.Body, cfg, profile, contextMgmtEnabled)
	newBody = stripContextManagementIfUnsupported(newBody, contextMgmtEnabled)

	resp := pluginapi.RequestInterceptResponse{}
	if len(newBody) > 0 && !bytesEqual(newBody, req.Body) {
		resp.Body = newBody
	}

	if cfg.ObfuscateToolNames && !rw.empty() {
		rewriteStore.put(rewriteKey(req.RequestID, newBody, req.Headers), rw)
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
	rw := rewriteStore.get(rewriteKey(req.RequestID, req.RequestBody, req.RequestHeaders))
	// A stored map means the matching forward request WAS obfuscated, so its
	// response must be restored even if obfuscation was disabled mid-flight.
	// With a RequestID a missing map means the forward pass renamed nothing, or
	// its entry was evicted from the bounded store (ADR-006); either way the
	// static-prefix fallback, which could un-alias a client's own tool name, is
	// skipped. It remains for hosts that send no RequestID, gated on the config.
	if rw == nil && (req.RequestID != "" || !currentConfig().ObfuscateToolNames) {
		return okEnvelope(pluginapi.ResponseInterceptResponse{})
	}
	restored := restoreToolNamesInBytes(req.Body, rw)
	resp := pluginapi.ResponseInterceptResponse{}
	if !bytesEqual(restored, req.Body) {
		resp.Body = restored
	}
	return okEnvelope(resp)
}

func interceptStreamChunk(raw []byte) ([]byte, error) {
	var req pluginapi.StreamChunkInterceptRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	// The header-init call carries no payload; nothing to reverse.
	if req.ChunkIndex == pluginapi.StreamChunkHeaderInitIndex || len(req.Body) == 0 {
		return okEnvelope(pluginapi.StreamChunkInterceptResponse{})
	}
	rw := rewriteStore.get(rewriteKey(req.RequestID, req.RequestBody, req.RequestHeaders))
	// A stored map means the matching forward request WAS obfuscated, so its
	// chunks must be restored even if obfuscation was disabled mid-stream.
	if rw == nil && (req.RequestID != "" || !currentConfig().ObfuscateToolNames) {
		return okEnvelope(pluginapi.StreamChunkInterceptResponse{})
	}
	restored := restoreToolNamesInBytes(req.Body, rw)
	resp := pluginapi.StreamChunkInterceptResponse{}
	if !bytesEqual(restored, req.Body) {
		resp.Body = restored
	}
	return okEnvelope(resp)
}

// rewriteKey correlates the forward hook with the reverse hooks. The host
// passes one lifecycle RequestID to request.intercept_before,
// response.intercept_after and every response.intercept_stream_chunk call; ABI
// schema v3+ stream chunks carry no RequestBody, so a body signature cannot
// match there. The signature is kept for hosts that send no RequestID.
func rewriteKey(requestID string, body []byte, headers http.Header) string {
	if requestID != "" {
		return "id:" + requestID
	}
	return requestSignature(body, headers)
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

func headerValue(h http.Header, key string) string {
	if h == nil {
		return ""
	}
	return h.Get(key)
}

// boundedRewriteStore is a tiny mutex-guarded bounded FIFO keyed by
// rewriteKey. get() does not promote entries, so eviction is insertion-order.
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
