// Package sshpool - 主机密钥校验：strict known_hosts / 加载失败即拒绝 / 文件化 TOFU，三选一。
package sshpool

import (
	"bytes"
	"fmt"
	"log/slog"
	"net"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"managi/internal/config"
)

// hostKeyStore 主机密钥校验器。三种模式互斥，构造时一次定终身：
//   - MANAGI_KNOWN_HOSTS 配置且加载成功 → 严格校验（可持久化、可离线核对指纹）；
//   - 配置了但加载失败 → 拒绝所有连接，绝不静默退回 TOFU；
//   - 未配置 → TOFU：首次信任落盘在信任库（默认 ~/.managi/known_hosts），
//     路径取不到或不可写（如容器只读根文件系统）时降级为进程内记录并告警。
type hostKeyStore struct {
	strict ssh.HostKeyCallback
	err    error
	tofu   *tofuStore
}

// newHostKeys 按配置选择校验模式。
func newHostKeys(cfg *config.Config) *hostKeyStore {
	if path := cfg.KnownHostsFile; path != "" {
		cb, err := knownhosts.New(path)
		if err != nil {
			slog.Error("known_hosts 加载失败，将拒绝所有 SSH 连接（未退回 TOFU）", "path", path, "err", err)
			return &hostKeyStore{err: fmt.Errorf("MANAGI_KNOWN_HOSTS=%s 无法解析: %w", path, err)}
		}
		slog.Info("ssh host key verification: known_hosts", "path", path)
		return &hostKeyStore{strict: cb}
	}
	tofu, err := newTOFUStore(cfg.TOFUKnownHostsFile)
	if err != nil {
		slog.Error("TOFU 信任库加载失败，将拒绝所有 SSH 连接", "err", err)
		return &hostKeyStore{err: err}
	}
	return &hostKeyStore{tofu: tofu}
}

// callback 返回给 ssh.ClientConfig 用的回调；addr 为 host:port（TOFU 记录键）。
func (s *hostKeyStore) callback(addr string) ssh.HostKeyCallback {
	switch {
	case s.strict != nil:
		return s.strict
	case s.tofu != nil:
		return func(_ string, _ net.Addr, key ssh.PublicKey) error { return s.tofu.verify(addr, key) }
	default:
		return func(string, net.Addr, ssh.PublicKey) error { return s.err }
	}
}

// recorded 返回某地址已记录的公钥（TOFU 模式），供诊断与测试读取。
func (s *hostKeyStore) recorded(addr string) (ssh.PublicKey, bool) {
	if s.tofu == nil {
		return nil, false
	}
	return s.tofu.recorded(addr)
}

// sameKey 公钥是否等价：类型与编码字节都相同才算。
func sameKey(a, b ssh.PublicKey) bool {
	return a.Type() == b.Type() && bytes.Equal(a.Marshal(), b.Marshal())
}
