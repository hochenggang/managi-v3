package handler

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"managi/internal/testutil"
)

// memHandler 把 slog 记录拼成一行文本，仅供断言使用。
type memHandler struct{ lines *[]string }

func (m *memHandler) Enabled(context.Context, slog.Level) bool { return true }
func (m *memHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Message)
	r.Attrs(func(a slog.Attr) bool {
		b.WriteString(" " + a.Key + "=" + a.Value.String())
		return true
	})
	*m.lines = append(*m.lines, b.String())
	return nil
}
func (m *memHandler) WithAttrs([]slog.Attr) slog.Handler { return m }
func (m *memHandler) WithGroup(string) slog.Handler      { return m }

// captureLogs 临时接管 slog 默认输出，测试结束自动还原。
func captureLogs(t *testing.T) *[]string {
	t.Helper()
	lines := &[]string{}
	prev := slog.Default()
	slog.SetDefault(slog.New(&memHandler{lines: lines}))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return lines
}

// TestAccessLog_LogsRequest 验证访问日志含方法、路径、状态与写出字节，且跳过 /health。
func TestAccessLog_LogsRequest(t *testing.T) {
	lines := captureLogs(t)

	h := AccessLog(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("hi"))
	}), false)

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/ssh/test", nil))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/health", nil))

	require.Len(t, *lines, 1, "/health 不应产生日志")
	assert.Contains(t, (*lines)[0], "method=POST")
	assert.Contains(t, (*lines)[0], "path=/api/ssh/test")
	assert.Contains(t, (*lines)[0], "status=200")
	assert.Contains(t, (*lines)[0], "bytes=2")
	assert.Contains(t, (*lines)[0], "remote=192.0.2.1")
}

// TestAccessLog_NeverLogsCredentials 验证凭据绝不入日志：
// 访问日志会流向 journald / 容器标准输出，写下 Authorization 或查询串等于把密码抄送出去。
func TestAccessLog_NeverLogsCredentials(t *testing.T) {
	lines := captureLogs(t)

	cfg := testutil.TestConfig()
	cfg.BasicAuthEnabled = true
	cfg.BasicAuthUser = "admin"
	cfg.BasicAuthPassword = "s3cr3t-passphrase"

	h := AccessLog(BasicAuthMiddleware(cfg, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})), false)

	// 错误口令（401）与正确口令（200）都要留痕，但都不能带出凭据
	req := httptest.NewRequest("POST", "/api/ssh/test?auth_value=query-secret", nil)
	req.SetBasicAuth("admin", "header-secret")
	h.ServeHTTP(httptest.NewRecorder(), req)

	req2 := httptest.NewRequest("GET", "/api/ssh/test", nil)
	req2.SetBasicAuth("admin", cfg.BasicAuthPassword)
	h.ServeHTTP(httptest.NewRecorder(), req2)

	require.Len(t, *lines, 2)
	for _, line := range *lines {
		for _, secret := range []string{"s3cr3t-passphrase", "header-secret", "query-secret", "Authorization", "Basic "} {
			assert.NotContains(t, line, secret)
		}
	}
	assert.Contains(t, (*lines)[0], "status=401")
	assert.Contains(t, (*lines)[1], "status=200")
}

// TestAccessLog_PreservesHijacker 验证包装后仍是 Hijacker 与 Flusher：
// 缺 Hijacker 则 WS 升级直接失败，缺 Flusher 则流式下载无法及时刷新。
func TestAccessLog_PreservesHijacker(t *testing.T) {
	var isHijacker, isFlusher bool
	h := AccessLog(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, isHijacker = w.(http.Hijacker)
		f, ok := w.(http.Flusher)
		isFlusher = ok
		if ok {
			f.Flush()
		}
	}), false)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))

	assert.True(t, isHijacker)
	assert.True(t, isFlusher)
}

// TestAccessLog_TrustProxyOptIn 验证访问日志里的 remote 与登录限流同源：
// 默认只记真实连接地址，MANAGI_TRUST_PROXY=true 才采信 X-Forwarded-For 首段。
// 伪造该头即可让限流与日志按假 IP 计数，因此采信必须是显式选择。
func TestAccessLog_TrustProxyOptIn(t *testing.T) {
	lines := captureLogs(t)
	h := AccessLog(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}), true)

	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "203.0.113.9:55555"
	req.Header.Set("X-Forwarded-For", "198.51.100.7, 203.0.113.9")
	h.ServeHTTP(httptest.NewRecorder(), req)

	require.Len(t, *lines, 1)
	assert.Contains(t, (*lines)[0], "remote=198.51.100.7")
}

// 编译期断言：包装类型必须自带这两个接口，否则上面的透传只是空话
var (
	_ http.Hijacker = (*loggedResponse)(nil)
	_ http.Flusher  = (*loggedResponse)(nil)
)
