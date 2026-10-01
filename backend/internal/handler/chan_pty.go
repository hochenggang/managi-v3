// Package handler - PTY 通道：一路终端标签。
// 控制面 open/resize/close 管生命周期，数据面双向搬运：输入帧 → stdin，stdout → 输出帧。
package handler

import (
	"log/slog"

	"managi/internal/wire"
)

// ptyChannel 一路终端：后端 shell 会话 + 本通道的输出目的地。
type ptyChannel struct {
	ls   *liveSession
	sink *outSink
}

// openPTY 建立或复用后端 shell，并把「open 响应 → scrollback 回放 → 挂载输出」
// 放在同一把写锁里：前端因此先拿到响应，再看到历史输出，最后才看到实时输出，顺序不会倒。
func (h *hub) openPTY(seq int64, req openRequest) {
	ls, reattached, err := h.mgr.getOrCreate(req.SessionID, req.Node, req.Cols, req.Rows)
	if err != nil {
		_ = h.wc.writeError(0, seq, "open pty: "+err.Error())
		return
	}

	chanID := h.alloc()
	pty := &ptyChannel{ls: ls, sink: &outSink{wc: h.wc, chanID: chanID}}
	ch := &channel{id: chanID, handler: pty}
	h.chans[chanID] = ch

	// chunk_size 随响应下发：前端据此切粘贴分片，不必猜服务端的单帧上限
	// （config.DefaultChunkSize；超限的帧会被 WS 读上限以 1009 掐断）。
	resp := openResponse{Kind: kindPTY, Chan: chanID, Reattached: reattached, ChunkSize: h.inFrameSize()}
	if err := h.wc.writeOpen(chanID, resp, seq, h.outFrameSize(), func() []byte {
		return ls.attach(pty.sink)
	}); err != nil {
		// 写失败即连接已不可用，读循环下一帧就会退出并走 shutdown
		slog.Debug("pty open write failed", "chan", chanID, "err", err)
	}
}

// writeData 把数据帧载荷原样写进 shell stdin。
// 字节不再经过 JSON 字符串，因此任意编码与控制字符都保持原样。
func (c *ptyChannel) writeData(wc *wsConn, f wire.Frame) {
	if len(f.Payload) == 0 {
		return
	}
	stdin := c.ls.Session().Stdin()
	if stdin == nil {
		_ = wc.writeError(f.Chan, 0, "terminal session is closed")
		return
	}
	if _, err := stdin.Write(f.Payload); err != nil {
		// shell 已退出必须出声：否则用户只会看到「敲了没反应」
		_ = wc.writeError(f.Chan, 0, "terminal stdin: "+err.Error())
	}
}

// close 摘除本通道输出。shell 本身交给空闲计时器：TTL 内重开同 session_id 仍是同一个会话。
func (c *ptyChannel) close(h *hub) {
	h.mgr.detach(c.ls, c.sink)
}

// resizeRequest resize 负载。
type resizeRequest struct {
	Chan uint32 `json:"chan"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

// resize 调整 PTY 窗口。
// seq=0 时不等回复，因此尺寸漂移不值得一次往返；失败只记日志（前端无从处理）。
func (h *hub) resize(env wsEnvelope) {
	var req resizeRequest
	if err := decodeData(env.Data, &req); err != nil {
		_ = h.wc.writeError(0, env.Seq, "invalid resize data: "+err.Error())
		return
	}
	ch := h.get(req.Chan, env.Seq)
	if ch == nil {
		return
	}
	// resize 只对 PTY 有意义：种类由 handler 的具体类型说明，不必再额外存一份 kind 字段
	pty, ok := ch.handler.(*ptyChannel)
	if !ok {
		_ = h.wc.writeError(req.Chan, env.Seq, "resize: not a pty channel")
		return
	}
	if err := pty.ls.Session().Resize(req.Cols, req.Rows); err != nil {
		slog.Debug("terminal resize failed", "err", err, "cols", req.Cols, "rows", req.Rows)
	}
}
