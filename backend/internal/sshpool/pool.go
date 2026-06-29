// Package sshpool 实现 SSH 连接池。
// 对应 v2 的 ssh_pool.py，修正 v2 缺陷：命令执行路径也复用连接（引用计数）。
// 设计见 ../design-v3.md §4.2。
package sshpool

import (
	"bytes"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"managi/internal/config"
	"managi/internal/model"
)

// Connection 包装一条 SSH 连接及其引用计数。
type Connection struct {
	key      string
	refs     int
	lastUsed time.Time
	client   *ssh.Client
}

// Client 返回底层 SSH 客户端（供 sftp/terminal 复用）。
func (c *Connection) Client() *ssh.Client { return c.client }

// Pool SSH 连接池，进程内单例。
type Pool struct {
	mu          sync.Mutex
	conns       map[string]*Connection
	perKeyLocks map[string]*sync.Mutex
	perKeyLock  sync.Mutex // 保护 perKeyLocks 字典
	cfg         *config.Config
	maxSize     int
	idleTimeout time.Duration
}

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
		maxSize:     20,
		idleTimeout: idleTimeout,
	}
}

// NewWithSize 创建指定容量的连接池（测试用）。
func NewWithSize(cfg *config.Config, maxSize int) *Pool {
	p := New(cfg)
	p.maxSize = maxSize
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
	p.mu.Unlock()

	client, err := p.dial(node)
	if err != nil {
		return nil, err
	}
	cNew := &Connection{key: key, refs: 1, lastUsed: time.Now(), client: client}
	p.mu.Lock()
	p.conns[key] = cNew
	p.mu.Unlock()
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
		delete(p.conns, k)
	}
}

// StartCleaner 启动后台清理协程，回收空闲超时连接。
func (p *Pool) StartCleaner() {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			p.cleanIdle()
		}
	}()
}

// Execute 在指定连接上执行命令，返回按行拆分的 stdout 与 stderr。
// 调用方负责 Get/Release；Execute 本身不释放连接。
func (p *Pool) Execute(node model.Node, cmds []string) (output []string, errs []string, err error) {
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
	runErr := session.Run(joinLines(cmds))

	output = splitLines(stdout.String())
	errs = splitLines(stderr.String())
	if runErr != nil && len(errs) == 0 {
		errs = []string{runErr.Error()}
	}
	return output, errs, nil
}

// dial 建立一条新 SSH 连接。
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
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // 对应 v2 AutoAddPolicy
		Timeout:         timeout,
	}
	addr := node.Host + ":" + strconv.Itoa(node.Port)
	client, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return nil, fmt.Errorf("ssh dial %s: %w", addr, err)
	}
	// keepalive
	go p.keepalive(client)
	return client, nil
}

// keepalive 周期发送 keepalive 请求。
func (p *Pool) keepalive(client *ssh.Client) {
	interval := time.Duration(p.cfg.KeepaliveInterval) * time.Second
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		if !isAlive(client) {
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
			delete(p.conns, k)
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
		if c := p.conns[oldestKey]; c != nil && c.client != nil {
			_ = c.client.Close()
		}
		delete(p.conns, oldestKey)
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
func joinLines(cmds []string) string {
	out := ""
	for i, c := range cmds {
		if i > 0 {
			out += "\n"
		}
		out += c
	}
	return out
}

// splitLines 按行拆分，去掉空行。
func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			line := s[start:i]
			if len(line) > 0 {
				out = append(out, trimCR(line))
			}
			start = i + 1
		}
	}
	if start < len(s) {
		line := s[start:]
		if len(line) > 0 {
			out = append(out, trimCR(line))
		}
	}
	return out
}

func trimCR(s string) string {
	if len(s) > 0 && s[len(s)-1] == '\r' {
		return s[:len(s)-1]
	}
	return s
}
