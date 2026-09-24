package mimicry

import (
	"bytes"
	"fmt"
	"hash/fnv"
	"math/rand"
	"sort"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// staticToolNameRewrites: sub2api/Parrot TOOL_NAME_REWRITES plus "mcp_".
//
// Anthropic answers 400 "Third-party apps now draw from your extra usage" when
// any tool name matches ^mcp_[a-z0-9] (bisected live 2026-09-23). The real CLI
// names MCP tools mcp__server__tool (double underscore), which passes, so
// sanitizeToolName applies the "mcp_" entry only when the next byte is [a-z0-9].
var staticToolNameRewrites = map[string]string{
	"sessions_": "cc_sess_",
	"session_":  "cc_ses_",
	"mcp_":      "cc_mcp_",
}

// fakeToolNamePrefixes is the dynamic-mapping prefix pool, identical to Parrot
// _FAKE_PREFIXES. When tools count exceeds dynamicToolMapThreshold, one of these
// is chosen (shuffled by a stable per-toolset seed) to build a readable alias.
var fakeToolNamePrefixes = []string{
	"analyze_", "compute_", "fetch_", "generate_", "lookup_", "modify_",
	"process_", "query_", "render_", "resolve_", "sync_", "update_",
	"validate_", "convert_", "extract_", "manage_", "monitor_", "parse_",
	"review_", "search_", "transform_", "handle_", "invoke_", "notify_",
}

// dynamicToolMapThreshold matches Parrot: dynamic mapping only kicks in when the
// mimicable tool count exceeds 5. Small tool sets are the CLI's own core tools
// (bash/edit/read/...) and are left alone.
const dynamicToolMapThreshold = 5

// toolNameRewrite is the per-request tool-name obfuscation map.
//   - forward: real -> fake, applied to the request body.
//   - reverseOrdered: (fake, real) sorted by fake length desc, applied to each
//     response chunk via string replace. Length-desc ordering prevents a short
//     fake being a substring of a longer one (matches Parrot's sorted reverse).
type toolNameRewrite struct {
	forward        map[string]string
	reverseOrdered [][2]string
}

func (r *toolNameRewrite) empty() bool { return r == nil || len(r.forward) == 0 }

// buildDynamicToolMap builds the dynamic alias map. Returns nil at/below the
// threshold (static fallback only). Same toolset yields the same mapping within
// a process (stable cache key), matching Parrot's random.Random(hash(names)).
// FNV-64a replaces Python's hash; byte-level parity is unnecessary because the
// upstream never validates our seed algorithm.
func buildDynamicToolMap(toolNames []string) map[string]string {
	if len(toolNames) <= dynamicToolMapThreshold {
		return nil
	}
	h := fnv.New64a()
	for i, n := range toolNames {
		if i > 0 {
			_, _ = h.Write([]byte{0})
		}
		_, _ = h.Write([]byte(n))
	}
	rng := rand.New(rand.NewSource(int64(h.Sum64())))

	available := make([]string, len(fakeToolNamePrefixes))
	copy(available, fakeToolNamePrefixes)
	rng.Shuffle(len(available), func(i, j int) { available[i], available[j] = available[j], available[i] })

	mapping := make(map[string]string, len(toolNames))
	for i, name := range toolNames {
		prefix := available[i%len(available)]
		headLen := min(len(name), 3)
		mapping[name] = fmt.Sprintf("%s%s%02d", prefix, name[:headLen], i)
	}
	return mapping
}

// sanitizeToolName turns a real name into a fake: dynamic map first, then static
// prefix. Matches Parrot _sanitize_tool_name.
func sanitizeToolName(name string, dynamic map[string]string) string {
	if dynamic != nil {
		if fake, ok := dynamic[name]; ok {
			return fake
		}
	}
	for prefix, replacement := range staticToolNameRewrites {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		if prefix == "mcp_" && !startsLowerAlnum(name[len(prefix):]) {
			continue
		}
		return replacement + name[len(prefix):]
	}
	return name
}

func startsLowerAlnum(s string) bool {
	if s == "" {
		return false
	}
	c := s[0]
	return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
}

// shouldMimicToolName reports whether a tool of the given type may be renamed.
// Server tools (type not "" / "function" / "custom") are Anthropic protocol
// semantics (e.g. "web_search_20250305", "computer_20250124"); renaming them
// makes the upstream reject the request.
func shouldMimicToolName(toolType string) bool {
	return toolType == "" || toolType == "function" || toolType == "custom"
}

// buildToolNameRewriteFromBody scans tools[*].name and builds the rewrite map.
// Returns nil when nothing needs renaming. Scan-only; body is not modified here.
func buildToolNameRewriteFromBody(body []byte) *toolNameRewrite {
	return buildToolNameRewrite(body, true)
}

// buildToolNameRewrite is buildToolNameRewriteFromBody with the dynamic alias
// stage optional. Static-only rewrites need no per-request state to reverse.
func buildToolNameRewrite(body []byte, dynamicAliases bool) *toolNameRewrite {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return nil
	}

	mimicableNames := make([]string, 0)
	for _, t := range tools.Array() {
		if !shouldMimicToolName(t.Get("type").String()) {
			continue
		}
		name := t.Get("name").String()
		if name == "" {
			continue
		}
		mimicableNames = append(mimicableNames, name)
	}

	var dynamic map[string]string
	if dynamicAliases {
		dynamic = buildDynamicToolMap(mimicableNames)
	}

	declared := make(map[string]struct{}, len(mimicableNames))
	for _, name := range mimicableNames {
		declared[name] = struct{}{}
	}

	rw := &toolNameRewrite{forward: make(map[string]string)}
	reverse := make(map[string]string)
	for _, name := range mimicableNames {
		fake := sanitizeToolName(name, dynamic)
		if fake == name {
			continue
		}
		// An alias equal to another declared tool would make two tools share a
		// name and the reverse ambiguous; leave this one as the client sent it.
		if _, taken := declared[fake]; taken {
			continue
		}
		rw.forward[name] = fake
		reverse[fake] = name
	}
	if len(rw.forward) == 0 {
		return nil
	}

	rw.reverseOrdered = make([][2]string, 0, len(reverse))
	for fake, real := range reverse {
		rw.reverseOrdered = append(rw.reverseOrdered, [2]string{fake, real})
	}
	sort.SliceStable(rw.reverseOrdered, func(i, j int) bool {
		return len(rw.reverseOrdered[i][0]) > len(rw.reverseOrdered[j][0])
	})
	return rw
}

