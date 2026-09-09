package backend

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestSignHMACKnownAnswer 已知答案测试：签名向量取自后端接入协议文档
// （docs/protocol.md "测试向量"节），两边公式必须逐字节一致——这是对接契约。
func TestSignHMACKnownAnswer(t *testing.T) {
	// secret=s3cr3t appKey=app1 ts=1700000000 nonce=n1 method=GET
	// path=/v1/devices?page=2 body=空
	got := SignHMAC("s3cr3t", "app1", 1700000000, "n1", "GET", "/v1/devices?page=2", nil)
	const want = "79ae26ea8c30a0009abdddda1caa1c6674d2e6dea1b1b4dd7ae11914b58e6fd5"
	if got != want {
		t.Fatalf("sign mismatch:\n got %s\nwant %s", got, want)
	}
}

// hmacVerifier 独立编写的服务端验签桩：按后端 auth.go 的 verifyHMAC 逐字段复刻
// （头名、时间窗、签名公式、常量时间比较），不复用本包 SignHMAC 的实现路径——
// 用它验收客户端，构成"两端独立实现、线上互通"的对照。
func hmacVerifier(t *testing.T, appKey, secret string, next http.HandlerFunc) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-App-Key"); got != appKey {
			http.Error(w, "unknown app_key", http.StatusUnauthorized)
			return
		}
		ts, err := strconv.ParseInt(strings.TrimSpace(r.Header.Get("X-Timestamp")), 10, 64)
		if err != nil {
			http.Error(w, "missing/bad X-Timestamp", http.StatusUnauthorized)
			return
		}
		if d := time.Now().Unix() - ts; d > 300 || d < -300 {
			http.Error(w, "timestamp out of window", http.StatusUnauthorized)
			return
		}
		nonce := r.Header.Get("X-Nonce")
		if len(nonce) == 0 || len(nonce) > 64 {
			http.Error(w, "missing/bad X-Nonce", http.StatusUnauthorized)
			return
		}
		sign := r.Header.Get("X-Signature")
		if sign == "" {
			http.Error(w, "missing X-Signature", http.StatusUnauthorized)
			return
		}
		var body []byte
		if r.Body != nil {
			body, _ = io.ReadAll(r.Body)
		}
		// 与后端同一公式，但独立书写（sha256hex + "\n" 拼接 + hmac-sha256）。
		sum := sha256.Sum256(body)
		payload := appKey + "\n" + strconv.FormatInt(ts, 10) + "\n" + nonce + "\n" +
			r.Method + "\n" + r.URL.RequestURI() + "\n" + hex.EncodeToString(sum[:])
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(payload))
		want := hex.EncodeToString(mac.Sum(nil))
		if !hmac.Equal([]byte(want), []byte(sign)) {
			http.Error(w, "signature mismatch", http.StatusUnauthorized)
			return
		}
		next(w, r)
	})
}

// TestHMACClientAgainstVerifier 客户端 HMAC 凭据 → 独立验签桩放行；
// 错 secret → 40103 语义拒绝。覆盖 GET（manifest，空体）与 POST（invoke，有体）。
func TestHMACClientAgainstVerifier(t *testing.T) {
	next := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`[]`))
		default:
			_, _ = w.Write([]byte(`{"result":{"ok":true}}`))
		}
	}
	srv := httptest.NewServer(hmacVerifier(t, "app-demo", "s3cr3t", next))
	defer srv.Close()

	c := NewClient(srv.URL, "/v1/tools/openai.json", "/v1/tools/invoke",
		Credential{Kind: "hmac", AppKey: "app-demo", AppSecret: "s3cr3t"}, srv.Client())

	if _, err := c.FetchManifest(t.Context()); err != nil {
		t.Fatalf("FetchManifest with hmac: %v", err)
	}
	result, prob, err := c.Invoke(t.Context(), "control_device", json.RawMessage(`{"id":1,"command":{"power":1}}`))
	if err != nil || prob != nil {
		t.Fatalf("Invoke with hmac: err=%v prob=%+v", err, prob)
	}
	if string(result) != `{"ok":true}` {
		t.Fatalf("result: %s", result)
	}

	// 错 secret：验签桩必须拒绝（401）。
	bad := NewClient(srv.URL, "/v1/tools/openai.json", "/v1/tools/invoke",
		Credential{Kind: "hmac", AppKey: "app-demo", AppSecret: "wrong"}, srv.Client())
	if _, err := bad.FetchManifest(t.Context()); err == nil {
		t.Fatal("expecting 401 for wrong secret")
	}
}
