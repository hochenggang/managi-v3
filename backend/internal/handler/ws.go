// Package handler - WebSocket 端点 /ws：一条连接承载页面里的全部通道。
//
// 控制面（文本帧，JSON envelope {type, data, seq}）动词：
//
//	{type:"open",   data:{kind:"pty"|"sftp", node, session_id?, cols?, rows?}}  → {chan, kind, reattached?, home?, chunk_size?}
//	{type:"close",  data:{chan}}                                                → {chan}
//	{type:"resize", data:{chan, cols, rows}}                                    （seq=0，不等回复）
//	{type:"ls",     data:{chan, path}}                                          → {chan, path, files?}
//	{type:"mkdir"|"rm", data:{chan, path}}                                      → {chan, path}
//	{type:"upload", data:{chan, path, filename, size}}                          → {chan, offset, chunk_size}
//	{type:"download", data:{chan, path, offset?}}                               → {chan, filename, total}
//	{type:"ping"}                                                                → {type:"pong"}
//	服务端主动推：{type:"upload_end", data:{chan, size}}（seq 省略）
//	失败一律回 {type:"error", data:{chan?, message}}，seq 回填失败请求。
//
// 数据面（二进制帧，internal/wire）：
//   - 客户端 → 服务端：PTY 输入（写入 stdin）、上传分片（落进 .part）；
//     最后一片置 FlagEnd（上传即「写完请落定」）。
//   - 服务端 → 客户端：PTY 输出、下载内容；流结束时最后一片置 FlagEnd。
//
// 凭据只在请求体/内存里存在，服务端不落盘、不缓存到会话之外。
package handler

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"managi/internal/config"
	"managi/internal/model"
	"managi/internal/sshpool"
	"managi/internal/wire"
)

// wsReadDeadline 读超时：两次入站帧之间允许的最长间隔。
// 值由 config.Normalize 保证为正且大于心跳间隔，此处不再兜底。
func wsReadDeadline(cfg *config.Config) time.Duration {
	return time.Duration(cfg.WSReadDeadline) * time.Second
}

// wsHandler GET/WS /ws。
func wsHandler(mgr *sessionManager, pool *sshpool.Pool, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		conn.SetReadLimit(wsReadLimit(cfg))

		wc := newWSConn(conn)
		deadline := wsReadDeadline(cfg)
		// Pong 回调必须在第一次 ReadMessage 之前装好（见 installPongHandler）
		installPongHandler(wc, deadline)
		_ = wc.setReadDeadline(time.Now().Add(deadline))

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()

		// 服务端 WS 心跳：控制帧 Ping，避免浏览器后台定时器节流导致断连
		go startPingLoop(ctx, wc, cfg.WSPingInterval)

		h := &hub{cfg: cfg, pool: pool, mgr: mgr, wc: wc, ctx: ctx, cancel: cancel, chans: map[uint32]*channel{}}
		defer h.shutdown()
		h.serveReadLoop(deadline)
	}
}

// hub 一条 WS 连接的上下文与通道表。
type hub struct {
	cfg    *config.Config
	pool   *sshpool.Pool
	mgr    *sessionManager
	wc     *wsConn
	ctx    context.Context
	cancel context.CancelFunc

	// chans 只由读协程读写：控制帧与数据帧在同一条协程上分发，因此无需加锁。
	// 派生出的输出协程（PTY outputLoop、下载 pump）只持有 wc，不触碰这张表。
	chans map[uint32]*channel
	next  uint32
}

// channel 一路子通道：PTY（终端标签）或 SFTP（文件管理标签）。
// 两类通道对 hub 只需回答同样两个问题（上行的数据帧怎么处置、关闭时释放什么），
// 故表里只存一个 handler：不再用「哪个指针非空」表示种类，也就没有成片的 nil 判定。
type channel struct {
	id      uint32
	handler channelHandler
}

// channelHandler 一路子通道对 hub 的契约。
type channelHandler interface {
	// writeData 处置该通道上行的数据帧（PTY 输入 / 上传分片）。
	writeData(wc *wsConn, f wire.Frame)
	// close 释放该通道占用的资源（摘除输出 / 中止传输 / 归还连接）。
	close(h *hub)
}

// serveReadLoop 读帧并分发。返回即连接结束。
func (h *hub) serveReadLoop(deadline time.Duration) {
	for {
		msgType, data, err := h.wc.readMessage()
		if err != nil {
			return
		}
		_ = h.wc.setReadDeadline(time.Now().Add(deadline))
		switch msgType {
		case websocket.TextMessage:
			env, ok := parseEnvelope(data)
			if !ok {
				_ = h.wc.writeError(0, 0, "malformed control frame")
				continue
			}
			h.handleControl(env)
		case websocket.BinaryMessage:
			f, err := wire.Decode(data)
			if err != nil {
				// 帧头都解不出来 = 字节流已错位，留着只会让对端在错误的通道上等数据
				slog.Warn("dropping malformed data frame", "err", err)
				return
			}
			h.routeData(f)
		}
	}
}

