package handler

import (
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"managi/internal/model"
	"managi/internal/sshpool"
	"managi/internal/testutil"
)

// ===== sftpDownloadHandler 测试 =====

// nodeQuery 编码 node 为 URL query 参数值。
func nodeQuery(t *testing.T, node model.Node) string {
	t.Helper()
	b, err := json.Marshal(node)
	require.NoError(t, err)
	return url.QueryEscape(string(b))
}

// TestSftpDownloadHandler_Full 验证完整下载：200 + 完整内容 + Accept-Ranges。
func TestSftpDownloadHandler_Full(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	content := []byte("0123456789ABCDEFGHIJ") // 20 bytes
	require.NoError(t, os.WriteFile(filepath.Join(srv.RootDir(), "test.txt"), content, 0644))

	pool := sshpool.New(testutil.TestConfig())
	defer pool.CloseAll()
	h := sftpDownloadHandler(pool, testutil.TestConfig())

	target := "/api/sftp/download?node=" + nodeQuery(t, testutil.TestNode(srv.Host(), srv.Port())) + "&path=/test.txt"
	req := httptest.NewRequest("GET", target, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "bytes", rec.Header().Get("Accept-Ranges"))
	assert.Equal(t, content, rec.Body.Bytes())
}

// TestSftpDownloadHandler_Range 验证 Range 下载：206 + Content-Range + 部分内容。
func TestSftpDownloadHandler_Range(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	content := []byte("0123456789ABCDEFGHIJ") // 20 bytes
	require.NoError(t, os.WriteFile(filepath.Join(srv.RootDir(), "test.txt"), content, 0644))

	pool := sshpool.New(testutil.TestConfig())
	defer pool.CloseAll()
	h := sftpDownloadHandler(pool, testutil.TestConfig())

	target := "/api/sftp/download?node=" + nodeQuery(t, testutil.TestNode(srv.Host(), srv.Port())) + "&path=/test.txt"
	req := httptest.NewRequest("GET", target, nil)
	req.Header.Set("Range", "bytes=10-")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusPartialContent, rec.Code)
	assert.Equal(t, "bytes 10-19/20", rec.Header().Get("Content-Range"))
	assert.Equal(t, content[10:], rec.Body.Bytes())
}

