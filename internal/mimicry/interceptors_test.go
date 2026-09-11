package mimicry

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

// decodeEnvelopeResult unwraps the ok-envelope and unmarshals its result into v.
func decodeEnvelopeResult(t *testing.T, raw []byte, v any) {
	t.Helper()
	var env struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v (raw=%s)", err, raw)
	}
	if !env.OK {
		t.Fatalf("envelope not ok: %+v", env.Error)
	}
	if v != nil && len(env.Result) > 0 {
		if err := json.Unmarshal(env.Result, v); err != nil {
			t.Fatalf("unmarshal result: %v", err)
		}
	}
}

func TestIsAnthropicClaudeRequest(t *testing.T) {
	body := []byte(`{"model":"claude"}`)
	cases := []struct {
		format string
		body   []byte
		want   bool
	}{
		{"claude", body, true},
		{"anthropic", body, true},
		{"", body, true},
		{"", nil, false},
		{"openai", body, false},
		{"gemini", body, false},
		{"  Claude  ", body, true},
	}
	for _, c := range cases {
		if got := isAnthropicClaudeRequest(c.format, c.body); got != c.want {
			t.Fatalf("isAnthropicClaudeRequest(%q, len=%d) = %v, want %v", c.format, len(c.body), got, c.want)
		}
	}
}

func TestInterceptRequestBeforeSkipsNonAnthropic(t *testing.T) {
	_ = configure(nil)
	req := pluginapi.RequestInterceptRequest{
		SourceFormat: "openai",
		Body:         []byte(`{"model":"gpt-4","tools":[{"name":"read_file"}]}`),
	}
	raw, _ := json.Marshal(req)
	out, err := interceptRequestBefore(raw)
	if err != nil {
		t.Fatalf("interceptRequestBefore: %v", err)
	}
	var resp pluginapi.RequestInterceptResponse
	decodeEnvelopeResult(t, out, &resp)
	if len(resp.Body) != 0 {
		t.Fatalf("non-anthropic request was modified: %s", resp.Body)
	}
}

// TestInterceptRequestBeforeSkipsCountTokens pins the defensive guard: a
// count_tokens payload has no `messages` key and must NOT get a billing block
// emitted (billing only makes sense on /v1/messages).
func TestInterceptRequestBeforeSkipsCountTokens(t *testing.T) {
	_ = configure(nil)
	req := pluginapi.RequestInterceptRequest{
		SourceFormat: "claude",
		Body:         []byte(`{"model":"claude-sonnet-4","system":"hi"}`),
		Headers:      http.Header{},
	}
	raw, _ := json.Marshal(req)
	out, err := interceptRequestBefore(raw)
	if err != nil {
		t.Fatalf("interceptRequestBefore: %v", err)
	}
	var resp pluginapi.RequestInterceptResponse
	decodeEnvelopeResult(t, out, &resp)
	if len(resp.Body) != 0 {
		t.Fatalf("count_tokens payload was rewritten: %s", resp.Body)
	}
}

