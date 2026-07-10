// Package sftp 封装 SFTP 文件操作。
// 对应 v2 的 sftp_client.py，基于 github.com/pkg/sftp。
// 设计见 ../design-v3.md §4.1 与 §6.4 §6.5。
package sftp

import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"sync"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"managi/internal/model"
)

// uploadState 进程内 upload 会话状态（断点续传）。
type uploadState struct {
	partPath  string // .part 临时文件远程路径
	finalPath string // 最终文件远程路径
	totalSize int64  // 期望的最终文件大小，UploadComplete 时校验
	offset    int64
	file      *sftp.File // 保持打开的 .part 文件句柄，避免每分片重复 open/close
}

// Client 封装一次 SFTP 会话。
type Client struct {
	node model.Node
	sshc *ssh.Client
	sc   *sftp.Client

	mu      sync.Mutex
	uploads map[string]*uploadState
}

// New 创建 SFTP 客户端（复用 sshpool 连接）。
func New(node model.Node, sshc *ssh.Client) (*Client, error) {
	sc, err := sftp.NewClient(sshc)
	if err != nil {
		return nil, fmt.Errorf("sftp new client: %w", err)
	}
	return &Client{
		node:    node,
		sshc:    sshc,
		sc:      sc,
		uploads: make(map[string]*uploadState),
	}, nil
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
func (c *Client) Delete(remotePath string) error {
	info, err := c.sc.Stat(remotePath)
	if err != nil {
		return fmt.Errorf("sftp stat %s: %w", remotePath, err)
	}
	if info.IsDir() {
		return c.removeAll(remotePath)
	}
	return c.sc.Remove(remotePath)
}

// removeAll 递归删除目录（pkg/sftp 无 RemoveAll）。
func (c *Client) removeAll(remotePath string) error {
	entries, err := c.sc.ReadDir(remotePath)
	if err != nil {
		return err
	}
	for _, e := range entries {
		full := path.Join(remotePath, e.Name())
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

// Rename 重命名，目标父目录自动创建。
func (c *Client) Rename(oldPath, newPath string) error {
	if dir := path.Dir(newPath); dir != "" && dir != "." {
		_ = c.sc.MkdirAll(dir)
	}
	return c.sc.Rename(oldPath, newPath)
}

// UploadInit 初始化断点续传上传。
// 返回 uploadID 与当前 offset（已有 .part 文件则续传，否则 0）。
// 父目录不存在时自动递归创建（对应 v2 _ensure_remote_directory_exists）。
func (c *Client) UploadInit(remotePath, filename string, totalSize int64, chunkSize int) (uploadID string, offset int64, err error) {
	finalPath := path.Join(remotePath, filename)
	partPath := finalPath + ".part"

	// 确保父目录存在
	if mkErr := c.sc.MkdirAll(remotePath); mkErr != nil {
		return "", 0, fmt.Errorf("mkdir %s: %w", remotePath, mkErr)
	}

	// 查询已有 .part 文件大小作为续传 offset
	if info, statErr := c.sc.Stat(partPath); statErr == nil {
		offset = info.Size()
	}

	// 以写模式打开 .part 文件并保持句柄，减少每分片 open/close 的往返开销
	f, err := c.sc.OpenFile(partPath, os.O_WRONLY|os.O_CREATE)
	if err != nil {
		return "", 0, fmt.Errorf("sftp open %s: %w", partPath, err)
	}

	uploadID = makeUploadID(finalPath, c.node.ConnectionKey())

	c.mu.Lock()
	c.uploads[uploadID] = &uploadState{
		partPath:  partPath,
		finalPath: finalPath,
		totalSize: totalSize,
		offset:    offset,
		file:      f,
	}
	c.mu.Unlock()

	return uploadID, offset, nil
}

// UploadChunk 写入一个分片到指定 offset。
// 修复 B5：全程持锁访问 st.file，避免与 closeUpload/UploadComplete 竞态写已关闭句柄。
// H6：校验客户端传入的 offset 与服务端维护的 st.offset 一致，防止恶意客户端写任意位置。
func (c *Client) UploadChunk(uploadID string, chunkIndex int, offset int64, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	st, ok := c.uploads[uploadID]
	if !ok {
		return fmt.Errorf("unknown upload_id: %s", uploadID)
	}

	// H6：校验客户端 offset 与服务端期望一致，忽略客户端值做 Seek
	if offset != st.offset {
		return fmt.Errorf("chunk offset mismatch: got %d, expected %d", offset, st.offset)
	}
	if _, err := st.file.Seek(st.offset, io.SeekStart); err != nil {
		c.closeUploadLocked(uploadID)
		return fmt.Errorf("seek: %w", err)
	}
	if _, err := st.file.Write(data); err != nil {
		c.closeUploadLocked(uploadID)
		return fmt.Errorf("write: %w", err)
	}

	st.offset = st.offset + int64(len(data))
	return nil
}

// closeUploadLocked 关闭指定 upload_id 的文件句柄并清理状态。调用方需持 c.mu。
func (c *Client) closeUploadLocked(uploadID string) {
	if st, ok := c.uploads[uploadID]; ok {
		delete(c.uploads, uploadID)
		if st.file != nil {
			_ = st.file.Close()
		}
	}
}

// closeUpload 关闭指定 upload_id 的文件句柄并清理状态（加锁包装，保留供外部调用方使用）。
//
//nolint:unused // 保留为公开 API，供未来外部调用方手动取消上传会话
func (c *Client) closeUpload(uploadID string) {
	c.mu.Lock()
	c.closeUploadLocked(uploadID)
	c.mu.Unlock()
}

// UploadComplete 完成上传：.part → final。
func (c *Client) UploadComplete(uploadID string) error {
	c.mu.Lock()
	st, ok := c.uploads[uploadID]
	if ok {
		delete(c.uploads, uploadID)
	}
	c.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown upload_id: %s", uploadID)
	}

	// 关闭句柄后再重命名，避免远程文件被占用
	if st.file != nil {
		_ = st.file.Close()
	}

	// C5：校验 .part 文件大小 == totalSize，不符则报错保留 .part 允许续传
	if st.totalSize > 0 {
		info, statErr := c.sc.Stat(st.partPath)
		if statErr != nil {
			return fmt.Errorf("stat .part file: %w", statErr)
		}
		if info.Size() != st.totalSize {
			return fmt.Errorf("upload incomplete: .part size %d != expected %d", info.Size(), st.totalSize)
		}
	}

	if err := c.sc.Rename(st.partPath, st.finalPath); err != nil {
		return fmt.Errorf("rename .part to final: %w", err)
	}
	return nil
}

// DownloadStream 打开远程文件从 offset 起的读取流，返回 reader 与文件总大小（支持 HTTP Range）。
func (c *Client) DownloadStream(remotePath string, offset int64) (io.ReadCloser, int64, error) {
	info, err := c.sc.Stat(remotePath)
	if err != nil {
		return nil, 0, fmt.Errorf("sftp stat %s: %w", remotePath, err)
	}
	total := info.Size()

	f, err := c.sc.Open(remotePath)
	if err != nil {
		return nil, 0, fmt.Errorf("sftp open %s: %w", remotePath, err)
	}
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			_ = f.Close()
			return nil, 0, fmt.Errorf("seek: %w", err)
		}
	}
	return f, total, nil
}

// Close 关闭 SFTP 客户端（不关底层 ssh.Client，由 sshpool 管）。
func (c *Client) Close() error {
	c.mu.Lock()
	for _, st := range c.uploads {
		if st.file != nil {
			_ = st.file.Close()
		}
	}
	c.uploads = make(map[string]*uploadState) // 清理未完成的 upload 会话，避免泄漏
	c.mu.Unlock()
	if c.sc != nil {
		return c.sc.Close()
	}
	return nil
}

// makeUploadID 生成 upload 会话 ID（基于路径 + 节点 + 随机数的哈希）。
// 修正：原用 time.Now() 在 Windows 等时钟分辨率粗糙平台会产生碰撞，
// 改用 crypto/rand 保证跨平台唯一性。
func makeUploadID(finalPath, connKey string) string {
	h := sha1.New()
	h.Write([]byte(finalPath))
	h.Write([]byte(connKey))
	rb := make([]byte, 16)
	_, _ = rand.Read(rb)
	h.Write(rb)
	return hex.EncodeToString(h.Sum(nil))[:16]
}
