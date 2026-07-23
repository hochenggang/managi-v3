// Package sshpool 实现 SSH 连接池。
// 对应 v2 的 ssh_pool.py，修正 v2 缺陷：命令执行路径也复用连接（引用计数）。
// 设计见 ../design-v3.md §4.2。
package sshpool

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"managi/internal/config"
	"managi/internal/model"
)

// Connection 包装一条 SSH 连接及其引用计数。
// 仅由 Pool 内部构造与生命周期管理，外部仅通过 Client() 获取底层 *ssh.Client，
// 不应直接创建或关闭本类型（修复 E3：明确封装边界）。
type Connection struct {
	key      string
	refs     int
	lastUsed time.Time
	client   *ssh.Client
	done     chan struct{} // M2：close 时通知 keepalive goroutine 退出，避免短暂泄漏
	closeOnce sync.Once    // P3：保护 done 仅关闭一次，防止 keepalive 与 cleanIdle/evict 并发 close panic
}

// Client 返回底层 SSH 客户端（供 sftp/terminal 复用）。
func (c *Connection) Client() *ssh.Client { return c.client }

// closeDone 安全关闭 done channel（幂等，多次调用不会 panic）。
func (c *Connection) closeDone() {
	c.closeOnce.Do(func() { close(c.done) })
}

// hostKeyEntry TOFU 主机密钥记录条目。
// H5：lastSeen 用于 cleanIdle 清理长期未使用的主机密钥，防止 map 无限增长。
type hostKeyEntry struct {
	key      ssh.PublicKey
	lastSeen time.Time
}

// hostKeyTTL 主机密钥保留时长：超过此时长未连接的主机条目将被清理。
const hostKeyTTL = 24 * time.Hour

// Pool SSH 连接池，进程内单例。
type Pool struct {
	mu          sync.Mutex
	conns       map[string]*Connection
	perKeyLocks map[string]*sync.Mutex
	perKeyLock  sync.Mutex // 保护 perKeyLocks 字典
	cfg         *config.Config
	maxSize     int
	// hardCap 兜底上限，防止 evictOldestLocked 在「全部 refs>0」时无法淘汰导致池无限增长。
	// 触达 hardCap 时 Get 返回 errPoolFull，由调用方降级。
	hardCap     int
	idleTimeout time.Duration
	hostKeys    map[string]hostKeyEntry // TOFU: addr → 首次记录的主机公钥（含 lastSeen）
}

// errPoolFull 连接池触达硬上限且无空闲连接可淘汰。
var errPoolFull = fmt.Errorf("ssh pool full: no idle connection to evict")

// New 创建连接池。
func New(cfg *config.Config) *Pool {
	idleTimeout := time.Duration(cfg.SSHIdleTimeout) * time.Second
	if idleTimeout <= 0 {
		idleTimeout = 120 * time.Second
	}
	return &Pool{
		conns:       make(map[string]*Connection),
		perKeyLocks: make(map[string]*sync.Mutex),
		cfg:         cfg,
		maxSize:     cfg.SSHPoolSize,
		hardCap:     cfg.SSHPoolSize * 2, // 硬上限为 maxSize 2 倍，防止全部占用时无限增长
		idleTimeout: idleTimeout,
		hostKeys:    make(map[string]hostKeyEntry),
	}
}

// NewWithSize 创建指定容量的连接池（测试用）。
func NewWithSize(cfg *config.Config, maxSize int) *Pool {
	p := New(cfg)
	p.maxSize = maxSize
	p.hardCap = maxSize * 2
	return p
}

