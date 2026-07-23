package sftp

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"managi/internal/model"
	"managi/internal/testutil"
)

// dialMock 直连 mock SSH 服务器获取 *ssh.Client（测试 sftp 包用）。
func dialMock(t *testing.T, srv *testutil.Server) (*ssh.Client, model.Node) {
	t.Helper()
	node := testutil.TestNode(srv.Host(), srv.Port())
	cfg := &ssh.ClientConfig{
		User:            "test",
		Auth:            []ssh.AuthMethod{ssh.Password(srv.Password())},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}
	client, err := ssh.Dial("tcp", srv.Addr(), cfg)
	require.NoError(t, err)
	return client, node
}

// newClient 创建 SFTP 客户端连接到 mock server。
func newClient(t *testing.T) (*Client, *testutil.Server, *ssh.Client, func()) {
	t.Helper()
	srv := testutil.Start(t)
	sshc, node := dialMock(t, srv)
	sc, err := New(node, sshc)
	require.NoError(t, err)
	cleanup := func() {
		_ = sc.Close()
		_ = sshc.Close()
		srv.Close()
	}
	return sc, srv, sshc, cleanup
}

// TestList 验证目录列表。
func TestList(t *testing.T) {
	sc, srv, _, cleanup := newClient(t)
	defer cleanup()

	// 在 rootDir 预建文件
	require.NoError(t, os.WriteFile(filepath.Join(srv.RootDir(), "file1.txt"), []byte("hello"), 0644))
	require.NoError(t, os.MkdirAll(filepath.Join(srv.RootDir(), "subdir"), 0755))

	items, err := sc.List("/")
	require.NoError(t, err)
	assert.Len(t, items, 2)

	names := []string{items[0].Filename, items[1].Filename}
	assert.Contains(t, names, "file1.txt")
	assert.Contains(t, names, "subdir")

	// 验证 FileItem 字段
	for _, item := range items {
		if item.Filename == "file1.txt" {
			assert.False(t, item.IsDir)
			assert.Equal(t, int64(5), item.Size)
		}
		if item.Filename == "subdir" {
			assert.True(t, item.IsDir)
		}
	}
}

// TestMkdir 验证递归创建目录。
func TestMkdir(t *testing.T) {
	sc, srv, _, cleanup := newClient(t)
	defer cleanup()

	err := sc.Mkdir("/a/b/c")
	require.NoError(t, err)

	// 验证本地存在
	info, err := os.Stat(filepath.Join(srv.RootDir(), "a", "b", "c"))
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

// TestDelete_File 验证删除文件。
func TestDelete_File(t *testing.T) {
	sc, srv, _, cleanup := newClient(t)
	defer cleanup()

	target := filepath.Join(srv.RootDir(), "to_delete.txt")
	require.NoError(t, os.WriteFile(target, []byte("data"), 0644))

	err := sc.Delete("/to_delete.txt")
	require.NoError(t, err)

	_, err = os.Stat(target)
	assert.True(t, os.IsNotExist(err))
}

// TestDelete_Dir 验证递归删除目录。
func TestDelete_Dir(t *testing.T) {
	sc, srv, _, cleanup := newClient(t)
	defer cleanup()

	dirPath := filepath.Join(srv.RootDir(), "dir_to_delete")
	require.NoError(t, os.MkdirAll(dirPath, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dirPath, "inner.txt"), []byte("x"), 0644))

	err := sc.Delete("/dir_to_delete")
	require.NoError(t, err)

	_, err = os.Stat(dirPath)
	assert.True(t, os.IsNotExist(err))
}

// TestRename 验证重命名。
func TestRename(t *testing.T) {
	sc, srv, _, cleanup := newClient(t)
	defer cleanup()

	oldPath := filepath.Join(srv.RootDir(), "old.txt")
	require.NoError(t, os.WriteFile(oldPath, []byte("content"), 0644))

	err := sc.Rename("/old.txt", "/new.txt")
	require.NoError(t, err)

	_, err = os.Stat(oldPath)
	assert.True(t, os.IsNotExist(err))

	_, err = os.Stat(filepath.Join(srv.RootDir(), "new.txt"))
	assert.NoError(t, err)
}

