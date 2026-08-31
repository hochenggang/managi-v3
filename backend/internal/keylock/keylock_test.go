package keylock

import (
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestLock_SerializesSameKey 验证同 key 的临界区互斥：并发计数在加锁下不丢增量。
func TestLock_SerializesSameKey(t *testing.T) {
	m := New()
	var counter int
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.Lock("k")
			defer m.Unlock("k")
			counter++
		}()
	}
	wg.Wait()
	assert.Equal(t, 50, counter)
}

// TestLock_DifferentKeysIndependent 验证不同 key 之间不互相阻塞。
// 若实现退化为单把全局锁，持有 a 期间去锁 b 会死锁，测试超时即暴露。
func TestLock_DifferentKeysIndependent(t *testing.T) {
	m := New()
	m.Lock("a")
	done := make(chan struct{})
	go func() {
		m.Lock("b")
		m.Unlock("b")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("locking a different key blocked")
	}
	m.Unlock("a")
}

// TestUnlock_ReclaimsEntry 验证全部引用离开后条目被回收，Len 归零。
func TestUnlock_ReclaimsEntry(t *testing.T) {
	m := New()
	m.Lock("k")
	assert.Equal(t, 1, m.Len())
	m.Unlock("k")
	assert.Equal(t, 0, m.Len())
}

// TestLen_StableUnderKeyChurn 防止内存泄漏回归：
// 大量不同 key 依次加解锁后，条目数不应随历史 key 总量增长。
func TestLen_StableUnderKeyChurn(t *testing.T) {
	m := New()
	const distinctKeys = 5000
	for i := 0; i < distinctKeys; i++ {
		key := strconv.Itoa(i)
		m.Lock(key)
		m.Unlock(key)
	}
	assert.Equal(t, 0, m.Len(), "entries must be reclaimed after all holders leave")
}

// TestConcurrentChurn_NoLeakAndNoPanic 并发压测多 key 加解锁：
// 断言同一 key 的临界区从不重入（不同 key 并行是允许的），且结束后条目全部回收。
func TestConcurrentChurn_NoLeakAndNoPanic(t *testing.T) {
	m := New()
	const keyCount = 5
	// inside 按 key 记录当前临界区内 goroutine 数；不同 key 并行不违规，故必须分开统计
	var inside sync.Map // key -> *int32

	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := strconv.Itoa(i % keyCount)
			m.Lock(key)

			counter, _ := inside.LoadOrStore(key, new(int32))
			n := atomic.AddInt32(counter.(*int32), 1)
			if n != 1 {
				t.Errorf("key %s: %d goroutines inside the critical section at once", key, n)
			}
			atomic.AddInt32(counter.(*int32), -1)

			m.Unlock(key)
		}(i)
	}
	wg.Wait()
	assert.Equal(t, 0, m.Len())
}

// TestUnlock_NeverLockedIsNoop 验证误用（解锁未加锁的 key）不 panic。
func TestUnlock_NeverLockedIsNoop(t *testing.T) {
	m := New()
	assert.NotPanics(t, func() { m.Unlock("nobody") })
	assert.Equal(t, 0, m.Len())
}

// TestNewMap_ZeroValueUsable 验证零值 Map 也能工作（内部惰性建表），
// 避免调用方结构体字段忘初始化时的隐蔽 panic。
func TestNewMap_ZeroValueUsable(t *testing.T) {
	var m Map
	m.Lock("k")
	m.Unlock("k")
	assert.Equal(t, 0, m.Len())
}
