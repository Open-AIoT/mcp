<p align="center">
  <img src="docs/assets/logo.png" alt="Open AIoT" width="120">
</p>

# Open AIoT MCP Server

**AI Agent 与物联网设备之间的 MCP 协议适配器 —— Open AIoT 的 L1 协议绑定层参考实现。**

本服务是一个**纯协议适配器**：从后端物联网平台拉取工具清单（tools manifest）渲染为 MCP `tools/list`，将 MCP `tools/call` 翻译为对后端的 REST 调用。它不含任何物联网业务逻辑，可对接任何实现了 Open AIoT 公开契约的后端。

> 项目状态：早期开发中（Phase 1 预研阶段），API 与行为尚不稳定。

## 快速上手

（待首个可运行版本发布后补充——目标：clone → 启动演示后端 → 用 MCP Inspector 连上并调用一个工具，全程 ≤ 5 分钟。）

## 技术栈

Go + [MCP 官方 Go SDK](https://github.com/modelcontextprotocol/go-sdk)。

## 声明

**Open AIoT（开放AIoT）**——"Open"即"开放"。本项目是芯步（ThingBoot）主导的开放 AIoT 标准与生态品牌，与 OpenAI 公司无任何关联。

## License

[Apache-2.0](LICENSE)

---

*Open AIoT is an open device×AI standard and ecosystem led by ThingBoot. This repository is the reference implementation of its MCP protocol binding layer — a pure protocol adapter with no IoT business logic. Not affiliated with OpenAI.*
