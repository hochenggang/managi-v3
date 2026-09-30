package sftp

import (
	"io"
	"os"
	"path/filepath"
	"strings"
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

// TestHome 验证起始目录解析：返回绝对路径且可直接列目录（拿不到 cwd 时回退 "/"）。
func TestHome(t *testing.T) {
	sc, _, _, cleanup := newClient(t)
	defer cleanup()

	home := sc.Home()
	assert.True(t, strings.HasPrefix(home, "/"), "必须是绝对路径，实际 %q", home)

	_, err := sc.List(home)
	require.NoError(t, err)
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

// TestDelete_SymlinkToFile 验证删除文件软链接只删链接本身，目标内容完好。
func TestDelete_SymlinkToFile(t *testing.T) {
	sc, srv, _, cleanup := newClient(t)
	defer cleanup()

	target := filepath.Join(srv.RootDir(), "victim.txt")
	require.NoError(t, os.WriteFile(target, []byte("precious"), 0644))
	link := filepath.Join(srv.RootDir(), "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("平台不支持创建软链接: %v", err)
	}

	require.NoError(t, sc.Delete("/link.txt"))

	_, err := os.Lstat(link)
	assert.True(t, os.IsNotExist(err), "链接本身应被删除")
	info, err := os.Stat(target)
	require.NoError(t, err, "目标文件不得被删除")
	assert.Equal(t, int64(len("precious")), info.Size(), "目标文件不得被截断")
}

// TestDelete_SymlinkedDir 验证目录软链接不会被当成目录递归删除，
// 无论直接删链接，还是删包含该链接的父目录，链接目标都必须存活。
func TestDelete_SymlinkedDir(t *testing.T) {
	sc, srv, _, cleanup := newClient(t)
	defer cleanup()

	root := srv.RootDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "victim"), 0755))
	inner := filepath.Join(root, "victim", "keep.txt")
	require.NoError(t, os.WriteFile(inner, []byte("data"), 0644))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "parent"), 0755))

	link := filepath.Join(root, "parent", "shortcut")
	if err := os.Symlink(filepath.Join(root, "victim"), link); err != nil {
		t.Skipf("平台不支持创建软链接: %v", err)
	}

	// 1) 直接删链接
	require.NoError(t, sc.Delete("/parent/shortcut"))
	_, err := os.Lstat(link)
	assert.True(t, os.IsNotExist(err), "链接本身应被删除")
	assert.FileExists(t, inner, "链接目标目录内容不得被删除")

	// 2) 重建链接后删父目录：遍历目录项时按 Lstat 判软链接，只摘链接
	if err := os.Symlink(filepath.Join(root, "victim"), link); err != nil {
		t.Skipf("平台不支持创建软链接: %v", err)
	}
	require.NoError(t, sc.Delete("/parent"))
	_, err = os.Stat(filepath.Join(root, "parent"))
	assert.True(t, os.IsNotExist(err), "父目录应已删除")
	assert.FileExists(t, inner, "链接目标目录内容不得被删除")
	_, err = os.Stat(filepath.Join(root, "victim"))
	assert.NoError(t, err, "链接目标目录本身应存活")
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

	uploadID, offset, err := sc.UploadInit("/upload", "test.bin", 1024)
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

	uploadID, offset, err := sc.UploadInit("/upload", "test.bin", 4096)
	require.NoError(t, err)
	assert.NotEmpty(t, uploadID)
	assert.Equal(t, int64(1024), offset) // 断点续传核心
}

// TestUploadInit_DiscardsOversizedStalePart 验证脏 .part 被丢弃：
// 残留 .part 比本次要传的文件还大时，续传点永远追不上客户端 offset，
// 每个分片都会 mismatch，上传彻底卡死，只能从头传。
func TestUploadInit_DiscardsOversizedStalePart(t *testing.T) {
	sc, srv, _, cleanup := newClient(t)
	defer cleanup()

	require.NoError(t, os.MkdirAll(filepath.Join(srv.RootDir(), "upload"), 0755))
	partPath := filepath.Join(srv.RootDir(), "upload", "stale.bin.part")
	require.NoError(t, os.WriteFile(partPath, make([]byte, 4096), 0644))

	uploadID, offset, err := sc.UploadInit("/upload", "stale.bin", 1024)
	require.NoError(t, err)
	assert.NotEmpty(t, uploadID)
	assert.Equal(t, int64(0), offset, "stale .part larger than the payload must restart from 0")
	// 脏内容必须真正丢弃（重开后为 0 字节），否则客户端会续到错误数据之上
	info, err := os.Stat(partPath)
	require.NoError(t, err)
	assert.Equal(t, int64(0), info.Size())
}

// TestUploadInit_KeepsUsablePart 验证正常续传点不会被误删（.part 小于总大小）。
func TestUploadInit_KeepsUsablePart(t *testing.T) {
	sc, srv, _, cleanup := newClient(t)
	defer cleanup()

	require.NoError(t, os.MkdirAll(filepath.Join(srv.RootDir(), "upload"), 0755))
	partPath := filepath.Join(srv.RootDir(), "upload", "ok.bin.part")
	require.NoError(t, os.WriteFile(partPath, make([]byte, 512), 0644))

	_, offset, err := sc.UploadInit("/upload", "ok.bin", 1024)
	require.NoError(t, err)
	assert.Equal(t, int64(512), offset)
	require.FileExists(t, partPath)
}

