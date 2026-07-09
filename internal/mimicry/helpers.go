package mimicry

import (
	"bytes"
	"encoding/json"
	"hash/fnv"
	"net/http"
	"strconv"
)

// requestSignature derives a stable per-request key from the request body plus
// a couple of identifying headers, used to correlate the forward interceptor
// (which builds the tool-name rewrite map) with the response/stream interceptors
// (which reverse it). The body is the dominant discriminator; headers guard
// against cross-request collisions on identical bodies.
//
// Note: the body seen on the response side is the SAME request body the host
// captured pre-execution, so signatures match across the request/response pair.
func requestSignature(body []byte, headers http.Header) string {
	h := fnv.New64a()
	_, _ = h.Write(body)
	if headers != nil {
		// Include a couple of high-entropy, request-stable headers if present.
		for _, key := range []string{"X-Request-Id", "X-Stainless-Request-Id", "Anthropic-Client-Request-Id"} {
			if v := headers.Get(key); v != "" {
				_, _ = h.Write([]byte{0})
				_, _ = h.Write([]byte(key))
				_, _ = h.Write([]byte{'='})
				_, _ = h.Write([]byte(v))
			}
		}
	}
	return strconv.FormatUint(h.Sum64(), 16)
}

func bytesEqual(a, b []byte) bool { return bytes.Equal(a, b) }

// mustJSON JSON-encodes v and returns the raw bytes. Encoding a string can only
// fail on unsupported types, which never happens for the string inputs here, so
// on the impossible error path it returns an empty JSON string.
func mustJSON(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		return []byte(`""`)
	}
	return raw
}
