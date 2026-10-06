// Package handler - 终端会话注册表与复用。
// 维护后端到目标服务器的 shell 会话：连接或标签页关闭后保留 idleTTL（默认 60s），
// 期间重新 open 同一 session_id 可复用同一 shell（保留 CWD / 运行中进程 / scrollback）。
// 一路会话同时只挂一个输出目的地（单客户端模型，符合「一个节点一个终端标签」现状）。
package handler

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"managi/internal/config"
	"managi/internal/keylock"
	"managi/internal/model"
	"managi/internal/ring"
	"managi/internal/sshpool"
	"managi/internal/terminal"
	"managi/internal/wire"
)

// outSink 一路通道的输出目的地：把字节写成该通道上的数据帧。
// 用指针挂载，detach 时按身份比较，避免误摘别的通道挂上的 sink。
type outSink struct {
	wc     *wsConn
	chanID uint32
}

func (s *outSink) write(p []byte) error {
	return s.wc.writeFrame(wire.Frame{Chan: s.chanID, Payload: p})
}

// end 通知该通道数据流到此结束（会话被回收时对端要能看到「结束了」）。
func (s *outSink) end() error {
	return s.wc.writeFrame(wire.End(s.chanID))
}

// sessionManager 维护按 sessionID 索引的活跃终端会话。
type sessionManager struct {
	mu       sync.Mutex
	sessions map[string]*liveSession
	pool     *sshpool.Pool
	cfg      *config.Config
	idleTTL  time.Duration
	// keyLocks 按会话 ID 串行化同一 ID 的 getOrCreate，避免并发创建竞态。
	// 条目在无持有者无等待者时自动回收：会话 ID 由前端每次打开标签页新生成，
	// 锁字典若只增不删，长期运行必然累积。
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

// liveSession 一个后端维护的终端会话：SSH shell + scrollback + 当前挂载的输出通道。
// 锁顺序：wc.mu → ls.mu（ls.mu 内不做网络 I/O）。
type liveSession struct {
	id string
	// nodeKey 建会话时的节点连接键（model.Node.ConnectionKey）。
	// session_id 由客户端提供，复用时必须核对身份：id 对不上节点即拒绝（见 getOrCreate）。
	nodeKey string
	sess    *terminal.Session
	sshConn *sshpool.Connection
	history *ring.Buffer // scrollback：超限丢头部，截断不切断字符（不变量由 ring 保证）
	sink    *outSink     // 当前输出目的地；nil 表示空挂（输出只进 scrollback）
	mu      sync.Mutex
	// closeTimer 最后一个输出通道摘除后启动，到期关闭会话
	closeTimer *time.Timer
	mgr        *sessionManager
	cancel     context.CancelFunc
	done       chan struct{} // close 后关闭，用于 outputLoop 退出
}

// getOrCreate 查找或创建会话，返回 (会话, 是否复用已存在的)。
// 不挂载输出、不回放：这两步由调用方在写出 open 响应的同一把写锁里完成
// （见 wsConn.writeOpen），否则实时输出可能插到回放之前。
// 命中已有会话时核对节点身份：session_id 是客户端给的，若不核对，
// 一个陈旧或构造的 id 就能接上另一节点正在跑的 shell，输入被搬到别的机器执行。
// 用 keyLocks 串行化同一 sessionID 的并发创建，避免竞态导致 SSH 连接与 goroutine 泄漏。
func (m *sessionManager) getOrCreate(id string, node model.Node, cols, rows int) (*liveSession, bool, error) {
	wantKey := node.ConnectionKey()
	if id == "" {
		id = wantKey
	}

	m.keyLocks.Lock(id)
	defer m.keyLocks.Unlock(id)

	m.mu.Lock()
	existing, ok := m.sessions[id]
	if ok && existing.isClosed() {
		// 已关闭但尚未摘除的会话：删掉后重建，避免复用半死状态
		delete(m.sessions, id)
		ok = false
	}
	m.mu.Unlock()
	if ok {
		if existing.nodeKey != wantKey {
			return nil, false, fmt.Errorf("session_id %q 已属于另一节点，会话不能跨节点复用；请换一个新的 session_id", id)
		}
		return existing, true, nil
	}

	// 新建会话（perKey 锁保护，不会有并发同 id 创建）
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
		nodeKey: wantKey,
		sess:    sess,
		sshConn: sshConn,
		history: ring.New(ring.DefaultMax),
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

// attach 挂载输出通道，并返回挂载前累积的 scrollback 快照。
// 必须在 wc 的写锁内调用（锁顺序 wc.mu → ls.mu）：本函数返回后 outputLoop
// 转发的实时帧都要抢同一把写锁，因而必然排在调用方随后写完的回放之后。
// 快照在锁内取、sink 在锁内置起，二者之间不会漏字节也不会重复。
func (ls *liveSession) attach(sink *outSink) []byte {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if ls.isClosedLocked() {
		return nil
	}
	if ls.closeTimer != nil {
		ls.closeTimer.Stop()
		ls.closeTimer = nil
	}
	snapshot := ls.history.Bytes()
	ls.sink = sink
	return snapshot
}

// detach 摘除输出通道（仅当当前挂的就是它），必要时启动空闲关闭计时器。
func (m *sessionManager) detach(ls *liveSession, sink *outSink) {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if ls.sink == sink {
		ls.sink = nil
	}
	if ls.isClosedLocked() || ls.sink != nil || ls.closeTimer != nil {
		return
	}
	ls.closeTimer = time.AfterFunc(m.idleTTL, func() { m.close(ls.id) })
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
	sink := ls.sink
	ls.sink = nil
	ls.mu.Unlock()

	// 记录 sess.Close 错误，便于诊断 shell 已关闭等场景
	if err := ls.sess.Close(); err != nil {
		slog.Debug("terminal session close error", "id", id, "err", err)
	}
	m.pool.Release(ls.sshConn)
	if sink != nil {
		// 只结束这一路通道，不能关整条连接：同连接上别的标签还在用。
		_ = sink.end()
	}
	slog.Debug("terminal session closed", "id", id)
}

// outputLoop 持续读取 shell stdout：追加 scrollback，并转发给当前挂载的输出通道。
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
			ls.mu.Lock()
			if ls.isClosedLocked() {
				ls.mu.Unlock()
				return
			}
			ls.history.Append(data)
			sink := ls.sink
			ls.mu.Unlock()
			// 锁外写：sink 为 nil 时（空挂期）数据只进 scrollback，重连回放补齐
			if sink != nil {
				_ = sink.write(data)
			}
		}
		if err != nil {
			ls.mgr.close(ls.id)
			return
		}
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

// Session 返回底层终端会话（供通道转发输入与 resize）。
func (ls *liveSession) Session() *terminal.Session { return ls.sess }
