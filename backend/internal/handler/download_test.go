package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"managi/internal/model"
	"managi/internal/sshpool"
	"managi/internal/testutil"
)

// ===== POST /api/sftp/download =====

// downloadBody 构造 POST /api/sftp/download 的 JSON 请求体。
func downloadBody(t *testing.T, node model.Node, path string) *bytes.Reader {
	t.Helper()
	b, err := json.Marshal(sftpDownloadRequest{Node: node, Path: path})
	require.NoError(t, err)
	return bytes.NewReader(b)
}

// newDownloadHandler 创建连到 mock server 的下载 handler 及其连接池。
func newDownloadHandler(t *testing.T) http.HandlerFunc {
	t.Helper()
	pool := sshpool.New(testutil.TestConfig())
	t.Cleanup(pool.CloseAll)
	return sftpDownloadHandler(pool)
}

// TestSftpDownloadHandler_Full 验证完整下载：200 + 完整内容 + Accept-Ranges。
func TestSftpDownloadHandler_Full(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	content := []byte("0123456789ABCDEFGHIJ") // 20 bytes
	require.NoError(t, os.WriteFile(filepath.Join(srv.RootDir(), "test.txt"), content, 0644))

	req := httptest.NewRequest("POST", "/api/sftp/download",
		downloadBody(t, testutil.TestNode(srv.Host(), srv.Port()), "/test.txt"))
	rec := httptest.NewRecorder()
	newDownloadHandler(t).ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "bytes", rec.Header().Get("Accept-Ranges"))
	// Content-Length 必须有：前端靠它算进度，缺了就只能先收完再猜总长
	assert.Equal(t, "20", rec.Header().Get("Content-Length"))
	assert.Equal(t, content, rec.Body.Bytes())
}

// GET 拒绝用例见 TestRegister_MethodRouting：方法限制已上移到路由模式。

// TestSftpDownloadHandler_Range 验证 Range 下载：206 + Content-Range + 部分内容。
func TestSftpDownloadHandler_Range(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	content := []byte("0123456789ABCDEFGHIJ") // 20 bytes
	require.NoError(t, os.WriteFile(filepath.Join(srv.RootDir(), "test.txt"), content, 0644))

	req := httptest.NewRequest("POST", "/api/sftp/download",
		downloadBody(t, testutil.TestNode(srv.Host(), srv.Port()), "/test.txt"))
	req.Header.Set("Range", "bytes=10-")
	rec := httptest.NewRecorder()
	newDownloadHandler(t).ServeHTTP(rec, req)

	require.Equal(t, http.StatusPartialContent, rec.Code)
	assert.Equal(t, "bytes 10-19/20", rec.Header().Get("Content-Range"))
	assert.Equal(t, content[10:], rec.Body.Bytes())
}

// TestSftpDownloadHandler_RangeBeyondEOF 验证起点越过文件末尾回 416：
// 若返回 200/206 空体，续传客户端会认为剩余部分已取完，停在错误偏移不再重试。
func TestSftpDownloadHandler_RangeBeyondEOF(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	require.NoError(t, os.WriteFile(filepath.Join(srv.RootDir(), "small.txt"), []byte("0123456789"), 0644))

	for _, header := range []string{"bytes=10-", "bytes=999-"} {
		req := httptest.NewRequest("POST", "/api/sftp/download",
			downloadBody(t, testutil.TestNode(srv.Host(), srv.Port()), "/small.txt"))
		req.Header.Set("Range", header)
		rec := httptest.NewRecorder()
		newDownloadHandler(t).ServeHTTP(rec, req)

		// 只有「起点就在文件外」才不可满足；结束位超出末尾按 RFC 7233 截到文件尾，仍是 206。
		require.Equal(t, http.StatusRequestedRangeNotSatisfiable, rec.Code, "header=%s", header)
		assert.Equal(t, "bytes */10", rec.Header().Get("Content-Range"))
	}
}

