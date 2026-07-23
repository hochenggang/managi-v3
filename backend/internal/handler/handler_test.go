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

// TestRegister 验证 Register 注册了全部路由（已注册路由不应返回 404）。
func TestRegister(t *testing.T) {
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "index.html")
	require.NoError(t, os.WriteFile(tmpFile, []byte("<html><body>ok</body></html>"), 0644))

	cfg := testutil.TestConfig()
	cfg.IndexHTMLPath = tmpFile

	mux := http.NewServeMux()
	Register(mux, cfg, nil)

	routes := []struct {
		method string
		path   string
	}{
		{"GET", "/"},
		{"POST", "/api/ssh/test"},
		{"POST", "/api/ssh/batch"},
		{"GET", "/ws/ssh"},
		{"GET", "/ws/sftp"},
		{"GET", "/api/sftp/download"},
	}

	for _, r := range routes {
		req := httptest.NewRequest(r.method, r.path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.NotEqual(t, http.StatusNotFound, rec.Code,
			"%s %s should not return 404 (route must be registered)", r.method, r.path)
	}
}

// TestIndexHandler 验证静态首页服务：存在文件返回 200，不存在返回 404。
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

	t.Run("non-existing file returns 404", func(t *testing.T) {
		cfg := testutil.TestConfig()
		cfg.IndexHTMLPath = "/nonexistent/path/that/does/not/exist.html"
		h := indexHandler(cfg)

		req := httptest.NewRequest("GET", "/", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusNotFound, rec.Code)
	})
}
