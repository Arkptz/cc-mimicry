package mimicry

// egress_headers.go implements the EgressHeaderInterceptor hook. It overrides
// the final outbound HTTP header set the CPA claude executor is about to send
// upstream so it matches the fingerprint of the real Claude Code CLI 2.1.206
// entrypoint selected by the plugin config (cli or sdk-cli).
//
// The hook runs post-auth, after CPA's applyClaudeHeaders and before request
// logging + httpClient.Do. It is headers-only — the request body / cch signature
// must not be touched here (that is done in interceptRequestBefore).

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// Static x-stainless-* fingerprint captured from CLI 2.1.206. These values are
// surface-invariant (identical between the interactive TUI and the -p / print
// entrypoint) — only the User-Agent and Anthropic-Beta set diverge per surface.
const (
	stainlessPackageVersion = "0.94.0"
	stainlessRuntimeVersion = "v26.3.0"
	stainlessOS             = "Linux"
	stainlessArch           = "x64"
	stainlessRuntime        = "node"
	stainlessLang           = "js"
	anthropicVersion        = "2023-06-01"
	anthropicBrowserAccess  = "true"
	// xAppValue is "cli" for BOTH surfaces (the sdk-cli entrypoint still sets
	// x-app=cli in real captures — only User-Agent carries the sdk-cli marker).
	xAppValue = "cli"
)

// interceptEgressHeaders builds the header override for the currently-active
// surface. The response merges over CPA's current header set: named headers are
// replaced, unlisted headers are preserved.
func interceptEgressHeaders(raw []byte) ([]byte, error) {
	var req pluginapi.EgressHeaderInterceptRequest
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
	}
	profile := resolveSurface(currentConfig().Surface)
	return okEnvelope(buildEgressHeaderResponse(profile))
}

// buildEgressHeaderResponse assembles the surface-specific header override. It
// is factored out so tests can assert per-surface headers without going through
// the JSON envelope.
func buildEgressHeaderResponse(profile SurfaceProfile) pluginapi.EgressHeaderInterceptResponse {
	return pluginapi.EgressHeaderInterceptResponse{
		Headers: http.Header{
			"User-Agent":                                {profile.UserAgent()},
			"Anthropic-Beta":                            {strings.Join(profile.Betas, ",")},
			"X-Stainless-Package-Version":               {stainlessPackageVersion},
			"X-Stainless-Runtime-Version":               {stainlessRuntimeVersion},
			"X-Stainless-Os":                            {stainlessOS},
			"X-Stainless-Arch":                          {stainlessArch},
			"X-Stainless-Runtime":                       {stainlessRuntime},
			"X-Stainless-Lang":                          {stainlessLang},
			"Anthropic-Version":                         {anthropicVersion},
			"Anthropic-Dangerous-Direct-Browser-Access": {anthropicBrowserAccess},
			"X-App": {xAppValue},
		},
	}
}
