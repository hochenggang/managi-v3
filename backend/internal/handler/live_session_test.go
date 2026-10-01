package handler

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"managi/internal/model"
	"managi/internal/ring"
	"managi/internal/sshpool"
	"managi/internal/testutil"
)

// TestLiveSession_IsClosed 验证 isClosed 状态检测。
func TestLiveSession_IsClosed(t *testing.T) {
	ls := &liveSession{done: make(chan struct{})}
	assert.False(t, ls.isClosed(), "should not be closed before done channel close")

	close(ls.done)
	assert.True(t, ls.isClosed(), "should be closed after done channel close")
}

// TestLiveSession_IsClosedLocked 验证 isClosedLocked 状态检测（调用方持锁）。
func TestLiveSession_IsClosedLocked(t *testing.T) {
	ls := &liveSession{done: make(chan struct{})}

	ls.mu.Lock()
	assert.False(t, ls.isClosedLocked())
	ls.mu.Unlock()

	close(ls.done)

	ls.mu.Lock()
	assert.True(t, ls.isClosedLocked())
	ls.mu.Unlock()
}

// TestSessionManager_KeyLocksReclaimed 验证 per-sessionID 锁字典随使用回收：
// 会话 ID 由前端每次打开标签页新生成，锁字典若只增不删，长期运行必然累积。
// 这里用 50 个必然拨号失败的 ID 依次 getOrCreate，断言结束后字典归零。
func TestSessionManager_KeyLocksReclaimed(t *testing.T) {
	pool := sshpool.New(testutil.TestConfig())
	defer pool.CloseAll()
	mgr := newSessionManager(pool, testutil.TestConfig())

	bad := model.Node{Host: "127.0.0.1", Port: 1, Username: "x", AuthType: model.AuthPassword, AuthValue: "p"}
	for i := 0; i < 50; i++ {
		_, _, err := mgr.getOrCreate("sess-"+strconv.Itoa(i), bad, 80, 24)
		assert.Error(t, err)
	}
	assert.Equal(t, 0, mgr.keyLocks.Len(), "per-session lock entries must be reclaimed")
}

// TestGetOrCreate_FallsBackToConnectionKey 验证未传 session_id 时以连接键兜底：
// 同一节点的两次 open 必须落到同一个会话，而不是各建一个 shell。
func TestGetOrCreate_FallsBackToConnectionKey(t *testing.T) {
	srv := testutil.Start(t)
	t.Cleanup(srv.Close)

	cfg := testutil.TestConfig()
	pool := sshpool.New(cfg)
	t.Cleanup(pool.CloseAll)
	mgr := newSessionManager(pool, cfg)

	node := testutil.TestNode(srv.Host(), srv.Port())
	ls1, reattached, err := mgr.getOrCreate("", node, 80, 24)
	require.NoError(t, err)
	assert.False(t, reattached, "首次 open 不该被判为复用")
	t.Cleanup(func() { mgr.close(ls1.id) })

	ls2, reattached, err := mgr.getOrCreate("", node, 80, 24)
	require.NoError(t, err)
	assert.True(t, reattached, "同一连接键的第二次 open 必须复用会话")
	assert.Same(t, ls1, ls2)
}

// TestLiveSession_AttachReplaysSnapshot 验证 attach 的原子约定：
// 返回挂载前累积的 scrollback，并把输出目的地换成自己——此后实时输出都走新 sink。
func TestLiveSession_AttachReplaysSnapshot(t *testing.T) {
	ls := &liveSession{history: ring.New(1024), done: make(chan struct{})}
	ls.history.Append([]byte("HISTORY"))
	sink := &outSink{chanID: 7}

	snapshot := ls.attach(sink)

	assert.Equal(t, "HISTORY", string(snapshot))
	assert.Same(t, sink, ls.sink)
}

// TestLiveSession_AttachOnClosedIsNoop 验证已关闭会话不给快照也不挂 sink：
// 否则前端会连上一个再也读不到输出的 shell。
func TestLiveSession_AttachOnClosedIsNoop(t *testing.T) {
	ls := &liveSession{history: ring.New(1024), done: make(chan struct{})}
	ls.history.Append([]byte("HISTORY"))
	close(ls.done)

	assert.Nil(t, ls.attach(&outSink{chanID: 1}))
	assert.Nil(t, ls.sink)
}

// TestSessionManager_DetachIdentity 验证 detach 按 sink 身份摘除：
// 旧通道（重连后被顶替的那路）的 detach 不能把当前通道正在用的 sink 摘掉。
func TestSessionManager_DetachIdentity(t *testing.T) {
	pool := sshpool.New(testutil.TestConfig())
	defer pool.CloseAll()
	mgr := newSessionManager(pool, testutil.TestConfig())

	current := &outSink{chanID: 1}
	stale := &outSink{chanID: 2}
	ls := &liveSession{id: "s", done: make(chan struct{}), sink: current}

	mgr.detach(ls, stale)
	assert.Same(t, current, ls.sink, "别的通道的 detach 不应摘掉当前 sink")
	assert.Nil(t, ls.closeTimer, "仍有输出通道时不该启动空闲计时器")

	mgr.detach(ls, current)
	assert.Nil(t, ls.sink)
	require.NotNil(t, ls.closeTimer, "最后一个通道摘除后应启动空闲计时器")
	ls.closeTimer.Stop()
}
