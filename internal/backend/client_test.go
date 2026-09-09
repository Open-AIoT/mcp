package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stub 契约桩后端：manifest 与 invoke 各一个固定响应。
func stub(t *testing.T, manifestBody string, invokeStatus int, invokeBody string) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer upstream-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "openai.json"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(manifestBody))
		default: // invoke
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(invokeStatus)
			_, _ = w.Write([]byte(invokeBody))
		}
	}))
	t.Cleanup(srv.Close)
	return NewClient(srv.URL, "/v1/tools/openai.json", "/v1/tools/invoke", Credential{Kind: "bearer", Token: "upstream-token"}, srv.Client()), srv
}

func TestFetchManifest(t *testing.T) {
	c, _ := stub(t, `[{"type":"function","function":{"name":"list_devices","description":"d","parameters":{"type":"object"}}}]`, 200, `{}`)
	tools, err := c.FetchManifest(context.Background())
	if err != nil {
		t.Fatalf("FetchManifest: %v", err)
	}
	if len(tools) != 1 || tools[0].Function.Name != "list_devices" {
		t.Fatalf("unexpected tools: %+v", tools)
	}
	if string(tools[0].Function.Parameters) != `{"type":"object"}` {
		t.Fatalf("parameters not preserved verbatim: %s", tools[0].Function.Parameters)
	}
}

func TestFetchManifestEmptyParameters(t *testing.T) {
	c, _ := stub(t, `[{"type":"function","function":{"name":"ping","description":"d"}}]`, 200, `{}`)
	tools, err := c.FetchManifest(context.Background())
	if err != nil {
		t.Fatalf("FetchManifest: %v", err)
	}
	// 无参数工具补空对象 schema（规范 §4.3）。
	if !strings.Contains(string(tools[0].Function.Parameters), `"type":"object"`) {
		t.Fatalf("default parameters not filled: %s", tools[0].Function.Parameters)
	}
}

func TestInvokeSuccess(t *testing.T) {
	c, _ := stub(t, `[]`, 200, `{"result":{"data":{"sent":1}}}`)
	result, prob, err := c.Invoke(context.Background(), "control_device", json.RawMessage(`{"id":1}`))
	if err != nil || prob != nil {
		t.Fatalf("unexpected err=%v prob=%v", err, prob)
	}
	if string(result) != `{"data":{"sent":1}}` {
		t.Fatalf("result: %s", result)
	}
}

func TestInvokeProblem(t *testing.T) {
	// 后端业务错误：problem+json 必须解析为 Problem 而非 err。
	c, _ := stub(t, `[]`, 404, `{"type":"about:blank","title":"Not Found","status":404,"code":40401,"detail":"device 9 not found"}`)
	_, prob, err := c.Invoke(context.Background(), "get_device_overview", json.RawMessage(`{"id":9}`))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if prob == nil || prob.Status != 404 || prob.Code != 40401 || prob.Detail != "device 9 not found" {
		t.Fatalf("problem: %+v", prob)
	}
}

func TestInvokeUnreachable(t *testing.T) {
	// 连不上的后端 → 传输层 err（由适配器合成 503 problem）。
	c := NewClient("http://127.0.0.1:1", "/m.json", "/i", Credential{Kind: "bearer", Token: "tok"}, nil)
	_, _, err := c.Invoke(context.Background(), "x", nil)
	if err == nil {
		t.Fatal("expecting transport error")
	}
}

func TestInvokeMalformed200(t *testing.T) {
	// 200 但不是 {"result":...} 形态 → err。
	c, _ := stub(t, `[]`, 200, `{"oops":true}`)
	_, _, err := c.Invoke(context.Background(), "x", nil)
	if err == nil {
		t.Fatal("expecting malformed-response error")
	}
}
