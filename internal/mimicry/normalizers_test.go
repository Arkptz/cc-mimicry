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

func TestSanitizeToolNameMCPRule(t *testing.T) {
	cases := map[string]string{
		"mcp_server_fetch_fetch": "cc_mcp_server_fetch_fetch",
		"mcp_x":                  "cc_mcp_x",
		"mcp_0":                  "cc_mcp_0",
		"mcp__fetch__fetch":      "mcp__fetch__fetch",
		"MCP_x":                  "MCP_x",
		"mcp_":                   "mcp_",
		"mcp_X":                  "mcp_X",
		"mcp-x":                  "mcp-x",
		"x_mcp_y":                "x_mcp_y",
	}
	for in, want := range cases {
		if got := sanitizeToolName(in, nil); got != want {
			t.Errorf("sanitizeToolName(%q) = %q, want %q", in, got, want)
		}
	}
}

// A single mcp_ tool is below the dynamic threshold; the static rule alone must
// still rename it on the native path.
func TestNativeRequestRenamesSingleMCPTool(t *testing.T) {
	_ = configure(nil)
	body := []byte(`{"model":"claude","tools":[{"name":"mcp_server_fetch_fetch"}],"tool_choice":{"type":"tool","name":"mcp_server_fetch_fetch"},"messages":[{"role":"user","content":"go"}]}`)
	raw, _ := json.Marshal(pluginapi.RequestInterceptRequest{RequestID: "r-mcp", SourceFormat: "claude", Body: body, Headers: http.Header{}})
	out, err := interceptRequestBefore(raw)
	if err != nil {
		t.Fatalf("interceptRequestBefore: %v", err)
	}
	var resp pluginapi.RequestInterceptResponse
	decodeEnvelopeResult(t, out, &resp)
	for _, path := range []string{"tools.0.name", "tool_choice.name"} {
		if got := gjson.GetBytes(resp.Body, path).String(); got != "cc_mcp_server_fetch_fetch" {
			t.Fatalf("%s = %q", path, got)
		}
	}
}

// Stream chunks on ABI schema v3+ carry no RequestBody; the reverse must still
// find the dynamic map through the lifecycle RequestID.
func TestStreamChunkReverseWithoutRequestBody(t *testing.T) {
	_ = configure(nil)
	body := []byte(`{"model":"claude","tools":[` +
		`{"name":"alpha_tool"},{"name":"beta_tool"},{"name":"gamma_tool"},` +
		`{"name":"delta_tool"},{"name":"epsilon_tool"},{"name":"zeta_tool"}],"messages":[]}`)
	fwdRaw, _ := json.Marshal(pluginapi.RequestInterceptRequest{RequestID: "r-stream", SourceFormat: "claude", Body: body, Headers: http.Header{}})
	fwdOut, err := interceptRequestBefore(fwdRaw)
	if err != nil {
		t.Fatalf("forward: %v", err)
	}
	var fwd pluginapi.RequestInterceptResponse
	decodeEnvelopeResult(t, fwdOut, &fwd)
	fake := gjson.GetBytes(fwd.Body, "tools.0.name").String()
	if fake == "alpha_tool" || fake == "" {
		t.Fatalf("dynamic alias not applied: %q", fake)
	}

	chunkRaw, _ := json.Marshal(pluginapi.StreamChunkInterceptRequest{
		RequestID:  "r-stream",
		ChunkIndex: 0,
		Body:       []byte(`event: content_block_start` + "\n" + `data: {"content_block":{"type":"tool_use","name":"` + fake + `"}}`),
	})
	chunkOut, err := interceptStreamChunk(chunkRaw)
	if err != nil {
		t.Fatalf("stream chunk: %v", err)
	}
	var chunk pluginapi.StreamChunkInterceptResponse
	decodeEnvelopeResult(t, chunkOut, &chunk)
	if !strings.Contains(string(chunk.Body), `"name":"alpha_tool"`) {
		t.Fatalf("stream reverse failed: %s", chunk.Body)
	}
}

