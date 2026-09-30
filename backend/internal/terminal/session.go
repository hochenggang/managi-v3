// Package terminal 封装 SSH 交互式终端会话。
// 对应 v2 routers.py 的 terminal 部分，修复 resize 与心跳缺陷。
// 设计见 ../design-v3.md §6.1（换行）与 §6.3（心跳）。
package terminal

import (
	"fmt"
	"io"
	"sync"

	"golang.org/x/crypto/ssh"
)

// Session 一次终端会话。
// session/stdin/stdout 会被三类协程触碰：WS 读协程（Resize、写 stdin）、
// 输出协程（读 stdout）、空闲计时器或输出协程触发的 Close，
// 故字段一律经 mu 访问；对外方法只取快照、网络 I/O 留在锁外。
type Session struct {
	client *ssh.Client

	mu      sync.Mutex
	session *ssh.Session
	stdin   io.WriteCloser
	stdout  io.Reader
}

// New 创建终端会话。
func New(sshc *ssh.Client) *Session {
	return &Session{client: sshc}
}

// Open 申请 PTY 并启动 Shell。
// 修正 v2：使用 RequestPty + Shell，cols/rows 由结构化参数传入。
func (s *Session) Open(cols, rows int) error {
	sess, err := s.client.NewSession()
	if err != nil {
		return fmt.Errorf("new ssh session: %w", err)
	}

	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	// 前端就是 xterm.js，声明 xterm-256color 让 vim/htop/git diff 用满 256 色；
	// 只写 "xterm" 会让这些程序退回 8 色并丢掉部分能力。
	if err := sess.RequestPty("xterm-256color", rows, cols, modes); err != nil {
		_ = sess.Close()
		return fmt.Errorf("request pty: %w", err)
	}

	stdin, err := sess.StdinPipe()
	if err != nil {
		_ = sess.Close()
		return fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		_ = sess.Close()
		return fmt.Errorf("stdout pipe: %w", err)
	}

	if err := sess.Shell(); err != nil {
		_ = sess.Close()
		return fmt.Errorf("start shell: %w", err)
	}

	s.mu.Lock()
	s.session, s.stdin, s.stdout = sess, stdin, stdout
	s.mu.Unlock()
	return nil
}

// Resize 调整 PTY 窗口大小。
// 非正尺寸会把 PTY 压成 0 列（输出换行全乱、vim/htop 排版崩掉），
// 而客户端在拿到真实尺寸前常先发来一帧 0×0，故在这里统一拒绝，
// 调用方不必各自再判一遍。
func (s *Session) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return fmt.Errorf("invalid terminal size: cols=%d rows=%d", cols, rows)
	}
	sess := s.currentSession()
	if sess == nil {
		return fmt.Errorf("session not opened")
	}
	// 修正 v2：结构化 WindowChange 替代 \x1b[8;rows;cols t 转义序列解析。
	return sess.WindowChange(rows, cols)
}

// Stdin 返回用户输入写入流；会话未打开或已关闭时返回 nil。
func (s *Session) Stdin() io.WriteCloser {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stdin
}

// Stdout 返回 Shell 输出读取流；会话未打开或已关闭时返回 nil。
func (s *Session) Stdout() io.Reader {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stdout
}

// Close 关闭会话（幂等：并发调用只有一次真正执行底层 Close）。
func (s *Session) Close() error {
	s.mu.Lock()
	sess := s.session
	s.session = nil
	s.mu.Unlock()
	if sess == nil {
		return nil
	}
	return sess.Close()
}

// currentSession 取底层会话快照，未打开或已关闭时为 nil。
func (s *Session) currentSession() *ssh.Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.session
}
