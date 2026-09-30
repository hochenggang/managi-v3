package handler

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"managi/internal/testutil"
)

// newTestMux 注册全部路由供路由层测试使用（handler 层测试仍直接调 handler）。
func newTestMux(t *testing.T) *http.ServeMux {
	t.Helper()
	tmpFile := filepath.Join(t.TempDir(), "index.html")
	require.NoError(t, os.WriteFile(tmpFile, []byte("<html><body>ok</body></html>"), 0644))

	cfg := testutil.TestConfig()
	cfg.IndexHTMLPath = tmpFile

	mux := http.NewServeMux()
	Register(mux, cfg, nil)
	return mux
}

// TestRegister 验证 Register 注册了全部路由（已注册路由不应返回 404）。
func TestRegister(t *testing.T) {
	mux := newTestMux(t)

	routes := []struct {
		method string
		path   string
	}{
		{"GET", "/"},
		{"POST", "/api/ssh/test"},
		{"POST", "/api/ssh/batch"},
		{"GET", "/ws/ssh"},
		{"GET", "/ws/sftp"},
		{"POST", "/api/sftp/download"},
	}

	for _, r := range routes {
		req := httptest.NewRequest(r.method, r.path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.NotEqual(t, http.StatusNotFound, rec.Code,
			"%s %s should not return 404 (route must be registered)", r.method, r.path)
	}
}

// TestRegister_MethodRouting 验证 POST-only 端点由路由层挡下其他方法。
// 凭据只能走请求体：GET + 查询串会把密码留在 URL、浏览器历史与访问日志里。
// 方法校验统一交给 ServeMux 的 "POST /x" 模式，handler 内不再各自重复。
func TestRegister_MethodRouting(t *testing.T) {
	mux := newTestMux(t)

	for _, p := range []string{"/api/ssh/test", "/api/ssh/batch", "/api/sftp/download"} {
		req := httptest.NewRequest("GET", p+"?node=%7B%7D&path=/x.txt", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code, p)
		assert.Equal(t, "POST", rec.Header().Get("Allow"), p)
	}
}

// TestRegister_UnknownAPIPath 验证未注册的 /api/ 路径回 JSON 404：
// 否则会被 "/" 首页兜底吞掉，前端 res.json() 抛错却看不出是路径写错。
func TestRegister_UnknownAPIPath(t *testing.T) {
	mux := newTestMux(t)

	req := httptest.NewRequest("POST", "/api/sftp/nope", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "application/json")
	assert.Contains(t, rec.Body.String(), "unknown api path")
}

// TestIndexHandler 验证静态首页服务：存在文件返回 200，缺失返回可操作提示。
func TestIndexHandler(t *testing.T) {
	t.Run("existing file returns 200 with text/html", func(t *testing.T) {
		tmpDir := t.TempDir()
		tmpFile := filepath.Join(tmpDir, "index.html")
		require.NoError(t, os.WriteFile(tmpFile, []byte("<html><body>hello</body></html>"), 0644))

		cfg := testutil.TestConfig()
		cfg.IndexHTMLPath = tmpFile
		h := indexHandler(cfg)

		req := httptest.NewRequest("GET", "/", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Header().Get("Content-Type"), "text/html")
		assert.Contains(t, rec.Body.String(), "hello")
	})

	t.Run("missing file explains how to fix it", func(t *testing.T) {
		cfg := testutil.TestConfig()
		cfg.IndexHTMLPath = filepath.Join(t.TempDir(), "does-not-exist.html")
		h := indexHandler(cfg)

		req := httptest.NewRequest("GET", "/", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
		assert.Contains(t, rec.Body.String(), "未找到前端页面")
		assert.Contains(t, rec.Body.String(), "MANAGI_INDEX_HTML")
	})
}
