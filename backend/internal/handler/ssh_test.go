package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"managi/internal/model"
	"managi/internal/sshpool"
	"managi/internal/testutil"
)

// TestTestHandler_Success 验证单节点命令执行接口。
func TestTestHandler_Success(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	pool := sshpool.New(testutil.TestConfig())
	defer pool.CloseAll()

	h := testHandler(pool, testutil.TestConfig())

	body, _ := json.Marshal(map[string]any{
		"node": testutil.TestNode(srv.Host(), srv.Port()),
		"cmds": []string{"echo hi"},
	})

	req := httptest.NewRequest("POST", "/api/ssh/test", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var result model.CmdsTestResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	assert.True(t, result.Success)
	assert.Contains(t, result.Output, "hi")
	assert.Equal(t, "***", result.Node.AuthValue) // 验证脱敏
}

// TestTestHandler_BadJSON 验证非法 JSON 返回 400。
func TestTestHandler_BadJSON(t *testing.T) {
	pool := sshpool.New(testutil.TestConfig())
	defer pool.CloseAll()

	h := testHandler(pool, testutil.TestConfig())
	req := httptest.NewRequest("POST", "/api/ssh/test", bytes.NewReader([]byte("not json")))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestTestHandler_AuthFailure 验证认证失败时 Success=false。
func TestTestHandler_AuthFailure(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	pool := sshpool.New(testutil.TestConfig())
	defer pool.CloseAll()

	h := testHandler(pool, testutil.TestConfig())

	body, _ := json.Marshal(map[string]any{
		"node": testutil.BadPasswordNode(srv.Host(), srv.Port()),
		"cmds": []string{"echo hi"},
	})

	req := httptest.NewRequest("POST", "/api/ssh/test", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var result model.CmdsTestResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	assert.False(t, result.Success)
}

// TestBatchHandler_Success 验证批量命令执行。
func TestBatchHandler_Success(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	pool := sshpool.New(testutil.TestConfig())
	defer pool.CloseAll()

	h := batchHandler(pool, testutil.TestConfig())

	req := model.BatchCmdRequest{
		Nodes: []model.Node{
			testutil.TestNode(srv.Host(), srv.Port()),
			testutil.TestNode(srv.Host(), srv.Port()),
		},
		Cmds: []string{"echo batch"},
	}
	body, _ := json.Marshal(req)

	httpReq := httptest.NewRequest("POST", "/api/ssh/batch", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httpReq)

	require.Equal(t, http.StatusOK, rec.Code)

	var results []model.CmdsTestResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &results))
	assert.Len(t, results, 2)
	for _, r := range results {
		assert.True(t, r.Success)
		assert.Contains(t, r.Output, "batch")
	}
}

// TestBatchHandler_PartialFailure 验证部分失败时各自结果正确。
func TestBatchHandler_PartialFailure(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	pool := sshpool.New(testutil.TestConfig())
	defer pool.CloseAll()

	h := batchHandler(pool, testutil.TestConfig())

	req := model.BatchCmdRequest{
		Nodes: []model.Node{
			testutil.TestNode(srv.Host(), srv.Port()),       // 成功
			testutil.BadPasswordNode(srv.Host(), srv.Port()), // 失败
		},
		Cmds: []string{"echo ok"},
	}
	body, _ := json.Marshal(req)

	httpReq := httptest.NewRequest("POST", "/api/ssh/batch", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httpReq)

	require.Equal(t, http.StatusOK, rec.Code)

	var results []model.CmdsTestResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &results))
	assert.Len(t, results, 2)
	assert.True(t, results[0].Success)
	assert.False(t, results[1].Success)
}

// TestBatchHandler_BadJSON 验证非法 JSON 返回 400。
func TestBatchHandler_BadJSON(t *testing.T) {
	pool := sshpool.New(testutil.TestConfig())
	defer pool.CloseAll()

	h := batchHandler(pool, testutil.TestConfig())
	req := httptest.NewRequest("POST", "/api/ssh/batch", bytes.NewReader([]byte("bad")))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestExecuteSingle_MasksNode 验证返回的 Node 已脱敏。
func TestExecuteSingle_MasksNode(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	pool := sshpool.New(testutil.TestConfig())
	defer pool.CloseAll()

	node := testutil.TestNode(srv.Host(), srv.Port())
	result := executeSingle(pool, node, []string{"echo masked"})

	assert.Equal(t, "***", result.Node.AuthValue)
	assert.NotEqual(t, "testpass", result.Node.AuthValue)
}

// TestJoinCmds 验证命令拼接。
func TestJoinCmds(t *testing.T) {
	assert.Equal(t, "a", joinCmds([]string{"a"}))
	assert.Equal(t, "a\nb", joinCmds([]string{"a", "b"}))
	assert.Equal(t, "", joinCmds([]string{}))
}

// TestParseRangeOffset 验证 Range 头解析。
func TestParseRangeOffset(t *testing.T) {
	cases := []struct {
		input string
		want  int64
	}{
		{"", 0},
		{"bytes=0-", 0},
		{"bytes=10-", 10},
		{"bytes=1024-", 1024},
		{"bytes=-50", 0},      // 不支持 suffix range
		{"items=0-", 0},       // 非 bytes 前缀
		{"invalid", 0},
		{"bytes=abc-", 0},     // 非法数字
	}
	for _, c := range cases {
		assert.Equal(t, c.want, parseRangeOffset(c.input), "input=%q", c.input)
	}
}

// TestParseEnvelope 验证 envelope 解析（取代旧版 isResizeControl 前缀匹配）。
func TestParseEnvelope(t *testing.T) {
	// 合法 envelope
	env, ok := parseEnvelope([]byte(`{"type":"resize","data":{"cols":80,"rows":24}}`))
	assert.True(t, ok)
	assert.Equal(t, "resize", env.Type)

	env, ok = parseEnvelope([]byte(`{"type":"msg","data":"hello"}`))
	assert.True(t, ok)
	assert.Equal(t, "msg", env.Type)

	// 非法 JSON
	_, ok = parseEnvelope([]byte(`ls -la`))
	assert.False(t, ok)

	// 截断 JSON
	_, ok = parseEnvelope([]byte(`{"type":"resize"`))
	assert.False(t, ok)
}
