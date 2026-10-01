package handler

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"managi/internal/testutil"
	"managi/internal/wire"
)

// ===== 帧大小：入站上限与出站切片都直接来自配置 =====

// TestFrameSizesFromConfig 验证「客户端按此切片一定不会被读上限掐断」这条跨文件不变量：
// 连接级 read limit 必须容得下一整帧入站载荷（载荷 + 帧头），否则前端按 chunk_size
// 发出的合法帧会被 gorilla 以 1009 关掉整条连接。
func TestFrameSizesFromConfig(t *testing.T) {
	cfg := testutil.TestConfig()
	h := &hub{cfg: cfg}

	assert.Equal(t, cfg.ChunkSize, h.inFrameSize())
	assert.Equal(t, cfg.DownloadChunkSize, h.outFrameSize())
	assert.GreaterOrEqual(t, wsReadLimit(cfg), int64(wire.HeaderLen+h.inFrameSize()))
}

// ===== splitBytes：回放与下载共同的切片组合子 =====

// TestSplitBytes 验证回放/下载切片的硬要求：不丢字节、必然终止。
// 切点允许落在多字节字符中间——前端用流式解码器拼帧，服务端无需为此保留一条兜底路径。
func TestSplitBytes(t *testing.T) {
	// 1) 中文 + emoji 长文本：chunk=7 与 3/4 字节字符不整除
	data := []byte(strings.Repeat("终端输出😀", 2000))
	parts := splitBytes(data, 7)
	assert.Equal(t, data, bytes.Join(parts, nil))
	for _, p := range parts {
		assert.NotEmpty(t, p, "切片不应为空")
		assert.LessOrEqual(t, len(p), 7)
	}

	// 2) 任意二进制（终端里合法存在）：不丢字节即可
	raw := bytes.Repeat([]byte{0x80, 0x81, 0x01, 0x02}, 100)
	assert.Equal(t, raw, bytes.Join(splitBytes(raw, 7), nil))

	// 3) 边界：短于 chunk / chunk<=0 都只出一块；空输入不出块
	//    （空快照若也发一帧，回放就混进了一条零长度载荷）
	assert.Equal(t, [][]byte{[]byte("abc")}, splitBytes([]byte("abc"), 8))
	assert.Equal(t, [][]byte{[]byte("abc")}, splitBytes([]byte("abc"), 0))
	assert.Empty(t, splitBytes(nil, 8))
}

// ===== pump：终端回放与文件下载共用的搬运组合子 =====

// captureWriter 记下写出的每一帧；err 非空则模拟「连接已断，写不出去了」。
type captureWriter struct {
	mu     sync.Mutex
	frames []wire.Frame
	err    error
}

func (w *captureWriter) writeFrame(f wire.Frame) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return w.err
	}
	// pump 复用同一个 buf 装载荷，收集时必须另存一份
	f.Payload = append([]byte(nil), f.Payload...)
	w.frames = append(w.frames, f)
	return nil
}

func (w *captureWriter) payload() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	var got bytes.Buffer
	for _, f := range w.frames {
		got.Write(f.Payload)
	}
	return got.Bytes()
}

func (w *captureWriter) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.frames)
}

// ended 是否出现过带 FlagEnd 的帧。
func (w *captureWriter) ended() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, f := range w.frames {
		if f.Flags&wire.FlagEnd != 0 {
			return true
		}
	}
	return false
}

// errReader 先交出一段内容，再以非 EOF 错误结束：模拟传输中途远端断了。
type errReader struct {
	content string
	done    bool
}

func (r *errReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, errReaderBroken
	}
	r.done = true
	return copy(p, r.content), errReaderBroken
}

func (r *errReader) Close() error { return nil }

var errReaderBroken = errors.New("sftp: unexpected packet")

// blockReader 的 Read 一直堵到 Close：模拟慢速远端上卡住的读。
type blockReader struct {
	once sync.Once
	done chan struct{}
}

func newBlockReader() *blockReader { return &blockReader{done: make(chan struct{})} }

func (r *blockReader) Read(p []byte) (int, error) {
	<-r.done
	return 0, io.ErrClosedPipe
}

func (r *blockReader) Close() error {
	r.once.Do(func() { close(r.done) })
	return nil
}

func (r *blockReader) wasClosed() bool {
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}

// TestPump_FramesThenEnd 验证收尾约定：内容按帧序完整出去，最后单独一帧 FlagEnd。
func TestPump_FramesThenEnd(t *testing.T) {
	fw := &captureWriter{}
	src := io.NopCloser(bytes.NewReader([]byte("0123456789")))

	require.NoError(t, pump(context.Background(), fw, 3, src, 4))

	assert.Equal(t, []byte("0123456789"), fw.payload())
	assert.True(t, fw.ended(), "读完必须以 FlagEnd 收尾，否则前端永远等不到「传完了」")
}

// TestPump_ReadErrorLeavesStreamUnterminated 验证读侧出错时不补 FlagEnd 并返回错误：
// 补了 FlagEnd 等于告诉前端「这路流正常结束」，截断的文件会被当成完好文件保存。
func TestPump_ReadErrorLeavesStreamUnterminated(t *testing.T) {
	fw := &captureWriter{}
	require.Error(t, pump(context.Background(), fw, 3, &errReader{content: "AB"}, 8))

	assert.Equal(t, []byte("AB"), fw.payload())
	assert.False(t, fw.ended())
}

// TestPump_CancelClosesSource 验证 ctx 取消能解除阻塞读并关掉来源，且不补 FlagEnd：
// sftp 句柄不关，每条被中断的下载都泄漏一个远端文件句柄；
// 补了 FlagEnd 则前端会把这路半途而废的流当成传完了。
func TestPump_CancelClosesSource(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fw := &captureWriter{}
	src := newBlockReader()
	errCh := make(chan error, 1)
	go func() { errCh <- pump(ctx, fw, 3, src, 8) }()

	cancel()
	select {
	case err := <-errCh:
		require.Error(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("pump 应在 ctx 取消后返回")
	}
	assert.True(t, src.wasClosed(), "取消后必须关闭来源，否则 Read 永远阻塞")
	assert.False(t, fw.ended())
}

// TestPump_WriteFailureStops 验证写失败即止：连接已不可用时不该继续读远端字节。
func TestPump_WriteFailureStops(t *testing.T) {
	want := errors.New("websocket: close sent")
	fw := &captureWriter{err: want}
	src := io.NopCloser(bytes.NewReader([]byte("AB")))

	assert.ErrorIs(t, pump(context.Background(), fw, 3, src, 8), want)
	assert.Zero(t, fw.count())
}
