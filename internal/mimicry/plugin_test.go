package mimicry

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestSetVersion(t *testing.T) {
	orig := pluginVersion
	t.Cleanup(func() { pluginVersion = orig })

	SetVersion("9.9.9")
	if pluginVersion != "9.9.9" {
		t.Fatalf("SetVersion did not apply: %q", pluginVersion)
	}
	SetVersion("") // empty must be ignored
	if pluginVersion != "9.9.9" {
		t.Fatalf("empty SetVersion overwrote version: %q", pluginVersion)
	}
}

func TestHandleRegisterReturnsCapabilities(t *testing.T) {
	out, err := Handle(pluginabi.MethodPluginRegister, nil)
	if err != nil {
		t.Fatalf("register Handle: %v", err)
	}
	var env struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(out, &env); err != nil || !env.OK {
		t.Fatalf("register envelope not ok: %v (%s)", err, out)
	}
	var reg struct {
		SchemaVersion uint32             `json:"schema_version"`
		Metadata      pluginapi.Metadata `json:"metadata"`
		Capabilities  struct {
			RequestInterceptor       bool `json:"request_interceptor"`
			ResponseInterceptor      bool `json:"response_interceptor"`
			StreamChunkInterceptor   bool `json:"response_stream_interceptor"`
			RequestNormalizer        bool `json:"request_normalizer"`
			ResponseBeforeTranslator bool `json:"response_before_translator"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(env.Result, &reg); err != nil {
		t.Fatalf("unmarshal registration: %v", err)
	}
	if reg.Metadata.Name != "cc-mimicry" {
		t.Fatalf("plugin name = %q", reg.Metadata.Name)
	}
	c := reg.Capabilities
	if !c.RequestInterceptor || !c.ResponseInterceptor || !c.StreamChunkInterceptor || !c.RequestNormalizer || !c.ResponseBeforeTranslator {
		t.Fatalf("missing capability flags: %+v", reg.Capabilities)
	}
	if len(reg.Metadata.ConfigFields) == 0 {
		t.Fatal("registration exposed no config fields")
	}
}

func TestHandleReconfigureAppliesConfig(t *testing.T) {
	raw, _ := json.Marshal(lifecycleRequest{ConfigYAML: []byte("surface: sdk-cli\n")})
	out, err := Handle(pluginabi.MethodPluginReconfigure, raw)
	if err != nil {
		t.Fatalf("reconfigure: %v", err)
	}
	if !strings.Contains(string(out), `"ok":true`) {
		t.Fatalf("reconfigure not ok: %s", out)
	}
	if currentConfig().Surface != "sdk-cli" {
		t.Fatal("reconfigure did not apply surface=sdk-cli")
	}
	t.Cleanup(func() { _ = configure(nil) })
}

func TestHandleRegisterRejectsBadConfig(t *testing.T) {
	if _, err := Handle(pluginabi.MethodPluginRegister, []byte("{bad json")); err == nil {
		t.Fatal("expected error from register with malformed config")
	}
	_ = configure(nil)
}

func TestErrorEnvelopeShape(t *testing.T) {
	raw := ErrorEnvelope("some_code", "some message")
	var env struct {
		OK    bool `json:"ok"`
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.OK || env.Error.Code != "some_code" || env.Error.Message != "some message" {
		t.Fatalf("bad error envelope: %s", raw)
	}
}
