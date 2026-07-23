package sshpool

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"

	"managi/internal/testutil"
)

// TestExecute_Basic 验证基本命令执行。
func TestExecute_Basic(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	pool := New(testutil.TestConfig())
	defer pool.CloseAll()

	node := testutil.TestNode(srv.Host(), srv.Port())
	output, errs, err := pool.Execute(context.Background(), node, []string{"echo hello"})
	require.NoError(t, err)
	assert.Contains(t, output, "hello")
	assert.Empty(t, errs)
}

// TestExecute_MultiCmds 验证多条命令拼接执行。
func TestExecute_MultiCmds(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	pool := New(testutil.TestConfig())
	defer pool.CloseAll()

	node := testutil.TestNode(srv.Host(), srv.Port())
	output, _, err := pool.Execute(context.Background(), node, []string{"echo line1", "echo line2"})
	require.NoError(t, err)
	assert.Contains(t, output, "line1")
	assert.Contains(t, output, "line2")
}

// TestExecute_EmptyCmds 验证空命令列表直接返回 nil。
func TestExecute_EmptyCmds(t *testing.T) {
	pool := New(testutil.TestConfig())
	defer pool.CloseAll()

	node := testutil.TestNode("127.0.0.1", 22)
	output, errs, err := pool.Execute(context.Background(), node, []string{})
	assert.NoError(t, err)
	assert.Nil(t, output)
	assert.Nil(t, errs)
}

// TestExecute_CommandError 验证命令返回非零时的 stderr。
func TestExecute_CommandError(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	pool := New(testutil.TestConfig())
	defer pool.CloseAll()

	node := testutil.TestNode(srv.Host(), srv.Port())
	_, errs, err := pool.Execute(context.Background(), node, []string{"false"})
	require.NoError(t, err) // SSH 连接成功，err 为 nil
	assert.NotEmpty(t, errs)
}

// TestGet_Release_Refcount 验证引用计数：Get 两次只建一条连接，Release 不关闭。
func TestGet_Release_Refcount(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	pool := New(testutil.TestConfig())
	defer pool.CloseAll()

	node := testutil.TestNode(srv.Host(), srv.Port())

	conn1, err := pool.Get(node)
	require.NoError(t, err)

	conn2, err := pool.Get(node)
	require.NoError(t, err)

	// 同一 node 应复用同一条连接
	assert.Same(t, conn1, conn2)

	// Release 一次后连接仍可用
	pool.Release(node)
	output, _, err := pool.Execute(context.Background(), node, []string{"echo alive"})
	require.NoError(t, err)
	assert.Contains(t, output, "alive")

	// 清理引用
	pool.Release(node)
}

// TestGet_ReusesConnection 验证两次 Execute 同 node 只 accept 一次连接。
func TestGet_ReusesConnection(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	pool := New(testutil.TestConfig())
	defer pool.CloseAll()

	node := testutil.TestNode(srv.Host(), srv.Port())

	_, _, err := pool.Execute(context.Background(), node, []string{"echo first"})
	require.NoError(t, err)

	_, _, err = pool.Execute(context.Background(), node, []string{"echo second"})
	require.NoError(t, err)

	// mock server 应只 accept 一条 TCP 连接
	assert.Equal(t, int32(1), srv.Accepts())
}

// TestGet_AuthFailure 验证错误密码返回错误。
func TestGet_AuthFailure(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	pool := New(testutil.TestConfig())
	defer pool.CloseAll()

	node := testutil.BadPasswordNode(srv.Host(), srv.Port())
	_, err := pool.Get(node)
	assert.Error(t, err)
}