// TestUploadInit_Fresh 验证首次上传 offset=0。
func TestUploadInit_Fresh(t *testing.T) {
	sc, _, _, cleanup := newClient(t)
	defer cleanup()

	uploadID, offset, err := sc.UploadInit("/upload", "test.bin", 1024, 512)
	require.NoError(t, err)
	assert.NotEmpty(t, uploadID)
	assert.Equal(t, int64(0), offset)
}

// TestUploadInit_Resume 验证断点续传：已有 .part 文件返回其大小作为 offset。
func TestUploadInit_Resume(t *testing.T) {
	sc, srv, _, cleanup := newClient(t)
	defer cleanup()

	// 预建 .part 文件，写入 1024 字节
	require.NoError(t, os.MkdirAll(filepath.Join(srv.RootDir(), "upload"), 0755))
	partPath := filepath.Join(srv.RootDir(), "upload", "test.bin.part")
	require.NoError(t, os.WriteFile(partPath, make([]byte, 1024), 0644))

	uploadID, offset, err := sc.UploadInit("/upload", "test.bin", 4096, 512)
	require.NoError(t, err)
	assert.NotEmpty(t, uploadID)
	assert.Equal(t, int64(1024), offset) // 断点续传核心
}

// TestUploadChunk_WriteAtOffset 验证分片写入到指定 offset。
func TestUploadChunk_WriteAtOffset(t *testing.T) {
	sc, srv, _, cleanup := newClient(t)
	defer cleanup()

	uploadID, _, err := sc.UploadInit("/upload", "chunk.bin", 100, 50)
	require.NoError(t, err)

	// 写入第一块到 offset 0
	err = sc.UploadChunk(uploadID, 0, 0, []byte("AAAA"))
	require.NoError(t, err)

	// 写入第二块到 offset 4
	err = sc.UploadChunk(uploadID, 1, 4, []byte("BBBB"))
	require.NoError(t, err)

	// 验证 .part 文件内容
	content, err := os.ReadFile(filepath.Join(srv.RootDir(), "upload", "chunk.bin.part"))
	require.NoError(t, err)
	assert.Equal(t, "AAAABBBB", string(content))
}

// TestUploadComplete_Rename 验证上传完成：.part → final。
func TestUploadComplete_Rename(t *testing.T) {
	sc, srv, _, cleanup := newClient(t)
	defer cleanup()

	uploadID, _, err := sc.UploadInit("/upload", "done.bin", 10, 4)
	require.NoError(t, err)

	require.NoError(t, sc.UploadChunk(uploadID, 0, 0, []byte(" Completed")))
	require.NoError(t, sc.UploadComplete(uploadID))

	// .part 应消失
	_, err = os.Stat(filepath.Join(srv.RootDir(), "upload", "done.bin.part"))
	assert.True(t, os.IsNotExist(err))

	// final 文件应存在
	content, err := os.ReadFile(filepath.Join(srv.RootDir(), "upload", "done.bin"))
	require.NoError(t, err)
	assert.Equal(t, " Completed", string(content))
}

// TestUploadComplete_UnknownID 验证未知 upload_id 返回错误。
func TestUploadComplete_UnknownID(t *testing.T) {
	sc, _, _, cleanup := newClient(t)
	defer cleanup()

	err := sc.UploadComplete("nonexistent-id")
	assert.Error(t, err)
}

