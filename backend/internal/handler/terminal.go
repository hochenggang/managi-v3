// Package handler - WebSocket SSH 终端端点 /ws。
// v3 协议：统一 {type, data} envelope。
//   客户端 → 服务端：
//     - 首帧: {type:"login", data: Node}
//     - 输入: {type:"msg", data: "按键字符串"}
//     - 调整: {type:"resize", data: {cols, rows}}
//     - 心跳: {type:"ping"}
//   服务端 → 客户端：
//     - 登录结果: {type:"login", data: {success, message?}}
//     - 终端输出: {type:"msg", data: "输出字符串"}
//     - 错误: {type:"error", data: {message}}
//     - 心跳响应: {type:"pong"}
package handler

import (
	"context"
	"encoding/json"
	"errors"
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

var errLoginFrameExpected = errors.New(`expected login frame: {type:"login",data:Node}`)

// terminalWSHandler WS /ws
func terminalWSHandler(pool *sshpool.Pool, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := terminalUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		wc := newWSConn(conn)

		deadline := wsReadDeadline(cfg)

		node, err := readLoginFrame(wc, deadline)
		if err != nil {
			_ = wc.writeError(err.Error())
			return
		}

		sshConn, err := pool.Get(node)
		if err != nil {
			_ = wc.writeLoginResult(false, err.Error())
			return
		}
		defer pool.Release(node)

		sess := terminal.New(node, sshConn.Client())
		if err := sess.Open(80, 24); err != nil {
			_ = wc.writeLoginResult(false, err.Error())
			return
		}
		defer func() { _ = sess.Close() }()

		// 登录成功
		_ = wc.writeLoginResult(true, "")

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()

		// goroutine: stdout → ws（封装为 msg envelope）
		go forwardOutput(ctx, wc, sess.Stdout(), cancel)

		// 主循环: ws → stdin（识别 msg/resize/ping）
		forwardInput(wc, sess, cancel, deadline)
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

// readLoginFrame 读取首帧 login envelope，返回 Node。
func readLoginFrame(wc *wsConn, deadline time.Duration) (model.Node, error) {
	_ = wc.setReadDeadline(time.Now().Add(deadline))
	_, data, err := wc.readMessage()
	if err != nil {
		return model.Node{}, err
	}
	env, ok := parseEnvelope(data)
	if !ok || env.Type != msgTypeLogin {
		return model.Node{}, errLoginFrameExpected
	}
	var node model.Node
	if err := json.Unmarshal(env.Data, &node); err != nil {
		return model.Node{}, err
	}
	return node, nil
}

// forwardOutput 把 Shell 输出封装为 {type:"msg",data:"..."} 转发到 WS。
func forwardOutput(ctx context.Context, wc *wsConn, stdout io.Reader, cancel context.CancelFunc) {
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
			if werr := wc.writeMsg(string(buf[:n])); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
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
				_, _ = stdin.Write([]byte(s))
			}
		case msgTypeResize:
			var rc wsResizeData
			if json.Unmarshal(env.Data, &rc) == nil {
				_ = sess.Resize(rc.Cols, rc.Rows)
			}
		case msgTypePing:
			_ = wc.writePong()
		}
	}
}