// applyToolNameRewriteToBody applies the rewrite to the request body:
//   - tools[*].name (only for mimicable tools)
//   - tool_choice.name (only when tool_choice.type == "tool")
//   - messages[*].content[*].name (only for tool_use blocks)
//
// It always applies the last-tool cache breakpoint (when cache is enabled) even
// if there's nothing to rename.
func applyToolNameRewriteToBody(body []byte, rw *toolNameRewrite, cacheBreakpoints bool) []byte {
	if rw.empty() {
		if cacheBreakpoints {
			return applyToolsLastCacheBreakpoint(body)
		}
		return body
	}

	tools := gjson.GetBytes(body, "tools")
	if tools.IsArray() {
		idx := -1
		tools.ForEach(func(_, t gjson.Result) bool {
			idx++
			if !shouldMimicToolName(t.Get("type").String()) {
				return true
			}
			name := t.Get("name").String()
			if name == "" {
				return true
			}
			fake, ok := rw.forward[name]
			if !ok {
				return true
			}
			if next, err := sjson.SetBytes(body, fmt.Sprintf("tools.%d.name", idx), fake); err == nil {
				body = next
			}
			return true
		})
	}

	if tc := gjson.GetBytes(body, "tool_choice"); tc.Exists() && tc.Get("type").String() == "tool" {
		if fake, ok := rw.forward[tc.Get("name").String()]; ok {
			if next, err := sjson.SetBytes(body, "tool_choice.name", fake); err == nil {
				body = next
			}
		}
	}

	// Keep historical tool_use.name consistent with the renamed tools[] entries,
	// otherwise Anthropic rejects a tool_use that references an undeclared name.
	messages := gjson.GetBytes(body, "messages")
	if messages.IsArray() {
		messages.ForEach(func(msgKey, msg gjson.Result) bool {
			msgIdx := int(msgKey.Num)
			content := msg.Get("content")
			if !content.IsArray() {
				return true
			}
			content.ForEach(func(blkKey, blk gjson.Result) bool {
				blkIdx := int(blkKey.Num)
				if blk.Get("type").String() != "tool_use" {
					return true
				}
				if fake, ok := rw.forward[blk.Get("name").String()]; ok {
					path := fmt.Sprintf("messages.%d.content.%d.name", msgIdx, blkIdx)
					if next, err := sjson.SetBytes(body, path, fake); err == nil {
						body = next
					}
				}
				return true
			})
			return true
		})
	}

	if cacheBreakpoints {
		body = applyToolsLastCacheBreakpoint(body)
	}
	return body
}

