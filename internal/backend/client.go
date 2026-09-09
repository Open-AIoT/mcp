// Package backend 上游后端契约客户端：拉取 tools manifest、转发工具调用。
//
// 契约（与 cloud-api 的 /v1/tools/* 对齐，也是适配器可对接任何后端的公开契约）：
//   - GET  {base}/v1/tools/openai.json  → FC 格式工具清单 [{"type":"function","function":{...}}]
//     （端点名为既有契约；manifest 格式的中立术语为 function-calling（FC）格式）
//   - POST {base}/v1/tools/invoke       → 请求 {"name","arguments"}；
//     成功 200 {"result":...}；失败非 200 + problem+json（type/title/status/code/detail）
//
// 上游凭据两级（与后端凭据体系一一对应，签名公式严格按后端契约实现）：
//   - bearer：Authorization: Bearer <token>（后端只读语义）；
//   - hmac：X-App-Key + X-Timestamp + X-Nonce + X-Signature 每请求签名（后端全权语义，
//     控制设备需要它）。sign = HMAC-SHA256(secret,
//     appKey+"\n"+timestamp+"\n"+nonce+"\n"+method+"\n"+path+"\n"+sha256hex(body))，
//     path 为 origin-form（含 query）。
package backend

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
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

// Credential 上游凭据（适配器自持；可信网关模式，不透传客户端凭据）。
type Credential struct {
	Kind      string // "bearer" | "hmac"
	Token     string // bearer
	AppKey    string // hmac
	AppSecret string // hmac
}

// apply 把凭据附着到请求上。body 为请求体原文（HMAC 需参与签名；无体传 nil）。
func (c Credential) apply(req *http.Request, body []byte) {
	if c.Kind == "hmac" {
		ts := time.Now().Unix()
		nonce := newNonce()
		req.Header.Set("X-App-Key", c.AppKey)
		req.Header.Set("X-Timestamp", strconv.FormatInt(ts, 10))
		req.Header.Set("X-Nonce", nonce)
		req.Header.Set("X-Signature", SignHMAC(c.AppSecret, c.AppKey, ts, nonce, req.Method, req.URL.RequestURI(), body))
		return
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
}

// SignHMAC 计算 HMAC-SHA256 请求签名（与后端验签同一公式；返回值 64 字符小写 hex）。
// path 为 origin-form（路径含 query string）；body 原文参与 sha256hex，空 body 也可签。
func SignHMAC(secret, appKey string, ts int64, nonce, method, path string, body []byte) string {
	sum := sha256.Sum256(body)
	payload := appKey + "\n" + strconv.FormatInt(ts, 10) + "\n" + nonce + "\n" +
		method + "\n" + path + "\n" + hex.EncodeToString(sum[:])
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

// newNonce 生成随机 nonce（16 字节 → 32 hex 字符，在契约 1–64 字符范围内）。
func newNonce() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil { // 加密随机源不可用时退化为时间戳，nonce 语义仍是单请求唯一性
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b)
}

// Client 后端契约客户端。所有请求带适配器自持的上游凭据。
type Client struct {
	baseURL      string
	manifestPath string
	invokePath   string
	cred         Credential
	hc           *http.Client
}

// NewClient 构建客户端；hc 为 nil 时用 http.DefaultClient。
func NewClient(baseURL, manifestPath, invokePath string, cred Credential, hc *http.Client) *Client {
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Client{baseURL: baseURL, manifestPath: manifestPath, invokePath: invokePath, cred: cred, hc: hc}
}

// get / post 带认证头的原始请求。
func (c *Client) get(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	c.cred.apply(req, nil)
	req.Header.Set("Accept", "application/json")
	return c.hc.Do(req)
}

func (c *Client) post(ctx context.Context, path string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	c.cred.apply(req, body)
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
