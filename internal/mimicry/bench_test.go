package mimicry

import (
	"fmt"
	"strings"
	"testing"
)

// benchToolsBody builds a request body declaring n renameable tools.
func benchToolsBody(n int) []byte {
	var b strings.Builder
	b.WriteString(`{"model":"claude-sonnet-4","messages":[{"role":"user","content":"go"}],"tools":[`)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"name":"tool_%02d_do_thing"}`, i)
	}
	b.WriteString(`]}`)
	return []byte(b.String())
}

func BenchmarkBuildToolNameRewrite(b *testing.B) {
	body := benchToolsBody(12)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = buildToolNameRewriteFromBody(body)
	}
}

func BenchmarkApplyToolNameRewrite(b *testing.B) {
	body := benchToolsBody(12)
	rw := buildToolNameRewriteFromBody(body)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = applyToolNameRewriteToBody(body, rw, true)
	}
}

// BenchmarkRestoreToolNamesInChunk measures the hot path: the byte-level reverse
// replace runs on EVERY stream chunk, so its per-chunk cost is throughput-critical.
func BenchmarkRestoreToolNamesInChunk(b *testing.B) {
	body := benchToolsBody(12)
	rw := buildToolNameRewriteFromBody(body)
	// A representative SSE-ish chunk referencing one fake name.
	fake := ""
	for _, v := range rw.forward {
		fake = v
		break
	}
	chunk := []byte(`event: content_block_delta
data: {"type":"tool_use","name":"` + fake + `","input":{"path":"x"}}

`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = restoreToolNamesInBytes(chunk, rw)
	}
}

// BenchmarkRestoreToolNamesNoMatch measures the common case where a chunk contains
// no fake name at all (most stream chunks) — this should be cheap.
func BenchmarkRestoreToolNamesNoMatch(b *testing.B) {
	body := benchToolsBody(12)
	rw := buildToolNameRewriteFromBody(body)
	chunk := []byte(`event: content_block_delta
data: {"type":"text_delta","text":"some ordinary streamed assistant text"}

`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = restoreToolNamesInBytes(chunk, rw)
	}
}