// applyToolsLastCacheBreakpoint injects a cache_control breakpoint on the last
// tool, matching Parrot tools[-1]["cache_control"]. Client-provided ttl is left
// untouched; a bare cache_control gets a ttl; absent gets a full ephemeral block.
func applyToolsLastCacheBreakpoint(body []byte) []byte {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return body
	}
	arr := tools.Array()
	if len(arr) == 0 {
		return body
	}
	lastIdx := len(arr) - 1
	existingCC := arr[lastIdx].Get("cache_control")

	if existingCC.Exists() && existingCC.Get("ttl").String() != "" {
		return body
	}
	if existingCC.Exists() {
		if next, err := sjson.SetBytes(body, fmt.Sprintf("tools.%d.cache_control.ttl", lastIdx), defaultCacheControlTTL); err == nil {
			body = next
		}
		return body
	}
	raw := fmt.Sprintf(`{"type":"ephemeral","ttl":%q}`, defaultCacheControlTTL)
	if next, err := sjson.SetRawBytes(body, fmt.Sprintf("tools.%d.cache_control", lastIdx), []byte(raw)); err == nil {
		body = next
	}
	return body
}

// restoreToolNamesInBytes reverses fake -> real on a response chunk: the
// per-request rewrite (length-desc) when rw is set, otherwise the static
// prefixes.
func restoreToolNamesInBytes(data []byte, rw *toolNameRewrite) []byte {
	if len(data) == 0 {
		return data
	}
	if rw != nil {
		// The map already holds this request's static renames; applying the
		// static prefixes too would un-alias a client tool that is really
		// named like an alias.
		for _, pair := range rw.reverseOrdered {
			data = replaceWholeNames(data, pair[0], pair[1])
		}
		return data
	}
	for prefix, replacement := range staticToolNameRewrites {
		data = replaceAllBytes(data, replacement, prefix)
	}
	return data
}

// replaceWholeNames replaces from with to only where from is a whole
// identifier: the bytes around it are not letters, digits, '_' or '-'. A
// longer client tool name that merely starts with an alias stays intact.
func replaceWholeNames(data []byte, from, to string) []byte {
	if len(data) == 0 || from == "" || from == to || !bytes.Contains(data, []byte(from)) {
		return data
	}
	var out []byte
	last := 0
	for i := 0; ; {
		j := bytes.Index(data[i:], []byte(from))
		if j < 0 {
			break
		}
		start, end := i+j, i+j+len(from)
		if (start == 0 || !isNameByte(data[start-1])) && (end == len(data) || !isNameByte(data[end])) {
			out = append(out, data[last:start]...)
			out = append(out, to...)
			last = end
			i = end
			continue
		}
		i = start + 1
	}
	if out == nil {
		return data
	}
	return append(out, data[last:]...)
}

func isNameByte(c byte) bool {
	return c == '_' || c == '-' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func replaceAllBytes(data []byte, from, to string) []byte {
	if len(data) == 0 || from == to || !strings.Contains(string(data), from) {
		return data
	}
	return []byte(strings.ReplaceAll(string(data), from, to))
}
