package sftp

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"managi/internal/testutil"
)

// newClient 创建 SFTP 客户端连接到 mock server。
// 只暴露 Client 与 server：底层 SSH 连接由 cleanup 负责关闭，测试无需触碰。
func newClient(t *testing.T) (*Client, *testutil.Server, func()) {
	t.Helper()
	srv := testutil.Start(t)
	sshc := srv.Dial(t)
	sc, err := New(sshc)
	require.NoError(t, err)
	cleanup := func() {
		_ = sc.Close()
		_ = sshc.Close()
		srv.Close()
	}
	return sc, srv, cleanup
}

// TestList 验证目录列表。
func TestList(t *testing.T) {
	sc, srv, cleanup := newClient(t)
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
	sc, _, cleanup := newClient(t)
	defer cleanup()

	home := sc.Home()
	assert.True(t, strings.HasPrefix(home, "/"), "必须是绝对路径，实际 %q", home)

	_, err := sc.List(home)
	require.NoError(t, err)
}

// TestMkdir 验证递归创建目录。
func TestMkdir(t *testing.T) {
	sc, srv, cleanup := newClient(t)
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
	sc, srv, cleanup := newClient(t)
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
	sc, srv, cleanup := newClient(t)
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
	sc, srv, cleanup := newClient(t)
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
	sc, srv, cleanup := newClient(t)
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

// TestUpload_BeginFresh 验证首次上传从 0 开始。
func TestUpload_BeginFresh(t *testing.T) {
	sc, _, cleanup := newClient(t)
	defer cleanup()

	u, err := sc.BeginUpload("/upload", "test.bin", 1024)
	require.NoError(t, err)
	assert.Equal(t, int64(0), u.Offset())
}

// TestUpload_BeginResume 验证断点续传：已有 .part 时从其大小续传，且新数据接在尾部。
func TestUpload_BeginResume(t *testing.T) {
	sc, srv, cleanup := newClient(t)
	defer cleanup()

	require.NoError(t, os.MkdirAll(filepath.Join(srv.RootDir(), "upload"), 0755))
	partPath := filepath.Join(srv.RootDir(), "upload", "test.bin.part")
	require.NoError(t, os.WriteFile(partPath, []byte("AAAA"), 0644))

	u, err := sc.BeginUpload("/upload", "test.bin", 8)
	require.NoError(t, err)
	assert.Equal(t, int64(4), u.Offset())

	// 续传点之后的写入必须落在偏移 4，而不是覆盖已有内容
	_, err = u.Write([]byte("BBBB"))
	require.NoError(t, err)
	size, err := u.Finish()
	require.NoError(t, err)
	assert.Equal(t, int64(8), size)

	content, err := os.ReadFile(filepath.Join(srv.RootDir(), "upload", "test.bin"))
	require.NoError(t, err)
	assert.Equal(t, "AAAABBBB", string(content))
}

// TestUpload_BeginDiscardsOversizedStalePart 验证脏 .part 被丢弃：
// 残留 .part 比本次要传的文件还大时，续传点永远追不上，只能从头传。
func TestUpload_BeginDiscardsOversizedStalePart(t *testing.T) {
	sc, srv, cleanup := newClient(t)
	defer cleanup()

	require.NoError(t, os.MkdirAll(filepath.Join(srv.RootDir(), "upload"), 0755))
	partPath := filepath.Join(srv.RootDir(), "upload", "stale.bin.part")
	require.NoError(t, os.WriteFile(partPath, make([]byte, 4096), 0644))

	u, err := sc.BeginUpload("/upload", "stale.bin", 1024)
	require.NoError(t, err)
	assert.Equal(t, int64(0), u.Offset(), "stale .part larger than the payload must restart from 0")
	// 脏内容必须真正丢弃（重开后为 0 字节），否则客户端会续到错误数据之上
	info, err := os.Stat(partPath)
	require.NoError(t, err)
	assert.Equal(t, int64(0), info.Size())
}

// TestUpload_BeginKeepsUsablePart 验证正常续传点不会被误删（.part 小于总大小）。
func TestUpload_BeginKeepsUsablePart(t *testing.T) {
	sc, srv, cleanup := newClient(t)
	defer cleanup()

	require.NoError(t, os.MkdirAll(filepath.Join(srv.RootDir(), "upload"), 0755))
	partPath := filepath.Join(srv.RootDir(), "upload", "ok.bin.part")
	require.NoError(t, os.WriteFile(partPath, make([]byte, 512), 0644))

	u, err := sc.BeginUpload("/upload", "ok.bin", 1024)
	require.NoError(t, err)
	assert.Equal(t, int64(512), u.Offset())
	require.FileExists(t, partPath)
}

// TestUpload_BeginRejectsPathEscapeFilenames 验证含路径成分的文件名被拒绝。
// path.Join 会 Clean 掉 "../"，不校验即可越出目标目录，故必须在入口挡住。
func TestUpload_BeginRejectsPathEscapeFilenames(t *testing.T) {
	sc, _, cleanup := newClient(t)
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
			_, err := sc.BeginUpload("/upload", tc.filename, 1024)
			assert.Error(t, err, "filename %q must be rejected", tc.filename)
		})
	}
}