// handleControl 按动词分发控制帧。
func (h *hub) handleControl(env wsEnvelope) {
	switch env.Type {
	case msgPing:
		_ = h.wc.writeEnvelope(msgPong, nil, env.Seq)
	case msgOpen:
		h.open(env)
	case msgClose:
		h.closeChannel(env)
	case msgResize:
		h.resize(env)
	case msgLS, msgMkdir, msgRM, msgUpload, msgDownload:
		h.sftpOp(env)
	case msgUploadEnd:
		// 客户端不会发这个动词：忽略并出声，避免前端误以为已经落定
		_ = h.wc.writeError(0, env.Seq, "upload_end is server-only")
	default:
		_ = h.wc.writeError(0, env.Seq, "unknown verb: "+env.Type)
	}
}

// alloc 分配一个通道号。0 保留给「与具体通道无关」的控制帧，故从 1 起。
func (h *hub) alloc() uint32 {
	h.next++
	return h.next
}

// get 按号取通道；不存在时回错误帧（客户端可能没收到 close 就继续发）。
func (h *hub) get(chanID uint32, seq int64) *channel {
	ch := h.chans[chanID]
	if ch == nil {
		_ = h.wc.writeError(chanID, seq, "unknown channel")
	}
	return ch
}

// open 处理 open 动词：按 kind 建 PTY 会话或 SFTP 会话。
func (h *hub) open(env wsEnvelope) {
	var req openRequest
	if err := decodeData(env.Data, &req); err != nil {
		_ = h.wc.writeError(0, env.Seq, "invalid open data: "+err.Error())
		return
	}
	if req.Node.Host == "" {
		_ = h.wc.writeError(0, env.Seq, "open: node.host is required")
		return
	}
	switch req.Kind {
	case kindPTY:
		h.openPTY(env.Seq, req)
	case kindSFTP:
		h.openSFTP(env.Seq, req.Node)
	default:
		_ = h.wc.writeError(0, env.Seq, `open: kind must be "pty" or "sftp", got `+req.Kind)
	}
}

// closeChannel 关闭一路通道：PTY 只摘输出（shell 交给空闲计时器），SFTP 释放会话。
func (h *hub) closeChannel(env wsEnvelope) {
	var req chanRequest
	if err := decodeData(env.Data, &req); err != nil {
		_ = h.wc.writeError(0, env.Seq, "invalid close data: "+err.Error())
		return
	}
	ch := h.get(req.Chan, env.Seq)
	if ch == nil {
		return
	}
	h.remove(ch.id)
	_ = h.wc.writeEnvelope(msgClose, map[string]any{"chan": ch.id}, env.Seq)
}

// remove 从表里摘掉通道并释放其资源（幂等）。
func (h *hub) remove(chanID uint32) {
	ch := h.chans[chanID]
	if ch == nil {
		return
	}
	delete(h.chans, chanID)
	ch.handler.close(h)
}

// shutdown 连接结束：摘除全部通道。PTY 会话按空闲 TTL 保留，供重连复用同一 shell。
func (h *hub) shutdown() {
	for id := range h.chans {
		h.remove(id)
	}
	h.cancel()
}

// routeData 把数据帧投递到所属通道。
func (h *hub) routeData(f wire.Frame) {
	ch := h.chans[f.Chan]
	if ch == nil {
		_ = h.wc.writeError(f.Chan, 0, "unknown channel")
		return
	}
	ch.handler.writeData(h.wc, f)
}

// chanRequest 只带通道号的请求（close）。
type chanRequest struct {
	Chan uint32 `json:"chan"`
}

// openRequest open 请求负载。
// SessionID 相同则复用后端 shell（重连不断上下文）；留空则以节点连接键兜底。
type openRequest struct {
	Kind      string     `json:"kind"`
	Node      model.Node `json:"node"`
	SessionID string     `json:"session_id,omitempty"`
	Cols      int        `json:"cols,omitempty"`
	Rows      int        `json:"rows,omitempty"`
}

// openResponse open 响应负载。
// ChunkSize 只对 PTY 有意义（SFTP 的切片大小随 upload 响应下发），故 omitempty。
type openResponse struct {
	Kind       string `json:"kind"`
	Chan       uint32 `json:"chan"`
	Reattached bool   `json:"reattached,omitempty"`
	Home       string `json:"home,omitempty"`
	ChunkSize  int    `json:"chunk_size,omitempty"`
}
