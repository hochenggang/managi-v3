package handler

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