// TestUpload_BeginTraversalFilenameWritesNowhere 验证恶意文件名不会在目标目录外留下文件。
func TestUpload_BeginTraversalFilenameWritesNowhere(t *testing.T) {
	sc, srv, cleanup := newClient(t)
	defer cleanup()

	_, err := sc.BeginUpload("/upload", "../escaped.bin", 1024)
	require.Error(t, err)

	_, statErr := os.Stat(filepath.Join(srv.RootDir(), "escaped.bin"))
	assert.True(t, os.IsNotExist(statErr), "escape attempt must not create a file outside target dir")
}

// TestUpload_WriteAppendsInFrameOrder 验证帧序即文件序：连续写入按到达顺序落盘。
func TestUpload_WriteAppendsInFrameOrder(t *testing.T) {
	sc, srv, cleanup := newClient(t)
	defer cleanup()

	u, err := sc.BeginUpload("/upload", "chunk.bin", 100)
	require.NoError(t, err)

	// 分片边界与 pkg/sftp 的 maxPacket 无关：一次写一帧即可
	for _, part := range []string{"AAAA", "BBBB", "CCCC"} {
		n, err := u.Write([]byte(part))
		require.NoError(t, err)
		assert.Equal(t, len(part), n)
	}
	assert.Equal(t, int64(12), u.Offset())

	content, err := os.ReadFile(filepath.Join(srv.RootDir(), "upload", "chunk.bin.part"))
	require.NoError(t, err)
	assert.Equal(t, "AAAABBBBCCCC", string(content))
}

// TestUpload_FinishRenames 验证上传完成：.part → final，临时文件消失。
func TestUpload_FinishRenames(t *testing.T) {
	sc, srv, cleanup := newClient(t)
	defer cleanup()

	u, err := sc.BeginUpload("/upload", "done.bin", 10)
	require.NoError(t, err)
	_, err = u.Write([]byte(" Completed"))
	require.NoError(t, err)
	size, err := u.Finish()
	require.NoError(t, err)
	assert.Equal(t, int64(10), size)

	_, err = os.Stat(filepath.Join(srv.RootDir(), "upload", "done.bin.part"))
	assert.True(t, os.IsNotExist(err))

	content, err := os.ReadFile(filepath.Join(srv.RootDir(), "upload", "done.bin"))
	require.NoError(t, err)
	assert.Equal(t, " Completed", string(content))
}

// TestUpload_FinishRejectsShortPart 验证大小不足时不落定最终文件：
// 否则客户端会看到一个「上传成功」但内容被截断的文件。
func TestUpload_FinishRejectsShortPart(t *testing.T) {
	sc, srv, cleanup := newClient(t)
	defer cleanup()

	u, err := sc.BeginUpload("/upload", "short.bin", 100)
	require.NoError(t, err)
	_, err = u.Write([]byte("only a part"))
	require.NoError(t, err)

	_, err = u.Finish()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "upload incomplete")

	_, err = os.Stat(filepath.Join(srv.RootDir(), "upload", "short.bin"))
	assert.True(t, os.IsNotExist(err), "落定必须失败")
	require.FileExists(t, filepath.Join(srv.RootDir(), "upload", "short.bin.part"), ".part 必须保留供续传")
}

