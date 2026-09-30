// Package sshpool 实现 SSH 连接池。
// 对应 v2 的 ssh_pool.py，修正 v2 缺陷：命令执行路径也复用连接（引用计数）。
// 设计见 ../design-v3.md §4.2。
package sshpool

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"managi/internal/config"
	"managi/internal/keylock"
	"managi/internal/model"
)

// Connection 包装一条 SSH 连接及其引用计数。
// 仅由 Pool 内部构造与生命周期管理，外部仅通过 Client() 获取底层 *ssh.Client，
// 不应直接创建或关闭本类型（修复 E3：明确封装边界）。
type Connection struct {
	refs     int
	lastUsed time.Time
	client   *ssh.Client
	done     chan struct{} // close 时通知 keepalive goroutine 退出，避免短暂泄漏
}

// Client 返回底层 SSH 客户端（供 sftp/terminal 复用）。
func (c *Connection) Client() *ssh.Client { return c.client }

// hostKeyEntry TOFU 主机密钥记录条目。
// lastSeen 用于 cleanIdle 清理长期未使用的主机密钥，防止 map 无限增长。
type hostKeyEntry struct {
	key      ssh.PublicKey
	lastSeen time.Time
}

// hostKeyTTL 主机密钥保留时长：超过此时长未连接的主机条目将被清理。
const hostKeyTTL = 24 * time.Hour

// Pool SSH 连接池，进程内单例。
type Pool struct {
	mu      sync.Mutex
	conns   map[string]*Connection
	cfg     *config.Config
	maxSize int
	// keyLocks 按连接键串行化「探测 → 创建 → 入池」流程，不同节点之间保持并行。
	// 引用计数式回收，避免旧实现中锁字典随历史节点数无限增长。
	keyLocks *keylock.Map
	// hardCap 兜底上限，防止 evictOldestLocked 在「全部 refs>0」时无法淘汰导致池无限增长。
	// 触达 hardCap 时 Get 返回 errPoolFull，由调用方降级。
	hardCap     int
	idleTimeout time.Duration
	hostKeys    map[string]hostKeyEntry // TOFU: host:port → 首次记录的主机公钥（含 lastSeen）
	// knownHosts 非 nil 时启用严格主机密钥校验（MANAGI_KNOWN_HOSTS 指向 OpenSSH known_hosts）。
	knownHosts ssh.HostKeyCallback
	// hostKeyErr：配置了 known_hosts 却加载失败。此时拒绝所有连接而不是退回 TOFU——
	// 用户要的是严格校验，静默降级成「首次遇到谁都信」比直接报错更危险。
	hostKeyErr error
}

// errPoolFull 连接池触达硬上限且无空闲连接可淘汰。
// 必须给出下一步动作：只说 "pool full" 用户无从下手（关窗口还是改配置）。
var errPoolFull = fmt.Errorf("SSH 连接池已满且无空闲连接：请关闭部分终端/文件标签后重试，或调大 MANAGI_SSH_POOL_SIZE")

// New 创建连接池。容量取 MANAGI_SSH_POOL_SIZE（≤0 时用默认值，覆盖测试直构的 Config）。
func New(cfg *config.Config) *Pool {
	idleTimeout := time.Duration(cfg.SSHIdleTimeout) * time.Second
	if idleTimeout <= 0 {
		idleTimeout = 120 * time.Second
	}
	maxSize := cfg.SSHPoolSize
	if maxSize <= 0 {
		maxSize = config.DefaultSSHPoolSize
	}
	p := &Pool{
		conns:       make(map[string]*Connection),
		keyLocks:    keylock.New(),
		cfg:         cfg,
		maxSize:     maxSize,
		hardCap:     maxSize * 2, // 硬上限为 maxSize 2 倍，防止全部占用时无限增长
		idleTimeout: idleTimeout,
		hostKeys:    make(map[string]hostKeyEntry),
	}
	p.initHostKeyVerification()
	return p
}

