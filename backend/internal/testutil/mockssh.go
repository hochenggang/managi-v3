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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
	// silentGlobals 为真时对全局请求只收不回（模拟半开链路），见 SilentGlobalRequests。
	silentGlobals atomic.Bool
	// stalledShell 为真时 shell 不读 stdin（模拟远端读端卡死），见 StalledShell。
	stalledShell atomic.Bool
	// writesStalled 为真时 SFTP 写句柄的每次写入都先等 writeGate（模拟落盘卡死），见 StallWrites。
	writesStalled atomic.Bool
	writeGate     chan struct{}
	writeGateOnce sync.Once
	stopOnce      sync.Once
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
		listener:  listener,
		hostKey:   signer,
		rootDir:   t.TempDir(),
		password:  "testpass",
		writeGate: make(chan struct{}),
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
	if s.silentGlobals.Load() {
		// 只收不回：want-reply 的全局请求（保活探测）在客户端侧会一直等——半开链路的样子
		go func() {
			for range reqs {
			}
		}()
	} else {
		go ssh.DiscardRequests(reqs)
	}

	for newChannel := range chans {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "unknown channel type")
			continue
		}
		channel, reqs, err := newChannel.Accept()
		if err != nil {
			continue
		}
		// 每个 session channel 独立协程：一条 SSH 连接上可以并发开多路
		// shell/subsystem（连接池复用同一条连接），串行处理会让后开的通道永远排队。
		go s.handleSession(channel, reqs)
	}
}