// Get 按 node.ConnectionKey() 获取连接，引用计数 +1。
// 不存在或失效则新建并入池。
// 修复 A10：isAlive 是阻塞网络调用，移出 p.mu.Lock() 范围，避免慢节点卡死全池。
func (p *Pool) Get(node model.Node) (*Connection, error) {
	key := node.ConnectionKey()
	perKey := p.keyLock(key)
	perKey.Lock()
	defer perKey.Unlock()

	// 第一阶段：锁内取引用，锁外做 isAlive 网络探测
	p.mu.Lock()
	c, ok := p.conns[key]
	if ok && c.client != nil {
		clientRef := c.client
		p.mu.Unlock()
		// 锁外探测（perKey 锁保证同 key 串行，不会重复探测）
		if isAlive(clientRef) {
			p.mu.Lock()
			// re-validate：探测期间连接可能被 cleanIdle/evict 清理
			c2, ok2 := p.conns[key]
			if ok2 && c2 == c && c2.client != nil {
				c2.refs++
				c2.lastUsed = time.Now()
				slog.Debug("ssh pool hit", "key", key)
				p.mu.Unlock()
				return c2, nil
			}
			p.mu.Unlock()
		} else {
			// 失效：锁内清理
			p.mu.Lock()
			if cStill, ok2 := p.conns[key]; ok2 && cStill == c {
				if cStill.client != nil {
					_ = cStill.client.Close()
				}
				cStill.closeDone() // M2：通知 keepalive goroutine 退出
				delete(p.conns, key)
				delete(p.perKeyLocks, key) // P1：清理已删除连接的 per-key 锁
			}
			p.mu.Unlock()
		}
	} else {
		p.mu.Unlock()
	}

	// 第二阶段：新建连接（锁外 dial）
	p.mu.Lock()
	if len(p.conns) >= p.maxSize {
		p.evictOldestLocked()
	}
	// 修复 B3：evictOldestLocked 在全部 refs>0 时无法淘汰，池可能超 maxSize。
	// 触达 hardCap 时拒绝新连接，防止无限增长。
	if len(p.conns) >= p.hardCap {
		p.mu.Unlock()
		return nil, errPoolFull
	}
	p.mu.Unlock()

	client, err := p.dial(node)
	if err != nil {
		return nil, err
	}
	done := make(chan struct{})
	cNew := &Connection{key: key, refs: 1, lastUsed: time.Now(), client: client, done: done}
	// 修复 B2：dial 在锁外，并发同 key 可能他人已先入池。
	// 此时复用既有连接（持锁 refs++），关闭新建连接避免泄漏。
	p.mu.Lock()
	if exist, ok := p.conns[key]; ok && exist.client != nil {
		exist.refs++
		exist.lastUsed = time.Now()
		p.mu.Unlock()
		_ = client.Close()
		// done 未启动 keepalive，无需 close，GC 回收即可
		slog.Debug("ssh pool concurrent dial race, reuse existing", "key", key)
		return exist, nil
	}
	p.conns[key] = cNew
	p.mu.Unlock()
	// M2：keepalive 在连接提交到 map 后启动，接收 done 以便连接被清理时及时退出
	go p.keepalive(key, client, done)
	return cNew, nil
}

// Release 引用计数 -1，不立即关闭（修正 v2 release 即关闭的缺陷）。
func (p *Pool) Release(node model.Node) {
	key := node.ConnectionKey()
	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok := p.conns[key]; ok && c.refs > 0 {
		c.refs--
		c.lastUsed = time.Now()
	}
}

// CloseAll 关闭全部连接（进程退出时调用）。
func (p *Pool) CloseAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, c := range p.conns {
		if c.client != nil {
			_ = c.client.Close()
		}
		c.closeDone() // M2：通知 keepalive goroutine 退出
		delete(p.conns, k)
		delete(p.perKeyLocks, k) // P1：清理 per-key 锁
	}
	for k := range p.hostKeys {
		delete(p.hostKeys, k)
	}
}

// StartCleaner 启动后台清理协程，回收空闲超时连接。
// 修复 B10：接收 done channel，进程退出时停止协程，避免 goroutine 泄漏。
func (p *Pool) StartCleaner(done ...<-chan struct{}) {
	var d <-chan struct{}
	if len(done) > 0 {
		d = done[0]
	}
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-d:
				return
			case <-ticker.C:
				p.cleanIdle()
			}
		}
	}()
}

