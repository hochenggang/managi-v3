package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"managi/internal/config"
	"managi/internal/testutil"
)

// newTestServer 用测试配置装配服务，返回 *http.Server 供对 Handler 直接发请求。
func newTestServer(t *testing.T, mutate func(*config.Config)) *http.Server {
	t.Helper()
	cfg := testutil.TestConfig()
	if mutate != nil {
		mutate(cfg)
	}
	srv, pool := New(cfg, make(chan struct{}))
	t.Cleanup(func() { pool.CloseAll() })
	return srv
}

// TestNew_AddrAndHealth 验证监听地址来自配置，且 /health 免鉴权返回 200。
func TestNew_AddrAndHealth(t *testing.T) {
	srv := newTestServer(t, func(c *config.Config) {
		c.Host = "127.0.0.1"
		c.Port = 18099
	})
	assert.Equal(t, "127.0.0.1:18099", srv.Addr)

	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest("GET", "/health", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "ok", rec.Body.String())
}

// TestNew_SecurityHeaders 验证装配链最外层附加了基础安全响应头。
func TestNew_SecurityHeaders(t *testing.T) {
	srv := newTestServer(t, nil)

	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest("GET", "/health", nil))
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "DENY", rec.Header().Get("X-Frame-Options"))
	assert.Equal(t, "no-referrer", rec.Header().Get("Referrer-Policy"))
}

// TestNew_HealthBypassesBasicAuth 验证启用 BasicAuth 后 /health 仍可免鉴权访问。
// Docker healthcheck 与桌面端就绪探测不携带凭据，必须放行。
func TestNew_HealthBypassesBasicAuth(t *testing.T) {
	srv := newTestServer(t, func(c *config.Config) {
		c.BasicAuthEnabled = true
		c.BasicAuthUser = "admin"
		c.BasicAuthPassword = "secret"
	})

	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest("GET", "/health", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestNew_BasicAuthChallenges 验证启用 BasicAuth 后受保护端点未带凭据返回 401，带正确凭据放行。
func TestNew_BasicAuthChallenges(t *testing.T) {
	srv := newTestServer(t, func(c *config.Config) {
		c.BasicAuthEnabled = true
		c.BasicAuthUser = "admin"
		c.BasicAuthPassword = "secret"
	})

	// 未带凭据 → 401 并携带 WWW-Authenticate
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.NotEmpty(t, rec.Header().Get("WWW-Authenticate"))

	// 正确凭据 → 放行（首页可能 404/200，取决于 index.html 是否存在，但不再是 401）
	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("admin", "secret")
	rec = httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)
	assert.NotEqual(t, http.StatusUnauthorized, rec.Code)
}

// TestNew_GeneratesPasswordWhenMissing 验证启用 BasicAuth 但未配置密码时自动生成随机口令。
// 取代固定弱默认值；生成的口令应为 32 位十六进制。
func TestNew_GeneratesPasswordWhenMissing(t *testing.T) {
	cfg := testutil.TestConfig()
	cfg.BasicAuthEnabled = true
	cfg.BasicAuthPassword = ""

	_, pool := New(cfg, make(chan struct{}))
	defer pool.CloseAll()

	require.NotEmpty(t, cfg.BasicAuthPassword, "password must be generated when missing")
	assert.Len(t, cfg.BasicAuthPassword, 32)
}

// TestNew_KeepsExplicitPassword 验证显式配置的密码不被覆盖。
func TestNew_KeepsExplicitPassword(t *testing.T) {
	cfg := testutil.TestConfig()
	cfg.BasicAuthEnabled = true
	cfg.BasicAuthPassword = "my-explicit-pass"

	_, pool := New(cfg, make(chan struct{}))
	defer pool.CloseAll()

	assert.Equal(t, "my-explicit-pass", cfg.BasicAuthPassword)
}
