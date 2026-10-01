// Package sshpool 实现 SSH 连接池。
// 对应 v2 的 ssh_pool.py，修正 v2 缺陷：命令执行路径也复用连接（引用计数）。
// 设计见 design-v5.md §4.2。
//
// 本文件只负责「一条连接的生命周期」：查复用、建新的、引用计数、容量与空闲回收。
// 拨号与时长见 dial.go，主机密钥校验见 hostkey.go，命令执行见 exec.go。
package sshpool

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"managi/internal/config"
	"managi/internal/keylock"
	"managi/internal/model"
)

// Connection 包装一条 SSH 连接及其引用计数。
// 仅由 Pool 内部构造与生命周期管理，外部仅通过 Client() 获取底层 *ssh.Client，
// 不应直接创建或关闭本类型（修复 E3：明确封装边界）。
//
// client 与 done 在构造时一次给定、此后不再改写，因此可在 p.mu 锁外读；
// refs 与 lastUsed 由池按 key 的当前状态维护，一律持 p.mu 访问。
type Connection struct {
	refs     int
	lastUsed time.Time
	client   *ssh.Client
	done     chan struct{} // close 时通知 keepalive goroutine 退出，避免短暂泄漏
}

// Client 返回底层 SSH 客户端（供 sftp/terminal 复用）。
func (c *Connection) Client() *ssh.Client { return c.client }

// Pool SSH 连接池，进程内单例。
type Pool struct {
	mu      sync.Mutex
	conns   map[string]*Connection
	cfg     *config.Config
	timing  timing
	maxSize int
	// keyLocks 按连接键串行化「探测 → 创建 → 入池」流程，不同节点之间保持并行。
	// 引用计数式回收，避免旧实现中锁字典随历史节点数无限增长。
	keyLocks *keylock.Map
	// hardCap 兜底上限，防止淘汰不动（全部 refs>0）时池无限增长。
	// 触达 hardCap 时 Get 返回 errPoolFull，由调用方降级。
	hardCap  int
	hostKeys *hostKeyStore
}

// errPoolFull 连接池触达硬上限且无空闲连接可淘汰。
// 必须给出下一步动作：只说 "pool full" 用户无从下手。
var errPoolFull = fmt.Errorf("SSH 连接池已满且无空闲连接：请关闭部分终端/文件标签后重试")

// New 创建连接池。容量取 cfg.SSHPoolSize，其正值由 config.Normalize 保证。
func New(cfg *config.Config) *Pool { return newPool(cfg, cfg.SSHPoolSize) }

// NewWithSize 创建指定容量的连接池（测试用）。
func NewWithSize(cfg *config.Config, maxSize int) *Pool { return newPool(cfg, maxSize) }

func newPool(cfg *config.Config, maxSize int) *Pool {
	return &Pool{
		conns:    make(map[string]*Connection),
		keyLocks: keylock.New(),
		cfg:      cfg,
		timing:   newTiming(cfg),
		maxSize:  maxSize,
		hardCap:  maxSize * 2, // 硬上限为 maxSize 2 倍，防止全部占用时无限增长
		hostKeys: newHostKeys(cfg),
	}
}

// Get 按 node.ConnectionKey() 获取连接，引用计数 +1；不存在或失效则新建并入池。
// 流程三步，每步各自持锁、都不跨网络调用：
//  1. reuse   —— 命中且存活即复用；探测是一次阻塞网络往返，必须在锁外；
//  2. reserve —— 容量闸（满了先淘汰最久空闲的）；
//  3. adopt   —— 锁外拨号，再入池；同键竞态则复用先入池的那条。
//
// per-key 锁让同一节点的并发 Get 串行，不同节点互不影响。
func (p *Pool) Get(node model.Node) (*Connection, error) {
	// 所有入口（HTTP 执行/下载、WS 终端、WS SFTP）都经过这里，校验放一处即全覆盖
	if err := node.Validate(); err != nil {
		return nil, err
	}
	key := node.ConnectionKey()
	p.keyLocks.Lock(key)
	defer p.keyLocks.Unlock(key)

	if c := p.reuse(key); c != nil {
		return c, nil
	}
	if err := p.reserve(); err != nil {
		return nil, err
	}
	client, err := p.dial(node)
	if err != nil {
		return nil, err
	}
	return p.adopt(key, client), nil
}

// reuse 尝试复用池中的既有连接；连接已被确认死亡则顺手剔除并返回 nil。
// isAlive 是一次阻塞网络往返，必须在锁外做，因此锁只用于取/核对快照；
// Connection.client 入池后不再改写（见类型注释），锁外读它安全。
func (p *Pool) reuse(key string) *Connection {
	p.mu.Lock()
	c := p.conns[key]
	p.mu.Unlock()
	if c == nil {
		return nil
	}

	if isAlive(c.client) {
		p.mu.Lock()
		defer p.mu.Unlock()
		// re-validate：探测期间这条连接可能已被清理或替换
		if cur := p.conns[key]; cur == c {
			cur.refs++
			cur.lastUsed = time.Now()
			slog.Debug("ssh pool hit", "key", key)
			return cur
		}
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conns[key] == c {
		p.dropLocked(key)
	}
	return nil
}

// reserve 容量闸：超过 maxSize 先淘汰最久空闲的连接；仍触达 hardCap 则拒绝新建。
func (p *Pool) reserve() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.conns) >= p.maxSize {
		p.evictOldestLocked()
	}
	// 全部 refs>0 时 evictOldestLocked 淘汰不动，故还有一道硬闸。
	if len(p.conns) >= p.hardCap {
		return errPoolFull
	}
	return nil
}

