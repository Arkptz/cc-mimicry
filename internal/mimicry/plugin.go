// Package mimicry implements the request/response transforms for the cc-mimicry
// CLIProxyAPI plugin: it makes Claude (Anthropic) OAuth requests look like they
// came from the native Claude Code CLI. The CGO C-ABI shim lives in the root
// main package; everything protocol- and transform-related lives here so it can
// be unit-tested without cgo.
package mimicry

import (
	"encoding/json"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// pluginVersion is the plugin release version, overridden at build time by the
// root package via SetVersion (which is fed by -ldflags "-X main.pluginVersion=...").
var pluginVersion = "0.1.0"

// SetVersion overrides the reported plugin version. The root C-ABI shim calls it
// with the ldflags-injected value before the host queries registration.
func SetVersion(v string) {
	if v != "" {
		pluginVersion = v
	}
}

// Handle dispatches one host JSON method call to the matching transform and
// returns the marshaled envelope. It is the single entry point the C-ABI shim
// delegates to, so the cgo layer stays free of any plugin logic.
func Handle(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		if errConfigure := configure(request); errConfigure != nil {
			return nil, errConfigure
		}
		return okEnvelope(pluginRegistration())
	case pluginabi.MethodRequestInterceptBefore:
		return interceptRequestBefore(request)
	case pluginabi.MethodRequestInterceptAfter:
		return interceptRequestAfter(request)
	case pluginabi.MethodResponseInterceptAfter:
		return interceptResponse(request)
	case pluginabi.MethodResponseInterceptStreamChunk:
		return interceptStreamChunk(request)
	case pluginabi.MethodEgressHeaderIntercept:
		return interceptEgressHeaders(request)
	case pluginabi.MethodRequestNormalize:
		return normalizeRequest(request)
	case pluginabi.MethodResponseNormalizeBefore:
		return normalizeResponseBefore(request)
	default:
		return ErrorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

// lifecycleRequest is the plugin.register / plugin.reconfigure payload.
type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             "cc-mimicry",
			Version:          pluginVersion,
			Author:           "arkptz",
			GitHubRepository: "https://github.com/arkptz/cc-mimicry",
			ConfigFields:     configFields(),
		},
		Capabilities: registrationCapability{
			RequestInterceptor:       true,
			ResponseInterceptor:      true,
			StreamChunkInterceptor:   true,
			EgressHeaderInterceptor:  true,
			RequestNormalizer:        true,
			ResponseBeforeTranslator: true,
		},
	}
}

// registration mirrors the host rpcRegistration JSON shape.
type registration struct {
	SchemaVersion uint32                 `json:"schema_version"`
	Metadata      pluginapi.Metadata     `json:"metadata"`
	Capabilities  registrationCapability `json:"capabilities"`
}

// registrationCapability mirrors the host rpcCapabilities JSON tags for the
// capabilities this plugin implements.
type registrationCapability struct {
	RequestInterceptor       bool `json:"request_interceptor"`
	ResponseInterceptor      bool `json:"response_interceptor"`
	StreamChunkInterceptor   bool `json:"response_stream_interceptor"`
	EgressHeaderInterceptor  bool `json:"egress_header_interceptor"`
	RequestNormalizer        bool `json:"request_normalizer"`
	ResponseBeforeTranslator bool `json:"response_before_translator"`
}

// envelope mirrors pluginabi.Envelope for local marshaling.
type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func okEnvelope(v any) ([]byte, error) {
	raw, errMarshal := json.Marshal(v)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

// ErrorEnvelope marshals a failure envelope. It is exported so the C-ABI shim
// can build error responses for calls that never reach Handle (nil method or a
// Handle error).
func ErrorEnvelope(code, message string) []byte {
	// Error discarded: an envelope of two plain strings always marshals cleanly.
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	return raw
}
