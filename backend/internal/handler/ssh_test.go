package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"managi/internal/model"
	"managi/internal/sshpool"
	"managi/internal/testutil"
)

// TestBatchHandler_Success 验证批量命令执行。
func TestBatchHandler_Success(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	pool := sshpool.New(testutil.TestConfig())
	defer pool.CloseAll()

	h := batchHandler(pool)

	req := model.BatchCmdRequest{
		Nodes: []model.Node{
			testutil.TestNode(srv.Host(), srv.Port()),
			testutil.TestNode(srv.Host(), srv.Port()),
		},
		Cmds: []string{"echo batch"},
	}
	body, _ := json.Marshal(req)

	httpReq := jsonPost("/api/ssh/batch", body)
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

	h := batchHandler(pool)

	// 两个节点须身份不同：连接池键为 host:port:username，若仅密码不同则键相同，
	// 并发执行时坏节点可能复用已建立的好连接而「假成功」，导致结果非确定。
	// 故失败节点用独立用户名（凭据仍不匹配 → 认证失败），确保键不同、结果确定。
	failingNode := testutil.TestNode(srv.Host(), srv.Port())
	failingNode.Username = "denied"
	failingNode.AuthValue = "wrong-password"

	req := model.BatchCmdRequest{
		Nodes: []model.Node{
			testutil.TestNode(srv.Host(), srv.Port()), // 成功
			failingNode, // 失败
		},
		Cmds: []string{"echo ok"},
	}
	body, _ := json.Marshal(req)

	httpReq := jsonPost("/api/ssh/batch", body)
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

	h := batchHandler(pool)
	req := jsonPost("/api/ssh/batch", []byte("bad"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestBatchHandler_TooManyNodes 验证节点数超上限直接 400，不进入逐个拨号。
func TestBatchHandler_TooManyNodes(t *testing.T) {
	pool := sshpool.New(testutil.TestConfig())
	defer pool.CloseAll()

	h := batchHandler(pool)
	req := model.BatchCmdRequest{
		Nodes: make([]model.Node, maxBatchNodes+1),
		Cmds:  []string{"echo x"},
	}
	body, _ := json.Marshal(req)

	httpReq := jsonPost("/api/ssh/batch", body)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httpReq)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "上限")
}

// TestExecuteSingle_TimeoutMessage 验证总时限到点时给出可读文案而非裸 ctx 错误。
func TestExecuteSingle_TimeoutMessage(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	pool := sshpool.New(testutil.TestConfig())
	defer pool.CloseAll()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	result := executeSingle(ctx, pool, testutil.TestNode(srv.Host(), srv.Port()), []string{"hang"})
	assert.False(t, result.Success)
	require.NotEmpty(t, result.Error)
	assert.Contains(t, result.Error[0], "超时")
}

// TestExecuteSingle_MasksNode 验证返回的 Node 已脱敏。
func TestExecuteSingle_MasksNode(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	pool := sshpool.New(testutil.TestConfig())
	defer pool.CloseAll()

	node := testutil.TestNode(srv.Host(), srv.Port())
	result := executeSingle(context.Background(), pool, node, []string{"echo masked"})

	assert.Equal(t, "***", result.Node.AuthValue)
	assert.NotEqual(t, "testpass", result.Node.AuthValue)
}

// TestJoinCmds 验证命令拼接。
func TestJoinCmds(t *testing.T) {
	assert.Equal(t, "a", joinCmds([]string{"a"}))
	assert.Equal(t, "a\nb", joinCmds([]string{"a", "b"}))
	assert.Equal(t, "", joinCmds([]string{}))
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