// Execute 在指定连接上执行命令，返回按行拆分的 stdout 与 stderr。
// 调用方负责 Get/Release；Execute 本身不释放连接。
// 修复 B11：支持 ctx 取消，客户端断开时终止 SSH 命令执行。
func (p *Pool) Execute(ctx context.Context, node model.Node, cmds []string) (output []string, errs []string, err error) {
	if len(cmds) == 0 {
		return nil, nil, nil
	}
	conn, err := p.Get(node)
	if err != nil {
		return nil, nil, err
	}
	defer p.Release(node)

	session, err := conn.client.NewSession()
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = session.Close() }()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	// 修复 B11：ctx 取消时关闭 session 终止命令执行
	if err := session.Start(joinLines(cmds)); err != nil {
		return nil, nil, err
	}
	waitCh := make(chan error, 1)
	go func() {
		waitCh <- session.Wait()
	}()
	select {
	case runErr := <-waitCh:
		output = splitLines(stdout.String())
		errs = splitLines(stderr.String())
		if runErr != nil && len(errs) == 0 {
			errs = []string{runErr.Error()}
		}
		return output, errs, nil
	case <-ctx.Done():
		_ = session.Close() // 终止 Wait
		<-waitCh            // 等待 goroutine 退出
		return nil, nil, ctx.Err()
	}
}

// dial 建立一条新 SSH 连接（不含 keepalive 启动，由 Get 统一管理生命周期）。
func (p *Pool) dial(node model.Node) (*ssh.Client, error) {
	authMethods, err := authMethods(node)
	if err != nil {
		return nil, err
	}
	timeout := time.Duration(p.cfg.SSHTimeout) * time.Second
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	cfg := &ssh.ClientConfig{
		User:            node.Username,
		Auth:            authMethods,
		HostKeyCallback: p.hostKeyCallback(node),
		Timeout:         timeout,
	}
	addr := net.JoinHostPort(node.Host, strconv.Itoa(node.Port))
	client, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return nil, fmt.Errorf("ssh dial %s: %w", addr, err)
	}
	return client, nil
}

// hostKeyCallback 返回 TOFU（Trust On First Use）主机密钥校验回调。
// 首次连接：记录公钥并接受；后续连接：比对公钥，不匹配则拒绝（防 MITM）。
// 进程内有效，重启后重新信任（简约优先；持久化可后续迭代）。
// H5：每次连接更新 lastSeen，供 cleanIdle 清理长期未使用的主机密钥。
func (p *Pool) hostKeyCallback(node model.Node) ssh.HostKeyCallback {
	addr := net.JoinHostPort(node.Host, strconv.Itoa(node.Port))
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		p.mu.Lock()
		defer p.mu.Unlock()
		entry, ok := p.hostKeys[addr]
		if !ok {
			p.hostKeys[addr] = hostKeyEntry{key: key, lastSeen: time.Now()}
			slog.Info("ssh host key recorded (TOFU)", "addr", addr, "fingerprint", ssh.FingerprintSHA256(key))
			return nil
		}
		known := entry.key
		if known.Type() != key.Type() || !bytes.Equal(known.Marshal(), key.Marshal()) {
			return fmt.Errorf("ssh host key mismatch for %s: expected %s, got %s",
				addr, ssh.FingerprintSHA256(known), ssh.FingerprintSHA256(key))
		}
		// H5：更新 lastSeen，标记该主机近期活跃
		entry.lastSeen = time.Now()
		p.hostKeys[addr] = entry
		return nil
	}
}

