package sshpool

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	crand "crypto/rand"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
	"golang.org/x/sync/errgroup"

	"managi/internal/model"
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
	pool.Release(conn2)
	output, _, err := pool.Execute(context.Background(), node, []string{"echo alive"})
	require.NoError(t, err)
	assert.Contains(t, output, "alive")

	// 清理引用
	pool.Release(conn2)
}

// TestRelease_DeadConnectionDoesNotHarmNew 验证 Release 按连接对象身份归还。
// 死连接被剔除、同 key 新连接入池后，旧持有者迟到的 Release 若按 key 查找，
// 会把新连接的 refs 从 1 误减为 0，正在使用的连接随即被 cleanIdle 当空闲回收。
func TestRelease_DeadConnectionDoesNotHarmNew(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	pool := New(testutil.TestConfig())
	defer pool.CloseAll()

	node := testutil.TestNode(srv.Host(), srv.Port())
	stale, err := pool.Get(node)
	require.NoError(t, err)

	// 模拟连接死亡：Get 的 isAlive 探测会失败并剔除它，随后拨入新连接
	require.NoError(t, stale.client.Close())
	fresh, err := pool.Get(node)
	require.NoError(t, err)
	require.NotSame(t, stale, fresh)

	pool.Release(stale)

	pool.mu.Lock()
	refs := fresh.refs
	pool.mu.Unlock()
	assert.Equal(t, 1, refs, "late Release of a stale connection must not decrement the replacement")
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
	conn1, err := pool.Get(node1)
	require.NoError(t, err)
	pool.Release(conn1)

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

	conn, err := pool.Get(node)
	require.NoError(t, err)
	pool.Release(conn)

	beforeAccepts := srv.Accepts()
	pool.CloseAll()

	// CloseAll 后再 Get 同 node，应新建连接（旧连接已关闭）
	_, err = pool.Get(node)
	require.NoError(t, err)
	// srv.Accepts() 增加 1 证明是新建连接而非复用
	require.Equal(t, beforeAccepts+1, srv.Accepts(),
		"CloseAll should close old conn, forcing new dial")
}

// TestSplitLines 验证行拆分。
func TestSplitLines(t *testing.T) {
	assert.Equal(t, []string{"a", "b"}, splitLines("a\nb\n"))
	assert.Equal(t, []string{"a", "b"}, splitLines("a\nb"))
	assert.Equal(t, []string{"a"}, splitLines("a\r\n")) // trimCR
	assert.Empty(t, splitLines(""))
	assert.Empty(t, splitLines("\n\n"))
	// 中间空行是内容（cat/df 输出），不得被吞掉
	assert.Equal(t, []string{"a", "", "b"}, splitLines("a\n\nb\n"))
	assert.Equal(t, "a\n\nb", strings.Join(splitLines("a\n\nb\n"), "\n"))
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

// TestGet_KeyLocksReclaimed 验证 per-key 锁字典随使用回收而非累积：
// 成功获取/释放后、以及大量失败节点依次尝试后，字典都应归零。
// 回归旧实现「锁字典只增不删」的内存增长缺陷。
func TestGet_KeyLocksReclaimed(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	pool := New(testutil.TestConfig())
	defer pool.CloseAll()

	node := testutil.TestNode(srv.Host(), srv.Port())
	c, err := pool.Get(node)
	require.NoError(t, err)
	require.NotNil(t, c)
	pool.Release(c)
	assert.Equal(t, 0, pool.keyLocks.Len(), "entry must be reclaimed after Get/Release")

	// 大量拨号必然失败的节点（关闭的端口），每次 Get 走完加解锁后条目须被回收
	for i := 0; i < 50; i++ {
		bad := model.Node{Host: "127.0.0.1", Port: 1, Username: "x", AuthType: model.AuthPassword, AuthValue: "p"}
		_, _ = pool.Get(bad)
	}
	assert.Equal(t, 0, pool.keyLocks.Len(), "entries must be reclaimed even when dial fails")
}

// TestGet_RejectsInvalidNode 验证缺字段的节点在拨号前就被挡下：
// 否则报错来自 SSH 层（"dial tcp: missing address"），用户不知道该改哪一项。
func TestGet_RejectsInvalidNode(t *testing.T) {
	pool := New(testutil.TestConfig())
	defer pool.CloseAll()

	cases := []struct {
		name string
		node model.Node
		want string
	}{
		{"empty host", model.Node{Port: 22, Username: "root", AuthValue: "p"}, "host"},
		{"zero port", model.Node{Host: "10.0.0.1", Username: "root", AuthValue: "p"}, "端口"},
		{"port overflow", model.Node{Host: "10.0.0.1", Port: 70000, Username: "root", AuthValue: "p"}, "端口"},
		{"empty username", model.Node{Host: "10.0.0.1", Port: 22, AuthValue: "p"}, "username"},
		{"empty credential", model.Node{Host: "10.0.0.1", Port: 22, Username: "root"}, "认证内容"},
		{"unknown auth type", model.Node{Host: "10.0.0.1", Port: 22, Username: "root", AuthValue: "p", AuthType: "cert"}, "认证方式"},
	}
	for _, tc := range cases {
		_, err := pool.Get(tc.node)
		require.Error(t, err, tc.name)
		assert.Contains(t, err.Error(), tc.want, tc.name)
	}
}

// TestAuthMethods_PassphraseProtectedKey 验证口令保护私钥给出可操作的绕开方式。
func TestAuthMethods_PassphraseProtectedKey(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	require.NoError(t, err)
	block, err := ssh.MarshalPrivateKeyWithPassphrase(key, "", []byte("hunter2"))
	require.NoError(t, err)

	_, err = authMethods(model.Node{AuthType: model.AuthKey, AuthValue: string(pem.EncodeToMemory(block))})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "口令保护")
	assert.Contains(t, err.Error(), "ssh-keygen")
}

