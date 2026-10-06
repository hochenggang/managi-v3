// TOFU（首次信任）主机密钥库：信任锚落盘，跨进程重启延续。
package sshpool

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// tofuStore 记录「主机首次出现时的公钥」，此后只有逐字节相同的公钥才放行。
// 记录写在 OpenSSH known_hosts 文件里（默认 ~/.managi/known_hosts）：
// 只留在内存的信任锚会随每次重启失忆，把首次连接窗口重新丢给中间人——
// 对隔几天才连一次的跳板机场景，等于没有防 MITM。
type tofuStore struct {
	path string // 空 = 降级：记录只在进程内，重启即忘

	mu   sync.Mutex
	keys map[string]ssh.PublicKey // tofuKey(host:port) → 首次记录的公钥
}

// newTOFUStore 打开（或初始化）信任库并载入既有记录。
// 文件损坏时返回错误而非跳过坏行：坏行会让对应主机的信任锚静默消失，
// 下次连接被当作首次而信任任意密钥——宁可拒绝全部连接等人工处置。
func newTOFUStore(path string) (*tofuStore, error) {
	s := &tofuStore{path: path, keys: make(map[string]ssh.PublicKey)}
	if path == "" {
		slog.Warn("TOFU 信任库路径不可用，主机密钥仅进程内记录（重启即忘）")
		return s, nil
	}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := s.load(data); err != nil {
			return nil, fmt.Errorf("TOFU 信任库 %s 无法解析: %w（修复或删除该文件后重启；改用 MANAGI_KNOWN_HOSTS 可手工管理信任）", path, err)
		}
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
		// 首次使用：信任库还不存在（ENOTDIR：路径上有文件挡道，等价于没有库）
	default:
		return nil, fmt.Errorf("TOFU 信任库 %s 无法读取: %w", path, err)
	}
	if err := s.ensureWritable(); err != nil {
		slog.Warn("TOFU 信任库不可写，主机密钥仅进程内记录（重启即忘）",
			"path", path, "err", err, "hint", "只读根文件系统的容器可为 ~/.managi 挂载卷")
		s.path = ""
		return s, nil
	}
	slog.Info("ssh host key verification: TOFU（首次信任落盘）", "path", path)
	return s, nil
}

// ensureWritable 预检写能力：目录可建、文件可追加打开。
// 在启动时做，让「信任无法落盘」进一次日志并带上原因，而不是等首次记录才暴露。
func (s *tofuStore) ensureWritable() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(s.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	return f.Close()
}

// load 解析信任库。只认本库写出的三列格式 <host> <keytype> <base64>（外加空行与注释）；
// 哈希主机名、@cert-authority 等进阶格式不支持，那种需求应改用 MANAGI_KNOWN_HOSTS 严格模式。
func (s *tofuStore) load(data []byte) error {
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			return fmt.Errorf("第 %d 行: 期望 <host> <keytype> <key> 三列", i+1)
		}
		key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.Join(fields[1:], " ")))
		if err != nil {
			return fmt.Errorf("第 %d 行: %w", i+1, err)
		}
		s.keys[tofuKey(fields[0])] = key
	}
	return nil
}

// verify TOFU 判定：无记录则记下首个公钥并落盘后接受，有记录则必须逐字节相同。
func (s *tofuStore) verify(addr string, key ssh.PublicKey) error {
	k := tofuKey(addr)
	s.mu.Lock()
	defer s.mu.Unlock()
	known, ok := s.keys[k]
	if !ok {
		s.keys[k] = key
		slog.Info("ssh host key recorded (TOFU)", "addr", addr, "fingerprint", ssh.FingerprintSHA256(key))
		if err := s.persistLocked(); err != nil {
			slog.Warn("TOFU 信任库写入失败：该记录仅本次进程有效", "path", s.path, "err", err)
		}
		return nil
	}
	if !sameKey(known, key) {
		return fmt.Errorf("ssh host key mismatch for %s: expected %s, got %s",
			addr, ssh.FingerprintSHA256(known), ssh.FingerprintSHA256(key))
	}
	return nil
}

// persistLocked 全量重写信任库（临时文件 + rename 原子落位）。
// 逐条追加在崩溃时会留下半行，而半行即整库解析失败；机器管理的文件先要防自己写坏自己。
// 调用方需持 s.mu；s.path 为空（降级）时不落盘。
func (s *tofuStore) persistLocked() error {
	if s.path == "" {
		return nil
	}
	keys := make([]string, 0, len(s.keys))
	for k := range s.keys {
		keys = append(keys, k)
	}
	sort.Strings(keys) // 稳定输出，信任库 diff 可读
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(knownhosts.Line([]string{k}, s.keys[k]))
		b.WriteString("\n")
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// recorded 返回某地址已记录的公钥，供诊断与测试读取。
func (s *tofuStore) recorded(addr string) (ssh.PublicKey, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, ok := s.keys[tofuKey(addr)]
	return key, ok
}

// tofuKey 信任库键：knownhosts 规范化（非 22 端口记作 [host]:port）并小写。
// 落盘与查找共用同一函数，重启后从文件载回的键才能跟拨号地址对上——
// 对不上的后果不是报错，而是旧信任被当成「首次」重新建立（防 MITM 静默失效）。
func tofuKey(addr string) string {
	return strings.ToLower(knownhosts.Normalize(addr))
}
