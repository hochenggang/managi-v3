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
	"managi/internal/keylock"
	"managi/internal/model"
	"managi/internal/sshpool"
	"managi/internal/terminal"
)

// scrollback 上限：256KB，足够回看数百行终端输出。
const scrollbackMax = 256 * 1024

// scrollbackChunk 分块回放大小（修复 B13：避免单个超大 WS 帧导致前端卡顿/内存峰值）。
const scrollbackChunk = 32 * 1024

// splitScrollback 把回放快照切成不超过 chunk 字节的片段。
// 切点若落在 UTF-8 多字节字符中间，前端按 UTF-8 解码会渲染出乱码，
// 故向回退到字符起始字节；UTF-8 序列最长 4 字节，回退上限 3 即可，
// 且末片直接收到尾（远端输出本就可能是任意二进制，找不到边界就原样切，
// 保证不丢字节、每片至少 1 字节，循环必然前进）。
func splitScrollback(data []byte, chunk int) [][]byte {
	if chunk <= 0 {
		return [][]byte{data}
	}
	var out [][]byte
	pos := 0
	for pos < len(data) {
		end := pos + chunk
		if end >= len(data) {
			out = append(out, data[pos:])
			break
		}
		// 回退找字符起始字节：最多退 3，且每片至少留 1 字节
		limit := max(pos+1, end-3)
		for end > limit && data[end]&0xC0 == 0x80 {
			end--
		}
		out = append(out, data[pos:end])
		pos = end
	}
	return out
}

// sessionManager 维护按 sessionID 索引的活跃终端会话。
type sessionManager struct {
	mu       sync.Mutex
	sessions map[string]*liveSession
	pool     *sshpool.Pool
	cfg      *config.Config
	idleTTL  time.Duration
	// keyLocks 按会话 ID 串行化同一 ID 的 AttachOrCreate，避免并发创建竞态。
	// 条目在无持有者无等待者时自动回收：会话 ID 由前端每次打开标签页新生成，
	// 旧实现的锁字典只增不删，长期运行必然累积。
	keyLocks *keylock.Map
}

func newSessionManager(pool *sshpool.Pool, cfg *config.Config) *sessionManager {
	ttl := time.Duration(cfg.SessionIdleTimeout) * time.Second
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	return &sessionManager{
		sessions: make(map[string]*liveSession),
		pool:     pool,
		cfg:      cfg,
		idleTTL:  ttl,
		keyLocks: keylock.New(),
	}
}

// liveSession 一个后端维护的终端会话：SSH shell + scrollback + 当前挂载的 WS 客户端。
// 锁顺序：ioMu → mu（ls.mu 内不做网络 I/O）。
type liveSession struct {
	id      string
	sess    *terminal.Session
	sshConn *sshpool.Connection
	buf     []byte  // scrollback，超 scrollbackMax 截断头部
	cur     *wsConn // 当前挂载的 WS 客户端（nil 表示空挂）
	mu      sync.Mutex
	// ioMu 串行化「回放 → 实时输出」的 WS 写入：重连回放期间不允许实时输出插队，
	// 否则用户会先看到最新一行、再看到历史 scrollback，顺序错乱。
	// 只在写 WS 时持有，不在其上做 Read。
	ioMu       sync.Mutex
	closeTimer *time.Timer // 最后一个客户端断开后启动，到期关闭会话
	mgr        *sessionManager
	cancel     context.CancelFunc
	done       chan struct{} // close 后关闭，用于 outputLoop 退出
}

