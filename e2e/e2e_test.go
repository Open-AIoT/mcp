// Package e2e 端到端测试：演示后端 + 适配器 + 官方 SDK 客户端全链路。
package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Open-AIoT/mcp/internal/adapter"
	"github.com/Open-AIoT/mcp/internal/auth"
	"github.com/Open-AIoT/mcp/internal/backend"
	"github.com/Open-AIoT/mcp/internal/demobackend"
)

const (
	upstreamToken = "demo-backend-token"
	clientToken   = "demo-client-token"
)

// authTransport 给客户端请求加 Bearer 头的 RoundTripper。
type authTransport struct{ base http.RoundTripper }

func (a authTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+clientToken)
	return a.base.RoundTrip(r)
}

// stack 装配完整链路：demobackend → backend client → adapter server → auth → httptest。
func stack(t *testing.T) *mcp.ClientSession {
	t.Helper()

	// 演示后端
	demo := demobackend.New(upstreamToken)
	demoSrv := httptest.NewServer(demo)
	t.Cleanup(demoSrv.Close)

	// 适配器
	be := backend.NewClient(demoSrv.URL, "/v1/tools/openai.json", "/v1/tools/invoke", backend.Credential{Kind: "bearer", Token: upstreamToken}, demoSrv.Client())
	tools, err := be.FetchManifest(context.Background())
	if err != nil {
		t.Fatalf("FetchManifest: %v", err)
	}
	if len(tools) != 3 {
		t.Fatalf("demo manifest tools: %d", len(tools))
	}
	srv, err := adapter.NewServer(tools, be, nil, "e2e")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	adapterSrv := httptest.NewServer(auth.New(map[string]string{clientToken: "e2e"}).Wrap(mcpHandler))
	t.Cleanup(adapterSrv.Close)

	client := mcp.NewClient(&mcp.Implementation{Name: "e2e-client", Version: "0"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:             adapterSrv.URL + "/mcp",
		HTTPClient:           &http.Client{Transport: authTransport{http.DefaultTransport}},
		MaxRetries:           -1,
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

// callText 调工具并取首个 text 块。
func callText(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	return res.Content[0].(*mcp.TextContent).Text, res.IsError
}

func TestEndToEnd(t *testing.T) {
	session := stack(t)

	// 1. tools/list：演示后端三工具，按名升序，描述双语。
	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := []string{}
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	if strings.Join(names, ",") != "control_device,get_device_overview,list_devices" {
		t.Fatalf("tools: %v", names)
	}

	// 2. list_devices：两台虚拟设备。
	text, isErr := callText(t, session, "list_devices", nil)
	if isErr {
		t.Fatalf("list_devices isError: %s", text)
	}
	if !strings.Contains(text, "living-room-light") || !strings.Contains(text, "bedroom-thermometer") {
		t.Fatalf("list_devices: %s", text)
	}

	// 3. control_device 开灯。
	text, isErr = callText(t, session, "control_device", map[string]any{
		"id": 1, "command": map[string]any{"power": 1},
	})
	if isErr {
		t.Fatalf("control_device isError: %s", text)
	}
	if !strings.Contains(text, `"sent":1`) {
		t.Fatalf("control_device: %s", text)
	}

	// 4. get_device_overview 确认状态变化。
	text, isErr = callText(t, session, "get_device_overview", map[string]any{"id": 1})
	if isErr {
		t.Fatalf("get_device_overview isError: %s", text)
	}
	var out struct {
		Data struct {
			State struct {
				Power bool `json:"power"`
			} `json:"state"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("overview not JSON: %v (%s)", err, text)
	}
	if !out.Data.State.Power {
		t.Fatalf("light power should be on after control_device: %s", text)
	}

	// 5. 执行错误形态：不存在的设备 → isError + problem JSON（40401）。
	text, isErr = callText(t, session, "get_device_overview", map[string]any{"id": 99})
	if !isErr {
		t.Fatalf("expecting isError, got: %s", text)
	}
	if !strings.Contains(text, `"code":40401`) {
		t.Fatalf("problem code: %s", text)
	}
}

// TestUnauthorizedClient 无/错客户端凭据 → 401（可信网关：客户端 token 不透传）。
func TestUnauthorizedClient(t *testing.T) {
	demo := demobackend.New(upstreamToken)
	demoSrv := httptest.NewServer(demo)
	t.Cleanup(demoSrv.Close)
	be := backend.NewClient(demoSrv.URL, "/v1/tools/openai.json", "/v1/tools/invoke", backend.Credential{Kind: "bearer", Token: upstreamToken}, demoSrv.Client())
	tools, _ := be.FetchManifest(context.Background())
	srv, _ := adapter.NewServer(tools, be, nil, "e2e")
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	adapterSrv := httptest.NewServer(auth.New(map[string]string{clientToken: "e2e"}).Wrap(h))
	t.Cleanup(adapterSrv.Close)

	// 不带凭据直接 POST initialize。
	resp, err := http.Post(adapterSrv.URL+"/mcp", "application/json",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}