// TestUploadComplete_RenameFailure_PreservesState 验证 B34 修复：
// Rename 失败时 upload 状态被保留，客户端可重试 UploadComplete。
func TestUploadComplete_RenameFailure_PreservesState(t *testing.T) {
	sc, srv, sshc, cleanup := newClient(t)
	defer cleanup()

	// totalSize=0 跳过 stat 校验，直接到 Rename 步骤
	uploadID, _, err := sc.UploadInit("/upload", "retry.bin", 0, 4)
	require.NoError(t, err)
	require.NoError(t, sc.UploadChunk(uploadID, 0, 0, []byte("data")))

	// 关闭 SFTP 连接使 Rename 失败
	require.NoError(t, sc.sc.Close())

	// UploadComplete 应因 Rename 失败而报错
	err = sc.UploadComplete(uploadID)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "rename .part to final")

	// 修复 B34：upload 状态应保留（不会返回 unknown upload_id）
	sc.mu.Lock()
	_, exists := sc.uploads[uploadID]
	sc.mu.Unlock()
	assert.True(t, exists, "upload state should be preserved after Rename failure")

	// 用同一 SSH 连接创建新 SFTP 客户端，重试 UploadComplete
	newSc, err := sftp.NewClient(sshc)
	require.NoError(t, err)
	sc.sc = newSc

	// 重试应成功（.part 文件已完整，只需 rename）
	err = sc.UploadComplete(uploadID)
	require.NoError(t, err)

	// 验证 final 文件存在且内容正确
	content, err := os.ReadFile(filepath.Join(srv.RootDir(), "upload", "retry.bin"))
	require.NoError(t, err)
	assert.Equal(t, "data", string(content))
}

// TestUploadChunk_UnknownID 验证未知 upload_id 分片写入返回错误。
func TestUploadChunk_UnknownID(t *testing.T) {
	sc, _, _, cleanup := newClient(t)
	defer cleanup()

	err := sc.UploadChunk("nonexistent-id", 0, 0, []byte("data"))
	assert.Error(t, err)
}

// TestDownloadStream_Full 验证完整下载（offset=0）。
func TestDownloadStream_Full(t *testing.T) {
	sc, srv, _, cleanup := newClient(t)
	defer cleanup()

	// 预建远程文件
	content := []byte("0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	require.NoError(t, os.WriteFile(filepath.Join(srv.RootDir(), "download.bin"), content, 0644))

	reader, total, err := sc.DownloadStream("/download.bin", 0)
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()

	assert.Equal(t, int64(len(content)), total)

	got, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, content, got)
}

// TestDownloadStream_Range 验证 Range 下载（offset>0）。
func TestDownloadStream_Range(t *testing.T) {
	sc, srv, _, cleanup := newClient(t)
	defer cleanup()

	content := []byte("0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	require.NoError(t, os.WriteFile(filepath.Join(srv.RootDir(), "range.bin"), content, 0644))

	offset := int64(10)
	reader, total, err := sc.DownloadStream("/range.bin", offset)
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()

	assert.Equal(t, int64(len(content)), total) // total 是文件总大小

	got, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, content[offset:], got) // 从 offset 开始的内容
}

// TestDownloadStream_NotFound 验证文件不存在返回错误。
func TestDownloadStream_NotFound(t *testing.T) {
	sc, _, _, cleanup := newClient(t)
	defer cleanup()

	_, _, err := sc.DownloadStream("/nonexistent.bin", 0)
	assert.Error(t, err)
}

// TestMakeUploadID 验证 upload ID 生成格式与唯一性。
func TestMakeUploadID(t *testing.T) {
	id1 := makeUploadID("/path/a", "key1")
	id2 := makeUploadID("/path/b", "key2")

	assert.NotEmpty(t, id1)
	assert.Len(t, id1, 16)
	assert.Regexp(t, `^[0-9a-f]{16}$`, id1) // hex 格式
	assert.NotEqual(t, id1, id2)            // 不同输入产生不同 ID

	// 唯一性：同输入连续 100 次不应碰撞（时间戳保证）
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id := makeUploadID("/path/a", "key1")
		assert.False(t, seen[id], "collision at iteration %d", i)
		seen[id] = true
	}
}
