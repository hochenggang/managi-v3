package terminal

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"managi/internal/model"
	"managi/internal/testutil"
)

// dialMock 直连 mock SSH 服务器。
func dialMock(t *testing.T, srv *testutil.Server) (*ssh.Client, model.Node) {
	t.Helper()
	node := testutil.TestNode(srv.Host(), srv.Port())
	cfg := &ssh.ClientConfig{
		User:            "test",
		Auth:            []ssh.AuthMethod{ssh.Password(srv.Password())},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}
	client, err := ssh.Dial("tcp", srv.Addr(), cfg)
	require.NoError(t, err)
	return client, node
}

// TestOpen_Resize_Close 验证 PTY 打开、调整大小、关闭。
func TestOpen_Resize_Close(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	sshc, node := dialMock(t, srv)
	defer sshc.Close()

	sess := New(node, sshc)

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

	sshc, node := dialMock(t, srv)
	defer sshc.Close()

	sess := New(node, sshc)
	err := sess.Resize(80, 24)
	assert.Error(t, err)
}

// TestClose_NotOpened 验证未 Open 时 Close 不 panic。
func TestClose_NotOpened(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	sshc, node := dialMock(t, srv)
	defer sshc.Close()

	sess := New(node, sshc)
	err := sess.Close()
	assert.NoError(t, err) // nil session → no-op
}

// TestStdin_Stdout_Echo 验证 shell 回显：写入 stdin → 从 stdout 读到回显。
func TestStdin_Stdout_Echo(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	sshc, node := dialMock(t, srv)
	defer sshc.Close()

	sess := New(node, sshc)
	require.NoError(t, sess.Open(80, 24))
	defer sess.Close()

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
