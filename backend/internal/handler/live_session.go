// Package handler - 终端会话注册表与复用。
// 维护后端到目标服务器的 shell 会话，前端断开后保留 idleTTL（默认 60s），
// 期间前端重连可复用同一会话（保留 CWD / 运行中进程 / scrollback）。
// 单客户端模型：一个会话同时只挂一个 WS 客户端（符合「一个节点一个终端 tab」现状）。
package handler

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"managi/internal/config"
	"managi/internal/model"
	"managi/internal/sshpool"
	"managi/internal/terminal"
)

// scrollback 上限：256KB，足够回看数百行终端输出。
const scrollbackMax = 256 * 1024

// scrollbackChunk 分块回放大小（修复 B13：避免单个超大 WS 帧导致前端卡顿/内存峰值）。
const scrollbackChunk = 32 * 1024

// sessionManager 维护按 sessionID 索引的活跃终端会话。
type sessionManager struct {
	mu          sync.Mutex
	sessions    map[string]*liveSession
	pool        *sshpool.Pool
	cfg         *config.Config
	idleTTL     time.Duration
	perKeyLocks map[string]*sync.Mutex // C2：per-sessionID 锁，串行化同 id 的 AttachOrCreate
	perKeyLock  sync.Mutex             // 保护 perKeyLocks 字典
}

func newSessionManager(pool *sshpool.Pool, cfg *config.Config) *sessionManager {
	ttl := time.Duration(cfg.SessionIdleTimeout) * time.Second
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	return &sessionManager{
		sessions:    make(map[string]*liveSession),
		pool:        pool,
		cfg:         cfg,
		idleTTL:     ttl,
		perKeyLocks: make(map[string]*sync.Mutex),
	}
}

// keyLock 获取指定 sessionID 的 per-key 锁（C2：串行化同 id 的 AttachOrCreate，避免竞态创建）。
func (m *sessionManager) keyLock(id string) *sync.Mutex {
	m.perKeyLock.Lock()
	defer m.perKeyLock.Unlock()
	if l, ok := m.perKeyLocks[id]; ok {
		return l
	}
	l := &sync.Mutex{}
	m.perKeyLocks[id] = l
	return l
}

// liveSession 一个后端维护的终端会话：SSH shell + scrollback + 当前挂载的 WS 客户端。
type liveSession struct {
	id         string
	node       model.Node
	sess       *terminal.Session
	sshConn    *sshpool.Connection
	buf        []byte  // scrollback，超 scrollbackMax 截断头部
	cur        *wsConn // 当前挂载的 WS 客户端（nil 表示空挂）
	mu         sync.Mutex
	closeTimer *time.Timer // 最后一个客户端断开后启动，到期关闭会话
	mgr        *sessionManager
	cancel     context.CancelFunc
	done       chan struct{} // close 后关闭，用于 outputLoop 退出
}

