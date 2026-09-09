// Package adapter MCP 服务端装配：把后端 tools manifest 渲染为 MCP tools，
// 把 tools/call 翻译为后端 invoke。错误按绑定规范两级制包装：
//   - 协议错误（未知工具、参数形态非法）由官方 SDK 的协议层处理（-32602 等）；
//   - 执行错误（后端 problem+json、后端不可达）在本层包装为
//     isError:true + problem JSON 文本（规范 §8.2 形态）。
package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Open-AIoT/mcp/internal/audit"
	"github.com/Open-AIoT/mcp/internal/backend"
)

// Backend 适配器需要的后端能力（*backend.Client 实现；测试可打桩）。
type Backend interface {
	Invoke(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, *backend.Problem, error)
}

// NewServer 以 manifest 工具集构建 MCP Server。version 写入 serverInfo。
// manifest 拉取失败/为空由调用方（main）在启动期拒绝，本函数假定 tools 已校验。
func NewServer(tools []backend.Tool, be Backend, aud *audit.Logger, version string) (*mcp.Server, error) {
	// Capabilities 显式收敛：去掉 SDK 历史默认的 logging 能力，tools 不声明
	// listChanged（规范 §4.4.3：一期无订阅通道，不得声明清单热变更）。
	s := mcp.NewServer(&mcp.Implementation{Name: "openaiot-mcp", Version: version},
		&mcp.ServerOptions{Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}}})
	for _, t := range tools {
		tool := &mcp.Tool{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			// manifest 的 parameters 原样透传为 inputSchema（规范 §4.3 机械映射）；
			// 用 AddTool 低层接口而非泛型 AddTool：schema 来自后端，本层不做类型推导。
			InputSchema: json.RawMessage(t.Function.Parameters),
			Annotations: mapAnnotations(t.Function.Annotations),
		}
		s.AddTool(tool, makeHandler(t.Function.Name, be, aud))
	}
	return s, nil
}

// mapAnnotations manifest 可选扩展 → MCP annotations。
// 缺失时不编造（规范 §7：hint 是不可信提示；manifest 契约的标注扩展待
// manifest 绑定规范定稿，见 spec 仓 TBD-4）。
func mapAnnotations(a *backend.Annotations) *mcp.ToolAnnotations {
	if a == nil {
		return nil
	}
	out := &mcp.ToolAnnotations{}
	if a.ReadOnlyHint != nil {
		out.ReadOnlyHint = *a.ReadOnlyHint
	}
	if a.DestructiveHint != nil {
		out.DestructiveHint = a.DestructiveHint
	}
	if a.IdempotentHint != nil {
		out.IdempotentHint = *a.IdempotentHint
	}
	return out
}

// makeHandler 单个工具的调用处理器：转发后端 + 两级错误包装 + 审计。
func makeHandler(name string, be Backend, aud *audit.Logger) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.Params.Arguments
		if len(args) == 0 {
			args = json.RawMessage(`{}`)
		}

		result, prob, err := be.Invoke(ctx, name, args)
		switch {
		case err != nil:
			// 传输层/形态错误：合成为 503 语义的 problem（规范 §8.2 执行错误）。
			p := &backend.Problem{
				Type:   "about:blank",
				Title:  "Service Unavailable",
				Status: http.StatusServiceUnavailable,
				Code:   50301,
				Detail: "upstream backend unavailable: " + err.Error(),
			}
			logCall(ctx, aud, name, p.Status)
			return errorResult(p), nil
		case prob != nil:
			// 后端业务错误：problem 体原样包进 isError content（规范 §8.2）。
			logCall(ctx, aud, name, prob.Status)
			return errorResult(prob), nil
		default:
			logCall(ctx, aud, name, http.StatusOK)
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: string(result)}},
			}, nil
		}
	}
}

// errorResult problem → isError:true 的 CallToolResult（content 为单个 text 块，
// 内嵌 problem JSON——与后端 problem+json 同字段，模型可读可自纠）。
func errorResult(p *backend.Problem) *mcp.CallToolResult {
	data, err := json.Marshal(p)
	if err != nil {
		data = []byte(fmt.Sprintf(`{"type":"about:blank","title":"Internal Server Error","status":500,"code":50001,"detail":"internal error"}`))
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(data)}},
		IsError: true,
	}
}

// logCall 审计（aud 为 nil 时跳过，便于测试）。
func logCall(ctx context.Context, aud *audit.Logger, tool string, status int) {
	if aud != nil {
		aud.LogTool(ctx, tool, status, "")
	}
}