func TestNormalizeRequestTranslatedRoute(t *testing.T) {
	_ = configure(nil)
	body := []byte(`{"model":"claude","tools":[{"name":"mcp_searxng_web_search"},{"name":"read_file"}],` +
		`"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"mcp_searxng_web_search","input":{}}]}]}`)
	raw, _ := json.Marshal(pluginapi.RequestTransformRequest{FromFormat: "openai-response", ToFormat: "claude", Body: body})
	out, err := Handle(pluginabi.MethodRequestNormalize, raw)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	var resp pluginapi.PayloadResponse
	decodeEnvelopeResult(t, out, &resp)
	for path, want := range map[string]string{
		"tools.0.name":              "cc_mcp_searxng_web_search",
		"tools.1.name":              "read_file",
		"messages.0.content.0.name": "cc_mcp_searxng_web_search",
	} {
		if got := gjson.GetBytes(resp.Body, path).String(); got != want {
			t.Fatalf("%s = %q, want %q", path, got, want)
		}
	}
}

// The static-only path never applies dynamic aliases, even above the threshold:
// the response side has no state to reverse them with.
func TestNormalizeRequestNoDynamicAliases(t *testing.T) {
	_ = configure(nil)
	body := []byte(`{"tools":[{"name":"a1"},{"name":"a2"},{"name":"a3"},{"name":"a4"},{"name":"a5"},{"name":"a6"}]}`)
	raw, _ := json.Marshal(pluginapi.RequestTransformRequest{FromFormat: "openai", ToFormat: "claude", Body: body})
	out, err := normalizeRequest(raw)
	if err != nil {
		t.Fatalf("normalizeRequest: %v", err)
	}
	var resp pluginapi.PayloadResponse
	decodeEnvelopeResult(t, out, &resp)
	if len(resp.Body) != 0 {
		t.Fatalf("expected passthrough, got %s", resp.Body)
	}
}

func TestNormalizeRequestSkipsNativeAndDisabled(t *testing.T) {
	body := []byte(`{"tools":[{"name":"mcp_x"}]}`)
	cases := []struct {
		name, from, to, cfg string
	}{
		{"claude->claude", "claude", "claude", ""},
		{"openai->gemini", "openai", "gemini", ""},
		{"disabled", "openai", "claude", "obfuscate_tool_names: false\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfgRaw, _ := json.Marshal(lifecycleRequest{ConfigYAML: []byte(c.cfg)})
			if err := configure(cfgRaw); err != nil {
				t.Fatalf("configure: %v", err)
			}
			t.Cleanup(func() { _ = configure(nil) })
			raw, _ := json.Marshal(pluginapi.RequestTransformRequest{FromFormat: c.from, ToFormat: c.to, Body: body})
			out, err := normalizeRequest(raw)
			if err != nil {
				t.Fatalf("normalizeRequest: %v", err)
			}
			var resp pluginapi.PayloadResponse
			decodeEnvelopeResult(t, out, &resp)
			if len(resp.Body) != 0 {
				t.Fatalf("expected passthrough, got %s", resp.Body)
			}
		})
	}
}

func callNormalizeResponseBefore(t *testing.T, req pluginapi.ResponseTransformRequest) []byte {
	t.Helper()
	raw, _ := json.Marshal(req)
	out, err := Handle(pluginabi.MethodResponseNormalizeBefore, raw)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	var resp pluginapi.PayloadResponse
	decodeEnvelopeResult(t, out, &resp)
	return resp.Body
}