// AttachOrCreate 查找或创建会话。
// 返回 (会话, 是否复用已存在的)。失败返回 error。
// 使用 keyLocks 串行化同一 sessionID 的并发创建，避免竞态导致 SSH 连接与 goroutine 泄漏。
func (m *sessionManager) AttachOrCreate(id string, node model.Node, wc *wsConn, cols, rows int) (*liveSession, bool, error) {
	if id == "" {
		id = node.ConnectionKey()
	}

	m.keyLocks.Lock(id)
	defer m.keyLocks.Unlock(id)

	// 1. 尝试复用已有会话
	m.mu.Lock()
	if ls, ok := m.sessions[id]; ok && !ls.isClosed() {
		m.mu.Unlock()
		// 回放整段 scrollback 期间持 ioMu：实时输出必须排在回放之后，
		// 且 ls.mu 只在锁内取快照，网络写入全部在 ls.mu 之外（避免卡住 Detach/close）。
		ls.ioMu.Lock()
		defer ls.ioMu.Unlock()

		ls.mu.Lock()
		if ls.closeTimer != nil {
			ls.closeTimer.Stop()
			ls.closeTimer = nil
		}
		// 先挂载再取快照：挂载之后到达的输出会阻塞在 ioMu 上，回放完成后按序写出
		ls.cur = wc
		snapshot := make([]byte, len(ls.buf))
		copy(snapshot, ls.buf)
		ls.mu.Unlock()

		// 分块回放，避免单个超大 WS 帧导致前端卡顿/内存峰值
		for _, part := range splitScrollback(snapshot, scrollbackChunk) {
			if err := wc.writeMsg(string(part)); err != nil {
				break
			}
		}
		// 同步 PTY 尺寸到新客户端（非正尺寸由 Resize 拒绝）
		_ = ls.sess.Resize(cols, rows)
		slog.Debug("terminal session reused", "id", id)
		return ls, true, nil
	}
	m.mu.Unlock()

	// 2. 新建会话（perKey 锁保护，不会有并发同 id 创建）
	sshConn, err := m.pool.Get(node)
	if err != nil {
		return nil, false, err
	}
	sess := terminal.New(sshConn.Client())
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	if err := sess.Open(cols, rows); err != nil {
		m.pool.Release(sshConn)
		return nil, false, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	ls := &liveSession{
		id:      id,
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

	// 记录 sess.Close 错误，便于诊断 shell 已关闭等场景
	if err := ls.sess.Close(); err != nil {
		slog.Debug("terminal session close error", "id", id, "err", err)
	}
	ls.mgr.pool.Release(ls.sshConn)
	if cur != nil {
		_ = cur.conn.Close()
	}
	slog.Debug("terminal session closed", "id", id)
}

// outputLoop 持续读取 shell stdout，追加 scrollback 并转发给当前客户端。
// select 仅在 Read 阻塞前检查退出信号；Read 阻塞期间由 close() 调用
// sess.Close() 解除阻塞（PTY 关闭后 Read 返回 EOF/error），随后 err 分支触发 close。
func (ls *liveSession) outputLoop(ctx context.Context) {
	reader := ls.sess.Stdout()
	if reader == nil {
		// Open 未成功（理论上不会走到这里）：无 stdout 可读，直接退出避免空指针
		slog.Error("terminal session has no stdout reader", "id", ls.id)
		ls.mgr.close(ls.id)
		return
	}
	buf := make([]byte, 4096)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ls.done:
			return
		default:
		}
		n, err := reader.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])
			// ioMu 先于 ls.mu：与重连回放共用同一条写入序列，
			// 保证「回放历史 → 实时输出」的先后顺序不被插队。
			ls.ioMu.Lock()
			ls.mu.Lock()
			if ls.isClosedLocked() {
				ls.mu.Unlock()
				ls.ioMu.Unlock()
				return
			}
			ls.appendScrollbackLocked(data)
			cur := ls.cur
			// 复制 cur 引用后释放锁，避免 writeMsg 网络阻塞时持锁卡死 Detach/close 等操作
			ls.mu.Unlock()
			if cur != nil {
				_ = cur.writeMsg(string(data))
			}
			ls.ioMu.Unlock()
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
		// 截断头部保留尾部；起点推进到下一个字符起始字节（最多 3 字节），
		// 否则回放的第一帧以半个字符开头，前端渲染成乱码。
		cut := len(ls.buf) - scrollbackMax
		for i := 0; i < 3 && cut < len(ls.buf) && ls.buf[cut]&0xC0 == 0x80; i++ {
			cut++
		}
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
