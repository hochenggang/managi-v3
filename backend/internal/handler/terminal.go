// Package handler - WebSocket SSH 终端端点 /ws/ssh。
// v3 协议：统一 {type, data} envelope。
//
//	客户端 → 服务端：
//	  - 首帧: {type:"login", data:{node, session_id, cols, rows}}（兼容旧格式 data:Node）
//	  - 输入: {type:"msg", data: "按键字符串"}
//	  - 调整: {type:"resize", data: {cols, rows}}
//	  - 心跳: {type:"ping"}
//	服务端 → 客户端：
//	  - 登录结果: {type:"login", data: {success, message?, reattached?}}
//	  - 终端输出: {type:"msg", data: "输出字符串"}
//	  - 错误: {type:"error", data: {message}}
//	  - 心跳响应: {type:"pong"}
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"managi/internal/config"
	"managi/internal/model"
	"managi/internal/terminal"
)

var terminalUpgrader = websocket.Upgrader{
	CheckOrigin: checkOrigin,
}

var errLoginFrameExpected = errors.New(`expected login frame: {type:"login",data:{node,session_id}}`)

// terminalWSHandler WS /ws/ssh
// 通过 sessionManager 复用终端会话：前端 60s 内重连可恢复同一 shell。
func terminalWSHandler(mgr *sessionManager, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := terminalUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		// H3：限制 WS 消息大小，防止恶意客户端发送超大消息导致 OOM
		conn.SetReadLimit(64 * 1024) // 64KB，终端消息足够
		wc := newWSConn(conn)

		deadline := wsReadDeadline(cfg)

		lf, err := readLoginFrame(wc, deadline)
		if err != nil {
			_ = wc.writeError(err.Error())
			return
		}

		ls, reattached, err := mgr.AttachOrCreate(lf.SessionID, lf.Node, wc, lf.Cols, lf.Rows)
		if err != nil {
			_ = wc.writeLoginResult(false, err.Error(), false)
			return
		}
		// 前端断开时仅 detach，不关闭 shell（交给 60s 空闲计时器）
		defer mgr.Detach(ls, wc)

		// 登录成功（附带 reattached 标志，前端据此提示「已恢复会话」）
		_ = wc.writeLoginResult(true, "", reattached)

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()

		// 服务端 WS 心跳：控制帧 Ping，避免浏览器后台定时器节流导致断连
		go startPingLoop(ctx, wc, deadline, cfg.WSPingInterval)

		// 主循环: ws → stdin（识别 msg/resize/ping）
		forwardInput(wc, ls.Session(), cancel, deadline)
	}
}

// wsReadDeadline 返回读超时。
func wsReadDeadline(cfg *config.Config) time.Duration {
	d := time.Duration(cfg.WSReadDeadline) * time.Second
	if d <= 0 {
		d = 60 * time.Second
	}
	return d
}

// readLoginFrame 读取首帧 login envelope，返回 loginFrame。
// 兼容两种格式：
//   - 新格式: {type:"login", data:{node, session_id, cols, rows}}
//   - 旧格式: {type:"login", data:Node}（session_id 空，cols/rows 默认 80×24）
func readLoginFrame(wc *wsConn, deadline time.Duration) (loginFrame, error) {
	_ = wc.setReadDeadline(time.Now().Add(deadline))
	_, data, err := wc.readMessage()
	if err != nil {
		return loginFrame{}, err
	}
	env, ok := parseEnvelope(data)
	if !ok || env.Type != msgTypeLogin {
		return loginFrame{}, errLoginFrameExpected
	}
	// 先尝试新格式：{node, session_id, cols, rows}
	var lf loginFrame
	if err := json.Unmarshal(env.Data, &lf); err == nil && lf.Node.Host != "" {
		if lf.Cols <= 0 {
			lf.Cols = 80
		}
		if lf.Rows <= 0 {
			lf.Rows = 24
		}
		return lf, nil
	}
	// 回退旧格式：data 直接为 Node
	var node model.Node
	if err := json.Unmarshal(env.Data, &node); err != nil {
		return loginFrame{}, err
	}
	return loginFrame{Node: node, Cols: 80, Rows: 24}, nil
}

// forwardInput 把 WS 输入转发到 Shell stdin，识别 msg/resize/ping。
func forwardInput(wc *wsConn, sess *terminal.Session, cancel context.CancelFunc, deadline time.Duration) {
	defer cancel()
	stdin := sess.Stdin()
	for {
		msgType, data, err := wc.readMessage()
		if err != nil {
			return
		}
		_ = wc.setReadDeadline(time.Now().Add(deadline))
		if msgType != websocket.TextMessage {
			continue
		}
		env, ok := parseEnvelope(data)
		if !ok {
			continue
		}
		switch env.Type {
		case msgTypeMsg:
			var s string
			if json.Unmarshal(env.Data, &s) == nil {
				// 修复 T1：stdin 写入失败记录日志，便于诊断 shell 已关闭等场景
				if _, err := stdin.Write([]byte(s)); err != nil {
					slog.Debug("terminal stdin write failed", "err", err)
				}
			}
		case msgTypeResize:
			var rc wsResizeData
			if json.Unmarshal(env.Data, &rc) == nil {
				if err := sess.Resize(rc.Cols, rc.Rows); err != nil {
					slog.Debug("terminal resize failed", "err", err, "cols", rc.Cols, "rows", rc.Rows)
				}
			}
		case msgTypePing:
			_ = wc.writePong()
		}
	}
}
