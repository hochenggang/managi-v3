package handler

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"managi/internal/sshpool"
	"managi/internal/testutil"
)

// readWSRaw 读取一帧 WS 消息返回原始字节（带超时）。
func readWSRaw(t *testing.T, conn *websocket.Conn) []byte {
	t.Helper()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(10*time.Second)))
	_, data, err := conn.ReadMessage()
	require.NoError(t, err)
	return data
}

// TestTerminalWSHandler_Basic 验证终端 WS：认证 → 输入回显 → resize 控制。
func TestTerminalWSHandler_Basic(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	pool := sshpool.New(testutil.TestConfig())
	defer pool.CloseAll()
	h := terminalWSHandler(pool, testutil.TestConfig())
	httpSrv := httptest.NewServer(h)
	defer httpSrv.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(t, httpSrv.URL, "/ws"), nil)
	require.NoError(t, err)
	defer conn.Close()

	// 首帧认证
	writeWSJSON(t, conn, testutil.TestNode(srv.Host(), srv.Port()))

	// 发送输入（mock shell 回显输入）
	input := []byte("echo hi\n")
	require.NoError(t, conn.WriteMessage(websocket.TextMessage, input))

	// 读取回显
	echo := readWSRaw(t, conn)
	assert.Equal(t, input, echo)

	// 发送 resize 控制消息（不产生输出，验证不报错即可）
	writeWSJSON(t, conn, map[string]any{
		"type": "resize",
		"cols": 120,
		"rows": 40,
	})

	// 短暂等待确保 resize 被处理
	time.Sleep(100 * time.Millisecond)
}

// TestTerminalWSHandler_BadAuthFrame 验证首帧非法 JSON → 连接关闭。
func TestTerminalWSHandler_BadAuthFrame(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	pool := sshpool.New(testutil.TestConfig())
	defer pool.CloseAll()
	h := terminalWSHandler(pool, testutil.TestConfig())
	httpSrv := httptest.NewServer(h)
	defer httpSrv.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(t, httpSrv.URL, "/ws"), nil)
	require.NoError(t, err)
	defer conn.Close()

	// 发非法 JSON 首帧
	require.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte("not json")))

	// 连接应被关闭（ReadMessage 返回错误）
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, _, err = conn.ReadMessage()
	assert.Error(t, err)
}

// TestTerminalWSHandler_AuthFailure 验证认证失败 → 收到 error 消息。
func TestTerminalWSHandler_AuthFailure(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	pool := sshpool.New(testutil.TestConfig())
	defer pool.CloseAll()
	h := terminalWSHandler(pool, testutil.TestConfig())
	httpSrv := httptest.NewServer(h)
	defer httpSrv.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(t, httpSrv.URL, "/ws"), nil)
	require.NoError(t, err)
	defer conn.Close()

	// 发错误密码节点
	writeWSJSON(t, conn, testutil.BadPasswordNode(srv.Host(), srv.Port()))

	// 读 error 消息
	msg := readWSJSON(t, conn)
	assert.Equal(t, "error", msg["type"])
}