func TestNormalizeResponseBeforeRestoresToolUseName(t *testing.T) {
	_ = configure(nil)
	clientReq := []byte(`{"tools":[{"type":"function","name":"mcp_searxng_web_search"}]}`)
	cases := map[string]struct{ in, want string }{
		"sse line": {
			`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"t","name":"cc_mcp_searxng_web_search","input":{}}}`,
			`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"t","name":"mcp_searxng_web_search","input":{}}}`,
		},
		"message": {
			`{"content":[{"type":"text","text":"x"},{"type":"tool_use","id":"t","name":"cc_mcp_searxng_web_search","input":{}}]}`,
			`{"content":[{"type":"text","text":"x"},{"type":"tool_use","id":"t","name":"mcp_searxng_web_search","input":{}}]}`,
		},
		"sse transcript": {
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"content_block\":{\"type\":\"tool_use\",\"name\":\"cc_mcp_searxng_web_search\"}}\n\n",
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"content_block\":{\"type\":\"tool_use\",\"name\":\"mcp_searxng_web_search\"}}\n\n",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := callNormalizeResponseBefore(t, pluginapi.ResponseTransformRequest{
				FromFormat: "claude", ToFormat: "openai-response", Stream: true,
				OriginalRequest: clientReq, Body: []byte(c.in),
			})
			if string(got) != c.want {
				t.Fatalf("got  %s\nwant %s", got, c.want)
			}
		})
	}
}

