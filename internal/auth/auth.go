// Package auth 适配器侧的客户端认证（可信网关模式，合规要点：禁止 token 透传）。
//
// 适配器校验自己的客户端 Bearer token（配置静态列表），随后以自持的上游凭据调后端——
// 客户端 token 永不转发给后端。认证通过后把客户端指纹（token 的 sha256，不含明文）
// 写入请求上下文，供审计日志使用。
package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
)

// ctxKey 上下文键（未导出，防碰撞）。
type ctxKey struct{}

// Authenticator 静态 token 校验器。
type Authenticator struct {
	byToken map[string]string // token 明文 → 客户端名（启动期从配置装载，之后只读）
}

// New 从客户端凭据列表构建校验器。
func New(clients map[string]string) *Authenticator {
	return &Authenticator{byToken: clients}
}

// Fingerprint 计算 token 指纹：sha256 十六进制（审计用；不可逆推明文）。
func Fingerprint(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// FingerprintFrom 取请求上下文中的客户端指纹（未认证请求返回空串）。
func FingerprintFrom(ctx context.Context) string {
	fp, _ := ctx.Value(ctxKey{}).(string)
	return fp
}

// problem 认证失败的 problem+json 响应（字段与后端错误形态一致：type/title/status/code/detail）。
func problem(w http.ResponseWriter, status int, code int, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":   "about:blank",
		"title":  title,
		"status": status,
		"code":   code,
		"detail": detail,
	})
}

// Wrap 认证中间件：无/错凭据 → 401 problem+json；通过 → 指纹入上下文。
func (a *Authenticator) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		if !strings.HasPrefix(h, "Bearer ") {
			problem(w, http.StatusUnauthorized, 40101, "Unauthorized", "missing credential (Authorization: Bearer <token>)")
			return
		}
		token := strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
		if token == "" {
			problem(w, http.StatusUnauthorized, 40101, "Unauthorized", "missing credential (Authorization: Bearer <token>)")
			return
		}
		// 常数时间比较遍历静态列表（一期列表很短，遍历可接受）。
		ok := false
		for t := range a.byToken {
			if subtle.ConstantTimeCompare([]byte(t), []byte(token)) == 1 {
				ok = true
			}
		}
		if !ok {
			problem(w, http.StatusUnauthorized, 40102, "Unauthorized", "invalid token")
			return
		}
		ctx := context.WithValue(r.Context(), ctxKey{}, Fingerprint(token))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
