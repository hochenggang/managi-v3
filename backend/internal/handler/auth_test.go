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

// TestClientIP 验证客户端 IP 只取真实连接地址：伪造 X-Forwarded-For 不能改变
// 限流与日志眼里的「谁在连」（同时保住 R6：IPv6 由 SplitHostPort 正确切分端口）。
func TestClientIP(t *testing.T) {
	// 伪造 XFF：忽略，仍按连接地址计
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 5.6.7.8")
	req.RemoteAddr = "203.0.113.9:55555"
	assert.Equal(t, "203.0.113.9", clientIP(req))

	// RemoteAddr IPv4
	req = httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "1.2.3.4:5678"
	assert.Equal(t, "1.2.3.4", clientIP(req))

	// RemoteAddr IPv6（R6 修复：按 ':' 截断会破坏 IPv6）
	req = httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "[::1]:5678"
	assert.Equal(t, "::1", clientIP(req))

	// RemoteAddr 无端口（异常情况，原样返回）
	req = httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "1.2.3.4"
	assert.Equal(t, "1.2.3.4", clientIP(req))
}

// TestRandomBasicAuthPassword 验证随机口令为 32 位十六进制且两次生成不同。
func TestRandomBasicAuthPassword(t *testing.T) {
	a := RandomBasicAuthPassword()
	b := RandomBasicAuthPassword()
	assert.Len(t, a, 32)
	assert.Len(t, b, 32)
	assert.NotEqual(t, a, b, "consecutive passwords must differ")
	assert.Regexp(t, `^[0-9a-f]{32}$`, a)
}