// AttachOrCreate 查找或创建会话。
// 返回 (会话, 是否复用已存在的)。失败返回 error。
// C2：使用 per-key 锁串行化同一 sessionID 的并发创建，避免竞态导致 SSH 连接与 goroutine 泄漏。
func (m *sessionManager) AttachOrCreate(id string, node model.Node, wc *wsConn, cols, rows int) (*liveSession, bool, error) {
	if id == "" {
		id = node.ConnectionKey()
	}

	// C2：per-key 锁保证同一 sessionID 的 AttachOrCreate 串行执行
	perKey := m.keyLock(id)
	perKey.Lock()
	defer perKey.Unlock()

	// 1. 尝试复用已有会话
	m.mu.Lock()
	if ls, ok := m.sessions[id]; ok && !ls.isClosed() {
		// 复用：停止空闲计时器，回放 scrollback，挂载新客户端
		ls.mu.Lock()
		if ls.closeTimer != nil {
			ls.closeTimer.Stop()
			ls.closeTimer = nil
		}
		// 回放 scrollback（持锁保证回放先于后续实时输出）
		// 修复 B13：分块发送，避免单个超大 WS 帧导致前端卡顿/内存峰值
		for pos := 0; pos < len(ls.buf); pos += scrollbackChunk {
			end := pos + scrollbackChunk
			if end > len(ls.buf) {
				end = len(ls.buf)
			}
			if err := wc.writeMsg(string(ls.buf[pos:end])); err != nil {
				break
			}
		}
		ls.cur = wc
		ls.mu.Unlock()
		m.mu.Unlock()
		// 同步 PTY 尺寸到新客户端
		if cols > 0 && rows > 0 {
			_ = ls.sess.Resize(cols, rows)
		}
		slog.Debug("terminal session reused", "id", id)
		return ls, true, nil
	}
	m.mu.Unlock()

	// 2. 新建会话（perKey 锁保护，不会有并发同 id 创建）
	sshConn, err := m.pool.Get(node)
	if err != nil {
		return nil, false, err
	}
	sess := terminal.New(node, sshConn.Client())
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	if err := sess.Open(cols, rows); err != nil {
		m.pool.Release(node)
		return nil, false, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	ls := &liveSession{
		id:      id,
		node:    node,
		sess:    sess,
		sshConn: sshConn,
		cur:     wc,
		mgr:     m,
		cancel:  cancel,
		done:    make(chan struct{}),
	}

	m.mu.Lock()
	m.sessions[id] = ls
	m.mu.Unlock()

	go ls.outputLoop(ctx)
	slog.Debug("terminal session created", "id", id)
	return ls, false, nil
}

// Detach 客户端断开。若无可挂载客户端，启动空闲计时器；到期关闭会话。
func (m *sessionManager) Detach(ls *liveSession, wc *wsConn) {
	ls.mu.Lock()
	if ls.cur == wc {
		ls.cur = nil
	}
	if ls.isClosedLocked() {
		ls.mu.Unlock()
		return
	}
	if ls.closeTimer == nil {
		ls.closeTimer = time.AfterFunc(m.idleTTL, func() {
			m.close(ls.id)
		})
	}
	ls.mu.Unlock()
}

// close 关闭并清理会话（幂等）。
func (m *sessionManager) close(id string) {
	m.mu.Lock()
	ls, ok := m.sessions[id]
	if !ok {
		m.mu.Unlock()
		return
	}
	delete(m.sessions, id)
	m.mu.Unlock()

	ls.mu.Lock()
	if ls.isClosedLocked() {
		ls.mu.Unlock()
		return
	}
	close(ls.done)
	ls.cancel()
	if ls.closeTimer != nil {
		ls.closeTimer.Stop()
		ls.closeTimer = nil
	}
	cur := ls.cur
	ls.cur = nil
	ls.mu.Unlock()

	// 修复 B22：记录 sess.Close 错误，便于诊断 shell 已关闭等场景
	if err := ls.sess.Close(); err != nil {
		slog.Debug("terminal session close error", "id", id, "err", err)
	}
	ls.mgr.pool.Release(ls.node)
	if cur != nil {
		_ = cur.conn.Close()
	}
	slog.Debug("terminal session closed", "id", id)
}

// outputLoop 持续读取 shell stdout，追加 scrollback 并转发给当前客户端。
// 修复 S6：select 仅在 Read 阻塞前检查退出信号；Read 阻塞期间由 close() 调用
// sess.Close() 解除阻塞（PTY 关闭后 Read 返回 EOF/error），随后 err 分支触发 close。
func (ls *liveSession) outputLoop(ctx context.Context) {
	buf := make([]byte, 4096)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ls.done:
			return
		default:
		}
		n, err := ls.sess.Stdout().Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])
			ls.mu.Lock()
			if ls.isClosedLocked() {
				ls.mu.Unlock()
				return
			}
			ls.appendScrollbackLocked(data)
			cur := ls.cur
			// H7：复制 cur 引用后释放锁，避免 writeMsg 网络阻塞时持锁卡死 Detach/close 等操作
			ls.mu.Unlock()
			if cur != nil {
				_ = cur.writeMsg(string(data))
			}
		}
		if err != nil {
			ls.mgr.close(ls.id)
			return
		}
	}
}

// appendScrollbackLocked 追加 scrollback，超限时截断头部。调用方需持 ls.mu。
func (ls *liveSession) appendScrollbackLocked(data []byte) {
	ls.buf = append(ls.buf, data...)
	if len(ls.buf) > scrollbackMax {
		// 截断头部保留尾部
		cut := len(ls.buf) - scrollbackMax
		ls.buf = append([]byte(nil), ls.buf[cut:]...)
	}
}

func (ls *liveSession) isClosed() bool {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	return ls.isClosedLocked()
}

// isClosedLocked 是否已关闭。调用方需持 ls.mu。
func (ls *liveSession) isClosedLocked() bool {
	select {
	case <-ls.done:
		return true
	default:
		return false
	}
}

// Session 返回底层终端会话（供 handler 转发输入/resize）。
func (ls *liveSession) Session() *terminal.Session { return ls.sess }
