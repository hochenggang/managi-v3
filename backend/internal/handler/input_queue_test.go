package handler

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"managi/internal/wire"
)

// TestInputQueue_PreservesOrder 队列是上传字节序与「输入/控制」次序的唯一依据：先入先出。
func TestInputQueue_PreservesOrder(t *testing.T) {
	q := newInputQueue(1 << 20)
	require.True(t, q.pushFrame(wire.Frame{Chan: 1, Payload: []byte("a")}))
	require.True(t, q.pushFrame(wire.Frame{Chan: 1, Payload: []byte("b")}))

	first, ok := q.pop()
	require.True(t, ok)
	second, ok := q.pop()
	require.True(t, ok)
	assert.Equal(t, "a", string(first.frame.Payload))
	assert.Equal(t, "b", string(second.frame.Payload))
}

// TestInputQueue_MixedOpsPreserveOrder 数据帧与控制动词同队保序：
// 「先 resize 后粘贴」不能在远端变成「先粘贴后 resize」。
func TestInputQueue_MixedOpsPreserveOrder(t *testing.T) {
	q := newInputQueue(1 << 20)
	require.True(t, q.pushControl(wsEnvelope{Type: msgResize}))
	require.True(t, q.pushFrame(wire.Frame{Chan: 3, Payload: []byte("x")}))

	first, ok := q.pop()
	require.True(t, ok)
	second, ok := q.pop()
	require.True(t, ok)
	assert.Equal(t, opControl, first.kind)
	assert.Equal(t, opFrame, second.kind)
}

// TestInputQueue_BudgetRejectsAndRecovers 预算按字节计：满了拒绝（不阻塞），
// 消费腾出空间后同一帧又能入队。
func TestInputQueue_BudgetRejectsAndRecovers(t *testing.T) {
	payload := make([]byte, 100)
	per := frameOpCost + len(payload)
	q := newInputQueue(2 * per) // 恰好两帧

	require.True(t, q.pushFrame(wire.Frame{Chan: 1, Payload: payload}))
	require.True(t, q.pushFrame(wire.Frame{Chan: 1, Payload: payload}))
	require.False(t, q.pushFrame(wire.Frame{Chan: 1, Payload: payload}), "超出预算必须拒绝")

	_, ok := q.pop()
	require.True(t, ok)
	require.True(t, q.pushFrame(wire.Frame{Chan: 1, Payload: payload}), "腾出空间后应可再入队")
}

// TestInputQueue_ControlCountsTowardBudget 控制帧同样占预算：
// 不占的话一片 resize 洪泛就能把队列内存撑爆。
func TestInputQueue_ControlCountsTowardBudget(t *testing.T) {
	q := newInputQueue(controlOpCost + controlOpCost/2)
	require.True(t, q.pushControl(wsEnvelope{Type: msgResize}))
	require.False(t, q.pushControl(wsEnvelope{Type: msgResize}))
}

// TestInputQueue_ClearReturnsDroppedPayload clear 丢弃全部待办并只计数据帧载荷：
// 溢出终止时靠这个数把窗口补偿给客户端。
func TestInputQueue_ClearReturnsDroppedPayload(t *testing.T) {
	q := newInputQueue(1 << 20)
	require.True(t, q.pushFrame(wire.Frame{Chan: 1, Payload: make([]byte, 100)}))
	require.True(t, q.pushControl(wsEnvelope{Type: msgResize}))
	require.True(t, q.pushFrame(wire.Frame{Chan: 1, Payload: make([]byte, 50)}))

	assert.Equal(t, 150, q.clear(), "只计数据帧载荷，控制帧不占窗口")

	q.close()
	_, ok := q.pop()
	assert.False(t, ok, "clear 后队列必须为空")
}

// TestInputQueue_CloseUnblocksPop 空队列的 pop 阻塞，close 唤醒并返回 ok=false。
func TestInputQueue_CloseUnblocksPop(t *testing.T) {
	q := newInputQueue(1 << 20)
	returned := make(chan bool, 1)
	go func() {
		_, ok := q.pop()
		returned <- ok
	}()

	select {
	case ok := <-returned:
		t.Fatalf("空队列的 pop 不应立即返回（ok=%v）", ok)
	case <-time.After(50 * time.Millisecond):
	}

	q.close()
	select {
	case ok := <-returned:
		assert.False(t, ok, "close 后 pop 必须返回 ok=false")
	case <-time.After(5 * time.Second):
		t.Fatal("close 未唤醒 pop")
	}
}

// TestInputQueue_CloseIdempotentAndRejectsPush close 幂等；关闭后入队一律拒绝。
func TestInputQueue_CloseIdempotentAndRejectsPush(t *testing.T) {
	q := newInputQueue(1 << 20)
	q.close()
	q.close()

	assert.False(t, q.pushFrame(wire.Frame{Chan: 1, Payload: []byte("x")}))
	assert.False(t, q.pushControl(wsEnvelope{Type: msgResize}))
}
