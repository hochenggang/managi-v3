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

// TestTerminalWSHandler_Basic 验证终端 WS：login → 输入回显 → resize 控制。
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

	// 首帧 login envelope
	writeWSEnvelope(t, conn, msgTypeLogin, testutil.TestNode(srv.Host(), srv.Port()))

	// 读登录结果（成功）
	loginResp := readWSJSON(t, conn)
	assert.Equal(t, "login", loginResp["type"])
	assert.True(t, envelopeData(loginResp)["success"].(bool))

	// 发送输入（mock shell 回显输入）
	input := "echo hi\n"
	writeWSEnvelope(t, conn, msgTypeMsg, input)

	// 读取输出（封装为 {type:"msg",data:"..."}）
	out := readWSJSON(t, conn)
	assert.Equal(t, "msg", out["type"])
	assert.Equal(t, input, out["data"])

	// 发送 resize 控制消息（envelope 格式）
	writeWSEnvelope(t, conn, msgTypeResize, wsResizeData{Cols: 120, Rows: 40})

	// 验证 resize 不破坏会话：发 stdin 并读回显
	writeWSEnvelope(t, conn, msgTypeMsg, "echo RESIZE_OK\n")
	out2 := readWSJSON(t, conn)
	assert.Equal(t, "msg", out2["type"])
	assert.Contains(t, out2["data"], "RESIZE_OK",
		"session should remain usable after resize")
}

// TestTerminalWSHandler_BadAuthFrame 验证首帧非法 JSON → error 消息。
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

	// 应收到 error 消息
	msg := readWSJSON(t, conn)
	assert.Equal(t, "error", msg["type"])
}

// TestTerminalWSHandler_AuthFailure 验证认证失败 → login failure 消息。
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

	// 首帧 login envelope（错误密码）
	writeWSEnvelope(t, conn, msgTypeLogin, testutil.BadPasswordNode(srv.Host(), srv.Port()))

	// 读 login failure
	msg := readWSJSON(t, conn)
	assert.Equal(t, "login", msg["type"])
	d := envelopeData(msg)
	assert.False(t, d["success"].(bool))
	assert.NotEmpty(t, d["message"])
}

// TestTerminalWSHandler_Ping 验证客户端 ping → 服务端 pong。
func TestTerminalWSHandler_Ping(t *testing.T) {
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

	// 登录
	writeWSEnvelope(t, conn, msgTypeLogin, testutil.TestNode(srv.Host(), srv.Port()))
	_ = readWSJSON(t, conn) // login success

	// 发 ping
	writeWSEnvelope(t, conn, msgTypePing, nil)

	// 读 pong（注意：shell 可能有回显，但 ping 不写入 stdin，不会有回显）
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(10*time.Second)))
	for {
		msg := readWSJSON(t, conn)
		if msg["type"] == "pong" {
			return
		}
		// 跳过其他消息（如 shell 提示符输出）
	}
}
