// Package handler - WebSocket 消息协议定义。
// 所有 WS 文本帧统一为 {type, data} envelope，集中定义避免前后端协议漂移。
// 与前端 protocol/ws.ts 对齐。
package handler

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"managi/internal/model"
)

// WS 消息类型常量。
const (
	msgTypeLogin         = "login"          // 登录（首帧）/ 登录结果
	msgTypeMsg           = "msg"            // 终端输入/输出
	msgTypeResize        = "resize"         // 终端尺寸调整
	msgTypePing          = "ping"           // 心跳请求
	msgTypePong          = "pong"           // 心跳响应
	msgTypeError         = "error"          // 错误
	msgTypeList          = "list"           // SFTP 列目录
	msgTypeOk            = "ok"             // SFTP 操作成功
	msgTypeDownloadStart = "download_start" // SFTP 下载开始
	msgTypeComplete      = "complete"       // SFTP 下载完成
	msgTypeChunkAck      = "chunk_ack"      // SFTP 分片确认
	msgTypeUploadInit    = "upload_init"    // SFTP 上传初始化
	msgTypeUploadDone    = "upload_complete" // SFTP 上传完成
	msgTypeMkdir         = "mkdir"
	msgTypeDelete        = "delete"
	msgTypeRename        = "rename"
	msgTypeDownload      = "download"
)

// wsEnvelope 统一消息信封 {type, data}。
type wsEnvelope struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

// wsLoginResult 登录结果 data 负载。
type wsLoginResult struct {
	Success    bool   `json:"success"`
	Message    string `json:"message,omitempty"`
	Reattached bool   `json:"reattached,omitempty"` // true=复用了已存在的终端会话
}

// wsErrorData 错误 data 负载。
type wsErrorData struct {
	Message string `json:"message"`
}

// wsResizeData resize data 负载。
type wsResizeData struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

// loginFrame 新版 login 首帧 data 负载：{node, session_id, cols, rows}。
// 兼容旧格式（data 直接为 Node）：readLoginFrame 检测后回退。
type loginFrame struct {
	Node      model.Node `json:"node"`
	SessionID string     `json:"session_id"`
	Cols      int        `json:"cols"`
	Rows      int        `json:"rows"`
}

// wsConn 封装 *websocket.Conn，加互斥锁保护并发写。
// 读方法不加锁（由调用方保证单线程读）。
type wsConn struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func newWSConn(conn *websocket.Conn) *wsConn {
	return &wsConn{conn: conn}
}

func (w *wsConn) readMessage() (int, []byte, error) {
	return w.conn.ReadMessage()
}

func (w *wsConn) setReadDeadline(t time.Time) error {
	return w.conn.SetReadDeadline(t)
}

func (w *wsConn) writeJSON(v any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.conn.WriteJSON(v)
}

func (w *wsConn) writeRaw(msgType int, data []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.conn.WriteMessage(msgType, data)
}

// writeEnvelope 写入 {type, data} 消息。data 为 nil 时不带 data 字段。
func (w *wsConn) writeEnvelope(msgType string, data any) error {
	var dataBytes json.RawMessage
	if data != nil {
		b, err := json.Marshal(data)
		if err != nil {
			return err
		}
		dataBytes = b
	}
	return w.writeJSON(wsEnvelope{Type: msgType, Data: dataBytes})
}

func (w *wsConn) writeError(message string) error {
	return w.writeEnvelope(msgTypeError, wsErrorData{Message: message})
}

func (w *wsConn) writeLoginResult(success bool, message string) error {
	return w.writeEnvelope(msgTypeLogin, wsLoginResult{Success: success, Message: message})
}

func (w *wsConn) writeMsg(data string) error {
	return w.writeEnvelope(msgTypeMsg, data)
}

func (w *wsConn) writePong() error {
	return w.writeRaw(websocket.TextMessage, []byte(`{"type":"pong"}`))
}

func (w *wsConn) writePing() error {
	// 修复 B1：WriteControl 亦属写操作，gorilla/websocket 要求所有写（含控制帧）串行，
	// 必须复用 w.mu 与 writeJSON/writeRaw 互斥，否则并发写会破坏连接。
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.conn.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(10*time.Second))
}

func (w *wsConn) setPongHandler(h func(string) error) {
	w.conn.SetPongHandler(h)
}

// startPingLoop 启动服务端 WS Ping 循环：定期发送控制帧 Ping，并在收到 Pong 时重置读超时。
func startPingLoop(ctx context.Context, wc *wsConn, deadline time.Duration, intervalSec int) {
	if intervalSec <= 0 {
		intervalSec = 30
	}
	interval := time.Duration(intervalSec) * time.Second
	wc.setPongHandler(func(string) error {
		return wc.setReadDeadline(time.Now().Add(deadline))
	})
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := wc.writePing(); err != nil {
				return
			}
		}
	}
}

// parseEnvelope 解析 envelope，失败返回 ok=false。
func parseEnvelope(data []byte) (env wsEnvelope, ok bool) {
	if err := json.Unmarshal(data, &env); err != nil {
		return wsEnvelope{}, false
	}
	return env, true
}
