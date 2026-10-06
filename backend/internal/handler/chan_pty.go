// Package handler - PTY 通道：一路终端标签。
// 控制面 open/resize/close 管生命周期；数据面把输入搬到 stdin、把 stdout 搬回输出帧。
// 两个方向都不占读循环：输入由本通道的工作协程消费（input_queue.go），输出走 outputLoop。
package handler

import (
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"managi/internal/wire"
)

// ptyDropNoticeInterval 输入溢出提示的最小间隔：错误帧也是 WS 写，
// 溢出期间逐帧报信会反过来占满写锁，淹掉真正的终端输出。
const ptyDropNoticeInterval = time.Second

// ptyChannel 一路终端：后端 shell 会话 + 本通道的输入队列与输出目的地。
type ptyChannel struct {
	ls   *liveSession
	sink *outSink
	// input 输入队列：读循环入队，runInput 出队写 stdin。
	input *inputQueue
	// released 已从输入窗口释放的字节数（写入 stdin 的 + 溢出丢弃的）。
	// 读循环丢弃与工作协程完成写入都会推进它，故用原子量；
	// 每次推进都回一帧 ack——丢弃不补偿，对端的窗口就会卡在等待里。
	released atomic.Int64

	// 以下仅读循环触碰（writeData 是其唯一入口）
	lastDropNotice time.Time
	droppedSince   int
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
	pty := &ptyChannel{
		ls:    ls,
		sink:  &outSink{wc: h.wc, chanID: chanID},
		input: newInputQueue(h.inputBudget()),
	}
	ch := &channel{id: chanID, handler: pty}
	h.chans[chanID] = ch
	go pty.runInput(h.wc)

	// chunk_size 随响应下发：前端据此切粘贴分片，不必猜服务端的单帧上限
	// （config.DefaultChunkSize；超限的帧会被 WS 读上限以 1009 掐断）。
	// window 是输入窗口：前端据此节流发送，见 stream.go。
	resp := openResponse{
		Kind: kindPTY, Chan: chanID, Reattached: reattached,
		ChunkSize: h.inFrameSize(), Window: h.inputWindow(),
	}
	if err := h.wc.writeOpen(chanID, resp, seq, h.outFrameSize(), func() []byte {
		return ls.attach(pty.sink)
	}); err != nil {
		// 写失败即连接已不可用，读循环下一帧就会退出并走 shutdown
		slog.Debug("pty open write failed", "chan", chanID, "err", err)
	}
}

// runInput 通道工作协程：顺序执行入队的输入帧与 resize，直到通道关闭。
// 阻塞的 stdin 写只困住这条协程：读循环、其它通道与心跳都不受影响。
func (c *ptyChannel) runInput(wc *wsConn) {
	for {
		op, ok := c.input.pop()
		if !ok {
			return
		}
		switch op.kind {
		case opFrame:
			c.writeStdin(wc, op.frame)
		case opControl:
			c.resize(wc, op.env)
		}
	}
}

// writeStdin 把一帧载荷原样写进 shell stdin。
// 字节不再经过 JSON 字符串，因此任意编码与控制字符都保持原样。
func (c *ptyChannel) writeStdin(wc *wsConn, f wire.Frame) {
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
		return
	}
	c.release(wc, int64(len(f.Payload)))
}

// release 推进输入窗口并回 ack（窗口语义见 ackData）。
func (c *ptyChannel) release(wc *wsConn, n int64) {
	_ = wc.writeEnvelope(msgAck, ackData{Chan: c.sink.chanID, Bytes: c.released.Add(n)}, 0)
}

// resize 调整 PTY 窗口。
// 失败只记日志：seq=0 不等回复，尺寸下次 fit 时会再发一次，不值得一次往返。
func (c *ptyChannel) resize(wc *wsConn, env wsEnvelope) {
	var req resizeRequest
	if err := decodeData(env.Data, &req); err != nil {
		_ = wc.writeError(0, env.Seq, "invalid resize data: "+err.Error())
		return
	}
	if err := c.ls.Session().Resize(req.Cols, req.Rows); err != nil {
		slog.Debug("terminal resize failed", "err", err, "cols", req.Cols, "rows", req.Rows)
	}
}

// writeData 读循环入口：非阻塞入队。
// 队列满说明对端没按窗口节流（超出窗口的客户端或旧前端）：丢弃本帧并限频出声——
// 终端输入丢一帧是可用性的代价，冻住整条连接不是。
func (c *ptyChannel) writeData(wc *wsConn, f wire.Frame) bool {
	if c.input.pushFrame(f) {
		return true
	}
	// 丢弃的字节同样推进窗口：否则对端的发送循环会死等一个永远不来的 ack
	c.release(wc, int64(len(f.Payload)))
	c.droppedSince += len(f.Payload)
	if time.Since(c.lastDropNotice) < ptyDropNoticeInterval {
		return true
	}
	c.lastDropNotice = time.Now()
	_ = wc.writeError(c.sink.chanID, 0,
		fmt.Sprintf("终端输入积压，已丢弃 %d 字节（请等远端消化后再粘贴）", c.droppedSince))
	c.droppedSince = 0
	return true
}

// control 读循环入口：resize 入队；SFTP 动词发到 PTY 通道上明确报错。
func (c *ptyChannel) control(wc *wsConn, env wsEnvelope) {
	switch env.Type {
	case msgResize:
		if !c.input.pushControl(env) {
			// resize 是幂等的自愈提示（前端下次 fit 会再发），丢一次不值得打断对端
			slog.Debug("pty resize dropped: input queue full", "chan", c.sink.chanID)
		}
	case msgLS, msgMkdir, msgRM, msgUpload, msgDownload:
		_ = wc.writeError(c.sink.chanID, env.Seq, env.Type+": not an sftp channel")
	}
}

// close 摘除本通道输出并停掉输入协程。shell 本身交给空闲计时器：TTL 内重开同 session_id 仍是同一个会话。
func (c *ptyChannel) close(h *hub) {
	h.mgr.detach(c.ls, c.sink)
	c.input.close()
}

// resizeRequest resize 负载。
type resizeRequest struct {
	Chan uint32 `json:"chan"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}
