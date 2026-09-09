// Package backend 上游后端契约客户端：拉取 tools manifest、转发工具调用。
//
// 契约（与 cloud-api 的 /v1/tools/* 对齐，也是适配器可对接任何后端的公开契约）：
//   - GET  {base}/v1/tools/openai.json  → FC 格式工具清单 [{"type":"function","function":{...}}]
//     （端点名为既有契约；manifest 格式的中立术语为 function-calling（FC）格式）
//   - POST {base}/v1/tools/invoke       → 请求 {"name","arguments"}；
//     成功 200 {"result":...}；失败非 200 + problem+json（type/title/status/code/detail）
package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Annotations 工具标注（manifest 的可选扩展字段；FC 清单本身不携带，
// 后端可以附带，适配器原样透传给 MCP annotations；缺失时适配器不编造）。
type Annotations struct {
	ReadOnlyHint    *bool `json:"readOnlyHint,omitempty"`
	DestructiveHint *bool `json:"destructiveHint,omitempty"`
	IdempotentHint  *bool `json:"idempotentHint,omitempty"`
}

// Tool manifest 中的一个工具条目（FC 格式元素 + 可选 annotations 扩展）。
type Tool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"` // 原样透传为 MCP inputSchema
		Annotations *Annotations    `json:"annotations,omitempty"`
	} `json:"function"`
}

// Problem 后端 problem+json 错误体。
type Problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Code   int    `json:"code"`
	Detail string `json:"detail"`
}

// Client 后端契约客户端。所有请求带适配器自持的上游 Bearer 凭据。
type Client struct {
	baseURL      string
	manifestPath string
	invokePath   string
	token        string
	hc           *http.Client
}

// NewClient 构建客户端；hc 为 nil 时用 http.DefaultClient。
func NewClient(baseURL, manifestPath, invokePath, token string, hc *http.Client) *Client {
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Client{baseURL: baseURL, manifestPath: manifestPath, invokePath: invokePath, token: token, hc: hc}
}

// get / post 带认证头的原始请求。
func (c *Client) get(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	return c.hc.Do(req)
}

func (c *Client) post(ctx context.Context, path string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	return c.hc.Do(req)
}

// FetchManifest 拉取工具清单。后端 4xx/5xx 视为启动期致命错误（由调用方决定）。
func (c *Client) FetchManifest(ctx context.Context) ([]Tool, error) {
	resp, err := c.get(ctx, c.manifestPath)
	if err != nil {
		return nil, fmt.Errorf("fetch manifest: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch manifest: backend returned %s: %s", resp.Status, truncate(string(data), 200))
	}
	var tools []Tool
	if err := json.Unmarshal(data, &tools); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	for i, t := range tools {
		if t.Function.Name == "" {
			return nil, fmt.Errorf("manifest tool #%d has empty name", i)
		}
		if len(t.Function.Parameters) == 0 {
			tools[i].Function.Parameters = json.RawMessage(`{"type":"object","additionalProperties":false}`)
		}
	}
	return tools, nil
}

// Invoke 转发工具调用。
// 返回三态：result（200 的 result 字段原文）；prob（非 200 的 problem 体，尽量解析）；
// err（传输层/协议形态错误，如连不上后端、200 但体非法）。
func (c *Client) Invoke(ctx context.Context, name string, args json.RawMessage) (result json.RawMessage, prob *Problem, err error) {
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	payload, err := json.Marshal(map[string]any{"name": name, "arguments": args})
	if err != nil {
		return nil, nil, fmt.Errorf("marshal invoke request: %w", err)
	}
	resp, err := c.post(ctx, c.invokePath, payload)
	if err != nil {
		return nil, nil, fmt.Errorf("invoke backend: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, nil, fmt.Errorf("read invoke response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		p := &Problem{Type: "about:blank", Title: resp.Status, Status: resp.StatusCode, Code: 50001, Detail: "backend error (unrecognized body)"}
		if json.Unmarshal(data, p) == nil && p.Status != 0 {
			return nil, p, nil
		}
		p.Detail = "backend returned " + resp.Status + ": " + truncate(string(data), 200)
		return nil, p, nil
	}

	var okBody struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(data, &okBody); err != nil || len(okBody.Result) == 0 {
		return nil, nil, fmt.Errorf("invoke: malformed 200 response (expecting {\"result\":...})")
	}
	return okBody.Result, nil, nil
}

// truncate 截断错误摘要（防把超大后端响应写进日志/错误）。
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
