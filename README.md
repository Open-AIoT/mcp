<p align="center">
  <img src="docs/assets/logo.png" alt="Open AIoT" width="120">
</p>

# Open AIoT MCP Server

**AI Agent 与物联网设备之间的 MCP 协议适配器 —— Open AIoT 的 L1 协议绑定层参考实现。**

本服务是一个**纯协议适配器**：从后端物联网平台拉取工具清单（tools manifest）渲染为 MCP `tools/list`，将 MCP `tools/call` 翻译为对后端的 REST 调用。它不含任何物联网业务逻辑，可对接任何实现了 Open AIoT 公开契约的后端。

> 项目状态：早期开发中（Phase 1 预研阶段），API 与行为尚不稳定。

## 快速上手

目标 5 分钟：clone → 起演示后端 → 起适配器 → curl 调通一个工具。

```bash
# 1. 获取代码（Go 1.24+）
git clone https://github.com/Open-AIoT/mcp.git && cd mcp

# 2. 起演示后端（内存虚拟设备：客厅灯 + 卧室温湿度计），监听 :8080
go run ./examples/demo-backend -addr 127.0.0.1:8080 -token demo-backend-token &

# 3. 起适配器，监听 :8081
cp configs/config.example.yaml configs/config.yaml   # 默认配置即指向上面的演示后端
go run ./cmd/openaiot-mcp -config configs/config.yaml &
```

适配器以客户端凭据接入（`configs/config.yaml` 的 `clients` 列表；示例值为 `demo-client-token`）。
MCP 端点为 `POST http://127.0.0.1:8081/mcp`。用 MCP Inspector 连接时，将
`Authorization: Bearer demo-client-token` 配为自定义请求头即可。

curl 冒烟（initialize → tools/list → 开灯 → 确认状态）：

```bash
H_AUTH='Authorization: Bearer demo-client-token'
H_CT='Content-Type: application/json'
H_ACC='Accept: application/json, text/event-stream'
MCP=http://127.0.0.1:8081/mcp

# initialize（协议版本 2025-06-18）
curl -X POST $MCP -H "$H_AUTH" -H "$H_CT" -H "$H_ACC" -d \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"curl","version":"0"}}}'

# tools/list：三个工具（control_device / get_device_overview / list_devices）
curl -X POST $MCP -H "$H_AUTH" -H "$H_CT" -H "$H_ACC" -d \
  '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}'

# 打开客厅灯（id=1）
curl -X POST $MCP -H "$H_AUTH" -H "$H_CT" -H "$H_ACC" -d \
  '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"control_device","arguments":{"id":1,"command":{"power":1}}}}'

# 确认状态变化：返回中 state.power 应为 true
curl -X POST $MCP -H "$H_AUTH" -H "$H_CT" -H "$H_ACC" -d \
  '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"get_device_overview","arguments":{"id":1}}}'
```

## 架构与契约

```
MCP 客户端 ──/mcp──▶ openaiot-mcp（本仓）──REST──▶ 后端（cloud-api / 演示后端 / 任何契约实现）
                     纯协议适配器                  GET  /v1/tools/openai.json   工具清单（FC 格式）
                     客户端 Bearer 认证            POST /v1/tools/invoke       {name,arguments} → {result} | problem+json
                     tools/call 审计
```

- **可信网关模式**：适配器校验自己的客户端 token（配置静态列表），用自持的上游凭据调后端；客户端 token 不透传。
- **上游凭据两级**（`backend.auth_type`，与后端凭据体系对应）：`bearer` 为只读语义，适用只读演示/监控场景（演示后端即此模式）；`hmac` 为全权语义（app_key + HMAC-SHA256 每请求签名：`X-App-Key`/`X-Timestamp`/`X-Nonce`/`X-Signature`，签名公式与后端验签逐字段一致），**控制设备（control 类工具）需要它**。
- **无状态子集**：单端点 `POST /mcp`，纯 `application/json` 响应（无 SSE/session/批量请求），请求体上限 1 MiB。
- **错误两级制**：协议错误走 JSON-RPC error；后端业务错误（problem+json）包装为 `isError:true` 的正常结果回给模型。
- **审计**：每次 `tools/call` 落一行 JSON 结构化日志（时间、工具名、客户端 token 指纹 sha256、状态）。
- manifest 端点文件名 `openai.json` 是既有契约端点名（历史命名）；规范定稿后将迁移为中立命名，届时改配置项 `backend.manifest_path` 即可。

行为规范的权威来源是 spec 仓库的《MCP 绑定规范》与《工具描述写作规范》（specVersion 0.1.0-draft）。

## 技术栈

Go + [MCP 官方 Go SDK](https://github.com/modelcontextprotocol/go-sdk)（v1.7+，覆盖 2024-11-05 至 2026-07-28 协议版本；本服务基线为 2025-06-18）。

## 开发

```bash
go build ./...
go test ./...   # 单测 + 端到端测试（e2e：演示后端 + 适配器 + SDK 客户端全链路）
```

仓库结构：`cmd/openaiot-mcp`（入口）、`internal/adapter`（manifest→tools 渲染与调用翻译）、`internal/backend`（后端契约客户端）、`internal/auth`（客户端认证）、`internal/audit`（调用审计）、`internal/demobackend`（演示后端核心）、`examples/demo-backend`（演示后端入口）、`configs/`（配置样例）、`e2e/`（端到端测试）。

## 声明

**Open AIoT（开放AIoT）**——"Open"即"开放"。本项目是芯步（ThingBoot）主导的开放 AIoT 标准与生态品牌，与 OpenAI 公司无任何关联。

## License

[Apache-2.0](LICENSE)

---

*Open AIoT is an open device×AI standard and ecosystem led by ThingBoot. This repository is the reference implementation of its MCP protocol binding layer — a pure protocol adapter with no IoT business logic. Not affiliated with OpenAI.*
