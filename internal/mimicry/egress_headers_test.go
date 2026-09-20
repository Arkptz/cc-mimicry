package mimicry

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func assertSharedStainless(t *testing.T, h http.Header) {
	t.Helper()
	want := map[string]string{
		"X-Stainless-Package-Version":               "0.112.1",
		"X-Stainless-Runtime-Version":               "v26.3.0",
		"X-Stainless-Os":                            "Linux",
		"X-Stainless-Arch":                          "x64",
		"X-Stainless-Runtime":                       "node",
		"X-Stainless-Lang":                          "js",
		"Anthropic-Version":                         "2023-06-01",
		"Anthropic-Dangerous-Direct-Browser-Access": "true",
		"X-App": "cli",
	}
	for k, v := range want {
		if got := h.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

func TestBuildEgressHeaderResponse_CLISurface(t *testing.T) {
	resp := buildEgressHeaderResponse(CLISurface)
	ua := resp.Headers.Get("User-Agent")
	if !strings.Contains(ua, "(external, cli)") {
		t.Fatalf("cli User-Agent = %q, want '(external, cli)' marker", ua)
	}
	if !strings.HasPrefix(ua, "claude-cli/"+cliTargetVersion+" ") {
		t.Fatalf("cli User-Agent = %q, want claude-cli/%s prefix", ua, cliTargetVersion)
	}
	betas := strings.Split(resp.Headers.Get("Anthropic-Beta"), ",")
	if len(betas) != 11 {
		t.Fatalf("cli betas len = %d, want 11", len(betas))
	}
	if !containsToken(betas, "redact-thinking-2026-02-12") {
		t.Fatalf("cli betas missing redact-thinking-2026-02-12: %v", betas)
	}
	assertSharedStainless(t, resp.Headers)
}

func TestBuildEgressHeaderResponse_SDKCLISurface(t *testing.T) {
	resp := buildEgressHeaderResponse(SDKCLISurface)
	ua := resp.Headers.Get("User-Agent")
	if !strings.Contains(ua, "(external, sdk-cli)") {
		t.Fatalf("sdk-cli User-Agent = %q, want '(external, sdk-cli)' marker", ua)
	}
	betas := strings.Split(resp.Headers.Get("Anthropic-Beta"), ",")
	if len(betas) != 10 {
		t.Fatalf("sdk-cli betas len = %d, want 10", len(betas))
	}
	if containsToken(betas, "redact-thinking-2026-02-12") {
		t.Fatalf("sdk-cli betas MUST NOT contain redact-thinking-2026-02-12: %v", betas)
	}
	assertSharedStainless(t, resp.Headers)
}

func TestHandleEgressHeaderIntercept_UsesActiveSurface(t *testing.T) {
	t.Cleanup(func() { _ = configure(nil) })
	raw, _ := json.Marshal(lifecycleRequest{ConfigYAML: []byte("surface: sdk-cli\n")})
	if _, err := Handle(pluginabi.MethodPluginReconfigure, raw); err != nil {
		t.Fatalf("reconfigure: %v", err)
	}

	reqRaw, _ := json.Marshal(pluginapi.EgressHeaderInterceptRequest{
		Headers: http.Header{"User-Agent": {"stale"}},
		Model:   "claude-sonnet-4",
	})
	out, err := Handle(pluginabi.MethodEgressHeaderIntercept, reqRaw)
	if err != nil {
		t.Fatalf("egress intercept Handle: %v", err)
	}
	var env struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(out, &env); err != nil || !env.OK {
		t.Fatalf("envelope not ok: %v (%s)", err, out)
	}
	var resp pluginapi.EgressHeaderInterceptResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatalf("unmarshal resp: %v", err)
	}
	if !strings.Contains(resp.Headers.Get("User-Agent"), "(external, sdk-cli)") {
		t.Fatalf("active surface not honored: UA=%q", resp.Headers.Get("User-Agent"))
	}
}

func TestHandleRegister_ExposesEgressHeaderCapability(t *testing.T) {
	out, err := Handle(pluginabi.MethodPluginRegister, nil)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	var env struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(out, &env); err != nil || !env.OK {
		t.Fatalf("envelope not ok: %s", out)
	}
	var reg struct {
		Capabilities struct {
			EgressHeaderInterceptor bool `json:"egress_header_interceptor"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(env.Result, &reg); err != nil {
		t.Fatalf("unmarshal reg: %v", err)
	}
	if !reg.Capabilities.EgressHeaderInterceptor {
		t.Fatal("egress_header_interceptor capability not advertised")
	}
}

func containsToken(tokens []string, want string) bool {
	return slices.Contains(tokens, want)
}
