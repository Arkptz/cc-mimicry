package mimicry

import (
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// FuzzToolNameRoundTrip asserts the core invariant of the obfuscation layer:
// for any set of tool names, forward-rewriting a request then reverse-restoring
// a chunk that references the fake name must recover the ORIGINAL name. A broken
// reverse (e.g. a short fake being a substring of a longer one) corrupts the
// stream the client sees, so this is the property most worth fuzzing.
func FuzzToolNameRoundTrip(f *testing.F) {
	f.Add("read_file", "write_file", "list_dir", 6)
	f.Add("sessions_list", "session_get", "x", 2)
	f.Add("a", "ab", "abc", 8)

	f.Fuzz(func(t *testing.T, n1, n2, n3 string, count int) {
		names := sanitizeFuzzNames(n1, n2, n3, count)
		if len(names) == 0 {
			return
		}

		body := `{"tools":[`
		for i, name := range names {
			if i > 0 {
				body += ","
			}
			var err error
			body, err = sjson.Set(body, fmt.Sprintf("%d.name", i), name)
			if err != nil {
				return // sjson rejected the generated name; not our concern
			}
			body = strings.Replace(body, fmt.Sprintf(`"%d":`, i), "", 1)
		}
		body += `]}`

		// The hand-built body above can be malformed for exotic names; bail if so.
		if !gjson.Valid(body) {
			return
		}

		rw := buildToolNameRewriteFromBody([]byte(body))
		if rw.empty() {
			return // nothing was renamed (all names hit no rule) — invariant vacuous
		}

		// Build ONE chunk that references EVERY fake as a distinct JSON name
		// value. A single multi-fake payload is what actually exercises the
		// length-desc reverseOrdered ordering (a shorter fake being a substring
		// of a longer one); a per-fake single-name chunk never does.
		reals := make(map[string]string, len(rw.forward))
		blocks := make([]string, 0, len(rw.forward))
		for real, fake := range rw.forward {
			if fake == real {
				t.Fatalf("forward produced identity mapping for %q", real)
			}
			blocks = append(blocks, `{"type":"tool_use","name":"`+fake+`"}`)
			reals[fake] = real
		}
		chunk := `{"content":[` + strings.Join(blocks, ",") + `]}`

		restored := restoreToolNamesInBytes([]byte(chunk), rw)
		if !gjson.Valid(string(restored)) {
			t.Fatalf("restored chunk is not valid JSON: %s", restored)
		}

		// Exact assertion: every tool_use name must equal the ORIGINAL real name,
		// and no fake alias may survive as a name value.
		got := gjson.GetBytes(restored, "content.#.name").Array()
		if len(got) != len(rw.forward) {
			t.Fatalf("name count drift: got %d want %d in %s", len(got), len(rw.forward), restored)
		}
		wantReals := make(map[string]int, len(rw.forward))
		for _, real := range rw.forward {
			wantReals[real]++
		}
		for _, name := range got {
			if _, isFake := reals[name.String()]; isFake {
				t.Fatalf("fake alias %q survived restoration: %s", name.String(), restored)
			}
			wantReals[name.String()]--
		}
		for real, remaining := range wantReals {
			if remaining != 0 {
				t.Fatalf("real name %q not restored exactly (delta %d): %s", real, remaining, restored)
			}
		}
	})
}

// sanitizeFuzzNames turns raw fuzz inputs into a small, bounded, JSON-safe set of
// non-empty tool names (dedup + length clamp) so the fuzzer explores rewrite
// behavior rather than JSON-escaping edge cases.
func sanitizeFuzzNames(n1, n2, n3 string, count int) []string {
	if count < 0 {
		count = -count
	}
	count = count%20 + 1

	seed := []string{n1, n2, n3}
	seen := map[string]bool{}
	out := make([]string, 0, count)
	for i := 0; i < count; i++ {
		base := seed[i%len(seed)]
		name := fmt.Sprintf("%s_%d", base, i)
		if !isJSONSafeName(name) || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

func isJSONSafeName(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == '"' || r == '\\' {
			return false
		}
	}
	return true
}
