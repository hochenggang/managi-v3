// Package handler - WS 协议层：控制面 JSON envelope + 数据面二进制帧。
//
// 一条 /ws 连接服务整个页面：每个终端标签占一路 PTY 通道，每个文件管理标签占一路
// SFTP 通道。两类帧各走各的平面：
//   - 文本帧 = 控制面：{type, data, seq}，一问一答，靠 seq 关联。
//   - 二进制帧 = 数据面（internal/wire）：[chan][flags][原始字节]，
//     PTY 输出、粘贴输入、文件分片都在此处，不经过 string→JSON。
//
// 与前端 protocol/ws.ts、protocol/frames.ts 一一对应。
package handler

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"managi/internal/wire"
)

// 通道种类（open.kind）。
const (
	kindPTY  = "pty"
	kindSFTP = "sftp"
)

// 控制面动词。
// 请求 seq>0 ⇒ 同 type、同 seq 回且只回一次；seq=0 ⇒ 不等回复（resize/ping）。
const (
	msgOpen      = "open"       // 打开通道（pty 或 sftp）
	msgClose     = "close"      // 关闭通道
	msgResize    = "resize"     // PTY 窗口尺寸
	msgPing      = "ping"       // 客户端心跳请求
	msgPong      = "pong"       // 服务端心跳响应
	msgError     = "error"      // 错误（带 chan 表示某通道上的流错误）
	msgLS        = "ls"         // 列目录
	msgMkdir     = "mkdir"      // 建目录
	msgRM        = "rm"         // 删除
	msgUpload    = "upload"     // 开始上传（后续数据走数据面）
	msgUploadEnd = "upload_end" // 上传落定（服务端主动推）
	msgDownload  = "download"   // 开始下载（数据走数据面，以 FlagEnd 收尾）
)

// wsEnvelope 控制面信封 {type, data, seq?}。
// seq 回填请求的 seq，前端据此丢弃迟到的旧响应；服务端主动推的消息 seq=0，序列化时省略。
type wsEnvelope struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
	Seq  int64           `json:"seq,omitempty"`
}

// wsErrorData 错误 data 负载。chan 为 0 表示与具体通道无关。
type wsErrorData struct {
	Chan    uint32 `json:"chan,omitempty"`
	Message string `json:"message"`
}

// wsUpgrader /ws 的升级器。
// 不在此配置缓冲与消息上限：handler 升级后、读首帧前会按自己的帧大小 SetReadLimit，
// 此处再配一份只会漂移成第二个真相。
var wsUpgrader = websocket.Upgrader{CheckOrigin: checkOrigin}

// wsConn 封装 *websocket.Conn：一把写锁保护所有写（含控制帧）。
// 读方法不加锁，由 hub 的单读协程独占。
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

// lock 持锁执行 fn：一次锁内可以连续写多帧，用于「响应 + 回放」这类必须成原子段的输出。
// 锁顺序：wc.mu → 通道自己的锁，通道锁内不做网络 I/O。
// 写失败即关闭连接：gorilla 语义下写失败/超时后连接不可再用（半截帧已发出），
// 留着只会让对端一直等待，关掉才能让读侧及时退出并触发会话清理。
func (w *wsConn) lock(fn func() error) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.conn.SetWriteDeadline(time.Now().Add(wsWriteDeadline))
	if err := fn(); err != nil {
		_ = w.conn.Close()
		return err
	}
	return nil
}

// writeJSON 写一个文本帧（调用方需未持锁；持锁时直接用 w.conn.WriteJSON）。
func (w *wsConn) writeJSON(v any) error {
	return w.lock(func() error { return w.conn.WriteJSON(v) })
}

// writeEnvelope 写控制面响应。data 为 nil 时不带 data 字段。
func (w *wsConn) writeEnvelope(msgType string, data any, seq int64) error {
	var raw json.RawMessage
	if data != nil {
		b, err := json.Marshal(data)
		if err != nil {
			return err
		}
		raw = b
	}
	return w.writeJSON(wsEnvelope{Type: msgType, Data: raw, Seq: seq})
}

