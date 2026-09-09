// Package config 适配器配置（YAML）。字段风格对齐 cloud-api 的配置习惯：
// 小写下划线键、必填项缺失即报错、敏感项（token）只走本地配置文件（.gitignore 已排除）。
package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config 顶层配置。
type Config struct {
	Listen  string   `yaml:"listen"`  // 适配器监听地址，如 ":8081"
	Backend Backend  `yaml:"backend"` // 上游后端（cloud-api 或任何符合契约的实现）
	Clients []Client `yaml:"clients"` // 允许接入的客户端凭据（一期静态列表）
}

// Backend 上游后端契约配置。
type Backend struct {
	BaseURL string `yaml:"base_url"` // 如 "http://127.0.0.1:8080"，不带尾斜杠
	// AuthType 上游凭据类型（可信网关模式：不透传客户端 token）：
	//   - bearer：静态 token，后端侧只读语义——适用只读演示/监控场景；
	//   - hmac：app_key + HMAC-SHA256 每请求签名，后端侧全权——控制设备需要它。
	AuthType  string `yaml:"auth_type"`
	Token     string `yaml:"token"`      // bearer 凭据
	AppKey    string `yaml:"app_key"`    // hmac 凭据
	AppSecret string `yaml:"app_secret"` // hmac 凭据（仅存本地配置）
	// ManifestPath 工具清单端点。现值为既有契约端点名（含历史命名的 .json 文件名；
	// 规范定稿后将迁移为中立命名，届时改这里即可，代码不变）。
	ManifestPath string `yaml:"manifest_path"`
	InvokePath   string `yaml:"invoke_path"` // 工具调用端点
}

// Client 一个允许接入的 MCP 客户端凭据（一期：静态 Bearer token，只读语义——
// 写控制的拒绝由后端 scope 检查收口，适配器不复制该逻辑）。
type Client struct {
	Name  string `yaml:"name"`  // 人类可读标识（审计用）
	Token string `yaml:"token"` // Bearer token 明文（仅存本地配置）
}

// DefaultManifestPath / DefaultInvokePath 契约端点缺省值。
const (
	DefaultManifestPath = "/v1/tools/openai.json" // 既有契约端点名（见 Backend.ManifestPath 注释）
	DefaultInvokePath   = "/v1/tools/invoke"
)

// Load 读取并校验配置文件。
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	if c.Listen == "" {
		return nil, fmt.Errorf("config: listen is required")
	}
	c.Backend.BaseURL = strings.TrimRight(c.Backend.BaseURL, "/")
	if c.Backend.BaseURL == "" {
		return nil, fmt.Errorf("config: backend.base_url is required")
	}
	switch strings.ToLower(c.Backend.AuthType) {
	case "", "bearer":
		c.Backend.AuthType = "bearer" // 缺省 bearer（只读场景的最小权限缺省）
		if c.Backend.Token == "" {
			return nil, fmt.Errorf("config: backend.token is required for auth_type bearer")
		}
	case "hmac":
		if c.Backend.AppKey == "" || c.Backend.AppSecret == "" {
			return nil, fmt.Errorf("config: backend.app_key and backend.app_secret are required for auth_type hmac")
		}
	default:
		return nil, fmt.Errorf("config: backend.auth_type must be bearer or hmac, got %q", c.Backend.AuthType)
	}
	if c.Backend.ManifestPath == "" {
		c.Backend.ManifestPath = DefaultManifestPath
	}
	if c.Backend.InvokePath == "" {
		c.Backend.InvokePath = DefaultInvokePath
	}
	if len(c.Clients) == 0 {
		return nil, fmt.Errorf("config: at least one client credential is required")
	}
	for i, cl := range c.Clients {
		if cl.Token == "" {
			return nil, fmt.Errorf("config: clients[%d].token is empty", i)
		}
		if cl.Name == "" {
			c.Clients[i].Name = fmt.Sprintf("client-%d", i)
		}
	}
	return &c, nil
}
