package handler

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"managi/internal/config"
	"managi/internal/model"
	"managi/internal/sshpool"
	"managi/internal/testutil"
	"managi/internal/wire"
)

// ===== /ws 测试替身 =====

// hubEnv 一套 /ws 服务端：mock SSH + 连接池 + 会话表 + HTTP 路由。
// 与连接分开，才能测「同一条会话换个 WS 连接重开」这类重连场景。
type hubEnv struct {
	t       *testing.T
	srv     *testutil.Server
	pool    *sshpool.Pool
	mgr     *sessionManager
	httpURL string
}

func newHubEnv(t *testing.T, cfg *config.Config) *hubEnv {
	t.Helper()
	if cfg == nil {
		cfg = testutil.TestConfig()
	}
	srv := testutil.Start(t)
	t.Cleanup(srv.Close)

	pool := sshpool.New(cfg)
	t.Cleanup(pool.CloseAll)

	mgr := newSessionManager(pool, cfg)
	httpSrv := httptest.NewServer(wsHandler(mgr, pool, cfg))
	t.Cleanup(httpSrv.Close)

	return &hubEnv{t: t, srv: srv, pool: pool, mgr: mgr, httpURL: httpSrv.URL}
}

// connect 对同一套服务端再开一条 /ws 连接。
func (e *hubEnv) connect() *hubTest {
	e.t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(wsURL(e.t, e.httpURL, "/ws"), nil)
	require.NoError(e.t, err)
	e.t.Cleanup(func() { _ = conn.Close() })
	return &hubTest{t: e.t, srv: e.srv, env: e, conn: conn}
}

// newHubTest 起一条干净的 /ws 连接（cfg 为空则用测试默认配置）。
func newHubTest(t *testing.T, cfg *config.Config) *hubTest {
	t.Helper()
	return newHubEnv(t, cfg).connect()
}

// hubTest 一条 /ws 客户端连接。
type hubTest struct {
	t      *testing.T
	srv    *testutil.Server
	env    *hubEnv
	conn   *websocket.Conn
	nextID int64 // 控制帧 seq，逐次递增，便于断言响应回填
}

// wsURL 将 httptest.Server 的 http URL 转为 ws URL。
func wsURL(t *testing.T, httpURL, path string) string {
	t.Helper()
	u, err := url.Parse(httpURL)
	require.NoError(t, err)
	u.Scheme = "ws"
	u.Path = path
	return u.String()
}

// send 发一帧 JSON（自动补 seq，便于断言响应回填）。
func (h *hubTest) send(typ string, data any) int64 {
	h.t.Helper()
	h.nextID++
	raw, err := json.Marshal(wsEnvelope{Type: typ, Data: mustJSON(h.t, data), Seq: h.nextID})
	require.NoError(h.t, err)
	require.NoError(h.t, h.conn.WriteMessage(websocket.TextMessage, raw))
	return h.nextID
}

// sendNoReply 发一帧不等回复的控制请求（seq 省略，如 resize/ping）。
func (h *hubTest) sendNoReply(typ string, data any) {
	h.t.Helper()
	raw, err := json.Marshal(wsEnvelope{Type: typ, Data: mustJSON(h.t, data)})
	require.NoError(h.t, err)
	require.NoError(h.t, h.conn.WriteMessage(websocket.TextMessage, raw))
}

func mustJSON(t *testing.T, data any) json.RawMessage {
	t.Helper()
	if data == nil {
		return nil
	}
	b, err := json.Marshal(data)
	require.NoError(t, err)
	return b
}

// sendFrame 发一帧数据面二进制帧。
func (h *hubTest) sendFrame(f wire.Frame) {
	h.t.Helper()
	require.NoError(h.t, h.conn.WriteMessage(websocket.BinaryMessage, f.Bytes()))
}

