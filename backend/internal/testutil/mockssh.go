// Package testutil 提供后端测试基础设施：进程内 mock SSH/SFTP 服务器。
// 不依赖外部 SSH 服务，全部测试在 127.0.0.1 随机端口进行。
package testutil

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// Server 进程内 mock SSH/SFTP 服务器。
type Server struct {
	listener net.Listener
	hostKey  ssh.Signer
	rootDir  string
	password string
	accepts  int32 // 累计 accept 连接数（测试连接复用）
	stopOnce sync.Once
}

// Start 启动一个 mock SSH/SFTP 服务器在 127.0.0.1 随机端口。
// rootDir 为 SFTP 根目录（t.TempDir()，测试结束自动清理）。
func Start(t *testing.T) *Server {
	t.Helper()

	// 生成 ed25519 host key
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("new signer from key: %v", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	s := &Server{
		listener: listener,
		hostKey:  signer,
		rootDir:  t.TempDir(),
		password: "testpass",
	}

	sshConfig := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if c.User() == "test" && string(pass) == s.password {
				return nil, nil
			}
			return nil, fmt.Errorf("invalid credentials")
		},
	}
	sshConfig.AddHostKey(signer)

	go s.acceptLoop(sshConfig)
	return s
}

// acceptLoop 接受连接并分发。
func (s *Server) acceptLoop(cfg *ssh.ServerConfig) {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return // listener closed
		}
		go s.handleConn(conn, cfg)
	}
}

// handleConn 处理一条 SSH 连接。
func (s *Server) handleConn(nconn net.Conn, cfg *ssh.ServerConfig) {
	atomic.AddInt32(&s.accepts, 1)

	conn, chans, reqs, err := ssh.NewServerConn(nconn, cfg)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	go ssh.DiscardRequests(reqs)

	for newChannel := range chans {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "unknown channel type")
			continue
		}
		channel, reqs, err := newChannel.Accept()
		if err != nil {
			continue
		}
		s.handleSession(channel, reqs)
	}
}

// handleSession 处理一个 session channel 的请求序列。
func (s *Server) handleSession(channel ssh.Channel, reqs <-chan *ssh.Request) {
	for req := range reqs {
		switch req.Type {
		case "exec":
			cmd := parseStringPayload(req.Payload)
			_ = req.Reply(true, nil)
			s.handleExec(channel, cmd)
			return
		case "shell":
			_ = req.Reply(true, nil)
			s.handleShell(channel)
			return
		case "subsystem":
			if parseStringPayload(req.Payload) == "sftp" {
				_ = req.Reply(true, nil)
				s.handleSFTP(channel)
			} else {
				_ = req.Reply(false, nil)
			}
			return
		case "pty-req", "window-change", "env":
			_ = req.Reply(true, nil)
		default:
			_ = req.Reply(false, nil)
		}
	}
}

// handleExec 模拟命令执行（支持多行命令，用 \n 分隔）。
func (s *Server) handleExec(channel ssh.Channel, cmd string) {
	defer func() { _ = channel.Close() }()

	// 多行命令：按 \n 拆分逐行执行（模拟 shell）
	lines := strings.Split(cmd, "\n")
	var exitCode uint32
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		switch {
		case line == "false":
			_, _ = channel.Stderr().Write([]byte("command failed\n"))
			exitCode = 1
		case strings.HasPrefix(line, "echo "):
			_, _ = channel.Write([]byte(line[5:] + "\n"))
		case line == "echo":
			_, _ = channel.Write([]byte("\n"))
		default:
			// 其他命令模拟空输出成功
		}
	}
	sendExitStatus(channel, exitCode)
}

// handleShell 模拟交互式 shell：回显输入。
func (s *Server) handleShell(channel ssh.Channel) {
	defer func() { _ = channel.Close() }()
	buf := make([]byte, 4096)
	for {
		n, err := channel.Read(buf)
		if n > 0 {
			_, _ = channel.Write(buf[:n]) // 回显
		}
		if err != nil {
			break
		}
	}
}

// handleSFTP 启动 SFTP request server 服务 rootDir。
func (s *Server) handleSFTP(channel ssh.Channel) {
	defer func() { _ = channel.Close() }()
	handler := &osHandler{root: s.rootDir}
	srv := sftp.NewRequestServer(channel, sftp.Handlers{
		FileGet:  handler,
		FilePut:  handler,
		FileCmd:  handler,
		FileList: handler,
	})
	_ = srv.Serve()
}