// TestUploadInit_RejectsPathEscapeFilenames 验证含路径成分的文件名被拒绝。
// path.Join 会 Clean 掉 "../"，不校验即可越出目标目录，故必须在入口挡住。
func TestUploadInit_RejectsPathEscapeFilenames(t *testing.T) {
	sc, _, _, cleanup := newClient(t)
	defer cleanup()

	cases := []struct {
		name     string
		filename string
	}{
		{"parent traversal", "../evil.bin"},
		{"nested traversal", "sub/../../evil.bin"},
		{"absolute path", "/etc/passwd"},
		{"windows separator", `..\evil.bin`},
		{"dot", "."},
		{"dotdot", ".."},
		{"empty", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := sc.UploadInit("/upload", tc.filename, 1024)
			assert.Error(t, err, "filename %q must be rejected", tc.filename)
		})
	}
}

// TestUploadInit_TraversalFilenameWritesNowhere 验证恶意文件名不会在目标目录外留下文件。
func TestUploadInit_TraversalFilenameWritesNowhere(t *testing.T) {
	sc, srv, _, cleanup := newClient(t)
	defer cleanup()

	_, _, err := sc.UploadInit("/upload", "../escaped.bin", 1024)
	require.Error(t, err)

	_, statErr := os.Stat(filepath.Join(srv.RootDir(), "escaped.bin"))
	assert.True(t, os.IsNotExist(statErr), "escape attempt must not create a file outside target dir")
}

// TestUploadChunk_WriteAtOffset 验证分片写入到指定 offset。
func TestUploadChunk_WriteAtOffset(t *testing.T) {
	sc, srv, _, cleanup := newClient(t)
	defer cleanup()

	uploadID, _, err := sc.UploadInit("/upload", "chunk.bin", 100)
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

	uploadID, _, err := sc.UploadInit("/upload", "done.bin", 10)
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
	uploadID, _, err := sc.UploadInit("/upload", "retry.bin", 0)
	require.NoError(t, err)
	require.NoError(t, sc.UploadChunk(uploadID, 0, 0, []byte("data")))

	// 关闭 SFTP 连接使 Rename 失败
	require.NoError(t, sc.sc.Close())

	// UploadComplete 应因 Rename 失败而报错
	err = sc.UploadComplete(uploadID)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "rename .part to final")

	// upload 状态应保留（不会返回 unknown upload_id）
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

// TestUploadChunk_OffsetMismatch 验证客户端 offset 与服务端期望不符时分片被拒：
// 否则客户端可把数据写到 .part 的任意位置，得到一份内容错乱却「成功」的文件。
func TestUploadChunk_OffsetMismatch(t *testing.T) {
	sc, srv, _, cleanup := newClient(t)
	defer cleanup()

	uploadID, _, err := sc.UploadInit("/upload", "mismatch.bin", 100)
	require.NoError(t, err)

	err = sc.UploadChunk(uploadID, 0, 9, []byte("AAAA"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "chunk offset mismatch")

	// 未写入任何内容
	info, err := os.Stat(filepath.Join(srv.RootDir(), "upload", "mismatch.bin.part"))
	require.NoError(t, err)
	assert.Zero(t, info.Size())
}

// TestUploadChunk_RejectsAfterFinalize 验证句柄已关闭后写入分片返回错误而非 panic：
// UploadComplete 的 rename 失败路径会保留状态但把 st.file 置 nil（供重试 Complete）。
func TestUploadChunk_RejectsAfterFinalize(t *testing.T) {
	sc, _, _, cleanup := newClient(t)
	defer cleanup()

	// totalSize=0 跳过 stat 校验，直连 rename
	uploadID, _, err := sc.UploadInit("/upload", "finalized.bin", 0)
	require.NoError(t, err)
	require.NoError(t, sc.UploadChunk(uploadID, 0, 0, []byte("data")))

	// 关闭 SFTP 连接使 rename 失败，走「保留状态 + 句柄置 nil」分支
	require.NoError(t, sc.sc.Close())
	require.Error(t, sc.UploadComplete(uploadID))

	err = sc.UploadChunk(uploadID, 1, 4, []byte("more"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already finalized")
}

// TestUploadChunk_RejectsOversized 验证超出声明 totalSize 的分片被拒：
// 否则一直发分片的客户端能把 .part 撑爆远端磁盘。
func TestUploadChunk_RejectsOversized(t *testing.T) {
	sc, _, _, cleanup := newClient(t)
	defer cleanup()

	uploadID, _, err := sc.UploadInit("/upload", "oversize.bin", 4)
	require.NoError(t, err)
	require.NoError(t, sc.UploadChunk(uploadID, 0, 0, []byte("data")))

	err = sc.UploadChunk(uploadID, 1, 4, []byte("x"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "chunk exceeds file size")
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