// adopt 把新建的连接入池并返回（refs=1）。
// dial 在锁外，并发同键可能他人已先入池：此时关掉自己刚建的，复用既有的那条。
func (p *Pool) adopt(key string, client *ssh.Client) *Connection {
	done := make(chan struct{})
	c := &Connection{refs: 1, lastUsed: time.Now(), client: client, done: done}

	p.mu.Lock()
	if exist := p.conns[key]; exist != nil {
		exist.refs++
		exist.lastUsed = time.Now()
		p.mu.Unlock()
		_ = client.Close()
		slog.Debug("ssh pool concurrent dial race, reuse existing", "key", key)
		return exist
	}
	p.conns[key] = c
	p.mu.Unlock()

	// keepalive 在连接入池后启动：它需要能从 map 里核对身份，也依赖 done 及时退出
	go p.keepalive(key, c)
	return c
}

// dropLocked 关掉并摘除一条连接，同时通知其 keepalive 退出。调用方需持 p.mu。
// 五处清理路径（复用探测到死亡、keepalive 探死、空闲回收、容量淘汰、CloseAll）共用这一份，
// 少一步都会留下仍在 map 里的死连接或永不退出的 goroutine。
func (p *Pool) dropLocked(key string) {
	c := p.conns[key]
	if c == nil {
		return
	}
	if c.client != nil {
		_ = c.client.Close()
	}
	close(c.done)
	delete(p.conns, key)
}

// Release 归还一次引用（与 Get 的 +1 配对），不立即关闭（修正 v2 release 即关闭的缺陷）。
// 按连接对象身份归还而非按 key：失效连接被剔除、同 key 新连接入池后，
// 按 key 归还会误减新连接的 refs，正在使用的连接随即被 cleanIdle 当空闲回收。
func (p *Pool) Release(c *Connection) {
	if c == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if c.refs > 0 {
		c.refs--
		c.lastUsed = time.Now()
	}
}

// CloseAll 关闭全部连接（进程退出时调用）。
func (p *Pool) CloseAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k := range p.conns {
		p.dropLocked(k)
	}
}

// StartCleaner 启动后台清理协程，回收空闲超时连接与超期主机密钥。
// done 为必填：清理协程必须可被停止，否则进程退出时泄漏。
func (p *Pool) StartCleaner(done <-chan struct{}) {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				p.cleanIdle()
			}
		}
	}()
}

// cleanIdle 回收空闲超时的连接，并顺带清理超期的主机密钥条目。
func (p *Pool) cleanIdle() {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	for k, c := range p.conns {
		if c.refs == 0 && now.Sub(c.lastUsed) > p.timing.idle {
			p.dropLocked(k)
		}
	}
	// 主机密钥按 host:port 记录，连接键是完整 ConnectionKey（host:port:user:指纹），
	// 故用前缀匹配判断该主机是否仍有任意用户/凭据的活跃连接。
	p.hostKeys.reap(now, p.hasActiveConnLocked)
}

// hasActiveConnLocked 判断指定 host:port 是否仍有任意用户的连接在池中。
// 调用方需持 p.mu。
func (p *Pool) hasActiveConnLocked(hostPort string) bool {
	prefix := hostPort + ":"
	for k := range p.conns {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	return false
}

// evictOldestLocked 淘汰最久未使用的空闲连接。全部占用时什么也不做（由 hardCap 兜底）。
// 调用方需持 p.mu。
func (p *Pool) evictOldestLocked() {
	var oldestKey string
	var oldestTime time.Time
	for k, c := range p.conns {
		if c.refs == 0 && (oldestKey == "" || c.lastUsed.Before(oldestTime)) {
			oldestKey, oldestTime = k, c.lastUsed
		}
	}
	if oldestKey != "" {
		p.dropLocked(oldestKey)
	}
}

// keepalive 周期发送 keepalive 请求，探到死亡即从池中清理（仅当连接仍是这一条且空闲），
// 避免死连接滞留到下一次 cleanIdle。
func (p *Pool) keepalive(key string, c *Connection) {
	ticker := time.NewTicker(p.timing.keepalive)
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
		}
		if !isAlive(c.client) {
			p.mu.Lock()
			// 只清理自己这条：同 key 可能已换成新连接，误删会打断正在用的会话
			if cur, ok := p.conns[key]; ok && cur == c && c.refs == 0 {
				p.dropLocked(key)
				slog.Debug("ssh pool keepalive detected dead conn, removed", "key", key)
			}
			p.mu.Unlock()
			return
		}
		_, _, _ = c.client.SendRequest("keepalive@openssh.com", true, nil)
	}
}