// read 读一帧：文本帧解析为 envelope，二进制帧原样返回。
func (h *hubTest) read() (wsEnvelope, wire.Frame, bool) {
	h.t.Helper()
	require.NoError(h.t, h.conn.SetReadDeadline(time.Now().Add(10*time.Second)))
	msgType, data, err := h.conn.ReadMessage()
	require.NoError(h.t, err)
	if msgType == websocket.BinaryMessage {
		f, err := wire.Decode(data)
		require.NoError(h.t, err)
		return wsEnvelope{}, f, false
	}
	var env wsEnvelope
	require.NoError(h.t, json.Unmarshal(data, &env))
	return env, wire.Frame{}, true
}

// readControl 读到下一个文本帧并返回其 data（map）。
func (h *hubTest) readControl() map[string]any {
	h.t.Helper()
	for {
		env, _, isText := h.read()
		if isText {
			var d map[string]any
			if len(env.Data) > 0 {
				require.NoError(h.t, json.Unmarshal(env.Data, &d))
			}
			if d == nil {
				d = map[string]any{}
			}
			d["_type"] = env.Type
			d["_seq"] = env.Seq
			return d
		}
	}
}

// expectControl 读到下一个指定 type 的文本帧（跳过数据帧与其它控制帧）。
func (h *hubTest) expectControl(typ string) map[string]any {
	h.t.Helper()
	for {
		d := h.readControl()
		if d["_type"] == typ {
			return d
		}
		require.NotEqual(h.t, msgError, d["_type"], "意外错误帧: %v", d)
	}
}

// openPTY 打开一路终端通道，返回通道号与 open 响应。
func (h *hubTest) openPTY(node model.Node, sessionID string, cols, rows int) (uint32, map[string]any) {
	h.t.Helper()
	seq := h.send(msgOpen, map[string]any{
		"kind": kindPTY, "node": node, "session_id": sessionID, "cols": cols, "rows": rows,
	})
	resp := h.expectControl(msgOpen)
	assert.Equal(h.t, seq, resp["_seq"], "open 响应必须回填请求 seq")
	assert.Equal(h.t, kindPTY, resp["kind"])
	chanID := uint32(resp["chan"].(float64))
	require.NotEqual(h.t, uint32(0), chanID)
	return chanID, resp
}

// openSFTP 打开一路文件管理通道，返回通道号与 open 响应。
func (h *hubTest) openSFTP(node model.Node) (uint32, map[string]any) {
	h.t.Helper()
	seq := h.send(msgOpen, map[string]any{"kind": kindSFTP, "node": node})
	resp := h.expectControl(msgOpen)
	assert.Equal(h.t, seq, resp["_seq"])
	assert.Equal(h.t, kindSFTP, resp["kind"])
	chanID := uint32(resp["chan"].(float64))
	require.NotEqual(h.t, uint32(0), chanID)
	return chanID, resp
}

// readOutput 累积指定通道的数据帧载荷，直到内容包含 want。
func (h *hubTest) readOutput(chanID uint32, want string) string {
	h.t.Helper()
	var got bytes.Buffer
	for {
		_, f, isText := h.read()
		if isText {
			continue
		}
		if f.Chan != chanID {
			continue
		}
		got.Write(f.Payload)
		if strings.Contains(got.String(), want) {
			return got.String()
		}
	}
}

// readN 累积指定通道的数据帧载荷，直到凑够 n 字节。
func (h *hubTest) readN(chanID uint32, n int) []byte {
	h.t.Helper()
	var got bytes.Buffer
	for got.Len() < n {
		_, f, isText := h.read()
		if isText || f.Chan != chanID {
			continue
		}
		got.Write(f.Payload)
	}
	return got.Bytes()
}

// readStream 读完指定通道的一路数据流（直到 FlagEnd），返回累积载荷。
func (h *hubTest) readStream(chanID uint32) []byte {
	h.t.Helper()
	var got bytes.Buffer
	for {
		env, f, isText := h.read()
		if isText {
			require.NotEqual(h.t, msgError, env.Type, "流中出现错误帧: %v", env)
			continue
		}
		require.Equal(h.t, chanID, f.Chan, "流中混入了别的通道")
		got.Write(f.Payload)
		if f.Flags&wire.FlagEnd != 0 {
			return got.Bytes()
		}
	}
}

