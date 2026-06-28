// Package handler - WebSocket SSH 终端端点 /ws。
// 对应 v2 routers.py 的 websocket_endpoint。
// 修复 v2 缺陷：结构化 resize 消息 + Ping/Pong 心跳（design-v3.md §6.1 §6.3）。
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"managi/internal/config"
	"managi/internal/model"
	"managi/internal/sshpool"
	"managi/internal/terminal"
)

var terminalUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// resizeControl 结构化 resize 控制消息（修正 v2 转义序列解析）。
type resizeControl struct {
	Type string `json:"type"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

// terminalWSHandler WS /ws
// 协议（与 v2 兼容）：
//   1. 首帧: Node JSON（首包认证）
//   2. 后续: 双向字节流透传
// v3 增强:
//   - 结构化 resize 消息 {type:"resize",cols,rows} → session.WindowChange
//   - Ping/Pong 心跳（30s），SetReadDeadline 超时检测
func terminalWSHandler(pool *sshpool.Pool, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := terminalUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		// 首帧认证
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var node model.Node
		if err := json.Unmarshal(msg, &node); err != nil {
			return
		}

		sshConn, err := pool.Get(node)
		if err != nil {
			conn.WriteJSON(map[string]any{"type": "error", "message": err.Error()})
			return
		}
		defer pool.Release(node)

		sess := terminal.New(node, sshConn.Client())
		if err := sess.Open(80, 24); err != nil {
			conn.WriteJSON(map[string]any{"type": "error", "message": err.Error()})
			return
		}
		defer sess.Close()

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()

		// 心跳：每 30s 发 Ping，SetReadDeadline 超时检测
		deadline := time.Duration(cfg.WSReadDeadline) * time.Second
		if deadline <= 0 {
			deadline = 60 * time.Second
		}
		conn.SetReadDeadline(time.Now().Add(deadline))
		conn.SetPongHandler(func(string) error {
			conn.SetReadDeadline(time.Now().Add(deadline))
			return nil
		})

		// goroutine 1: stdout → ws
		go forwardOutput(ctx, conn, sess.Stdout(), cancel)

		// goroutine 2: ping ticker
		go pingLoop(ctx, conn, 30*time.Second)

		// 主循环: ws → stdin（识别 resize 控制消息）
		forwardInput(conn, sess, cancel)
	}
}

// forwardOutput 把 Shell 输出转发到 WS。
func forwardOutput(ctx context.Context, conn *websocket.Conn, stdout io.Reader, cancel context.CancelFunc) {
	defer cancel()
	buf := make([]byte, 4096)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		n, err := stdout.Read(buf)
		if n > 0 {
			if werr := conn.WriteMessage(websocket.TextMessage, buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// pingLoop 周期发送 Ping 保持心跳（修正 v2 心跳失效）。
func pingLoop(ctx context.Context, conn *websocket.Conn, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second)); err != nil {
				return
			}
		}
	}
}

// forwardInput 把 WS 输入转发到 Shell stdin，识别 resize 控制消息。
func forwardInput(conn *websocket.Conn, sess *terminal.Session, cancel context.CancelFunc) {
	defer cancel()
	stdin := sess.Stdin()
	for {
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if msgType == websocket.TextMessage && isResizeControl(data) {
			var rc resizeControl
			if json.Unmarshal(data, &rc) == nil && rc.Type == "resize" {
				_ = sess.Resize(rc.Cols, rc.Rows)
				continue
			}
		}
		if _, err := stdin.Write(data); err != nil {
			return
		}
	}
}

// isResizeControl 判断是否为结构化 resize 控制消息（前缀匹配，避免完整解析）。
// 修复 A16：容忍前导空白，避免协议脆弱（前端可能在 JSON 前误带空格/换行）。
func isResizeControl(data []byte) bool {
	prefix := []byte(`{"type":"resize"`)
	trimmed := bytes.TrimLeft(data, " \t\r\n")
	if len(trimmed) < len(prefix) {
		return false
	}
	return bytes.HasPrefix(trimmed, prefix)
}
