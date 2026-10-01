// Package sftp 封装 SFTP 文件操作。
// 对应 v2 的 sftp_client.py，基于 github.com/pkg/sftp。
// 设计见 design-v5.md §4.1 与 §6.4 §6.5。
package sftp

import (
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"sync"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"managi/internal/model"
)

// Client 封装一次 SFTP 会话（一条 WS 通道一个）。
// 无状态中转：只持有远端 sftp 句柄，上传进度落在远端的 .part 文件上，
// 因此进程重启或客户端重连都能从 .part 续传，本地不需要任何登记表。
type Client struct {
	sc *sftp.Client
}

// New 创建 SFTP 客户端（复用 sshpool 的 SSH 连接）。
func New(sshc *ssh.Client) (*Client, error) {
	sc, err := sftp.NewClient(sshc)
	if err != nil {
		return nil, fmt.Errorf("sftp new client: %w", err)
	}
	return &Client{sc: sc}, nil
}

// Home 返回远端起始目录：SFTP 子系统的初始工作目录（通常为用户主目录）。
// 硬编码 "/" 会让无根目录读权限的账号一进来就报错，主目录则一定能访问；
// 取不到时退回 "/" 保持可用。
func (c *Client) Home() string {
	if wd, err := c.sc.Getwd(); err == nil && wd != "" {
		return wd
	}
	return "/"
}

// List 列出目录项。
func (c *Client) List(remotePath string) ([]model.FileItem, error) {
	entries, err := c.sc.ReadDir(remotePath)
	if err != nil {
		return nil, fmt.Errorf("sftp readdir %s: %w", remotePath, err)
	}
	items := make([]model.FileItem, 0, len(entries))
	for _, e := range entries {
		items = append(items, model.FileItem{
			Filename: e.Name(),
			Size:     e.Size(),
			Mode:     e.Mode().String(),
			IsDir:    e.IsDir(),
			Mtime:    e.ModTime().Unix(),
		})
	}
	return items, nil
}

// Mkdir 递归创建目录（对应 v2 _ensure_remote_directory_exists）。
func (c *Client) Mkdir(remotePath string) error {
	return c.sc.MkdirAll(remotePath)
}

// Delete 删除文件或目录（递归）。
// 先 Lstat：Stat 会跟随软链接，指向目录的链接会被当成目录，
// 递归删除就打在链接目标上（用户以为只删了链接，实际清空了别人的目录）。
func (c *Client) Delete(remotePath string) error {
	info, err := c.sc.Lstat(remotePath)
	if err != nil {
		// 少数服务器不支持 lstat，退回 Stat（跟随链接）保证删除仍可用
		info, err = c.sc.Stat(remotePath)
		if err != nil {
			return fmt.Errorf("sftp stat %s: %w", remotePath, err)
		}
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return c.sc.Remove(remotePath)
	}
	if info.IsDir() {
		return c.removeAll(remotePath)
	}
	return c.sc.Remove(remotePath)
}

// removeAll 递归删除目录（pkg/sftp 无 RemoveAll）。
// 目录项必须用 Lstat 判定：软链接指向目录时 Stat/ReadDir 会跟随到链接目标，
// 删除动作就打在目标上（用户以为只删了链接），目标里若还有回指链接更会无限递归。
func (c *Client) removeAll(remotePath string) error {
	entries, err := c.sc.ReadDir(remotePath)
	if err != nil {
		return err
	}
	for _, e := range entries {
		full := path.Join(remotePath, e.Name())
		if info, lErr := c.sc.Lstat(full); lErr == nil && info.Mode()&os.ModeSymlink != 0 {
			if err := c.sc.Remove(full); err != nil {
				return err
			}
			continue
		}
		if e.IsDir() {
			if err := c.removeAll(full); err != nil {
				return err
			}
		} else {
			if err := c.sc.Remove(full); err != nil {
				return err
			}
		}
	}
	return c.sc.Remove(remotePath)
}

// Upload 一次流式上传：数据帧按到达顺序追加进 .part，Finish 时校验大小并改名落定。
//
// 不再有 upload_id / chunk_index / offset 参数：一条通道同一时刻只有一个上传，
// WS 的帧序就是文件的字节序，服务端按到达顺序落盘即可。客户端想「写到任意位置」
// 的唯一途径是重开通道，而重开只会从 .part 的实际大小续传——旧的 offset 校验
// 防的那类攻击在新协议下不存在，每片一次的往返校验也就没必要了。
type Upload struct {
	client    *Client
	partPath  string
	finalPath string
	totalSize int64

	mu     sync.Mutex
	file   *sftp.File
	offset int64 // 已落盘字节数，等于 .part 当前大小
}

