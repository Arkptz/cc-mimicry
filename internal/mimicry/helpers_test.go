package mimicry

import (
	"net/http"
	"testing"
)

func TestRequestSignatureStableForSameBody(t *testing.T) {
	body := []byte(`{"model":"claude","messages":[]}`)
	a := requestSignature(body, nil)
	b := requestSignature(body, nil)
	if a != b {
		t.Fatalf("signature not stable: %q vs %q", a, b)
	}
}

func TestRequestSignatureDiffersByBody(t *testing.T) {
	a := requestSignature([]byte(`{"a":1}`), nil)
	b := requestSignature([]byte(`{"a":2}`), nil)
	if a == b {
		t.Fatal("different bodies produced the same signature")
	}
}

func TestRequestSignatureHeadersDisambiguateIdenticalBodies(t *testing.T) {
	body := []byte(`{"model":"claude","messages":[]}`)
	h1 := http.Header{"X-Request-Id": []string{"req-1"}}
	h2 := http.Header{"X-Request-Id": []string{"req-2"}}
	if requestSignature(body, h1) == requestSignature(body, h2) {
		t.Fatal("distinct request-ids on identical bodies collided")
	}
	// A body with no disambiguating header still matches itself.
	if requestSignature(body, http.Header{}) != requestSignature(body, nil) {
		t.Fatal("empty header should equal nil header")
	}
}

func TestRequestSignatureIgnoresIrrelevantHeaders(t *testing.T) {
	body := []byte(`{"x":1}`)
	base := requestSignature(body, nil)
	withNoise := requestSignature(body, http.Header{"User-Agent": []string{"whatever"}})
	if base != withNoise {
		t.Fatal("non-identifying headers must not change the signature")
	}
}

func TestBytesEqual(t *testing.T) {
	if !bytesEqual([]byte("x"), []byte("x")) {
		t.Fatal("equal bytes reported unequal")
	}
	if bytesEqual([]byte("x"), []byte("y")) {
		t.Fatal("unequal bytes reported equal")
	}
}

func TestMustJSONEncodesString(t *testing.T) {
	if got := string(mustJSON("hi")); got != `"hi"` {
		t.Fatalf("mustJSON = %s, want quoted string", got)
	}
}
