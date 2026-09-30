package handler

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"managi/internal/model"
	"managi/internal/sshpool"
	"managi/internal/testutil"
)

// TestAppendScrollback_Truncation 验证 scrollback 超限时截断头部保留尾部。
// 覆盖 appendScrollbackLocked 的截断逻辑。
func TestAppendScrollback_Truncation(t *testing.T) {
	ls := &liveSession{buf: make([]byte, 0, scrollbackMax+100)}
	data := make([]byte, 100)
	// 填充超过 scrollbackMax
	iterations := scrollbackMax/100 + 2
	for i := 0; i < iterations; i++ {
		ls.appendScrollbackLocked(data)
	}
	assert.LessOrEqual(t, len(ls.buf), scrollbackMax, "scrollback should be truncated to scrollbackMax")
	assert.Greater(t, len(ls.buf), 0, "scrollback should not be empty after data")
}

// TestAppendScrollback_SmallData 验证小数据不截断。
func TestAppendScrollback_SmallData(t *testing.T) {
	ls := &liveSession{buf: make([]byte, 0, 1024)}
	ls.appendScrollbackLocked([]byte("hello"))
	assert.Equal(t, "hello", string(ls.buf))
	assert.Len(t, ls.buf, 5)
}

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
// 会话 ID 由前端每次打开标签页新生成，旧实现的锁字典只增不删会累积，
// 此处用大量必然拨号失败的 ID 依次 AttachOrCreate，断言结束后字典归零。
// 失败路径在触碰 wsConn 之前即返回，故传 nil 安全。
func TestSessionManager_KeyLocksReclaimed(t *testing.T) {
	pool := sshpool.New(testutil.TestConfig())
	defer pool.CloseAll()
	mgr := newSessionManager(pool, testutil.TestConfig())

	bad := model.Node{Host: "127.0.0.1", Port: 1, Username: "x", AuthType: model.AuthPassword, AuthValue: "p"}
	for i := 0; i < 50; i++ {
		id := "sess-" + strconv.Itoa(i)
		_, _, err := mgr.AttachOrCreate(id, bad, nil, 80, 24)
		assert.Error(t, err)
	}
	assert.Equal(t, 0, mgr.keyLocks.Len(), "per-session lock entries must be reclaimed")
}

// TestSplitScrollback 验证回放切片的硬要求：不丢字节、不切断多字节字符、必然终止。
func TestSplitScrollback(t *testing.T) {
	// 1) 中文 + emoji 长文本：chunk=7 与 3/4 字节字符不整除，切点必然落在字符中间
	data := []byte(strings.Repeat("终端输出😀", 20000))
	parts := splitScrollback(data, 7)
	assert.Equal(t, data, bytes.Join(parts, nil))
	for _, p := range parts {
		assert.NotEmpty(t, p, "切片不应为空")
		assert.LessOrEqual(t, len(p), 7)
		assert.True(t, utf8.Valid(p), "切片不应落在字符中间: %q", p)
	}

	// 2) chunk 比一个字符还小：只能逐字节切，但不得死循环
	emoji := []byte("😀😀")
	assert.Equal(t, emoji, bytes.Join(splitScrollback(emoji, 1), nil))

	// 3) 任意二进制（终端里合法存在）：不丢字节即可
	raw := bytes.Repeat([]byte{0x80, 0x81, 0x01, 0x02}, 100)
	assert.Equal(t, raw, bytes.Join(splitScrollback(raw, 7), nil))

	// 4) 边界：空输入 / 短于 chunk / chunk<=0
	assert.Empty(t, splitScrollback(nil, 8))
	assert.Equal(t, [][]byte{[]byte("abc")}, splitScrollback([]byte("abc"), 8))
	assert.Equal(t, [][]byte{[]byte("abc")}, splitScrollback([]byte("abc"), 0))
}

// TestAppendScrollback_TruncationKeepsWholeChars 验证超限截断后的缓冲区
// 不以半个字符开头（回放首帧前端会渲染出乱码）。
func TestAppendScrollback_TruncationKeepsWholeChars(t *testing.T) {
	ls := &liveSession{}
	one := []byte("中") // 3 字节：256KB 上限不能整除，截断点必然落在字符中间
	for i := 0; i < scrollbackMax/len(one)+4; i++ {
		ls.appendScrollbackLocked(one)
	}
	require.LessOrEqual(t, len(ls.buf), scrollbackMax)
	assert.True(t, utf8.Valid(ls.buf), "截断后的 scrollback 应是完整字符序列")
}
