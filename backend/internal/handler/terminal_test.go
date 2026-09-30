package handler

import (
	"net/http/httptest"
	"strings"
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
	h := terminalWSHandler(newSessionManager(pool, testutil.TestConfig()), testutil.TestConfig())
	httpSrv := httptest.NewServer(h)
	defer httpSrv.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(t, httpSrv.URL, "/ws/ssh"), nil)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

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
	h := terminalWSHandler(newSessionManager(pool, testutil.TestConfig()), testutil.TestConfig())
	httpSrv := httptest.NewServer(h)
	defer httpSrv.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(t, httpSrv.URL, "/ws/ssh"), nil)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

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
	h := terminalWSHandler(newSessionManager(pool, testutil.TestConfig()), testutil.TestConfig())
	httpSrv := httptest.NewServer(h)
	defer httpSrv.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(t, httpSrv.URL, "/ws/ssh"), nil)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

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
	h := terminalWSHandler(newSessionManager(pool, testutil.TestConfig()), testutil.TestConfig())
	httpSrv := httptest.NewServer(h)
	defer httpSrv.Close()

	conn, _, err := websocket.DefaultDialer.Dial(wsURL(t, httpSrv.URL, "/ws/ssh"), nil)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

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

// TestTerminalWSHandler_Reattach 验证前端断线重连后复用同一 shell 会话。
func TestTerminalWSHandler_Reattach(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	pool := sshpool.New(testutil.TestConfig())
	defer pool.CloseAll()
	cfg := testutil.TestConfig()
	h := terminalWSHandler(newSessionManager(pool, cfg), cfg)
	httpSrv := httptest.NewServer(h)
	defer httpSrv.Close()

	node := testutil.TestNode(srv.Host(), srv.Port())
	sessionID := "test-reattach-id"

	// 第一次连接：建立会话
	conn1, _, err := websocket.DefaultDialer.Dial(wsURL(t, httpSrv.URL, "/ws/ssh"), nil)
	require.NoError(t, err)
	writeWSEnvelope(t, conn1, msgTypeLogin, loginFrame{
		Node: node, SessionID: sessionID, Cols: 80, Rows: 24,
	})
	// 读登录结果（成功，非复用）
	loginResp := readUntilType(t, conn1, "login")
	assert.True(t, envelopeData(loginResp)["success"].(bool))
	assert.Nil(t, envelopeData(loginResp)["reattached"], "first connection should not be reattach")
	_ = conn1.Close()

	// 等待后端处理 detach（defer 执行）
	time.Sleep(200 * time.Millisecond)

	// 第二次连接：相同 session_id → 应复用
	conn2, _, err := websocket.DefaultDialer.Dial(wsURL(t, httpSrv.URL, "/ws/ssh"), nil)
	require.NoError(t, err)
	defer func() { _ = conn2.Close() }()
	writeWSEnvelope(t, conn2, msgTypeLogin, loginFrame{
		Node: node, SessionID: sessionID, Cols: 80, Rows: 24,
	})
	loginResp2 := readUntilType(t, conn2, "login")
	d := envelopeData(loginResp2)
	assert.True(t, d["success"].(bool))
	assert.True(t, d["reattached"] == true, "second connection should reattach to existing session")
}

// readUntilType 读取消息直到匹配指定 type（跳过 scrollback 等中间消息）。
func readUntilType(t *testing.T, conn *websocket.Conn, typ string) map[string]any {
	t.Helper()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(10*time.Second)))
	for {
		msg := readWSJSON(t, conn)
		if msg["type"] == typ {
			return msg
		}
	}
}

// TestTerminalWSHandler_ReattachReplaysBeforeLive 验证重连时 scrollback 回放严格早于
// 新输入的回显：回放期间实时输出不得插队，否则用户先看到最新一行再看到历史，顺序错乱。
func TestTerminalWSHandler_ReattachReplaysBeforeLive(t *testing.T) {
	srv := testutil.Start(t)
	defer srv.Close()

	pool := sshpool.New(testutil.TestConfig())
	defer pool.CloseAll()
	cfg := testutil.TestConfig()
	h := terminalWSHandler(newSessionManager(pool, cfg), cfg)
	httpSrv := httptest.NewServer(h)
	defer httpSrv.Close()

	node := testutil.TestNode(srv.Host(), srv.Port())
	sessionID := "test-replay-order"

	// 第一段连接：产生一段历史输出后断开
	conn1, _, err := websocket.DefaultDialer.Dial(wsURL(t, httpSrv.URL, "/ws/ssh"), nil)
	require.NoError(t, err)
	writeWSEnvelope(t, conn1, msgTypeLogin, loginFrame{Node: node, SessionID: sessionID, Cols: 80, Rows: 24})
	_ = readUntilType(t, conn1, "login")
	writeWSEnvelope(t, conn1, msgTypeMsg, "echo HISTORY\n")
	require.Contains(t, readMsgUntil(t, conn1, "HISTORY"), "HISTORY")
	_ = conn1.Close()
	time.Sleep(200 * time.Millisecond) // 等后端 detach

	// 第二段连接：同一 session_id，登录后立即输入新命令
	conn2, _, err := websocket.DefaultDialer.Dial(wsURL(t, httpSrv.URL, "/ws/ssh"), nil)
	require.NoError(t, err)
	defer func() { _ = conn2.Close() }()
	writeWSEnvelope(t, conn2, msgTypeLogin, loginFrame{Node: node, SessionID: sessionID, Cols: 80, Rows: 24})
	writeWSEnvelope(t, conn2, msgTypeMsg, "echo LATE\n")

	var frames []string
	for {
		msg := readWSJSON(t, conn2)
		if msg["type"] != "msg" {
			continue
		}
		text, _ := msg["data"].(string)
		frames = append(frames, text)
		if strings.Contains(text, "LATE") {
			break
		}
	}

	historyAt, lateAt := indexOfContains(frames, "HISTORY"), indexOfContains(frames, "LATE")
	require.NotEqual(t, -1, historyAt, "replayed scrollback must reach the reattached client")
	assert.Less(t, historyAt, lateAt, "history must be replayed before live output")
}

// readMsgUntil 读取终端输出帧直到内容包含 want。
func readMsgUntil(t *testing.T, conn *websocket.Conn, want string) string {
	t.Helper()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(10*time.Second)))
	for {
		msg := readWSJSON(t, conn)
		if msg["type"] != "msg" {
			continue
		}
		if text, _ := msg["data"].(string); strings.Contains(text, want) {
			return text
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