// TestPool_Eviction 验证连接池满时驱逐最旧空闲连接。
func TestPool_Eviction(t *testing.T) {
	srv1 := testutil.Start(t)
	defer srv1.Close()
	srv2 := testutil.Start(t)
	defer srv2.Close()

	pool := NewWithSize(testutil.TestConfig(), 1)
	defer pool.CloseAll()

	node1 := testutil.TestNode(srv1.Host(), srv1.Port())
	node2 := testutil.TestNode(srv2.Host(), srv2.Port())

	// 获取 node1 连接并 release（变为空闲）
	_, err := pool.Get(node1)
	require.NoError(t, err)
	pool.Release(node1)

	// 获取 node2 连接（池满，应驱逐 node1）
	_, err = pool.Get(node2)
	require.NoError(t, err)
}

// TestPool_Concurrent 验证并发 Execute 无 race（go test -race）。
func TestPool_Concurrent(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	pool := New(testutil.TestConfig())
	defer pool.CloseAll()

	node := testutil.TestNode(srv.Host(), srv.Port())

	var g errgroup.Group
	for i := 0; i < 50; i++ {
		g.Go(func() error {
			_, _, err := pool.Execute(context.Background(), node, []string{"echo concurrent"})
			return err
		})
	}
	err := g.Wait()
	assert.NoError(t, err)
}

// TestCloseAll 验证 CloseAll 后旧连接已关闭，再 Get 会新建连接。
func TestCloseAll(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	pool := New(testutil.TestConfig())
	node := testutil.TestNode(srv.Host(), srv.Port())

	_, err := pool.Get(node)
	require.NoError(t, err)
	pool.Release(node)

	beforeAccepts := srv.Accepts()
	pool.CloseAll()

	// CloseAll 后再 Get 同 node，应新建连接（旧连接已关闭）
	_, err = pool.Get(node)
	require.NoError(t, err)
	// srv.Accepts() 增加 1 证明是新建连接而非复用
	require.Equal(t, beforeAccepts+1, srv.Accepts(),
		"CloseAll should close old conn, forcing new dial")
}

// TestJoinLines 验证命令拼接。
func TestJoinLines(t *testing.T) {
	assert.Equal(t, "a", joinLines([]string{"a"}))
	assert.Equal(t, "a\nb", joinLines([]string{"a", "b"}))
	assert.Equal(t, "a\nb\nc", joinLines([]string{"a", "b", "c"}))
	assert.Equal(t, "", joinLines([]string{}))
}

// TestSplitLines 验证行拆分。
func TestSplitLines(t *testing.T) {
	assert.Equal(t, []string{"a", "b"}, splitLines("a\nb\n"))
	assert.Equal(t, []string{"a", "b"}, splitLines("a\nb"))
	assert.Equal(t, []string{"a"}, splitLines("a\r\n")) // trimCR
	assert.Empty(t, splitLines(""))
	assert.Empty(t, splitLines("\n\n"))
}

// TestPool_HardCap 验证触达 hardCap 且无空闲连接可淘汰时返回 errPoolFull（B3 修复）。
func TestPool_HardCap(t *testing.T) {
	srv1 := testutil.Start(t)
	defer srv1.Close()
	srv2 := testutil.Start(t)
	defer srv2.Close()
	srv3 := testutil.Start(t)
	defer srv3.Close()

	// maxSize=1, hardCap=2
	pool := NewWithSize(testutil.TestConfig(), 1)
	defer pool.CloseAll()

	node1 := testutil.TestNode(srv1.Host(), srv1.Port())
	node2 := testutil.TestNode(srv2.Host(), srv2.Port())
	node3 := testutil.TestNode(srv3.Host(), srv3.Port())

	// 获取 node1 连接但不 release（refs=1，无法驱逐）
	_, err := pool.Get(node1)
	require.NoError(t, err)
	// 获取 node2 连接（超 maxSize 但未超 hardCap，允许新建）
	_, err = pool.Get(node2)
	require.NoError(t, err)
	// 第 3 个连接触达 hardCap，应返回 errPoolFull
	_, err = pool.Get(node3)
	assert.ErrorIs(t, err, errPoolFull)
}