// BeginUpload 准备上传：建父目录、探测 .part 续传点、打开写句柄。
// 返回的 Upload 从 Offset() 处继续接收数据，客户端据此跳过已上传的部分。
func (c *Client) BeginUpload(remoteDir, filename string, totalSize int64) (*Upload, error) {
	if err := validateFilename(filename); err != nil {
		return nil, err
	}
	finalPath := path.Join(remoteDir, filename)
	partPath := finalPath + ".part"

	if err := c.sc.MkdirAll(remoteDir); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", remoteDir, err)
	}

	// 已有 .part 说明上次上传中断，以其大小作为续传点
	var offset int64
	if info, err := c.sc.Stat(partPath); err == nil {
		offset = info.Size()
		// 脏 .part（上次崩溃残留，或同名文件换小了的旧内容）比本次总长还大时，
		// 续传点永远追不上客户端，只能丢弃从头传，而不是让上传卡死。
		if totalSize > 0 && offset > totalSize {
			if rmErr := c.sc.Remove(partPath); rmErr == nil {
				offset = 0
			}
		}
	}

	f, err := c.sc.OpenFile(partPath, os.O_WRONLY|os.O_CREATE)
	if err != nil {
		return nil, fmt.Errorf("sftp open %s: %w", partPath, err)
	}
	// pkg/sftp 的 File 自带写偏移，落到续传点只需这一次 Seek，之后顺序写即可
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("sftp seek %s: %w", partPath, err)
		}
	}

	return &Upload{
		client:    c,
		partPath:  partPath,
		finalPath: finalPath,
		totalSize: totalSize,
		file:      f,
		offset:    offset,
	}, nil
}

// Offset 返回已落盘字节数。
func (u *Upload) Offset() int64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.offset
}

// Write 追加一帧载荷；写入位置由 file 的内部偏移推进，帧序即文件序。
func (u *Upload) Write(p []byte) (int, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.file == nil {
		return 0, fmt.Errorf("upload already finalized: reopen the channel to resume")
	}
	// 以客户端声明的 totalSize 为上界：否则一直发帧的客户端能把 .part 撑爆远端磁盘。
	if u.totalSize > 0 && u.offset+int64(len(p)) > u.totalSize {
		return 0, fmt.Errorf("chunk exceeds file size: %d + %d > %d", u.offset, len(p), u.totalSize)
	}
	n, err := u.file.Write(p)
	u.offset += int64(n)
	if err != nil {
		return n, fmt.Errorf("write %s: %w", u.partPath, err)
	}
	return n, nil
}

// Finish 关闭句柄、校验大小并把 .part 改名成最终文件，返回最终大小。
// rename 失败时保留 .part 且句柄已关：重试 Finish 只需再 stat + rename，不必重传数据。
func (u *Upload) Finish() (int64, error) {
	if err := u.closeFile(); err != nil {
		return u.Offset(), fmt.Errorf("close %s: %w", u.partPath, err)
	}
	if u.totalSize > 0 {
		info, err := u.client.sc.Stat(u.partPath)
		if err != nil {
			return u.Offset(), fmt.Errorf("stat .part file: %w", err)
		}
		if info.Size() != u.totalSize {
			return info.Size(), fmt.Errorf("upload incomplete: .part size %d != expected %d",
				info.Size(), u.totalSize)
		}
	}
	if err := u.client.sc.Rename(u.partPath, u.finalPath); err != nil {
		return u.Offset(), fmt.Errorf("rename .part to final: %w", err)
	}
	return u.Offset(), nil
}

// Abort 放弃本次上传：只关句柄，保留 .part 供下次同路径续传。
func (u *Upload) Abort() {
	_ = u.closeFile()
}

// closeFile 关闭写句柄（幂等：已关闭则直接成功）。
func (u *Upload) closeFile() error {
	u.mu.Lock()
	f := u.file
	u.file = nil
	u.mu.Unlock()
	if f == nil {
		return nil
	}
	return f.Close()
}

// ReadSeekCloser 远端文件句柄对外的三个动作（标准库没有这个组合接口）。
// 调用方拿到的是接口而不是 *sftp.File：句柄细节不外泄，单测也能喂内存文件。
type ReadSeekCloser interface {
	io.ReadSeeker
	io.Closer
}

// OpenRead 打开远端文件供读取，返回句柄与其元数据（大小、修改时间）。
// 起点由调用方决定——WS 下载 Seek 到续传点后顺序读，
// HTTP 下载则交给 http.ServeContent，由它按 Range 头自行定位并生成区间响应。
func (c *Client) OpenRead(remotePath string) (ReadSeekCloser, os.FileInfo, error) {
	f, err := c.sc.Open(remotePath)
	if err != nil {
		return nil, nil, fmt.Errorf("sftp open %s: %w", remotePath, err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, fmt.Errorf("sftp stat %s: %w", remotePath, err)
	}
	return f, info, nil
}

// Close 关闭 SFTP 会话（不关底层 ssh.Client，由 sshpool 管引用计数）。
// 未完成的 Upload 由其所属通道的持有者负责 Abort，这里只断开会话本身。
func (c *Client) Close() error {
	if c.sc != nil {
		return c.sc.Close()
	}
	return nil
}

// validateFilename 校验上传文件名为纯文件名（不含路径成分）。
// path.Join 会 Clean 掉 "../"，因此不校验时 filename="../../etc/x"
// 可让写入路径越出调用方指定的目标目录。
func validateFilename(filename string) error {
	if filename == "" {
		return fmt.Errorf("invalid filename: empty")
	}
	if strings.ContainsAny(filename, `/\`) || filename == "." || filename == ".." {
		return fmt.Errorf("invalid filename: %q must be a plain file name", filename)
	}
	return nil
}
