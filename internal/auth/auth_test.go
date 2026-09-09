package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func testAuth() *Authenticator {
	return New(map[string]string{"good-token": "tester"})
}

func TestWrapValidToken(t *testing.T) {
	var fpInCtx string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fpInCtx = FingerprintFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer good-token")
	rec := httptest.NewRecorder()
	testAuth().Wrap(next).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	sum := sha256.Sum256([]byte("good-token"))
	if fpInCtx != hex.EncodeToString(sum[:]) {
		t.Fatalf("fingerprint in ctx: %q", fpInCtx)
	}
}

func TestWrapMissingAndBadToken(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	for _, tc := range []struct{ name, header string }{
		{"missing", ""},
		{"wrong-scheme", "Token abc"},
		{"bad-token", "Bearer nope"},
		{"empty-token", "Bearer  "},
	} {
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		if tc.header != "" {
			req.Header.Set("Authorization", tc.header)
		}
		rec := httptest.NewRecorder()
		testAuth().Wrap(next).ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status %d", tc.name, rec.Code)
		}
		// 失败响应必须是 problem+json 形态（与后端错误形态一致）。
		var p map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
			t.Fatalf("%s: not JSON: %v", tc.name, err)
		}
		for _, k := range []string{"type", "title", "status", "code", "detail"} {
			if _, ok := p[k]; !ok {
				t.Fatalf("%s: problem missing field %s: %v", tc.name, k, p)
			}
		}
	}
}
