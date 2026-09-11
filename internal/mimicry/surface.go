// surface.go defines the two Claude Code CLI 2.1.268 surface
// profiles the plugin can impersonate. Each SurfaceProfile captures the
// per-entrypoint fingerprint tuple (agent identity and anthropic-beta set)
// extracted from real mitmproxy captures.
//
// The captures live under testdata/captures/ and are re-derived by
// scripts/recon/capture-live.sh. The shared intro bundle (system[2]) is
// byte-identical between the cli (interactive TUI) and sdk-cli (-p print)
// surfaces, so it is stored once via //go:embed and referenced by both.

package mimicry

import (
	_ "embed"
	"fmt"
)

// cliVersion is the Claude Code CLI version this plugin impersonates. It is
// embedded in the User-Agent string emitted by the egress header interceptor
// and must match the version tuple captured under testdata/captures/.
const cliVersion = "2.1.268"

// UserAgent returns the outbound User-Agent string for this surface, matching
// the real CLI 2.1.268 format: "claude-cli/<version> (external, <entrypoint>)".
func (p SurfaceProfile) UserAgent() string {
	return fmt.Sprintf("claude-cli/%s (external, %s)", cliVersion, p.Entrypoint)
}

// SurfaceProfile describes one Claude Code CLI 2.1.268 entrypoint's static
// fingerprint that the plugin owns on the body path.
//
// Header-side fingerprint (user-agent, x-stainless-*, anthropic-beta) is owned
// by the plugin's egress header interceptor (P4 hook), not by this profile.
// Betas is kept here so future callers can align header + body coherently.
type SurfaceProfile struct {
	// Entrypoint is the cc_entrypoint token emitted in the billing block
	// ("cli" or "sdk-cli") and used to key snapshots.
	Entrypoint string
	// AgentIdentifier is the exact system[1] identity block.
	AgentIdentifier string
	// Betas is the anthropic-beta token set the surface sends (cli=11, sdk=10).
	Betas []string
}

// sharedSystemIntro is the surface-invariant system[2] block (intro, security,
// System, DoingTasks, Tone and — since 2.1.268 — the Text output section)
// captured from CLI 2.1.268. It is byte-identical between cli and sdk-cli, and
// stops before the client-dynamic "# Session-specific guidance" tail.
//
//go:embed surfacedata/shared_intro.txt
var sharedSystemIntro string

// cliBetas is the ordered anthropic-beta token set for the interactive TUI
// entrypoint (11 tokens: adds redact-thinking-2026-02-12 vs sdk-cli).
var cliBetas = []string{
	"claude-code-20250219",
	"oauth-2025-04-20",
	"interleaved-thinking-2025-05-14",
	"redact-thinking-2026-02-12",
	"thinking-token-count-2026-05-13",
	"context-management-2025-06-27",
	"prompt-caching-scope-2026-01-05",
	"advisor-tool-2026-03-01",
	"advanced-tool-use-2025-11-20",
	"extended-cache-ttl-2025-04-11",
	"cache-diagnosis-2026-04-07",
}

// sdkCLIBetas is the ordered anthropic-beta token set for the -p / print
// entrypoint (10 tokens: no redact-thinking-2026-02-12).
var sdkCLIBetas = []string{
	"claude-code-20250219",
	"oauth-2025-04-20",
	"interleaved-thinking-2025-05-14",
	"thinking-token-count-2026-05-13",
	"context-management-2025-06-27",
	"prompt-caching-scope-2026-01-05",
	"advisor-tool-2026-03-01",
	"advanced-tool-use-2025-11-20",
	"extended-cache-ttl-2025-04-11",
	"cache-diagnosis-2026-04-07",
}

// CLISurface is the interactive TUI ("cli") entrypoint profile.
var CLISurface = SurfaceProfile{
	Entrypoint:      "cli",
	AgentIdentifier: "You are Claude Code, Anthropic's official CLI for Claude.",
	Betas:           cliBetas,
}

// SDKCLISurface is the -p / print ("sdk-cli") entrypoint profile.
var SDKCLISurface = SurfaceProfile{
	Entrypoint:      "sdk-cli",
	AgentIdentifier: "You are a Claude agent, built on Anthropic's Claude Agent SDK.",
	Betas:           sdkCLIBetas,
}

// resolveSurface returns the SurfaceProfile matching a config token. Empty
// (unset) and unknown values fall back to CLI, matching the config default.
func resolveSurface(name string) SurfaceProfile {
	switch name {
	case "sdk-cli":
		return SDKCLISurface
	case "cli", "":
		return CLISurface
	default:
		return CLISurface
	}
}