// node 返回指向 mock server 的可用节点。
func (h *hubTest) node() model.Node { return testutil.TestNode(h.srv.Host(), h.srv.Port()) }

// ===== 终端通道 =====

// TestPTY_EchoOverDataPlane 验证终端字节走数据面：输入帧 → shell → 输出帧，
// 全程不产生 {type:"msg"} 文本帧（旧协议每段输出都要过一次 string→JSON）。
func TestPTY_EchoOverDataPlane(t *testing.T) {
	h := newHubTest(t, nil)
	chanID, resp := h.openPTY(h.node(), "sess-echo", 80, 24)
	assert.NotContains(t, resp, "reattached", "首次打开不应是复用")
	// 前端要靠它决定粘贴分片大小，不能让前端猜服务端的单帧上限
	assert.Greater(t, resp["chunk_size"].(float64), float64(0))

	h.sendFrame(wire.Frame{Chan: chanID, Payload: []byte("echo hi\n")})
	assert.Contains(t, h.readOutput(chanID, "echo hi"), "echo hi")
}

// TestPTY_PreservesRawBytes 验证数据面不改动字节：含 0x00/0xFF 等非法 UTF-8 的输入
// 原样回显。JSON 字符串协议下这些字节会被替换成 U+FFFD，粘贴转义序列也就失真了。
func TestPTY_PreservesRawBytes(t *testing.T) {
	h := newHubTest(t, nil)
	chanID, _ := h.openPTY(h.node(), "sess-raw", 80, 24)

	raw := []byte{0x00, 0xff, 0xfe, 0x80, 'A', '\n'}
	h.sendFrame(wire.Frame{Chan: chanID, Payload: raw})

	// mock shell 可能把一次写拆成几次 read，按字节数收齐再比内容
	assert.Equal(t, raw, h.readN(chanID, len(raw)))
}

// TestPTY_ResizeKeepsSession 验证 resize 不破坏会话（非正尺寸被拒绝，会话仍可用）。
func TestPTY_ResizeKeepsSession(t *testing.T) {
	h := newHubTest(t, nil)
	chanID, _ := h.openPTY(h.node(), "sess-resize", 80, 24)

	h.sendNoReply(msgResize, map[string]any{"chan": chanID, "cols": 120, "rows": 40})
	h.sendNoReply(msgResize, map[string]any{"chan": chanID, "cols": 0, "rows": 0})

	h.sendFrame(wire.Frame{Chan: chanID, Payload: []byte("echo RESIZED\n")})
	assert.Contains(t, h.readOutput(chanID, "RESIZED"), "RESIZED")
}

// TestPTY_BadPasswordRepliesErrorWithSeq 验证拨号失败回 error 帧且回填请求 seq：
// 前端据此把错误显示到对应标签页，而不是只看到一个断开的连接。
func TestPTY_BadPasswordRepliesErrorWithSeq(t *testing.T) {
	h := newHubTest(t, nil)
	seq := h.send(msgOpen, map[string]any{
		"kind": kindPTY, "node": testutil.BadPasswordNode(h.srv.Host(), h.srv.Port()),
		"session_id": "sess-bad", "cols": 80, "rows": 24,
	})
	d := h.readControl()
	assert.Equal(t, msgError, d["_type"])
	assert.Equal(t, seq, d["_seq"])
	assert.NotEmpty(t, d["message"])
}

// TestPingGetsPong 验证客户端 ping → 服务端 pong（ seq 回填，前端据此判活）。
func TestPingGetsPong(t *testing.T) {
	h := newHubTest(t, nil)
	seq := h.send(msgPing, nil)
	d := h.expectControl(msgPong)
	assert.Equal(t, seq, d["_seq"])
}

