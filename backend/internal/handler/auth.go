// Package handler - HTTP Basic Auth 中间件、认证失败速率限制、WS Origin 校验。
// 设计见 ../design-v3.md §4.1 与 plan: managi-v3-auth-conn-stability-fixes.md。
package handler

import (
	"crypto/subtle"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"managi/internal/config"
)

// 认证失败速率限制参数（硬编码合理默认，保持简约）。
const (
	authFailWindow      = 60 * time.Second // 滑动窗口
	authFailMaxAttempts = 10               // 每窗口每 IP 最大失败次数
)

// authFailLimiter 每 IP 滑动窗口失败计数器。
type authFailLimiter struct {
	mu      sync.Mutex
	attempts map[string][]time.Time // ip → 失败时间戳列表
}

func newAuthFailLimiter() *authFailLimiter {
	return &authFailLimiter{attempts: make(map[string][]time.Time)}
}

// tooMany 返回该 IP 是否已超限（窗口内失败次数 >= 上限）。
func (l *authFailLimiter) tooMany(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := time.Now().Add(-authFailWindow)
	ts := l.attempts[ip]
	// 丢弃过期时间戳
	keep := ts[:0]
	for _, t := range ts {
		if t.After(cutoff) {
			keep = append(keep, t)
		}
	}
	l.attempts[ip] = keep
	return len(keep) >= authFailMaxAttempts
}

// recordFailure 记录一次失败。
func (l *authFailLimiter) recordFailure(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.attempts[ip] = append(l.attempts[ip], time.Now())
}

// reset 清除该 IP 的失败记录（成功后调用）。
func (l *authFailLimiter) reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, ip)
}

// BasicAuthMiddleware 返回 Basic Auth 中间件。
// cfg.BasicAuthEnabled == false 时透传（零开销）。
// 浏览器在首次 401+WWW-Authenticate 后缓存凭据，后续同源 HTTP 与 WebSocket 升级请求自动携带。
func BasicAuthMiddleware(cfg *config.Config) func(http.Handler) http.Handler {
	if !cfg.BasicAuthEnabled {
		return func(next http.Handler) http.Handler { return next }
	}
	limiter := newAuthFailLimiter()
	expectedUser := []byte(cfg.BasicAuthUser)
	expectedPass := []byte(cfg.BasicAuthPassword)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// /health 放行：Docker healthcheck / Tauri sidecar 探活
			if r.URL.Path == "/health" {
				next.ServeHTTP(w, r)
				return
			}
			ip := clientIP(r)
			if limiter.tooMany(ip) {
				http.Error(w, "too many auth failures", http.StatusTooManyRequests)
				return
			}
			user, pass, ok := r.BasicAuth()
			if !ok ||
				subtle.ConstantTimeCompare([]byte(user), expectedUser) != 1 ||
				subtle.ConstantTimeCompare([]byte(pass), expectedPass) != 1 {
				limiter.recordFailure(ip)
				w.Header().Set("WWW-Authenticate", `Basic realm="managi", charset="UTF-8"`)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			limiter.reset(ip)
			next.ServeHTTP(w, r)
		})
	}
}

// clientIP 提取客户端 IP（优先 X-Forwarded-For 首段，回退 RemoteAddr）。
// 修复 R6：复用 net.SplitHostPort 正确处理 IPv6 地址（原手写按 ':' 截断会破坏 IPv6）。
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if idx := strings.IndexByte(xff, ','); idx >= 0 {
			return strings.TrimSpace(xff[:idx])
		}
		return strings.TrimSpace(xff)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// checkOrigin 校验 WebSocket 升级请求来源。
// Origin 为空 → 放行（非浏览器客户端如 Tauri sidecar / 测试工具）。
// 非空 → 必须与请求 Host 同源（防 WS CSRF）。
func checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return u.Host == r.Host
}