// Only tool_use names are restored: text, tool input and a client tool that
// really is named cc_mcp_* are left alone.
func TestNormalizeResponseBeforeLeavesOtherContent(t *testing.T) {
	_ = configure(nil)
	clientReq := []byte(`{"tools":[{"type":"function","name":"cc_mcp_fetch"}]}`)
	for name, line := range map[string]string{
		"text delta":        `data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"run cc_mcp_fetch now"}}`,
		"input json delta":  `data: {"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"{\"cmd\":\"echo cc_mcp_fetch\"}"}}`,
		"client's own name": `data: {"type":"content_block_start","content_block":{"type":"tool_use","name":"cc_mcp_fetch"}}`,
		"native claude":     `data: {"type":"content_block_start","content_block":{"type":"tool_use","name":"cc_mcp_x"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			to := "openai"
			if name == "native claude" {
				to = "claude"
			}
			got := callNormalizeResponseBefore(t, pluginapi.ResponseTransformRequest{
				FromFormat: "claude", ToFormat: to, OriginalRequest: clientReq, Body: []byte(line),
			})
			if len(got) != 0 {
				t.Fatalf("expected passthrough, got %s", got)
			}
		})
	}
}

// An mcp_ tool whose alias is already a declared tool keeps its name, so two
// tools never end up sharing one.
func TestStaticAliasCollisionKeepsName(t *testing.T) {
	rw := buildToolNameRewrite([]byte(`{"tools":[{"name":"mcp_x"},{"name":"cc_mcp_x"},{"name":"mcp_y"}]}`), false)
	if _, ok := rw.forward["mcp_x"]; ok {
		t.Fatalf("mcp_x renamed onto a declared tool: %v", rw.forward)
	}
	if rw.forward["mcp_y"] != "cc_mcp_y" {
		t.Fatalf("mcp_y not renamed: %v", rw.forward)
	}
}

// count_tokens bodies have no messages; the system rewrite is skipped but the
// tool-name rule still applies.
func TestCountTokensBodyRenamesMCPTool(t *testing.T) {
	_ = configure(nil)
	body := []byte(`{"model":"claude","tools":[{"name":"mcp_x"}]}`)
	raw, _ := json.Marshal(pluginapi.RequestInterceptRequest{SourceFormat: "claude", Body: body, Headers: http.Header{}})
	out, err := interceptRequestBefore(raw)
	if err != nil {
		t.Fatalf("interceptRequestBefore: %v", err)
	}
	var resp pluginapi.RequestInterceptResponse
	decodeEnvelopeResult(t, out, &resp)
	if got := gjson.GetBytes(resp.Body, "tools.0.name").String(); got != "cc_mcp_x" {
		t.Fatalf("tools.0.name = %q (body %s)", got, resp.Body)
	}
	if gjson.GetBytes(resp.Body, "system").Exists() {
		t.Fatalf("count_tokens body must not get a system block: %s", resp.Body)
	}
}

// On the native path the reverse uses only this request's own map, so a client
// tool that is really named like an alias comes back unchanged.
func TestNativeReverseKeepsClientAliasLikeNames(t *testing.T) {
	_ = configure(nil)
	cases := map[string]struct {
		tools, called string
	}{
		"only cc_mcp_fetch declared": {`[{"name":"cc_mcp_fetch"}]`, "cc_mcp_fetch"},
		"mcp_x and cc_mcp_x":         {`[{"name":"mcp_x"},{"name":"cc_mcp_x"}]`, "cc_mcp_x"},
		"mcp_x and cc_mcp_xyz":       {`[{"name":"mcp_x"},{"name":"cc_mcp_xyz"}]`, "cc_mcp_xyz"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			id := "native-" + name
			body := []byte(`{"model":"claude","tools":` + c.tools + `,"messages":[{"role":"user","content":"go"}]}`)
			fwdRaw, _ := json.Marshal(pluginapi.RequestInterceptRequest{RequestID: id, SourceFormat: "claude", Body: body, Headers: http.Header{}})
			if _, err := interceptRequestBefore(fwdRaw); err != nil {
				t.Fatalf("forward: %v", err)
			}
			line := []byte(`data: {"type":"content_block_start","content_block":{"type":"tool_use","name":"` + c.called + `"}}`)
			for _, method := range []string{pluginabi.MethodResponseInterceptAfter, pluginabi.MethodResponseInterceptStreamChunk} {
				var raw []byte
				if method == pluginabi.MethodResponseInterceptAfter {
					raw, _ = json.Marshal(pluginapi.ResponseInterceptRequest{RequestID: id, Body: line})
				} else {
					raw, _ = json.Marshal(pluginapi.StreamChunkInterceptRequest{RequestID: id, Body: line})
				}
				out, err := Handle(method, raw)
				if err != nil {
					t.Fatalf("%s: %v", method, err)
				}
				var resp struct{ Body []byte }
				decodeEnvelopeResult(t, out, &resp)
				if len(resp.Body) != 0 {
					t.Fatalf("%s rewrote a client name: %s", method, resp.Body)
				}
			}
		})
	}
}

func TestReplaceWholeNames(t *testing.T) {
	cases := []struct{ in, want string }{
		{`"name":"cc_mcp_x"`, `"name":"mcp_x"`},
		{`"name":"cc_mcp_xyz"`, `"name":"cc_mcp_xyz"`},
		{`"name":"acc_mcp_x"`, `"name":"acc_mcp_x"`},
		{`call cc_mcp_x, then cc_mcp_x-2 and cc_mcp_x.`, `call mcp_x, then cc_mcp_x-2 and mcp_x.`},
		{`cc_mcp_x`, `mcp_x`},
	}
	for _, c := range cases {
		if got := string(replaceWholeNames([]byte(c.in), "cc_mcp_x", "mcp_x")); got != c.want {
			t.Errorf("replaceWholeNames(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// Self-overlapping alias with a non-name byte must not panic.
	if got := string(replaceWholeNames([]byte("cc_mcp_x cc_mcp_x cc_mcp_x"), "cc_mcp_x cc_mcp_x", "mcp_x cc_mcp_x")); got != "mcp_x cc_mcp_x cc_mcp_x" {
		t.Errorf("overlapping alias: got %q", got)
	}
}

func FuzzReplaceWholeNames(f *testing.F) {
	f.Add([]byte("cc_mcp_x cc_mcp_x cc_mcp_x"), "cc_mcp_x cc_mcp_x", "mcp_x")
	f.Add([]byte(`"name":"cc_mcp_x"`), "cc_mcp_x", "mcp_x")
	f.Fuzz(func(_ *testing.T, data []byte, from, to string) {
		_ = replaceWholeNames(data, from, to)
	})
}
