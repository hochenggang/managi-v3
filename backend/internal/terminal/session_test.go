package terminal

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"managi/internal/testutil"
)

// dialMock 直连 mock SSH 服务器。
func dialMock(t *testing.T, srv *testutil.Server) *ssh.Client {
	t.Helper()
	cfg := &ssh.ClientConfig{
		User:            "test",
		Auth:            []ssh.AuthMethod{ssh.Password(srv.Password())},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}
	client, err := ssh.Dial("tcp", srv.Addr(), cfg)
	require.NoError(t, err)
	return client
}

// TestOpen_Resize_Close 验证 PTY 打开、调整大小、关闭。
func TestOpen_Resize_Close(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	sshc := dialMock(t, srv)
	defer func() { _ = sshc.Close() }()

	sess := New(sshc)

	// Open PTY
	err := sess.Open(80, 24)
	require.NoError(t, err)

	// Resize
	err = sess.Resize(120, 40)
	assert.NoError(t, err)

	// Close
	err = sess.Close()
	assert.NoError(t, err)
}

// TestResize_NotOpened 验证未 Open 时 Resize 返回错误。
func TestResize_NotOpened(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	sshc := dialMock(t, srv)
	defer func() { _ = sshc.Close() }()

	sess := New(sshc)
	err := sess.Resize(80, 24)
	assert.Error(t, err)
}

// TestResize_RejectsNonPositiveSize 验证 0/负尺寸被拒绝：
// 客户端在拿到真实尺寸前常先发来 0×0，下发给 PTY 会让输出换行全乱。
func TestResize_RejectsNonPositiveSize(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	sshc := dialMock(t, srv)
	defer func() { _ = sshc.Close() }()

	sess := New(sshc)
	require.NoError(t, sess.Open(80, 24))
	defer func() { _ = sess.Close() }()

	for _, tc := range []struct{ cols, rows int }{{0, 24}, {80, 0}, {-1, 24}, {80, -1}} {
		err := sess.Resize(tc.cols, tc.rows)
		require.Error(t, err, "cols=%d rows=%d must be rejected", tc.cols, tc.rows)
		assert.Contains(t, err.Error(), "invalid terminal size")
	}

	// 合法尺寸仍然可用
	assert.NoError(t, sess.Resize(120, 40))
}

// TestClose_NotOpened 验证未 Open 时 Close 不 panic。
func TestClose_NotOpened(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	sshc := dialMock(t, srv)
	defer func() { _ = sshc.Close() }()

	sess := New(sshc)
	err := sess.Close()
	assert.NoError(t, err) // nil session → no-op
}

// TestStdin_Stdout_Echo 验证 shell 回显：写入 stdin → 从 stdout 读到回显。
func TestStdin_Stdout_Echo(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	sshc := dialMock(t, srv)
	defer func() { _ = sshc.Close() }()

	sess := New(sshc)
	require.NoError(t, sess.Open(80, 24))
	defer func() { _ = sess.Close() }()

	stdin := sess.Stdin()
	stdout := sess.Stdout()

	require.NotNil(t, stdin)
	require.NotNil(t, stdout)

	// 写入数据
	testData := []byte("hello shell\n")
	_, err := stdin.Write(testData)
	require.NoError(t, err)

	// 读取回显（mock server 回显输入）
	buf := make([]byte, 256)

	// 带超时读取
	type readResult struct {
		n   int
		err error
	}
	ch := make(chan readResult, 1)
	go func() {
		n, err := stdout.Read(buf)
		ch <- readResult{n, err}
	}()

	select {
	case res := <-ch:
		assert.NoError(t, res.err)
		assert.Equal(t, testData, buf[:res.n])
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for echo")
	}
}

// TestOpen_AuthFailure 验证认证失败无法建连。
func TestOpen_AuthFailure(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	// 错误密码
	cfg := &ssh.ClientConfig{
		User:            "test",
		Auth:            []ssh.AuthMethod{ssh.Password("wrong")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}
	_, err := ssh.Dial("tcp", srv.Addr(), cfg)
	assert.Error(t, err) // 认证失败 → Dial 返回错误
}