// TestInterceptRequestBeforeEmitsSurfaceBillingBlock pins the body path: the
// forward interceptor rewrites system[] into the 4-block form with a billing
// header that carries the configured surface's entrypoint.
func TestInterceptRequestBeforeEmitsSurfaceBillingBlock(t *testing.T) {
	for _, tc := range []struct {
		name      string
		yaml      string
		wantEntry string
		wantAgent string
	}{
		{"cli default", "", "cli", CLISurface.AgentIdentifier},
		{"sdk-cli explicit", "surface: sdk-cli\n", "sdk-cli", SDKCLISurface.AgentIdentifier},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.yaml == "" {
				_ = configure(nil)
			} else {
				raw, _ := json.Marshal(lifecycleRequest{ConfigYAML: []byte(tc.yaml)})
				if err := configure(raw); err != nil {
					t.Fatalf("configure: %v", err)
				}
			}
			t.Cleanup(func() { _ = configure(nil) })

			req := pluginapi.RequestInterceptRequest{
				SourceFormat: "claude",
				Body:         []byte(`{"model":"claude-sonnet-4","messages":[{"role":"user","content":"hi"}]}`),
				Headers:      http.Header{},
			}
			raw, _ := json.Marshal(req)
			out, err := interceptRequestBefore(raw)
			if err != nil {
				t.Fatalf("interceptRequestBefore: %v", err)
			}
			var resp pluginapi.RequestInterceptResponse
			decodeEnvelopeResult(t, out, &resp)
			if len(resp.Body) == 0 {
				t.Fatal("body was not rewritten")
			}
			// Header overrides live in the P4 egress hook now — this path must NOT
			// return any Headers/ClearHeaders on the body interceptor response.
			if resp.Headers != nil {
				t.Fatalf("body interceptor must not set headers: %+v", resp.Headers)
			}
			if resp.ClearHeaders != nil {
				t.Fatalf("body interceptor must not clear headers: %+v", resp.ClearHeaders)
			}

			sys := gjson.GetBytes(resp.Body, "system")
			if !sys.IsArray() || len(sys.Array()) != 3 {
				t.Fatalf("expected 3 system blocks, got: %s", sys.Raw)
			}
			billing := sys.Array()[0].Get("text").String()
			wantPrefix := "x-anthropic-billing-header: cc_version=2.1.268."
			if !strings.HasPrefix(billing, wantPrefix) {
				t.Fatalf("billing block missing prefix: %q", billing)
			}
			if !strings.Contains(billing, "cc_entrypoint="+tc.wantEntry+";") {
				t.Fatalf("billing block wrong entrypoint: %q (want %q)", billing, tc.wantEntry)
			}
			if !strings.Contains(billing, "cch=00000;") {
				t.Fatalf("billing block missing cch placeholder: %q", billing)
			}
			if agent := sys.Array()[1].Get("text").String(); agent != tc.wantAgent {
				t.Fatalf("agent identifier wrong: got %q want %q", agent, tc.wantAgent)
			}
		})
	}
}

// TestForwardReverseRoundTripThroughHandle drives the full protocol: a forward
// request.intercept_before that obfuscates a tool name, then a
// response.intercept_after that must restore it — correlated via the request body
// signature, exactly as the host would call the plugin.
func TestForwardReverseRoundTripThroughHandle(t *testing.T) {
	_ = configure(nil)
	reqBody := []byte(`{"model":"claude-sonnet-4","tools":[{"name":"sessions_list"}],"messages":[{"role":"user","content":"go"}]}`)
	fwdReq := pluginapi.RequestInterceptRequest{SourceFormat: "claude", Body: reqBody, Headers: http.Header{}}
	fwdRaw, _ := json.Marshal(fwdReq)

	fwdOut, err := Handle(pluginabi.MethodRequestInterceptBefore, fwdRaw)
	if err != nil {
		t.Fatalf("forward Handle: %v", err)
	}
	var fwdResp pluginapi.RequestInterceptResponse
	decodeEnvelopeResult(t, fwdOut, &fwdResp)
	if len(fwdResp.Body) == 0 {
		t.Fatal("forward pass did not modify the body")
	}
	fakeName := gjson.GetBytes(fwdResp.Body, "tools.0.name").String()
	if fakeName == "sessions_list" || fakeName == "" {
		t.Fatalf("tool name not obfuscated: %q", fakeName)
	}

	// The response references the fake name; the host passes the SAME request body.
	respReq := pluginapi.ResponseInterceptRequest{
		RequestBody:    fwdResp.Body,
		RequestHeaders: http.Header{},
		Body:           []byte(`{"type":"tool_use","name":"` + fakeName + `"}`),
	}
	respRaw, _ := json.Marshal(respReq)
	respOut, err := Handle(pluginabi.MethodResponseInterceptAfter, respRaw)
	if err != nil {
		t.Fatalf("response Handle: %v", err)
	}
	var respResp pluginapi.ResponseInterceptResponse
	decodeEnvelopeResult(t, respOut, &respResp)
	if !strings.Contains(string(respResp.Body), "sessions_list") {
		t.Fatalf("reverse did not restore real name: %s", respResp.Body)
	}
}