// TestReattachReplaysHistoryBeforeLive 验证重连复用同一 shell 时，
// scrollback 回放严格早于新输入的回显：否则用户先看到最新一行、再看到历史，顺序错乱。
func TestReattachReplaysHistoryBeforeLive(t *testing.T) {
	env := newHubEnv(t, nil)
	sessionID := "sess-reattach"

	// 第一段连接：产生历史输出后断开
	h := env.connect()
	chan1, _ := h.openPTY(h.node(), sessionID, 80, 24)
	h.sendFrame(wire.Frame{Chan: chan1, Payload: []byte("echo HISTORY\n")})
	require.Contains(t, h.readOutput(chan1, "HISTORY"), "HISTORY")
	require.NoError(t, h.conn.Close())

	// 第二段连接：同一个 session_id，后端 shell 在空闲 TTL 内仍在
	h2 := env.connect()
	chan2, resp := h2.openPTY(h2.node(), sessionID, 80, 24)
	assert.Equal(t, true, resp["reattached"], "同 session_id 必须复用同一 shell")

	// 立刻输入新命令：回放（HISTORY）必须先于回显（LATE）到达
	h2.sendFrame(wire.Frame{Chan: chan2, Payload: []byte("echo LATE\n")})
	frames := h2.collect(chan2, "LATE")
	historyAt, lateAt := indexOfContains(frames, "HISTORY"), indexOfContains(frames, "LATE")
	require.NotEqual(t, -1, historyAt, "重连必须能回看到历史输出")
	assert.Less(t, historyAt, lateAt, "历史必须先于实时输出")
}

// collect 累积读取指定通道的数据帧，直到某帧包含 want，返回各帧文本。
func (h *hubTest) collect(chanID uint32, want string) []string {
	h.t.Helper()
	var frames []string
	for {
		_, f, isText := h.read()
		if isText || f.Chan != chanID {
			continue
		}
		frames = append(frames, string(f.Payload))
		if strings.Contains(string(f.Payload), want) {
			return frames
		}
	}
}

func indexOfContains(frames []string, want string) int {
	for i, f := range frames {
		if strings.Contains(f, want) {
			return i
		}
	}
	return -1
}

// TestCloseChannelRepliesThenUnknown 验证 close 关闭通道：响应回填 chan，
// 之后再向该通道发数据只会收到「unknown channel」，不会静默丢弃。
func TestCloseChannelRepliesThenUnknown(t *testing.T) {
	h := newHubTest(t, nil)
	chanID, _ := h.openSFTP(h.node())

	seq := h.send(msgClose, map[string]any{"chan": chanID})
	d := h.expectControl(msgClose)
	assert.Equal(t, seq, d["_seq"])
	assert.Equal(t, float64(chanID), d["chan"])

	h.sendFrame(wire.Frame{Chan: chanID, Payload: []byte("x")})
	errFrame := h.readControl()
	assert.Equal(t, msgError, errFrame["_type"])
	assert.Contains(t, errFrame["message"], "unknown channel")
}

// TestUnknownVerbRejected 验证未知动词回错误而不是静默忽略。
func TestUnknownVerbRejected(t *testing.T) {
	h := newHubTest(t, nil)
	seq := h.send("teleport", map[string]any{"chan": 1})
	d := h.readControl()
	assert.Equal(t, msgError, d["_type"])
	assert.Contains(t, d["message"], "unknown verb")
	assert.Equal(t, seq, d["_seq"])
}

// TestMalformedDataFrameClosesConn 验证帧头不完整的二进制帧直接断连：
// 字节流已错位，留着连接只会让前端在错误的通道上干等。
func TestMalformedDataFrameClosesConn(t *testing.T) {
	h := newHubTest(t, nil)
	require.NoError(t, h.conn.WriteMessage(websocket.BinaryMessage, []byte{0x00, 0x01}))

	require.NoError(t, h.conn.SetReadDeadline(time.Now().Add(10*time.Second)))
	_, _, err := h.conn.ReadMessage()
	require.Error(t, err, "服务端应已关闭连接")
}

