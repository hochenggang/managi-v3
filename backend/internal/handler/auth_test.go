package handler

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

// TestBasicAuthMiddleware_EmptyPasswordFailsClosed 验证「启用鉴权但口令为空」时全拒。
// ConstantTimeCompare 对两个空串判等，没有这道闸门，空口令会变成人人可进的假鉴权。
// /health 是唯一例外（探活需要，且不吐任何敏感信息）。
func TestBasicAuthMiddleware_EmptyPasswordFailsClosed(t *testing.T) {
	cfg := &config.Config{BasicAuthEnabled: true, BasicAuthUser: "admin", BasicAuthPassword: ""}
	h := BasicAuthMiddleware(cfg, nil)(okHandler())

	// 空凭据（空用户 + 空口令）不得判等通过
	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("", "")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	// 用户名匹配、口令留空同样拒绝
	req = httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("admin", "")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	// /health 仍放行：探活端点必须可达
	req = httptest.NewRequest("GET", "/health", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
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

// mustNets 把 CIDR/裸 IP 串表解析成网段表（测试专用，解析失败直接失败）。
func mustNets(t *testing.T, items ...string) []*net.IPNet {
	t.Helper()
	var nets []*net.IPNet
	for _, it := range items {
		if _, n, err := net.ParseCIDR(it); err == nil {
			nets = append(nets, n)
			continue
		}
		ip := net.ParseIP(it)
		require.NotNil(t, ip, "测试用例里的 %q 无法解析", it)
		bits := 128
		if ip.To4() != nil {
			bits = 32
		}
		nets = append(nets, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
	}
	return nets
}

// TestClientIP 验证默认（无可信代理）只取真实连接地址：伪造 X-Forwarded-For
// 不能改变限流与日志眼里的「谁在连」（同时保住 R6：IPv6 由 SplitHostPort 正确切分端口）。
func TestClientIP(t *testing.T) {
	// 伪造 XFF：忽略，仍按连接地址计
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 5.6.7.8")
	req.RemoteAddr = "203.0.113.9:55555"
	assert.Equal(t, "203.0.113.9", clientIP(req, nil))

	// RemoteAddr IPv4
	req = httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "1.2.3.4:5678"
	assert.Equal(t, "1.2.3.4", clientIP(req, nil))

	// RemoteAddr IPv6（R6 修复：按 ':' 截断会破坏 IPv6）
	req = httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "[::1]:5678"
	assert.Equal(t, "::1", clientIP(req, nil))

	// RemoteAddr 无端口（异常情况，原样返回）
	req = httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "1.2.3.4"
	assert.Equal(t, "1.2.3.4", clientIP(req, nil))
}

// TestClientIP_TrustedProxy 验证声明可信代理后按 XFF 归因：
// 只有来自可信网段的连接才被采信，且取最右侧的非代理地址——
// 代理追加模式下它是真正连上来的地址，客户端伪造的前缀不影响归属。
func TestClientIP_TrustedProxy(t *testing.T) {
	trusted := mustNets(t, "10.0.0.0/8", "127.0.0.1")

	// 对端是可信代理：取 XFF 最右侧非代理地址（客户端伪造的 1.2.3.4 被忽略）
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.1.2.3:55555"
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 203.0.113.9")
	assert.Equal(t, "203.0.113.9", clientIP(req, trusted))

	// 对端不可信：XFF 再像模像样也不认，仍按连接地址计
	req = httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "203.0.113.9:55555"
	req.Header.Set("X-Forwarded-For", "10.1.2.3, 9.9.9.9")
	assert.Equal(t, "203.0.113.9", clientIP(req, trusted))

	// XFF 缺失/空：归因到直连的代理那一跳
	req = httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.1.2.3:55555"
	assert.Equal(t, "10.1.2.3", clientIP(req, trusted))

	// 链上全是可信代理：退回直连地址，不把整条链最左端当客户端
	req = httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.1.2.3:55555"
	req.Header.Set("X-Forwarded-For", "10.9.9.9, 127.0.0.1")
	assert.Equal(t, "10.1.2.3", clientIP(req, trusted))

	// 畸形条目跳过，取更右侧可解析的地址
	req = httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.1.2.3:55555"
	req.Header.Set("X-Forwarded-For", "junk, 203.0.113.9")
	assert.Equal(t, "203.0.113.9", clientIP(req, trusted))

	// 裸 IP 视作单机网段：127.0.0.1 命中
	req = httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "127.0.0.1:55555"
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	assert.Equal(t, "203.0.113.9", clientIP(req, trusted))
}