// TestAuthMethods_GarbageKey 验证非私钥内容的报错带得上游原因，便于定位。
func TestAuthMethods_GarbageKey(t *testing.T) {
	_, err := authMethods(model.Node{AuthType: model.AuthKey, AuthValue: "not-a-private-key"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "私钥解析失败")
}

// ===== 主机密钥校验：MANAGI_KNOWN_HOSTS 严格模式与进程内 TOFU =====

// genHostKey 生成一把临时主机公钥，用于模拟与服务器实际密钥不符的情况。
func genHostKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	require.NoError(t, err)
	pub, err := ssh.NewPublicKey(&key.PublicKey)
	require.NoError(t, err)
	return pub
}

// writeKnownHosts 写出仅包含 pubkey 一行的 known_hosts 并返回路径。
func writeKnownHosts(t *testing.T, host string, port int, pubkey ssh.PublicKey) string {
	t.Helper()
	addr := knownhosts.Normalize(net.JoinHostPort(host, strconv.Itoa(port)))
	path := filepath.Join(t.TempDir(), "known_hosts")
	require.NoError(t, os.WriteFile(path, []byte(knownhosts.Line([]string{addr}, pubkey)+"\n"), 0o600))
	return path
}

// TestHostKey_KnownHostsAcceptsMatch 验证 MANAGI_KNOWN_HOSTS 指纹相符时正常连接。
func TestHostKey_KnownHostsAcceptsMatch(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	cfg := testutil.TestConfig()
	cfg.KnownHostsFile = writeKnownHosts(t, srv.Host(), srv.Port(), srv.HostKey())
	pool := New(cfg)
	defer pool.CloseAll()

	output, _, err := pool.Execute(context.Background(), testutil.TestNode(srv.Host(), srv.Port()), []string{"echo hello"})
	require.NoError(t, err)
	assert.Contains(t, output, "hello")
}

// TestHostKey_KnownHostsRejectsMismatch 验证指纹不符即拒绝：严格校验的价值就在于
// 首次连接也被核对，而不是信任当场见到的任何密钥。
func TestHostKey_KnownHostsRejectsMismatch(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	cfg := testutil.TestConfig()
	cfg.KnownHostsFile = writeKnownHosts(t, srv.Host(), srv.Port(), genHostKey(t))
	pool := New(cfg)
	defer pool.CloseAll()

	_, _, err := pool.Execute(context.Background(), testutil.TestNode(srv.Host(), srv.Port()), []string{"echo hi"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "knownhosts")
}

// TestHostKey_BrokenKnownHostsFailsClosed 验证 known_hosts 无法解析时拒绝所有连接，
// 而不是静默退回 TOFU：否则「配了严格校验」的部署在文件写坏那一刻已不再校验。
func TestHostKey_BrokenKnownHostsFailsClosed(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "known_hosts")
	require.NoError(t, os.WriteFile(path, []byte("127.0.0.1\n"), 0o600))

	cfg := testutil.TestConfig()
	cfg.KnownHostsFile = path
	pool := New(cfg)
	defer pool.CloseAll()

	_, _, err := pool.Execute(context.Background(), testutil.TestNode(srv.Host(), srv.Port()), []string{"echo hi"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MANAGI_KNOWN_HOSTS")
}

// TestHostKeyCallback_TOFU 验证未配置 known_hosts 时的进程内首次信任：
// 记录首个公钥并接受，之后密钥变化即拒绝（防中间人）。
// 直接驱动回调而非两次 Execute：同 key 的第二次拨号会复用池中连接，不再走校验。
func TestHostKeyCallback_TOFU(t *testing.T) {
	pool := New(testutil.TestConfig())
	defer pool.CloseAll()

	node := testutil.TestNode("127.0.0.1", 2222)
	addr := net.JoinHostPort(node.Host, strconv.Itoa(node.Port))
	remote := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 2222}
	first, other := genHostKey(t), genHostKey(t)

	cb := pool.hostKeys.callback(addr)
	require.NoError(t, cb(addr, remote, first))
	require.NoError(t, cb(addr, remote, first), "同一公钥重复出现应继续接受")

	err := cb(addr, remote, other)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ssh host key mismatch")
	assert.Contains(t, err.Error(), ssh.FingerprintSHA256(first), "报错需给出期望指纹，便于用户核对")

	recorded, ok := pool.hostKeys.recorded(addr)
	require.True(t, ok)
	assert.True(t, bytes.Equal(recorded.Marshal(), first.Marshal()), "TOFU 只记录首次公钥，不因拒绝而改写")
}