// ===== SFTP 通道 =====

// TestSFTP_ListMkdirRm 验证目录类动词：open 报 home，ls/mkdir/rm 各自回填请求 seq。
func TestSFTP_ListMkdirRm(t *testing.T) {
	h := newHubTest(t, nil)
	require.NoError(t, os.WriteFile(filepath.Join(h.srv.RootDir(), "hello.txt"), []byte("hi"), 0644))

	chanID, resp := h.openSFTP(h.node())
	home, _ := resp["home"].(string)
	require.NotEmpty(t, home, "open 必须报出起始目录")

	seq := h.send(msgLS, map[string]any{"chan": chanID, "path": "/"})
	d := h.expectControl(msgLS)
	assert.Equal(t, seq, d["_seq"])
	files, _ := d["files"].([]any)
	assert.Len(t, files, 1)

	h.send(msgMkdir, map[string]any{"chan": chanID, "path": "/a/b"})
	assert.Equal(t, "/a/b", h.expectControl(msgMkdir)["path"])
	require.DirExists(t, filepath.Join(h.srv.RootDir(), "a", "b"))

	h.send(msgRM, map[string]any{"chan": chanID, "path": "/a"})
	h.expectControl(msgRM)
	assert.NoDirExists(t, filepath.Join(h.srv.RootDir(), "a"))
}

// TestSFTP_EmptyPathRejected 验证空路径在入口被挡：否则 sftp 只会报出
// 一句看不出该改哪里的底层错误。
func TestSFTP_EmptyPathRejected(t *testing.T) {
	h := newHubTest(t, nil)
	chanID, _ := h.openSFTP(h.node())

	seq := h.send(msgMkdir, map[string]any{"chan": chanID, "path": ""})
	d := h.readControl()
	assert.Equal(t, msgError, d["_type"])
	assert.Equal(t, seq, d["_seq"])
	assert.Contains(t, d["message"], "path is required")
}

// TestSFTP_UploadStreamsFramesAndFinalizes 验证上传：upload 响应给续传点与切片大小，
// 数据帧直接落盘，FlagEnd 帧触发落定并主动推 upload_end——不再有每片一次的 chunk_ack 往返。
func TestSFTP_UploadStreamsFramesAndFinalizes(t *testing.T) {
	h := newHubTest(t, nil)
	chanID, _ := h.openSFTP(h.node())

	payload := []byte("AAAABBBB")
	seq := h.send(msgUpload, map[string]any{
		"chan": chanID, "path": "/up", "filename": "f.bin", "size": len(payload),
	})
	d := h.expectControl(msgUpload)
	assert.Equal(t, seq, d["_seq"])
	assert.Equal(t, float64(0), d["offset"], "首次上传从 0 开始")
	assert.Greater(t, d["chunk_size"].(float64), float64(0), "切片大小由服务端下发")

	h.sendFrame(wire.Frame{Chan: chanID, Payload: payload[:4]})
	h.sendFrame(wire.Frame{Chan: chanID, Payload: payload[4:], Flags: wire.FlagEnd})

	end := h.expectControl(msgUploadEnd)
	assert.Equal(t, float64(chanID), end["chan"])
	assert.Equal(t, float64(len(payload)), end["size"])

	content, err := os.ReadFile(filepath.Join(h.srv.RootDir(), "up", "f.bin"))
	require.NoError(t, err)
	assert.Equal(t, payload, content)
	assert.NoFileExists(t, filepath.Join(h.srv.RootDir(), "up", "f.bin.part"))
}

