// Package audit tools/call 的结构化审计日志：每次工具调用落一行 JSON
// （时间、工具名、客户端 token 指纹、结果状态）。只记指纹不记 token 明文。
package audit

import (
	"context"
	"log/slog"

	"github.com/Open-AIoT/mcp/internal/auth"
)

// Logger 审计日志器（基于 slog；输出目的地由 main 装配）。
type Logger struct {
	l *slog.Logger
}

// New 以给定的 slog.Logger 构建审计器。
func New(l *slog.Logger) *Logger {
	return &Logger{l: l}
}

// LogTool 记录一次工具调用。status 为执行结果的 HTTP 语义状态码
// （200=成功；执行错误取 problem.status；传输错误 503）。
func (a *Logger) LogTool(ctx context.Context, tool string, status int, detail string) {
	a.l.InfoContext(ctx, "tool_call",
		"tool", tool,
		"client_fp", auth.FingerprintFrom(ctx),
		"status", status,
		"detail", detail,
	)
}
