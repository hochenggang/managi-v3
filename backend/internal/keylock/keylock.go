// Package keylock 提供「按键串行化」的锁集合：同一 key 的临界区互斥，
// 不同 key 之间完全并行，且条目在「既无持有者也无等待者」时自动回收。
//
// 存在的理由：连接池与终端会话都需要按 key（节点标识 / 会话 ID）串行化
// 「查存在 → 探测 → 创建 → 入表」这段流程，以避免并发创建同一资源造成泄漏。
// 朴素实现（map[string]*sync.Mutex 只增不删）会让条目随历史 key 数量无限增长——
// 终端会话的 key 是前端每次打开标签页生成的新 ID，长期运行必然累积。
//
// 调用约定：Lock 之后应立即 defer Unlock，Unlock 之后不得再有属于该临界区的操作。
package keylock

import "sync"

// Map 是按 key 串行化的锁集合，非零值不可用，须由 New 创建。
type Map struct {
	mu    sync.Mutex
	locks map[string]*entry
}

// entry 一个 key 对应的锁及其引用计数（持有者 + 等待者）。
type entry struct {
	mu   sync.Mutex
	refs int
}

// New 创建空的锁集合。
func New() *Map {
	return &Map{locks: make(map[string]*entry)}
}

// Lock 获取 key 的互斥锁；该 key 已被持有时阻塞直到前一个持有者释放。
// 引用计数在集合锁内递增，保证本 goroutine 持有的 entry 不会被并发回收。
func (m *Map) Lock(key string) {
	m.mu.Lock()
	if m.locks == nil {
		m.locks = make(map[string]*entry)
	}
	e := m.locks[key]
	if e == nil {
		e = &entry{}
		m.locks[key] = e
	}
	e.refs++
	m.mu.Unlock()

	e.mu.Lock()
}

// Unlock 释放 key 的互斥锁；最后一个引用离开后回收该条目。
// key 未加锁时为无操作（防御误用，不 panic）。
func (m *Map) Unlock(key string) {
	m.mu.Lock()
	e := m.locks[key]
	if e == nil {
		m.mu.Unlock()
		return
	}
	e.refs--
	if e.refs <= 0 {
		delete(m.locks, key)
		e = nil
	}
	m.mu.Unlock()

	if e != nil {
		e.mu.Unlock()
	}
}

// Len 返回当前存活的条目数，用于测试与可观测：
// 它应始终与「近期活跃 key 数」同阶，不随历史 key 总量单调增长。
func (m *Map) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.locks)
}