// TestSFTP_UploadResumesFromPartOffset 验证断点续传：残留 .part 时 upload 响应给出续传点，
// 客户端从该偏移继续，服务端只按到达顺序追加。
func TestSFTP_UploadResumesFromPartOffset(t *testing.T) {
	h := newHubTest(t, nil)
	require.NoError(t, os.MkdirAll(filepath.Join(h.srv.RootDir(), "up"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(h.srv.RootDir(), "up", "f.bin.part"), []byte("AAAA"), 0644))

	chanID, _ := h.openSFTP(h.node())
	h.send(msgUpload, map[string]any{"chan": chanID, "path": "/up", "filename": "f.bin", "size": 8})
	d := h.expectControl(msgUpload)
	assert.Equal(t, float64(4), d["offset"], "必须报出 .part 已有大小")

	h.sendFrame(wire.Frame{Chan: chanID, Payload: []byte("BBBB"), Flags: wire.FlagEnd})
	h.expectControl(msgUploadEnd)

	content, err := os.ReadFile(filepath.Join(h.srv.RootDir(), "up", "f.bin"))
	require.NoError(t, err)
	assert.Equal(t, "AAAABBBB", string(content))
}

// TestSFTP_UploadSecondStartTakesOver 验证被放弃的上传可以重试：一条通道只有一个写入者，
// 且帧有序，所以没发 FlagEnd 就重发 upload 只可能是上一路出错/断线后被放弃。
// 此时接管并从未写完的 .part 续上，而不是拒绝到整条连接余生都传不了文件。
func TestSFTP_UploadSecondStartTakesOver(t *testing.T) {
	h := newHubTest(t, nil)
	chanID, _ := h.openSFTP(h.node())

	h.send(msgUpload, map[string]any{"chan": chanID, "path": "/up", "filename": "f.bin", "size": 8})
	h.expectControl(msgUpload)
	// 只写了一半就放弃这一路
	h.sendFrame(wire.Frame{Chan: chanID, Payload: []byte("AAAA")})

	seq := h.send(msgUpload, map[string]any{"chan": chanID, "path": "/up", "filename": "f.bin", "size": 8})
	d := h.expectControl(msgUpload)
	assert.Equal(t, seq, d["_seq"])
	assert.Equal(t, float64(4), d["offset"], "接管后从已落盘的 .part 续上")

	h.sendFrame(wire.Frame{Chan: chanID, Payload: []byte("BBBB"), Flags: wire.FlagEnd})
	assert.Equal(t, float64(8), h.expectControl(msgUploadEnd)["size"])

	content, err := os.ReadFile(filepath.Join(h.srv.RootDir(), "up", "f.bin"))
	require.NoError(t, err)
	assert.Equal(t, []byte("AAAABBBB"), content)
}

// TestSFTP_DataFrameWithoutUploadRejected 验证没有活动上传时的数据帧出声：
// 否则客户端会以为分片已经写进去了。
func TestSFTP_DataFrameWithoutUploadRejected(t *testing.T) {
	h := newHubTest(t, nil)
	chanID, _ := h.openSFTP(h.node())

	h.sendFrame(wire.Frame{Chan: chanID, Payload: []byte("orphan")})
	d := h.readControl()
	assert.Equal(t, msgError, d["_type"])
	assert.Contains(t, d["message"], "no upload in progress")
}

// TestSFTP_DownloadFramesEndWithFlag 验证下载：响应先给出 total/filename，
// 随后是若干数据帧，最后一片带 FlagEnd。
func TestSFTP_DownloadFramesEndWithFlag(t *testing.T) {
	cfg := testutil.TestConfig()
	cfg.DownloadChunkSize = 4 // 逼出多帧，验证收尾标志而不是一帧搞定
	h := newHubTest(t, cfg)

	content := []byte("0123456789ABCDEFGHIJ")
	require.NoError(t, os.WriteFile(filepath.Join(h.srv.RootDir(), "d.txt"), content, 0644))

	chanID, _ := h.openSFTP(h.node())
	seq := h.send(msgDownload, map[string]any{"chan": chanID, "path": "/d.txt"})
	d := h.expectControl(msgDownload)
	assert.Equal(t, seq, d["_seq"])
	assert.Equal(t, float64(len(content)), d["total"])
	assert.Equal(t, "d.txt", d["filename"])

	got := h.readStream(chanID)
	assert.Equal(t, content, got)
}

// TestSFTP_DownloadOffsetResumes 验证 WS 下载的 offset：从中间续传只回尾段，
// total 仍是文件总大小（前端据此显示进度）。
func TestSFTP_DownloadOffsetResumes(t *testing.T) {
	h := newHubTest(t, nil)
	content := []byte("0123456789ABCDEFGHIJ")
	require.NoError(t, os.WriteFile(filepath.Join(h.srv.RootDir(), "r.txt"), content, 0644))

	chanID, _ := h.openSFTP(h.node())
	h.send(msgDownload, map[string]any{"chan": chanID, "path": "/r.txt", "offset": 10})
	d := h.expectControl(msgDownload)
	assert.Equal(t, float64(len(content)), d["total"])
	assert.Equal(t, content[10:], h.readStream(chanID))
}

// TestSFTP_DownloadRejectsConcurrent 验证同一通道的第二路下载被拒：
// 两路字节交错写进同一个文件必然损坏（旧实现靠一把全局锁串行化）。
func TestSFTP_DownloadRejectsConcurrent(t *testing.T) {
	cfg := testutil.TestConfig()
	cfg.DownloadChunkSize = 1024
	h := newHubTest(t, cfg)

	// 4MB 远超 socket 缓冲：客户端不读，第一路 pump 必然还堵在写操作上
	require.NoError(t, os.WriteFile(filepath.Join(h.srv.RootDir(), "big.bin"), bytes.Repeat([]byte("x"), 4<<20), 0644))

	chanID, _ := h.openSFTP(h.node())
	h.send(msgDownload, map[string]any{"chan": chanID, "path": "/big.bin"})
	seq := h.send(msgDownload, map[string]any{"chan": chanID, "path": "/big.bin"})

	d := h.expectControl(msgDownload) // 第一路的响应
	assert.Equal(t, float64(4<<20), d["total"])
	second := h.readControl()
	assert.Equal(t, msgError, second["_type"])
	assert.Equal(t, seq, second["_seq"])
	assert.Contains(t, second["message"], "already running")
}

// TestSFTP_ReuseChannelsOnOneConnection 验证一条连接跑多路通道：
// 两个文件管理标签各自 ls，响应按 chan 区分，互不干扰。
func TestSFTP_ReuseChannelsOnOneConnection(t *testing.T) {
	h := newHubTest(t, nil)
	require.NoError(t, os.WriteFile(filepath.Join(h.srv.RootDir(), "one.txt"), []byte("1"), 0644))

	chanA, _ := h.openSFTP(h.node())
	chanB, _ := h.openSFTP(h.node())
	assert.NotEqual(h.t, chanA, chanB, "每次 open 必须分到新通道号")

	// 读循环串行处理请求，因此响应顺序就是请求顺序，且各带自己的 chan
	h.send(msgLS, map[string]any{"chan": chanB, "path": "/"})
	h.send(msgLS, map[string]any{"chan": chanA, "path": "/"})
	for _, want := range []uint32{chanB, chanA} {
		d := h.expectControl(msgLS)
		assert.Equal(t, float64(want), d["chan"])
	}
}

// TestSFTP_OperationOnPTYChannelRejected 验证把 SFTP 动词发给终端通道会明确报错，
// 而不是落到 nil 会话上 panic。
func TestSFTP_OperationOnPTYChannelRejected(t *testing.T) {
	h := newHubTest(t, nil)
	chanID, _ := h.openPTY(h.node(), "sess-kind", 80, 24)

	seq := h.send(msgLS, map[string]any{"chan": chanID, "path": "/"})
	d := h.readControl()
	assert.Equal(t, msgError, d["_type"])
	assert.Equal(t, seq, d["_seq"])
	assert.Contains(t, d["message"], "not an sftp channel")
}