// initHostKeyVerification 按 MANAGI_KNOWN_HOSTS 选择主机密钥校验方式：
// 配了且能加载 → 严格校验；配了但加载失败 → 记录错误并拒绝连接；没配 → 进程内 TOFU。
func (p *Pool) initHostKeyVerification() {
	path := p.cfg.KnownHostsFile
	if path == "" {
		slog.Info("ssh host key verification: TOFU（首次信任）。严格校验请设置 MANAGI_KNOWN_HOSTS")
		return
	}
	cb, err := knownhosts.New(path)
	if err != nil {
		p.hostKeyErr = fmt.Errorf("MANAGI_KNOWN_HOSTS=%s 无法解析: %w", path, err)
		slog.Error("known_hosts 加载失败，将拒绝所有 SSH 连接（未退回 TOFU）", "path", path, "err", err)
		return
	}
	p.knownHosts = cb
	slog.Info("ssh host key verification: known_hosts", "path", path)
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
// isAlive 是阻塞网络调用，移出 p.mu.Lock() 范围，避免慢节点卡死全池。
func (p *Pool) Get(node model.Node) (*Connection, error) {
	// 所有入口（HTTP 执行/下载、WS 终端、WS SFTP）都经过这里，校验放一处即全覆盖
	if err := node.Validate(); err != nil {
		return nil, err
	}
	key := node.ConnectionKey()
	p.keyLocks.Lock(key)
	defer p.keyLocks.Unlock(key)

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
				close(cStill.done) // 通知 keepalive goroutine 退出
				delete(p.conns, key)
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
	// evictOldestLocked 在全部 refs>0 时无法淘汰，池可能超 maxSize。
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
	cNew := &Connection{refs: 1, lastUsed: time.Now(), client: client, done: done}
	// dial 在锁外，并发同 key 可能他人已先入池。
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
	// keepalive 在连接提交到 map 后启动，接收 done 以便连接被清理时及时退出
	go p.keepalive(key, client, done)
	return cNew, nil
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
	for k, c := range p.conns {
		if c.client != nil {
			_ = c.client.Close()
		}
		close(c.done) // 通知 keepalive goroutine 退出
		delete(p.conns, k)
	}
	for k := range p.hostKeys {
		delete(p.hostKeys, k)
	}
}

// StartCleaner 启动后台清理协程，回收空闲超时连接。
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

// Execute 在指定连接上执行命令，返回按行拆分的 stdout 与 stderr。
// 调用方负责 Get/Release；Execute 本身不释放连接。
// 支持 ctx 取消，客户端断开时终止 SSH 命令执行。
func (p *Pool) Execute(ctx context.Context, node model.Node, cmds []string) (output []string, errs []string, err error) {
	if len(cmds) == 0 {
		return nil, nil, nil
	}
	conn, err := p.Get(node)
	if err != nil {
		return nil, nil, err
	}
	defer p.Release(conn)

	session, err := conn.client.NewSession()
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = session.Close() }()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	// ctx 取消时关闭 session 终止命令执行
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

// hostKeyCallback 返回主机密钥校验回调。
// 优先 MANAGI_KNOWN_HOSTS（严格、可持久化、可离线核对指纹）；配置加载失败时拒绝连接，
// 绝不退回 TOFU——否则「配了严格校验」的部署会在文件写坏那一刻静默变成首次即信任。
// 未配置时才是进程内 TOFU：首次记录公钥并接受，后续比对，不匹配即拒绝（防 MITM）。
func (p *Pool) hostKeyCallback(node model.Node) ssh.HostKeyCallback {
	if p.knownHosts != nil {
		return p.knownHosts
	}
	if p.hostKeyErr != nil {
		return func(string, net.Addr, ssh.PublicKey) error { return p.hostKeyErr }
	}
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
		// 更新 lastSeen，标记该主机近期活跃
		entry.lastSeen = time.Now()
		p.hostKeys[addr] = entry
		return nil
	}
}

// keepalive 周期发送 keepalive 请求。
// 探测失败时主动从池中清理死连接（仅当指针匹配且 refs==0），避免滞留至 cleanIdle。
// 接收 done channel，连接被 cleanIdle/evict/CloseAll 清理时及时退出，避免短暂泄漏。
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
				close(c.done) // 通知自身退出（安全：仅当连接仍在 map 中时执行）
				delete(p.conns, key)
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
		// ssh.ParsePrivateKey 已覆盖 RSA/Ed25519/ECDSA/PKCS8 等常见格式
		signer, err := ssh.ParsePrivateKey([]byte(node.AuthValue))
		if err != nil {
			var ppm *ssh.PassphraseMissingError
			if errors.As(err, &ppm) {
				// 当前没有口令输入口，故给出可操作的绕开方式，而不是 "ssh: no key found"
				return nil, errors.New("私钥带口令保护，暂不支持解锁：请用 ssh-keygen -p 去掉口令后重新粘贴，或改用密码认证")
			}
			return nil, fmt.Errorf("私钥解析失败（请确认粘贴的是完整私钥，含 BEGIN/END 行）: %w", err)
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	default: // password
		return []ssh.AuthMethod{ssh.Password(node.AuthValue)}, nil
	}
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
			close(c.done) // 通知 keepalive 退出
			delete(p.conns, k)
		}
	}
	// 清理长期未使用的主机密钥条目，防止 map 无限增长。
	// 仅删除超过 hostKeyTTL 且当前无活跃连接的条目。
	// 注意键格式差异：hostKeys 以 host:port 记录，conns 以完整 ConnectionKey
	// （host:port:username:凭据指纹）记录，故须按「host:port:」前缀匹配
	// 判断该主机上是否还有任意用户/任意凭据的活跃连接。
	for addr, entry := range p.hostKeys {
		if now.Sub(entry.lastSeen) > hostKeyTTL && !p.hasActiveConnLocked(addr) {
			delete(p.hostKeys, addr)
		}
	}
}

// hasActiveConnLocked 判断指定 host:port 是否仍有任意用户的连接在池中。
// 调用方需持 p.mu。连接键为 Node.ConnectionKey()（host:port:username:凭据指纹），
// 以 host:port 打头，故用「addr:」前缀匹配即可覆盖同一主机端口的全部条目。
func (p *Pool) hasActiveConnLocked(addr string) bool {
	prefix := addr + ":"
	for k := range p.conns {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	return false
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
			close(c.done) // 通知 keepalive 退出
			delete(p.conns, oldestKey)
		}
	}
}

// joinLines 将多条命令用换行拼接（对应 v2 "\n".join）。
// 复用 strings.Join，删除手写循环。
func joinLines(cmds []string) string {
	return strings.Join(cmds, "\n")
}

// splitLines 按行拆分命令输出：保留中间空行（cat/df/awk 等输出里的空行是内容的一部分，
// 丢掉会让前端显示串行错位），只去掉行尾 \r 与末尾换行产生的空尾行。
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	raw := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		out = append(out, strings.TrimRight(line, "\r"))
	}
	// 尾部空行不携带信息（多数命令以换行收尾），整体即空 ⇒ 返回空切片
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}