// Addr 返回服务器监听地址（127.0.0.1:port）。
func (s *Server) Addr() string {
	return s.listener.Addr().String()
}

// Port 返回监听端口。
func (s *Server) Port() int {
	return s.listener.Addr().(*net.TCPAddr).Port
}

// Host 返回监听主机。
func (s *Server) Host() string { return "127.0.0.1" }

// Password 返回认证密码。
func (s *Server) Password() string { return s.password }

// RootDir 返回 SFTP 根目录（本地 temp 路径）。
func (s *Server) RootDir() string { return s.rootDir }

// Accepts 返回累计接受的连接数（测试连接复用）。
func (s *Server) Accepts() int32 {
	return atomic.LoadInt32(&s.accepts)
}

// Close 关闭服务器。
func (s *Server) Close() {
	s.stopOnce.Do(func() {
		_ = s.listener.Close()
	})
}

// sendExitStatus 发送 exit-status 请求。
func sendExitStatus(channel ssh.Channel, code uint32) {
	_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct {
		Code uint32
	}{code}))
}

// parseStringPayload 解析 SSH 请求 payload 中的 string（4 字节长度前缀 + 内容）。
func parseStringPayload(payload []byte) string {
	if len(payload) < 4 {
		return ""
	}
	length := binary.BigEndian.Uint32(payload[:4])
	if int(length)+4 > len(payload) {
		return ""
	}
	return string(payload[4 : 4+length])
}

// ===== os-backed SFTP handler =====

// osHandler 将 SFTP 请求映射到本地 temp 目录的 os 操作。
type osHandler struct {
	root string
}

func (h *osHandler) abs(p string) string {
	cleaned := filepath.Clean(filepath.FromSlash(p))
	if strings.HasPrefix(cleaned, "..") || filepath.IsAbs(cleaned) {
		cleaned = strings.TrimLeft(cleaned, "/")
		cleaned = strings.TrimPrefix(cleaned, "..")
		cleaned = strings.TrimLeft(cleaned, string(filepath.Separator))
	}
	return filepath.Join(h.root, cleaned)
}

// Fileread 处理文件读取。
func (h *osHandler) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	f, err := os.Open(h.abs(r.Filepath))
	if err != nil {
		return nil, err
	}
	return f, nil
}

// Filewrite 处理文件写入（尊重 pflags，支持断点续传的 O_CREATE 不截断）。
func (h *osHandler) Filewrite(r *sftp.Request) (io.WriterAt, error) {
	flags := r.Pflags()
	mode := os.O_WRONLY
	if flags.Creat {
		mode |= os.O_CREATE
	}
	if flags.Trunc {
		mode |= os.O_TRUNC
	}
	if flags.Append {
		mode |= os.O_APPEND
	}
	f, err := os.OpenFile(h.abs(r.Filepath), mode, 0644)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// Filecmd 处理 rename/remove/mkdir 等命令。
func (h *osHandler) Filecmd(r *sftp.Request) error {
	switch r.Method {
	case "Rename", "PosixRename":
		return os.Rename(h.abs(r.Filepath), h.abs(r.Target))
	case "Setstat":
		return nil // 忽略属性设置
	case "Rmdir", "Remove":
		return os.Remove(h.abs(r.Filepath))
	case "Mkdir":
		return os.MkdirAll(h.abs(r.Filepath), 0755)
	}
	return fmt.Errorf("unsupported filecmd method: %s", r.Method)
}

// Filelist 处理 List/Stat。
func (h *osHandler) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	switch r.Method {
	case "List":
		entries, err := os.ReadDir(h.abs(r.Filepath))
		if err != nil {
			return nil, err
		}
		infos := make([]os.FileInfo, 0, len(entries))
		for _, e := range entries {
			info, err := e.Info()
			if err != nil {
				continue
			}
			infos = append(infos, info)
		}
		return listerAt(infos), nil
	case "Stat":
		info, err := os.Stat(h.abs(r.Filepath))
		if err != nil {
			return nil, err
		}
		return listerAt([]os.FileInfo{info}), nil
	}
	return nil, fmt.Errorf("unsupported filelist method: %s", r.Method)
}

// listerAt 实现 sftp.ListerAt。
type listerAt []os.FileInfo

func (l listerAt) ListAt(buf []os.FileInfo, offset int64) (int, error) {
	if offset >= int64(len(l)) {
		return 0, io.EOF
	}
	n := copy(buf, l[offset:])
	if offset+int64(n) >= int64(len(l)) {
		return n, io.EOF
	}
	return n, nil
}