// writeError 写错误响应/通知：seq 回填失败请求的 seq，chan 指出出问题的通道。
func (w *wsConn) writeError(chanID uint32, seq int64, message string) error {
	return w.writeEnvelope(msgError, wsErrorData{Chan: chanID, Message: message}, seq)
}

// frameWriter 数据面写出口：*wsConn 实现它，pump 因此能对着收集器单测。
type frameWriter interface {
	writeFrame(f wire.Frame) error
}

// writeFrame 写一个数据面二进制帧。
func (w *wsConn) writeFrame(f wire.Frame) error {
	return w.lock(func() error { return w.writeFrameLocked(f) })
}

// writeFrameLocked 编码并写出一帧。调用方必须持写锁。
func (w *wsConn) writeFrameLocked(f wire.Frame) error {
	buf := make([]byte, 0, wire.HeaderLen+len(f.Payload))
	return w.conn.WriteMessage(websocket.BinaryMessage, f.AppendTo(buf))
}

// writeOpen 原子写出「open 响应 + 回放快照」：二者之间不允许任何实时帧插队，
// 否则用户会先看到最新一行、再看到历史 scrollback。
// mount 在响应已发出后调用，负责挂载输出目的地并返回挂载前累积的快照；
// 此后实时帧都要抢同一把写锁，天然排在回放之后。
func (w *wsConn) writeOpen(chanID uint32, resp any, seq int64, frameSize int, mount func() []byte) error {
	return w.lock(func() error {
		if err := writeEnvelopeLocked(w.conn, msgOpen, resp, seq); err != nil {
			return err
		}
		frame := wire.Frame{Chan: chanID}
		for _, part := range splitBytes(mount(), frameSize) {
			frame.Payload = part
			if err := w.writeFrameLocked(frame); err != nil {
				return err
			}
		}
		return nil
	})
}

// writeEnvelopeLocked 在已持写锁的路径上写控制面帧（避免自锁）。
func writeEnvelopeLocked(conn *websocket.Conn, msgType string, data any, seq int64) error {
	var raw json.RawMessage
	if data != nil {
		b, err := json.Marshal(data)
		if err != nil {
			return err
		}
		raw = b
	}
	return conn.WriteJSON(wsEnvelope{Type: msgType, Data: raw, Seq: seq})
}

// startPingLoop 服务端 WS 心跳：定期发控制帧 Ping，避免浏览器后台定时器节流导致断连。
// Pong 回调由 installPongHandler 提前装好，此处只负责发包。
func startPingLoop(ctx context.Context, wc *wsConn, intervalSec int) {
	interval := time.Duration(intervalSec) * time.Second
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// PingMessage 亦属写操作，gorilla 要求所有写（含控制帧）串行，故复用写锁。
			if err := wc.lock(func() error {
				return wc.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second))
			}); err != nil {
				return
			}
		}
	}
}

// installPongHandler 安装「收到 Pong 即续期读超时」的回调。
// 必须在第一次 ReadMessage 之前调用：gorilla 把 handler 存为连接上的普通字段，
// 与 ReadMessage 并发赋值属数据竞争。
func installPongHandler(wc *wsConn, deadline time.Duration) {
	wc.conn.SetPongHandler(func(string) error {
		return wc.setReadDeadline(time.Now().Add(deadline))
	})
}

// parseEnvelope 解析控制面帧，失败返回 ok=false。
func parseEnvelope(data []byte) (env wsEnvelope, ok bool) {
	if err := json.Unmarshal(data, &env); err != nil {
		return wsEnvelope{}, false
	}
	return env, true
}

// decodeData 把 envelope.data 解进 dst；空 data 视作零值，由调用方的校验兜住。
func decodeData(raw json.RawMessage, dst any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, dst)
}