// TestUpload_FinishRetryAfterRenameFailure 验证 rename 失败后可重试：
// 句柄已关、.part 完整，重试只需再 stat + rename，不必重传数据。
func TestUpload_FinishRetryAfterRenameFailure(t *testing.T) {
	sc, srv, cleanup := newClient(t)
	defer cleanup()

	u, err := sc.BeginUpload("/upload", "retry.bin", 4)
	require.NoError(t, err)
	_, err = u.Write([]byte("data"))
	require.NoError(t, err)

	// 最终路径上已有同名目录 → rename 必然失败
	finalPath := filepath.Join(srv.RootDir(), "upload", "retry.bin")
	require.NoError(t, os.MkdirAll(finalPath, 0755))

	_, err = u.Finish()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rename .part to final")

	require.NoError(t, os.Remove(finalPath))
	size, err := u.Finish()
	require.NoError(t, err)
	assert.Equal(t, int64(4), size)

	content, err := os.ReadFile(finalPath)
	require.NoError(t, err)
	assert.Equal(t, "data", string(content))
}

// TestUpload_WriteRejectsAfterFinalize 验证落定/放弃之后再写返回错误而不是 panic。
func TestUpload_WriteRejectsAfterFinalize(t *testing.T) {
	sc, _, cleanup := newClient(t)
	defer cleanup()

	u, err := sc.BeginUpload("/upload", "finalized.bin", 4)
	require.NoError(t, err)
	_, err = u.Write([]byte("data"))
	require.NoError(t, err)
	_, err = u.Finish()
	require.NoError(t, err)

	_, err = u.Write([]byte("more"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already finalized")
}

// TestUpload_WriteRejectsOversized 验证超出声明 totalSize 的数据被拒：
// 否则一直发帧的客户端能把 .part 撑爆远端磁盘。
func TestUpload_WriteRejectsOversized(t *testing.T) {
	sc, _, cleanup := newClient(t)
	defer cleanup()

	u, err := sc.BeginUpload("/upload", "oversize.bin", 4)
	require.NoError(t, err)
	_, err = u.Write([]byte("data"))
	require.NoError(t, err)

	_, err = u.Write([]byte("x"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "chunk exceeds file size")
}

// TestUpload_AbortKeepsPartForResume 验证放弃只关句柄、保留 .part：
// 断线重连后同路径上传仍从已落盘处续传。
func TestUpload_AbortKeepsPartForResume(t *testing.T) {
	sc, srv, cleanup := newClient(t)
	defer cleanup()

	// 完整内容为 "halftime data"（13 字节），第一次只传到第 8 字节就放弃
	u, err := sc.BeginUpload("/upload", "abort.bin", 13)
	require.NoError(t, err)
	_, err = u.Write([]byte("halftime"))
	require.NoError(t, err)
	u.Abort()

	_, err = u.Write([]byte("more"))
	require.Error(t, err, "Abort 之后不得继续写")

	reopened, err := sc.BeginUpload("/upload", "abort.bin", 13)
	require.NoError(t, err)
	assert.Equal(t, int64(len("halftime")), reopened.Offset())

	_, err = reopened.Write([]byte(" data"))
	require.NoError(t, err)
	_, err = reopened.Finish()
	require.NoError(t, err)

	content, err := os.ReadFile(filepath.Join(srv.RootDir(), "upload", "abort.bin"))
	require.NoError(t, err)
	assert.Equal(t, "halftime data", string(content))
}

// TestOpenRead_Full 验证打开即从文件头读，元数据给出总大小。
func TestOpenRead_Full(t *testing.T) {
	sc, srv, cleanup := newClient(t)
	defer cleanup()

	// 预建远程文件
	content := []byte("0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	require.NoError(t, os.WriteFile(filepath.Join(srv.RootDir(), "download.bin"), content, 0644))

	reader, info, err := sc.OpenRead("/download.bin")
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()

	assert.Equal(t, int64(len(content)), info.Size())

	got, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, content, got)
}

// TestOpenRead_Seek 验证句柄可定位到任意起点（WS 续传与 ServeContent 都依赖它）。
func TestOpenRead_Seek(t *testing.T) {
	sc, srv, cleanup := newClient(t)
	defer cleanup()

	content := []byte("0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	require.NoError(t, os.WriteFile(filepath.Join(srv.RootDir(), "range.bin"), content, 0644))

	const offset = int64(10)
	reader, _, err := sc.OpenRead("/range.bin")
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()

	at, err := reader.Seek(offset, io.SeekStart)
	require.NoError(t, err)
	assert.Equal(t, offset, at)

	got, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, content[offset:], got)
}

// TestOpenRead_NotFound 验证文件不存在返回错误，且不留悬挂句柄。
func TestOpenRead_NotFound(t *testing.T) {
	sc, _, cleanup := newClient(t)
	defer cleanup()

	_, _, err := sc.OpenRead("/nonexistent.bin")
	assert.Error(t, err)
}
