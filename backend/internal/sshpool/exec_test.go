package sshpool

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"managi/internal/testutil"
)

// TestLimitedBuffer_TruncatesAtLimit 验证跨上限的写入被丢弃且标记截断，
// 且 Write 始终报告全部字节已接收（否则 ssh 侧复制协程会因写失败提前断流）。
func TestLimitedBuffer_TruncatesAtLimit(t *testing.T) {
	b := newLimitedBuffer(8)
	n, err := b.Write([]byte("1234567890"))
	require.NoError(t, err)
	assert.Equal(t, 10, n)
	assert.Equal(t, "12345678", b.String())
	assert.True(t, b.truncated)
}

// TestLimitedBuffer_NoTruncateUnderLimit 验证未超限时不截断。
func TestLimitedBuffer_NoTruncateUnderLimit(t *testing.T) {
	b := newLimitedBuffer(8)
	_, _ = b.Write([]byte("1234"))
	_, _ = b.Write([]byte("5678"))
	assert.Equal(t, "12345678", b.String())
	assert.False(t, b.truncated)
}

// TestExecute_OutputTruncated 验证单路输出超上限时保留上限字节并给出提示行。
func TestExecute_OutputTruncated(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	pool := New(testutil.TestConfig())
	defer pool.CloseAll()

	node := testutil.TestNode(srv.Host(), srv.Port())
	// 用远端的 flood 命令生成大输出：命令串本身保持小体积
	//（SSH 单包约 256KB，把 4MB 文本塞进 echo 参数连 exec 请求都发不出去）
	output, errs, err := pool.Execute(context.Background(), node,
		[]string{fmt.Sprintf("flood %d", MaxExecOutputBytes+4096)})
	require.NoError(t, err)
	assert.Empty(t, errs)
	require.Len(t, output, 2)
	assert.Len(t, output[0], MaxExecOutputBytes)
	assert.Contains(t, output[1], "stdout")
	assert.Contains(t, output[1], "已截断")
}

// TestExecute_ContextTimeout 验证 ctx 到点后挂死的命令被终止且错误可归属于超时。
func TestExecute_ContextTimeout(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	pool := New(testutil.TestConfig())
	defer pool.CloseAll()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	node := testutil.TestNode(srv.Host(), srv.Port())
	_, _, err := pool.Execute(ctx, node, []string{"hang"})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}