func TestStreamChunkReverseAndHeaderInit(t *testing.T) {
	_ = configure(nil)
	// Seed a rewrite via the forward pass.
	reqBody := []byte(`{"model":"claude","tools":[{"name":"sessions_get"}],"messages":[]}`)
	fwdRaw, _ := json.Marshal(pluginapi.RequestInterceptRequest{SourceFormat: "claude", Body: reqBody, Headers: http.Header{}})
	fwdOut, _ := interceptRequestBefore(fwdRaw)
	var fwdResp pluginapi.RequestInterceptResponse
	decodeEnvelopeResult(t, fwdOut, &fwdResp)
	fake := gjson.GetBytes(fwdResp.Body, "tools.0.name").String()

	// Header-init chunk (index -1) must be a no-op.
	initReq := pluginapi.StreamChunkInterceptRequest{ChunkIndex: pluginapi.StreamChunkHeaderInitIndex, RequestBody: fwdResp.Body}
	initRaw, _ := json.Marshal(initReq)
	initOut, err := interceptStreamChunk(initRaw)
	if err != nil {
		t.Fatalf("stream init: %v", err)
	}
	var initResp pluginapi.StreamChunkInterceptResponse
	decodeEnvelopeResult(t, initOut, &initResp)
	if len(initResp.Body) != 0 {
		t.Fatal("header-init chunk should not be rewritten")
	}

	// A payload chunk referencing the fake name must be reversed.
	chunkReq := pluginapi.StreamChunkInterceptRequest{
		ChunkIndex:     0,
		RequestBody:    fwdResp.Body,
		RequestHeaders: http.Header{},
		Body:           []byte(`{"name":"` + fake + `"}`),
	}
	chunkRaw, _ := json.Marshal(chunkReq)
	chunkOut, err := interceptStreamChunk(chunkRaw)
	if err != nil {
		t.Fatalf("stream chunk: %v", err)
	}
	var chunkResp pluginapi.StreamChunkInterceptResponse
	decodeEnvelopeResult(t, chunkOut, &chunkResp)
	if !strings.Contains(string(chunkResp.Body), "sessions_get") {
		t.Fatalf("stream reverse failed: %s", chunkResp.Body)
	}
}

func TestObfuscationDisabledPassesThrough(t *testing.T) {
	raw, _ := json.Marshal(lifecycleRequest{ConfigYAML: []byte("obfuscate_tool_names: false\n")})
	if err := configure(raw); err != nil {
		t.Fatalf("configure: %v", err)
	}
	t.Cleanup(func() { _ = configure(nil) })

	respReq := pluginapi.ResponseInterceptRequest{Body: []byte(`{"name":"sessions_list"}`)}
	respRaw, _ := json.Marshal(respReq)
	out, err := interceptResponse(respRaw)
	if err != nil {
		t.Fatalf("interceptResponse: %v", err)
	}
	var resp pluginapi.ResponseInterceptResponse
	decodeEnvelopeResult(t, out, &resp)
	if len(resp.Body) != 0 {
		t.Fatal("response was rewritten while obfuscation disabled")
	}
}

func TestInterceptRequestAfterIsNoOp(t *testing.T) {
	out, err := interceptRequestAfter(nil)
	if err != nil {
		t.Fatalf("interceptRequestAfter: %v", err)
	}
	var resp pluginapi.RequestInterceptResponse
	decodeEnvelopeResult(t, out, &resp)
	if len(resp.Body) != 0 || resp.Headers != nil {
		t.Fatal("interceptRequestAfter should be a pure no-op")
	}
}

func TestHandleUnknownMethod(t *testing.T) {
	out, err := Handle("no.such.method", nil)
	if err != nil {
		t.Fatalf("Handle unknown: %v", err)
	}
	if !strings.Contains(string(out), "unknown_method") {
		t.Fatalf("expected unknown_method envelope, got %s", out)
	}
}

func TestHandleRejectsBadJSON(t *testing.T) {
	if _, err := Handle(pluginabi.MethodRequestInterceptBefore, []byte("{bad")); err == nil {
		t.Fatal("expected error on malformed intercept request")
	}
}

func TestBoundedRewriteStoreEviction(t *testing.T) {
	store := newBoundedRewriteStore(2)
	mk := func() *toolNameRewrite {
		return &toolNameRewrite{forward: map[string]string{"a": "b"}}
	}
	store.put("k1", mk())
	store.put("k2", mk())
	store.put("k3", mk()) // evicts k1 (oldest)
	if store.get("k1") != nil {
		t.Fatal("k1 should have been evicted")
	}
	if store.get("k2") == nil || store.get("k3") == nil {
		t.Fatal("k2/k3 should be retained")
	}
}

func TestBoundedRewriteStoreIgnoresEmpty(t *testing.T) {
	store := newBoundedRewriteStore(4)
	store.put("", &toolNameRewrite{forward: map[string]string{"a": "b"}})
	store.put("k", nil) // nil rewrite is empty
	if store.get("") != nil {
		t.Fatal("empty key must not be stored")
	}
	if store.get("k") != nil {
		t.Fatal("empty rewrite must not be stored")
	}
}
