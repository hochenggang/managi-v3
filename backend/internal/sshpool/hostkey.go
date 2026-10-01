// Package sshpool - 主机密钥校验：strict known_hosts / 加载失败即拒绝 / 进程内 TOFU，三选一。
package sshpool

import (
	"bytes"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"managi/internal/config"
)

// hostKeyTTL 主机密钥保留时长：超过此时长未连接且无活跃连接的条目将被清理，
// 否则 map 会随历史节点数无限增长。
const hostKeyTTL = 24 * time.Hour

// hostKeyEntry TOFU 主机密钥记录条目（lastSeen 供 TTL 清理判活）。
type hostKeyEntry struct {
	key      ssh.PublicKey
	lastSeen time.Time
}

// hostKeyStore 主机密钥校验器。三种模式互斥，构造时一次定终身：
//   - MANAGI_KNOWN_HOSTS 配置且加载成功 → 严格校验（可持久化、可离线核对指纹）；
//   - 配置了但加载失败 → 拒绝所有连接，绝不静默退回 TOFU；
//   - 未配置 → 进程内 TOFU：首次记录并接受，后续比对，不匹配即拒绝（防 MITM）。
//
// 自带一把锁，与池锁互不嵌套（清理时由池持 p.mu 调入，顺序恒为 p.mu → s.mu）。
type hostKeyStore struct {
	strict ssh.HostKeyCallback
	err    error

	mu   sync.Mutex
	tofu map[string]hostKeyEntry
}

// newHostKeys 按配置选择校验模式。
func newHostKeys(cfg *config.Config) *hostKeyStore {
	s := &hostKeyStore{tofu: make(map[string]hostKeyEntry)}
	path := cfg.KnownHostsFile
	if path == "" {
		slog.Info("ssh host key verification: TOFU（首次信任）。严格校验请设置 MANAGI_KNOWN_HOSTS")
		return s
	}
	cb, err := knownhosts.New(path)
	if err != nil {
		s.err = fmt.Errorf("MANAGI_KNOWN_HOSTS=%s 无法解析: %w", path, err)
		slog.Error("known_hosts 加载失败，将拒绝所有 SSH 连接（未退回 TOFU）", "path", path, "err", err)
		return s
	}
	s.strict = cb
	slog.Info("ssh host key verification: known_hosts", "path", path)
	return s
}

// callback 返回给 ssh.ClientConfig 用的回调；addr 为 host:port（TOFU 记录键）。
func (s *hostKeyStore) callback(addr string) ssh.HostKeyCallback {
	if s.strict != nil {
		return s.strict
	}
	if s.err != nil {
		return func(string, net.Addr, ssh.PublicKey) error { return s.err }
	}
	return func(_ string, _ net.Addr, key ssh.PublicKey) error { return s.recordOrCheck(addr, key) }
}

// recordOrCheck TOFU 判定：无记录则记下首个公钥并接受，有记录则必须逐字节相同。
func (s *hostKeyStore) recordOrCheck(addr string, key ssh.PublicKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.tofu[addr]
	if !ok {
		s.tofu[addr] = hostKeyEntry{key: key, lastSeen: time.Now()}
		slog.Info("ssh host key recorded (TOFU)", "addr", addr, "fingerprint", ssh.FingerprintSHA256(key))
		return nil
	}
	if !sameKey(entry.key, key) {
		return fmt.Errorf("ssh host key mismatch for %s: expected %s, got %s",
			addr, ssh.FingerprintSHA256(entry.key), ssh.FingerprintSHA256(key))
	}
	// 记录活跃时间，供 TTL 清理判断该主机是否还值得保留
	entry.lastSeen = time.Now()
	s.tofu[addr] = entry
	return nil
}

// reap 清理超期且无活跃连接的条目。active 报告某 host:port 上是否仍有连接在池里。
// 调用方需持池锁（active 要读连接表）。
func (s *hostKeyStore) reap(now time.Time, active func(addr string) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for addr, entry := range s.tofu {
		if now.Sub(entry.lastSeen) > hostKeyTTL && !active(addr) {
			delete(s.tofu, addr)
		}
	}
}

// recorded 返回某地址已记录的公钥（TOFU 模式），供诊断与测试读取。
func (s *hostKeyStore) recorded(addr string) (ssh.PublicKey, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.tofu[addr]
	return entry.key, ok
}

// sameKey 公钥是否等价：类型与编码字节都相同才算。
func sameKey(a, b ssh.PublicKey) bool {
	return a.Type() == b.Type() && bytes.Equal(a.Marshal(), b.Marshal())
}
