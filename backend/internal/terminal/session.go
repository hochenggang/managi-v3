// Package terminal 封装 SSH 交互式终端会话。
// 对应 v2 routers.py 的 terminal 部分，修复 resize 与心跳缺陷。
// 设计见 ../design-v3.md §6.1（换行）与 §6.3（心跳）。
package terminal

import (
	"fmt"
	"io"

	"golang.org/x/crypto/ssh"

	"managi/internal/model"
)

// Session 一次终端会话。
type Session struct {
	node    model.Node
	client  *ssh.Client
	session *ssh.Session
	stdin   io.WriteCloser
	stdout  io.Reader
}

// New 创建终端会话。
func New(node model.Node, sshc *ssh.Client) *Session {
	return &Session{node: node, client: sshc}
}

// Open 申请 PTY 并启动 Shell。
// 修正 v2：使用 RequestPty + Shell，cols/rows 由结构化参数传入。
func (s *Session) Open(cols, rows int) error {
	sess, err := s.client.NewSession()
	if err != nil {
		return fmt.Errorf("new ssh session: %w", err)
	}
	s.session = sess

	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	if err := s.session.RequestPty("xterm", rows, cols, modes); err != nil {
		s.session.Close()
		s.session = nil
		return fmt.Errorf("request pty: %w", err)
	}

	stdin, err := s.session.StdinPipe()
	if err != nil {
		s.session.Close()
		s.session = nil
		return fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := s.session.StdoutPipe()
	if err != nil {
		s.session.Close()
		s.session = nil
		return fmt.Errorf("stdout pipe: %w", err)
	}
	s.stdin = stdin
	s.stdout = stdout

	if err := s.session.Shell(); err != nil {
		s.session.Close()
		s.session = nil
		return fmt.Errorf("start shell: %w", err)
	}
	return nil
}

// Resize 调整 PTY 窗口大小。
// 修正 v2：结构化 WindowChange 替代 \x1b[8;rows;cols t 转义序列解析。
func (s *Session) Resize(cols, rows int) error {
	if s.session == nil {
		return fmt.Errorf("session not opened")
	}
	return s.session.WindowChange(rows, cols)
}

// Stdin 返回用户输入写入流。
func (s *Session) Stdin() io.WriteCloser { return s.stdin }

// Stdout 返回 Shell 输出读取流。
func (s *Session) Stdout() io.Reader { return s.stdout }

// Close 关闭会话。
func (s *Session) Close() error {
	if s.session == nil {
		return nil
	}
	err := s.session.Close()
	s.session = nil
	return err
}