// TestSftpDownloadHandler_MissingParams 验证缺少参数返回 400。
func TestSftpDownloadHandler_MissingParams(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	pool := sshpool.New(testutil.TestConfig())
	defer pool.CloseAll()
	h := sftpDownloadHandler(pool, testutil.TestConfig())
	node := testutil.TestNode(srv.Host(), srv.Port())

	// 缺 path
	req := httptest.NewRequest("GET", "/api/sftp/download?node="+nodeQuery(t, node), nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	// 缺 node
	req = httptest.NewRequest("GET", "/api/sftp/download?path=/test.txt", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestSftpDownloadHandler_InvalidNodeJSON 验证非法 node JSON 返回 400。
func TestSftpDownloadHandler_InvalidNodeJSON(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	pool := sshpool.New(testutil.TestConfig())
	defer pool.CloseAll()
	h := sftpDownloadHandler(pool, testutil.TestConfig())

	req := httptest.NewRequest("GET", "/api/sftp/download?node=notjson&path=/test.txt", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestSftpDownloadHandler_AuthFailure 验证认证失败返回 502。
func TestSftpDownloadHandler_AuthFailure(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	pool := sshpool.New(testutil.TestConfig())
	defer pool.CloseAll()
	h := sftpDownloadHandler(pool, testutil.TestConfig())

	target := "/api/sftp/download?node=" + nodeQuery(t, testutil.BadPasswordNode(srv.Host(), srv.Port())) + "&path=/test.txt"
	req := httptest.NewRequest("GET", target, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadGateway, rec.Code)
}

// ===== sftpWSHandler 测试 =====

// wsURL 将 httptest.Server 的 http URL 转为 ws URL。
func wsURL(t *testing.T, httpURL, path string) string {
	t.Helper()
	u, err := url.Parse(httpURL)
	require.NoError(t, err)
	u.Scheme = "ws"
	u.Path = path
	return u.String()
}

// readWSJSON 读取一帧 WS 消息并解析为 map（带超时）。
func readWSJSON(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(10*time.Second)))
	_, data, err := conn.ReadMessage()
	require.NoError(t, err)
	var msg map[string]any
	require.NoError(t, json.Unmarshal(data, &msg))
	return msg
}

// writeWSJSON 发送一帧 JSON WS 消息。
func writeWSJSON(t *testing.T, conn *websocket.Conn, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	require.NoError(t, conn.WriteMessage(websocket.TextMessage, data))
}

// writeWSEnvelope 发送统一 envelope 消息 {type, data}。
func writeWSEnvelope(t *testing.T, conn *websocket.Conn, typ string, data any) {
	t.Helper()
	var dataBytes json.RawMessage
	if data != nil {
		b, err := json.Marshal(data)
		require.NoError(t, err)
		dataBytes = b
	}
	writeWSJSON(t, conn, wsEnvelope{Type: typ, Data: dataBytes})
}

// envelopeData 取 envelope 的 data 字段为 map。
func envelopeData(msg map[string]any) map[string]any {
	d, _ := msg["data"].(map[string]any)
	return d
}

// TestSftpWSHandler_Basic 验证 SFTP WS：login → 服务端主动 list / → 文件展示。
func TestSftpWSHandler_Basic(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	// 预置文件
	require.NoError(t, os.WriteFile(filepath.Join(srv.RootDir(), "hello.txt"), []byte("hi"), 0644))

	pool := sshpool.New(testutil.TestConfig())
	defer pool.CloseAll()
	h := sftpWSHandler(pool, testutil.TestConfig())
	httpSrv := httptest.NewServer(h)
	defer httpSrv.Close()

	// 拨号 WS
	conn, _, err := websocket.DefaultDialer.Dial(wsURL(t, httpSrv.URL, "/ws/sftp"), nil)
	require.NoError(t, err)
	defer conn.Close()

	// 首帧：login envelope
	writeWSEnvelope(t, conn, msgTypeLogin, testutil.TestNode(srv.Host(), srv.Port()))

	// 读 login 结果（成功）
	msg := readWSJSON(t, conn)
	assert.Equal(t, "login", msg["type"])
	assert.True(t, envelopeData(msg)["success"].(bool))

	// 服务端登录后主动推送 list /
	msg = readWSJSON(t, conn)
	assert.Equal(t, "list", msg["type"])
	d := envelopeData(msg)
	assert.Equal(t, "/", d["path"])
	files, ok := d["files"].([]any)
	require.True(t, ok)
	// 验证预置的 hello.txt 存在且 size==2
	var foundHello bool
	for _, f := range files {
		fm, _ := f.(map[string]any)
		if fm["filename"] == "hello.txt" {
			foundHello = true
			assert.Equal(t, float64(2), fm["size"])
			assert.False(t, fm["is_dir"].(bool))
		}
	}
	assert.True(t, foundHello, "hello.txt should be in listing")
}

// TestSftpWSHandler_UploadFlow 验证完整断点续传上传流程。
func TestSftpWSHandler_UploadFlow(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	pool := sshpool.New(testutil.TestConfig())
	defer pool.CloseAll()
	h := sftpWSHandler(pool, testutil.TestConfig())
	httpSrv := httptest.NewServer(h)
	defer httpSrv.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(t, httpSrv.URL, "/ws/sftp"), nil)
	require.NoError(t, err)
	defer conn.Close()

	// 首帧 login
	writeWSEnvelope(t, conn, msgTypeLogin, testutil.TestNode(srv.Host(), srv.Port()))
	msg := readWSJSON(t, conn)
	require.Equal(t, "login", msg["type"])
	// 服务端主动 list /
	_ = readWSJSON(t, conn) // list

	// upload_init
	writeWSEnvelope(t, conn, msgTypeUploadInit, sftpRequestData{
		RemotePath: "/upload",
		Filename:   "test.bin",
		TotalSize:  11,
		ChunkSize:  11,
	})
	msg = readWSJSON(t, conn)
	require.Equal(t, "upload_init", msg["type"])
	initData := envelopeData(msg)
	uploadID, ok := initData["upload_id"].(string)
	require.True(t, ok)
	assert.NotEmpty(t, uploadID)
	offset, _ := initData["offset"].(float64)
	assert.Equal(t, float64(0), offset)

	// upload_chunk：发二进制帧（帧头协议，design-v3.md §6.4）
	chunkData := []byte("hello world")
	frame := buildChunkFrame(uploadID, 0, 0, chunkData)
	require.NoError(t, conn.WriteMessage(websocket.BinaryMessage, frame))
	msg = readWSJSON(t, conn)
	assert.Equal(t, "chunk_ack", msg["type"])

	// upload_complete
	writeWSEnvelope(t, conn, msgTypeUploadDone, sftpRequestData{UploadID: uploadID})
	msg = readWSJSON(t, conn)
	assert.Equal(t, "ok", msg["type"])

	// 验证最终文件内容
	got, err := os.ReadFile(filepath.Join(srv.RootDir(), "upload", "test.bin"))
	require.NoError(t, err)
	assert.Equal(t, chunkData, got)
}

// TestSftpWSHandler_BadAuthFrame 验证首帧非法 JSON 返回 error。
func TestSftpWSHandler_BadAuthFrame(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	pool := sshpool.New(testutil.TestConfig())
	defer pool.CloseAll()
	h := sftpWSHandler(pool, testutil.TestConfig())
	httpSrv := httptest.NewServer(h)
	defer httpSrv.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(t, httpSrv.URL, "/ws/sftp"), nil)
	require.NoError(t, err)
	defer conn.Close()

	// 发非法 JSON 首帧
	require.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte("not json")))

	msg := readWSJSON(t, conn)
	assert.Equal(t, "error", msg["type"])
}

// buildChunkFrame 构造二进制分片帧（与 parseChunkFrame 对齐）。
// 帧格式（大端序）：[4字节 upload_id_len][upload_id][4字节 chunk_index][8字节 offset][8字节 data_len][data]
func buildChunkFrame(uploadID string, chunkIndex int, offset int64, data []byte) []byte {
	idBytes := []byte(uploadID)
	buf := make([]byte, 4+len(idBytes)+4+8+8+len(data))
	binary.BigEndian.PutUint32(buf[0:], uint32(len(idBytes)))
	copy(buf[4:], idBytes)
	binary.BigEndian.PutUint32(buf[4+len(idBytes):], uint32(chunkIndex))
	binary.BigEndian.PutUint64(buf[4+len(idBytes)+4:], uint64(offset))
	binary.BigEndian.PutUint64(buf[4+len(idBytes)+4+8:], uint64(len(data)))
	copy(buf[4+len(idBytes)+4+8+8:], data)
	return buf
}
