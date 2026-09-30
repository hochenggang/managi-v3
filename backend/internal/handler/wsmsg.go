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
	msgTypeLogin         = "login"           // 登录（首帧）/ 登录结果
	msgTypeMsg           = "msg"             // 终端输入/输出
	msgTypeResize        = "resize"          // 终端尺寸调整
	msgTypePing          = "ping"            // 心跳请求
	msgTypePong          = "pong"            // 心跳响应
	msgTypeError         = "error"           // 错误
	msgTypeList          = "list"            // SFTP 列目录
	msgTypeOk            = "ok"              // SFTP 操作成功
	msgTypeDownloadStart = "download_start"  // SFTP 下载开始
	msgTypeComplete      = "complete"        // SFTP 下载完成
	msgTypeChunkAck      = "chunk_ack"       // SFTP 分片确认
	msgTypeUploadInit    = "upload_init"     // SFTP 上传初始化
	msgTypeUploadDone    = "upload_complete" // SFTP 上传完成
	msgTypeMkdir         = "mkdir"
	msgTypeDelete        = "delete"
	msgTypeRename        = "rename"
	msgTypeDownload      = "download"
)

// wsEnvelope 统一消息信封 {type, data, seq?}。
// seq 用于 SFTP 请求-响应关联：响应回填请求的 seq，前端据此丢弃迟到的旧响应；
// 无请求-响应语义的消息（终端输出/登录/心跳）保持 0，序列化时省略。
type wsEnvelope struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
	Seq  int64           `json:"seq,omitempty"`
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

// wsWriteDeadline 单次写超时。客户端页面冻结 / 网络半断时，WriteMessage 会一直阻塞在
// 内核发送缓冲区上；没有写超时，回放与输出协程会永久挂住并拖住整条会话。
const wsWriteDeadline = 30 * time.Second

// writeLocked 在所有写操作持 mu 的前提下统一加写超时。
// 写失败即关闭连接：gorilla 语义下写失败/超时后连接不可再用（半截帧已发出），
// 留着只会让对端一直等待，关掉才能让读侧及时退出并触发会话清理。
func (w *wsConn) writeLocked(fn func() error) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.conn.SetWriteDeadline(time.Now().Add(wsWriteDeadline))
	if err := fn(); err != nil {
		_ = w.conn.Close()
		return err
	}
	return nil
}

func (w *wsConn) writeJSON(v any) error {
	return w.writeLocked(func() error { return w.conn.WriteJSON(v) })
}

func (w *wsConn) writeRaw(messageType int, data []byte) error {
	return w.writeLocked(func() error { return w.conn.WriteMessage(messageType, data) })
}

// writeEnvelope 写入 {type, data} 消息。data 为 nil 时不带 data 字段。
func (w *wsConn) writeEnvelope(msgType string, data any) error {
	return w.writeEnvelopeSeq(msgType, data, 0)
}

// writeEnvelopeSeq 写入带 seq 的消息：SFTP 响应回填请求的 seq，前端据此丢弃迟到的旧响应。
// seq=0 时序列化省略，与旧协议兼容。
func (w *wsConn) writeEnvelopeSeq(msgType string, data any, seq int64) error {
	var dataBytes json.RawMessage
	if data != nil {
		b, err := json.Marshal(data)
		if err != nil {
			return err
		}
		dataBytes = b
	}
	return w.writeJSON(wsEnvelope{Type: msgType, Data: dataBytes, Seq: seq})
}

func (w *wsConn) writeError(message string) error {
	return w.writeEnvelope(msgTypeError, wsErrorData{Message: message})
}

// writeLoginResult 发送登录结果。reattached=true 表示复用了已存在的终端会话。
func (w *wsConn) writeLoginResult(success bool, message string, reattached bool) error {
	return w.writeEnvelope(msgTypeLogin, wsLoginResult{Success: success, Message: message, Reattached: reattached})
}

func (w *wsConn) writeMsg(data string) error {
	return w.writeEnvelope(msgTypeMsg, data)
}

func (w *wsConn) writePong() error {
	return w.writeEnvelope(msgTypePong, nil)
}

func (w *wsConn) writePing() error {
	// WriteControl 亦属写操作，gorilla/websocket 要求所有写（含控制帧）串行，
	// 必须复用 w.mu 与 writeJSON/writeRaw 互斥，否则并发写会破坏连接。
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.conn.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(10*time.Second))
}

// installPongHandler 安装「收到 Pong 即续期读超时」的回调。
// 必须在启动心跳协程之前、由读侧调用：gorilla 把 handler 存为连接上的普通字段，
// 与 ReadMessage 并发赋值属数据竞争。
func installPongHandler(wc *wsConn, deadline time.Duration) {
	wc.conn.SetPongHandler(func(string) error {
		return wc.setReadDeadline(time.Now().Add(deadline))
	})
}

// startPingLoop 启动服务端 WS Ping 循环：定期发送控制帧 Ping。
// Pong 回调由 installPongHandler 提前装好，此处只负责发包。
func startPingLoop(ctx context.Context, wc *wsConn, intervalSec int) {
	if intervalSec <= 0 {
		intervalSec = 30
	}
	interval := time.Duration(intervalSec) * time.Second
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
