package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Open-AIoT/mcp/internal/backend"
)

// stubBackend 打桩后端（实现 Backend 接口）。
type stubBackend struct {
	result json.RawMessage
	prob   *backend.Problem
	err    error

	gotName string
	gotArgs json.RawMessage
}

func (s *stubBackend) Invoke(_ context.Context, name string, args json.RawMessage) (json.RawMessage, *backend.Problem, error) {
	s.gotName, s.gotArgs = name, args
	return s.result, s.prob, s.err
}

// manifestFixture 两个工具的清单（含 annotations 扩展）。
func manifestFixture() []backend.Tool {
	mk := func(name, desc string, params string, ann *backend.Annotations) backend.Tool {
		var t backend.Tool
		t.Type = "function"
		t.Function.Name = name
		t.Function.Description = desc
		t.Function.Parameters = json.RawMessage(params)
		t.Function.Annotations = ann
		return t
	}
	return []backend.Tool{
		mk("list_devices", "List devices.\n\n分页列出设备。", `{"type":"object","properties":{"limit":{"type":"integer"}}}`, &backend.Annotations{ReadOnlyHint: boolp(true), IdempotentHint: boolp(true)}),
		mk("control_device", "Control a device.\n\n控制设备。", `{"type":"object","required":["id"]}`, &backend.Annotations{ReadOnlyHint: boolp(false)}),
	}
}

func boolp(b bool) *bool { return &b }

// connect 起 httptest MCP 服务并接入 SDK 客户端。
func connect(t *testing.T, be Backend) *mcp.ClientSession {
	t.Helper()
	srv, err := NewServer(manifestFixture(), be, nil, "test")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	session, err := client.Connect(context.Background(),
		&mcp.StreamableClientTransport{Endpoint: ts.URL + "/mcp", MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func TestToolsListRendering(t *testing.T) {
	session := connect(t, &stubBackend{})
	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(res.Tools) != 2 {
		t.Fatalf("tools count: %d", len(res.Tools))
	}
	// 规范 §4.4.2：按名升序。
	if res.Tools[0].Name != "control_device" || res.Tools[1].Name != "list_devices" {
		t.Fatalf("tools not sorted by name: %s, %s", res.Tools[0].Name, res.Tools[1].Name)
	}
	// 描述原样（双语拼接串透传）。
	if !strings.Contains(res.Tools[1].Description, "\n\n") {
		t.Fatalf("description lost bilingual join: %q", res.Tools[1].Description)
	}
	// annotations 透传：control_device readOnlyHint=false，list_devices idempotentHint=true。
	if res.Tools[0].Annotations == nil || res.Tools[0].Annotations.ReadOnlyHint != false {
		t.Fatalf("control_device annotations: %+v", res.Tools[0].Annotations)
	}
	if res.Tools[1].Annotations == nil || !res.Tools[1].Annotations.IdempotentHint {
		t.Fatalf("list_devices annotations: %+v", res.Tools[1].Annotations)
	}
	// inputSchema 原样透传。
	schema, _ := json.Marshal(res.Tools[1].InputSchema)
	if !strings.Contains(string(schema), `"limit"`) {
		t.Fatalf("inputSchema not preserved: %s", schema)
	}
}

func TestCallToolSuccess(t *testing.T) {
	be := &stubBackend{result: json.RawMessage(`{"data":{"sent":1}}`)}
	session := connect(t, be)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "control_device",
		Arguments: map[string]any{"id": 1, "command": map[string]any{"power": 1}},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected isError: %+v", res.Content)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if text != `{"data":{"sent":1}}` {
		t.Fatalf("content: %s", text)
	}
	// 后端收到的 name/arguments 原样。
	if be.gotName != "control_device" || !strings.Contains(string(be.gotArgs), `"power":1`) {
		t.Fatalf("forwarded: name=%s args=%s", be.gotName, be.gotArgs)
	}
}

func TestCallToolExecutionError(t *testing.T) {
	// 后端 problem → isError:true + problem JSON 文本（规范 §8.2）。
	be := &stubBackend{prob: &backend.Problem{Type: "about:blank", Title: "Not Found", Status: 404, Code: 40401, Detail: "device 9 not found"}}
	session := connect(t, be)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "list_devices",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool should not return protocol error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expecting isError=true")
	}
	text := res.Content[0].(*mcp.TextContent).Text
	var p map[string]any
	if err := json.Unmarshal([]byte(text), &p); err != nil {
		t.Fatalf("content is not problem JSON: %v", err)
	}
	if p["code"] != float64(40401) || p["detail"] != "device 9 not found" {
		t.Fatalf("problem content: %s", text)
	}
}

func TestCallToolBackendDown(t *testing.T) {
	// 传输错误 → 合成的 503 problem（规范 §8.2：执行错误而非协议错误）。
	be := &stubBackend{err: errors.New("connection refused")}
	session := connect(t, be)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_devices"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("expecting isError=true")
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, `"status":503`) || !strings.Contains(text, "50301") {
		t.Fatalf("synthesized problem: %s", text)
	}
}

func TestCallUnknownTool(t *testing.T) {
	// 未知工具 → 协议错误（SDK 协议层 -32602），而非 isError。
	session := connect(t, &stubBackend{})
	_, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "no_such_tool"})
	if err == nil {
		t.Fatal("expecting protocol error for unknown tool")
	}
	if !strings.Contains(err.Error(), "unknown") && !strings.Contains(err.Error(), "-32602") {
		t.Fatalf("unexpected error: %v", err)
	}
}