// TestSftpDownloadHandler_ClosedRange 验证闭区间 Range 精确交付那一段字节：
// 前端分块续传与命令行工具用的都是 bytes=a-b，忽略结束位会让客户端多收到数据。
func TestSftpDownloadHandler_ClosedRange(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	content := []byte("0123456789ABCDEFGHIJ") // 20 bytes
	require.NoError(t, os.WriteFile(filepath.Join(srv.RootDir(), "closed.txt"), content, 0644))

	req := httptest.NewRequest("POST", "/api/sftp/download",
		downloadBody(t, testutil.TestNode(srv.Host(), srv.Port()), "/closed.txt"))
	req.Header.Set("Range", "bytes=5-9")
	rec := httptest.NewRecorder()
	newDownloadHandler(t).ServeHTTP(rec, req)

	require.Equal(t, http.StatusPartialContent, rec.Code)
	assert.Equal(t, "bytes 5-9/20", rec.Header().Get("Content-Range"))
	assert.Equal(t, content[5:10], rec.Body.Bytes())

	// 结束位越过文件尾：按标准截到最后一字节，而不是报错或多发
	req2 := httptest.NewRequest("POST", "/api/sftp/download",
		downloadBody(t, testutil.TestNode(srv.Host(), srv.Port()), "/closed.txt"))
	req2.Header.Set("Range", "bytes=15-200")
	rec2 := httptest.NewRecorder()
	newDownloadHandler(t).ServeHTTP(rec2, req2)
	require.Equal(t, http.StatusPartialContent, rec2.Code)
	assert.Equal(t, "bytes 15-19/20", rec2.Header().Get("Content-Range"))
	assert.Equal(t, content[15:], rec2.Body.Bytes())
}

// TestSftpDownloadHandler_SuffixRange 验证后缀区间 bytes=-n 交付末尾 n 个字节。
func TestSftpDownloadHandler_SuffixRange(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	content := []byte("0123456789ABCDEFGHIJ")
	require.NoError(t, os.WriteFile(filepath.Join(srv.RootDir(), "suffix.txt"), content, 0644))

	req := httptest.NewRequest("POST", "/api/sftp/download",
		downloadBody(t, testutil.TestNode(srv.Host(), srv.Port()), "/suffix.txt"))
	req.Header.Set("Range", "bytes=-4")
	rec := httptest.NewRecorder()
	newDownloadHandler(t).ServeHTTP(rec, req)

	require.Equal(t, http.StatusPartialContent, rec.Code)
	assert.Equal(t, "bytes 16-19/20", rec.Header().Get("Content-Range"))
	assert.Equal(t, content[16:], rec.Body.Bytes())
}

// TestSftpDownloadHandler_MissingParams 验证缺少参数返回 400。
func TestSftpDownloadHandler_MissingParams(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	h := newDownloadHandler(t)
	node := testutil.TestNode(srv.Host(), srv.Port())

	// 缺 path
	req := httptest.NewRequest("POST", "/api/sftp/download", downloadBody(t, node, ""))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	// 缺 node（host 为空）
	req = httptest.NewRequest("POST", "/api/sftp/download", downloadBody(t, model.Node{}, "/test.txt"))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestSftpDownloadHandler_InvalidBody 验证非法请求体返回 400。
func TestSftpDownloadHandler_InvalidBody(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/sftp/download", bytes.NewReader([]byte("notjson")))
	rec := httptest.NewRecorder()
	newDownloadHandler(t).ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestSftpDownloadHandler_AuthFailure 验证认证失败返回 502。
func TestSftpDownloadHandler_AuthFailure(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	req := httptest.NewRequest("POST", "/api/sftp/download",
		downloadBody(t, testutil.BadPasswordNode(srv.Host(), srv.Port()), "/test.txt"))
	rec := httptest.NewRecorder()
	newDownloadHandler(t).ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadGateway, rec.Code)
}
