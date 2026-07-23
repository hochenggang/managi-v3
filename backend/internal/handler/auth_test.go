package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"managi/internal/config"
)

// okHandler 是一个简单的 200 OK handler，用于测试中间件透传。
func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

// TestBasicAuthMiddleware_Disabled 验证 BasicAuthEnabled=false 时中间件透传。
func TestBasicAuthMiddleware_Disabled(t *testing.T) {
	cfg := &config.Config{BasicAuthEnabled: false}
	h := BasicAuthMiddleware(cfg, nil)(okHandler())

	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestBasicAuthMiddleware_HealthBypass 验证 /health 路径放行（无需凭据）。
func TestBasicAuthMiddleware_HealthBypass(t *testing.T) {
	cfg := &config.Config{BasicAuthEnabled: true, BasicAuthUser: "admin", BasicAuthPassword: "secret"}
	h := BasicAuthMiddleware(cfg, nil)(okHandler())

	req := httptest.NewRequest("GET", "/health", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestBasicAuthMiddleware_CorrectCredentials 验证正确凭据放行。
func TestBasicAuthMiddleware_CorrectCredentials(t *testing.T) {
	cfg := &config.Config{BasicAuthEnabled: true, BasicAuthUser: "admin", BasicAuthPassword: "secret"}
	h := BasicAuthMiddleware(cfg, nil)(okHandler())

	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("admin", "secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestBasicAuthMiddleware_WrongCredentials 验证错误凭据返回 401 + WWW-Authenticate。
func TestBasicAuthMiddleware_WrongCredentials(t *testing.T) {
	cfg := &config.Config{BasicAuthEnabled: true, BasicAuthUser: "admin", BasicAuthPassword: "secret"}
	h := BasicAuthMiddleware(cfg, nil)(okHandler())

	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("admin", "wrong")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, rec.Header().Get("WWW-Authenticate"), "Basic")
}

// TestBasicAuthMiddleware_NoCredentials 验证无凭据返回 401。
func TestBasicAuthMiddleware_NoCredentials(t *testing.T) {
	cfg := &config.Config{BasicAuthEnabled: true, BasicAuthUser: "admin", BasicAuthPassword: "secret"}
	h := BasicAuthMiddleware(cfg, nil)(okHandler())

	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

// TestCheckOrigin 验证 WebSocket Origin 校验逻辑。
func TestCheckOrigin(t *testing.T) {
	// 空 Origin → 放行（非浏览器客户端）
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "example.com:8080"
	assert.True(t, checkOrigin(req))

	// 同源 → 放行
	req = httptest.NewRequest("GET", "/", nil)
	req.Host = "example.com:8080"
	req.Header.Set("Origin", "http://example.com:8080")
	assert.True(t, checkOrigin(req))

	// 跨域 → 拒绝
	req = httptest.NewRequest("GET", "/", nil)
	req.Host = "example.com:8080"
	req.Header.Set("Origin", "http://evil.com")
	assert.False(t, checkOrigin(req))

	// 非法 Origin URL → 拒绝
	req = httptest.NewRequest("GET", "/", nil)
	req.Host = "example.com:8080"
	req.Header.Set("Origin", "://invalid")
	assert.False(t, checkOrigin(req))
}

// TestClientIP 验证客户端 IP 提取（XFF 优先、RemoteAddr 回退、IPv6 兼容）。
// 覆盖 R6 修复：net.SplitHostPort 正确处理 IPv6 地址。
func TestClientIP(t *testing.T) {
	// XFF 单 IP
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	assert.Equal(t, "1.2.3.4", clientIP(req))

	// XFF 多 IP（取首段，去空白）
	req = httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 5.6.7.8")
	assert.Equal(t, "1.2.3.4", clientIP(req))

	// XFF 带前后空白
	req = httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Forwarded-For", "  1.2.3.4  ")
	assert.Equal(t, "1.2.3.4", clientIP(req))

	// RemoteAddr IPv4
	req = httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "1.2.3.4:5678"
	assert.Equal(t, "1.2.3.4", clientIP(req))

	// RemoteAddr IPv6（R6 修复：原手写按 ':' 截断会破坏 IPv6）
	req = httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "[::1]:5678"
	assert.Equal(t, "::1", clientIP(req))

	// 无 XFF 且 RemoteAddr 无端口（异常情况，返回原值）
	req = httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "1.2.3.4"
	assert.Equal(t, "1.2.3.4", clientIP(req))
}
