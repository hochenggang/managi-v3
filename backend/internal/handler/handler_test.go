package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"managi/internal/testutil"
)

// jsonPost 构造带 application/json Content-Type 的 POST 请求。
// decodeJSONRequest 强制该头（CSRF 防线），构造请求的测试统一走这里，避免逐处漏设。
func jsonPost(path string, body []byte) *http.Request {
	req := httptest.NewRequest("POST", path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

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
		{"POST", "/api/ssh/batch"},
		{"GET", "/ws"},
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

// TestRegister_MethodRouting 验证写操作 API 挡下非 POST 方法（405 + Allow）。
// 凭据只能走请求体：GET + 查询串会把密码留在 URL、浏览器历史与访问日志里。
func TestRegister_MethodRouting(t *testing.T) {
	mux := newTestMux(t)

	for _, p := range []string{"/api/ssh/batch", "/api/sftp/download"} {
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

// TestRegister_CSRGGates 验证两道跨站闸在路由层真实生效（闸未接上时测试必失败）。
func TestRegister_CSRGGates(t *testing.T) {
	mux := newTestMux(t)
	body := []byte(`{"nodes":[],"cmds":[]}`) // 空节点：即便放行也不会拨号

	// 浏览器跨站页面发起的请求（会自动携带 Basic Auth 缓存凭据）→ 403
	for _, site := range []string{"cross-site", "same-site"} {
		req := jsonPost("/api/ssh/batch", body)
		req.Header.Set("Sec-Fetch-Site", site)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusForbidden, rec.Code, "Sec-Fetch-Site=%s", site)
	}

	// 跨站表单唯一能发出来的简单类型（无预检）→ 415
	req := httptest.NewRequest("POST", "/api/ssh/batch", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnsupportedMediaType, rec.Code)
}

// TestAPIPost_AllowsSameOriginAndNonBrowser 验证闸门只拦跨站：
// 同源页面、地址栏直发（none）与不发送该头的非浏览器客户端都应放行。
func TestAPIPost_AllowsSameOriginAndNonBrowser(t *testing.T) {
	h := apiPost(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

	for _, site := range []string{"", "same-origin", "none"} {
		req := httptest.NewRequest("POST", "/api/x", nil)
		if site != "" {
			req.Header.Set("Sec-Fetch-Site", site)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusNoContent, rec.Code, "Sec-Fetch-Site=%q 应放行", site)
	}

	// 方法闸仍在：非 POST → 405 + Allow
	req := httptest.NewRequest("GET", "/api/x", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	assert.Equal(t, "POST", rec.Header().Get("Allow"))
}

// TestDecodeJSONRequest_Guards 验证请求体三道闸：Content-Type、合法性、单一 JSON 值。
func TestDecodeJSONRequest_Guards(t *testing.T) {
	serve := func(ct, body string) (bool, *httptest.ResponseRecorder) {
		var dst map[string]any
		req := httptest.NewRequest("POST", "/x", strings.NewReader(body))
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		rec := httptest.NewRecorder()
		return decodeJSONRequest(rec, req, &dst), rec
	}

	// 合法：标准类型与带 charset 参数都接受，尾随空白不算多余内容
	ok, _ := serve("application/json", `{"a":1}`)
	assert.True(t, ok)
	ok, _ = serve("application/json; charset=utf-8", `{"a":1}`+"\n")
	assert.True(t, ok)

	// 缺头 / 跨站表单能发的类型 → 415
	for _, ct := range []string{"", "text/plain", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x", "text/json"} {
		ok, rec := serve(ct, `{"a":1}`)
		assert.False(t, ok, "ct=%q", ct)
		assert.Equal(t, http.StatusUnsupportedMediaType, rec.Code, "ct=%q", ct)
	}

	// 非法 JSON → 400
	ok, rec := serve("application/json", `{broken`)
	assert.False(t, ok)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	// 尾部多余内容 → 400（第二个 JSON 值或杂字符都不行）
	for _, body := range []string{`{"a":1}{"b":2}`, `{"a":1}=`, `{"a":1}garbage`} {
		ok, rec := serve("application/json", body)
		assert.False(t, ok, "body=%q", body)
		assert.Equal(t, http.StatusBadRequest, rec.Code, "body=%q", body)
	}
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