// handleSession 处理一个 session channel 的请求序列。
func (s *Server) handleSession(channel ssh.Channel, reqs <-chan *ssh.Request) {
	for req := range reqs {
		switch req.Type {
		case "exec":
			cmd := parseStringPayload(req.Payload)
			_ = req.Reply(true, nil)
			s.handleExec(channel, reqs, cmd)
			return
		case "shell":
			_ = req.Reply(true, nil)
			if s.stalledShell.Load() {
				s.handleStalledShell(channel, reqs)
			} else {
				s.handleShell(channel)
			}
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
// reqs 为通道请求流，仅在「挂死」模式使用：它随通道关闭而关闭，是唯一的关闭信号。
func (s *Server) handleExec(channel ssh.Channel, reqs <-chan *ssh.Request, cmd string) {
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
		case line == "hang":
			// 模拟挂死命令（如 sleep）：保持通道打开，直到客户端关闭通道。
			// 不能等 channel.Read：客户端未设 Stdin 时 x/crypto/ssh 在启动后
			// 立即发 stdin EOF，Read 会马上返回 EOF，与通道关闭区分不开。
			// reqs 只在客户端发 CHANNEL_CLOSE 或连接断开时关闭。
			for req := range reqs {
				_ = req.Reply(false, nil)
			}
			return
		case strings.HasPrefix(line, "flood "):
			// 模拟大输出命令（如 cat 大文件）：向 stdout 写 n 字节。
			// 用命令生成输出而不是把大载荷塞进命令串：SSH 单包上限约 256KB，
			// 超大的 exec 命令根本发不出去（连接会被直接掐断）。
			n, err := strconv.Atoi(strings.TrimSpace(line[len("flood "):]))
			if err != nil || n < 0 {
				_, _ = channel.Stderr().Write([]byte("flood: bad size\n"))
				exitCode = 1
				break
			}
			chunk := []byte(strings.Repeat("a", 32*1024))
			for written := 0; written < n; {
				size := min(len(chunk), n-written)
				if _, err := channel.Write(chunk[:size]); err != nil {
					return
				}
				written += size
			}
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

// handleStalledShell 模拟读端卡死的 shell：不读 stdin。
// 客户端在 SSH 通道窗口（x/crypto 默认 2MB）耗尽后写不进去，用来测输入队列的溢出策略。
// reqs 随通道关闭而关闭，是这里唯一的关闭信号（同 handleExec 的 hang）。
func (s *Server) handleStalledShell(channel ssh.Channel, reqs <-chan *ssh.Request) {
	defer func() { _ = channel.Close() }()
	for req := range reqs {
		_ = req.Reply(false, nil)
	}
}

// handleSFTP 启动 SFTP request server 服务 rootDir。
func (s *Server) handleSFTP(channel ssh.Channel) {
	defer func() { _ = channel.Close() }()
	handler := &osHandler{root: s.rootDir, srv: s}
	srv := sftp.NewRequestServer(channel, sftp.Handlers{
		FileGet:  handler,
		FilePut:  handler,
		FileCmd:  handler,
		FileList: handler,
	})
	_ = srv.Serve()
}

// Dial 拨一条已认证的 SSH 客户端连接到本服务器。
// 测试要直连 mock（不经连接池）时用它，握手细节不必在每个测试里重抄一遍。
// 用户名与 handleConn 的 PasswordCallback 保持一致。
func (s *Server) Dial(t *testing.T) *ssh.Client {
	t.Helper()
	client, err := ssh.Dial("tcp", s.Addr(), &ssh.ClientConfig{
		User:            "test",
		Auth:            []ssh.AuthMethod{ssh.Password(s.password)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	})
	if err != nil {
		t.Fatalf("dial mock ssh: %v", err)
	}
	return client
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

// HostKey 返回服务器主机公钥，供测试构造 known_hosts 条目。
func (s *Server) HostKey() ssh.PublicKey { return s.hostKey.PublicKey() }

// Password 返回认证密码。
func (s *Server) Password() string { return s.password }

// SilentGlobalRequests 使服务器对全局请求只收不回（含各类保活探测），
// 模拟「TCP 连着但对端不响应」的半开链路，供存活探测超时测试使用。
// 对调用后新接入的连接生效（拨号前调用即可）。
func (s *Server) SilentGlobalRequests() { s.silentGlobals.Store(true) }

// StalledShell 使后续 shell 不读 stdin（模拟远端读端卡死）：
// 客户端的 stdin 写会在 SSH 通道窗口耗尽后阻塞，供输入队列溢出测试使用。
// 对调用后新开的 shell 通道生效（open pty 前调用）。
func (s *Server) StalledShell() { s.stalledShell.Store(true) }

// StallWrites 使此后打开的 SFTP 写句柄每次 WriteAt 都先等 UnstallWrites（模拟落盘卡死）。
// 对调用后新打开的写句柄生效（即上传请求前调用）。
func (s *Server) StallWrites() { s.writesStalled.Store(true) }

// UnstallWrites 放行所有被 StallWrites 卡住的写入（幂等；Close 亦会放行）。
func (s *Server) UnstallWrites() { s.writeGateOnce.Do(func() { close(s.writeGate) }) }

// gate 返回写锁定用的信号通道。
func (s *Server) gate() chan struct{} { return s.writeGate }

// RootDir 返回 SFTP 根目录（本地 temp 路径）。
func (s *Server) RootDir() string { return s.rootDir }

// Accepts 返回累计接受的连接数（测试连接复用）。
func (s *Server) Accepts() int32 {
	return atomic.LoadInt32(&s.accepts)
}

// Close 关闭服务器。被 StallWrites 卡住的写入一并放行，测试清理不必手动解卡。
func (s *Server) Close() {
	s.stopOnce.Do(func() {
		s.UnstallWrites()
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
	srv  *Server
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
	// StallWrites 下写入被闸住：落在 WriteAt 上而不是 open 上，
	// 上传响应照常返回，测的是「客户端在等落盘」而不是「开不了文件」。
	if h.srv.writesStalled.Load() {
		return &gatedWriter{f: f, gate: h.srv.gate()}, nil
	}
	return f, nil
}

// gatedWriter 写前先等闸的 *os.File 包装：模拟落盘卡死。
type gatedWriter struct {
	f    *os.File
	gate chan struct{}
}

func (w *gatedWriter) WriteAt(p []byte, off int64) (int, error) {
	<-w.gate
	return w.f.WriteAt(p, off)
}

func (w *gatedWriter) Close() error { return w.f.Close() }

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
			// 用 Stat 而非 entry.Info()（lstat 语义）：真实 sftp-server 在 readdir
			// 中跟随软链接，目录软链接会以 IsDir()==true 出现在列表里。
			// mock 若用 lstat，软链接相关行为（删除链接 vs 删除目标）就测不到真实语义。
			info, err := os.Stat(filepath.Join(h.abs(r.Filepath), e.Name()))
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

// Lstat 实现 sftp.LstatFileLister：不跟随软链接，返回链接本身。
// 未实现该接口时 pkg/sftp 会把 Lstat 当 Stat 处理，客户端永远看不到链接位。
func (h *osHandler) Lstat(r *sftp.Request) (sftp.ListerAt, error) {
	info, err := os.Lstat(h.abs(r.Filepath))
	if err != nil {
		return nil, err
	}
	return listerAt([]os.FileInfo{info}), nil
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