// keepalive 周期发送 keepalive 请求。
// 修复 B4：探测失败时主动从池中清理死连接（仅当指针匹配且 refs==0），避免滞留至 cleanIdle。
// M2：接收 done channel，连接被 cleanIdle/evict/CloseAll 清理时及时退出，避免短暂泄漏。
func (p *Pool) keepalive(key string, client *ssh.Client, done <-chan struct{}) {
	interval := time.Duration(p.cfg.KeepaliveInterval) * time.Second
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
		}
		if !isAlive(client) {
			p.mu.Lock()
			if c, ok := p.conns[key]; ok && c.client == client && c.refs == 0 {
				_ = c.client.Close()
				c.closeDone() // 通知自身退出（安全：仅当连接仍在 map 中时执行）
				delete(p.conns, key)
				delete(p.perKeyLocks, key) // P1：清理 per-key 锁
				slog.Debug("ssh pool keepalive detected dead conn, removed", "key", key)
			}
			p.mu.Unlock()
			return
		}
		_, _, _ = client.SendRequest("keepalive@openssh.com", true, nil)
	}
}

// authMethods 构造认证方法列表。
func authMethods(node model.Node) ([]ssh.AuthMethod, error) {
	switch node.AuthType {
	case model.AuthKey:
		signer, err := parsePrivateKey([]byte(node.AuthValue))
		if err != nil {
			return nil, fmt.Errorf("parse private key: %w", err)
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	default: // password
		return []ssh.AuthMethod{ssh.Password(node.AuthValue)}, nil
	}
}

// parsePrivateKey 解析私钥（ssh.ParsePrivateKey 已支持 RSA/Ed25519/ECDSA/PKCS8 等常见格式）。
func parsePrivateKey(pem []byte) (ssh.Signer, error) {
	return ssh.ParsePrivateKey(pem)
}

// isAlive 判断连接 transport 是否活跃。
func isAlive(client *ssh.Client) bool {
	if client == nil {
		return false
	}
	_, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
	return err == nil
}

func (p *Pool) cleanIdle() {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	for k, c := range p.conns {
		if c.refs == 0 && now.Sub(c.lastUsed) > p.idleTimeout {
			if c.client != nil {
				_ = c.client.Close()
			}
			c.closeDone() // M2：通知 keepalive 退出
			delete(p.conns, k)
			delete(p.perKeyLocks, k) // P1：清理 per-key 锁
		}
	}
	// H5：清理长期未使用的主机密钥条目，防止 map 无限增长。
	// 仅删除超过 hostKeyTTL 且当前无活跃连接的条目。
	for addr, entry := range p.hostKeys {
		if now.Sub(entry.lastSeen) > hostKeyTTL {
			if _, inUse := p.conns[addr]; !inUse {
				delete(p.hostKeys, addr)
			}
		}
	}
}

func (p *Pool) evictOldestLocked() {
	var oldestKey string
	var oldestTime time.Time
	for k, c := range p.conns {
		if c.refs == 0 && (oldestKey == "" || c.lastUsed.Before(oldestTime)) {
			oldestKey = k
			oldestTime = c.lastUsed
		}
	}
	if oldestKey != "" {
		if c := p.conns[oldestKey]; c != nil {
			if c.client != nil {
				_ = c.client.Close()
			}
			c.closeDone() // M2：通知 keepalive 退出
			delete(p.conns, oldestKey)
			delete(p.perKeyLocks, oldestKey) // P1：清理 per-key 锁
		}
	}
}

func (p *Pool) keyLock(key string) *sync.Mutex {
	p.perKeyLock.Lock()
	defer p.perKeyLock.Unlock()
	if l, ok := p.perKeyLocks[key]; ok {
		return l
	}
	l := &sync.Mutex{}
	p.perKeyLocks[key] = l
	return l
}

// joinLines 将多条命令用换行拼接（对应 v2 "\n".join）。
// 修复 R3：复用 strings.Join，删除手写循环。
func joinLines(cmds []string) string {
	return strings.Join(cmds, "\n")
}

// splitLines 按行拆分，去掉空行。
// 修复 R4：复用 strings.Split + TrimRight，删除手写 trimCR。
func splitLines(s string) []string {
	parts := strings.Split(s, "\n")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		line := strings.TrimRight(p, "\r")
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}
