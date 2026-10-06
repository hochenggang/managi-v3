// Package handler - SFTP 通道：一路文件管理标签。
// 控制面负责目录操作与「开始上传/开始下载」，字节本身一律走数据面二进制帧。
// 所有会阻塞的操作（sftp 调用、上传落盘）都在本通道的工作协程上执行（input_queue.go），
// 读循环只入队——远端慢只会拖住这条通道，不会冻住整条连接。
package handler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"sync/atomic"

	"managi/internal/model"
	"managi/internal/sftp"
	"managi/internal/sshpool"
	"managi/internal/wire"
)

// sftpChannel 一路文件管理：一条 sftp 会话 + 输入队列 + 上传/下载状态。
// upload/dlDone/dlStop 只由工作协程读写（beginUpload、startDownload、teardown），
// 因此不需要加锁；读循环只碰 input 与 released。
type sftpChannel struct {
	sc      *sftp.Client
	sshConn *sshpool.Connection
	chanID  uint32
	input   *inputQueue
	// released 已从输入窗口释放的字节数（落盘成功的与作废的），见 ackData
	released atomic.Int64

	upload *sftp.Upload // 非 nil：正在接收数据面上的上传分片

	dlDone chan struct{}      // 非 nil 且未关闭：有一路下载在跑
	dlStop context.CancelFunc // 连接结束时停掉下载 pump

	// aborted 输入溢出已终止本通道：错误已报过一次，随后的写失败不再重复出声。
	aborted atomic.Bool
}

// sftpRequest SFTP 通道控制帧负载（按动词取用不同字段）。
// 切片大小不由客户端决定：服务端在 upload 响应里下发 chunk_size，客户端按它切帧。
// upload 的 Size+Mtime 一起构成 .part 的续传身份：两者都吻合才会从旧残片续上。
type sftpRequest struct {
	Chan     uint32 `json:"chan"`
	Path     string `json:"path,omitempty"`
	Filename string `json:"filename,omitempty"`
	Size     int64  `json:"size,omitempty"`
	Mtime    int64  `json:"mtime,omitempty"`
	Offset   int64  `json:"offset,omitempty"`
}

// SFTP 各动词的响应体：一个动词一份契约。
// 不用「一个大结构 + 全字段 omitempty」糊过去：offset=0、total=0 都是合法值，
// 字段一省略，前端就得靠猜——猜错的表现是续传点错位或空文件被判为失败。
type (
	lsResponse struct {
		Chan  uint32           `json:"chan"`
		Path  string           `json:"path"`
		Files []model.FileItem `json:"files,omitempty"` // 空目录省略 files 无歧义：没有条目就是没有
	}
	// pathResponse 是 mkdir/rm 的回声：只需确认操作落在这个路径上。
	pathResponse struct {
		Chan uint32 `json:"chan"`
		Path string `json:"path"`
	}
	uploadResponse struct {
		Chan      uint32 `json:"chan"`
		Offset    int64  `json:"offset"`
		ChunkSize int    `json:"chunk_size"`
	}
	downloadResponse struct {
		Chan     uint32 `json:"chan"`
		Filename string `json:"filename"`
		Total    int64  `json:"total"`
	}
)

// openSFTP 为一路文件管理标签建立 sftp 会话（复用连接池里的 SSH 连接）。
func (h *hub) openSFTP(seq int64, node model.Node) {
	sshConn, err := h.pool.Get(node)
	if err != nil {
		_ = h.wc.writeError(0, seq, "open sftp: "+err.Error())
		return
	}
	sc, err := sftp.New(sshConn.Client())
	if err != nil {
		h.pool.Release(sshConn)
		_ = h.wc.writeError(0, seq, "open sftp: "+err.Error())
		return
	}
	chanID := h.alloc()
	c := &sftpChannel{
		sc:      sc,
		sshConn: sshConn,
		chanID:  chanID,
		input:   newInputQueue(h.inputBudget()),
	}
	h.chans[chanID] = &channel{id: chanID, handler: c}
	go c.runInput(h)

	// home 是 sftp 子系统的初始目录（通常为主目录）：硬编码 "/" 会让无根目录读权限的
	// 账号一进来就报错，故由服务端报给客户端作为起始路径。
	// window 是输入窗口（上传分片按它节流），见 stream.go。
	_ = h.wc.writeEnvelope(msgOpen, openResponse{
		Kind: kindSFTP, Chan: chanID, Home: sc.Home(), Window: h.inputWindow(),
	}, seq)
}

// runInput 通道工作协程：数据帧与目录/传输动词都在这条协程上顺序执行。
// pkg/sftp 本身并发安全，但上传句柄（Write/Abort/Finish）不是：
// 收进一条协程后顺序由队列给定，不需要额外加锁，也不会因远端慢而占住读循环。
func (c *sftpChannel) runInput(h *hub) {
	wc := h.wc
	for {
		op, ok := c.input.pop()
		if !ok {
			break
		}
		switch op.kind {
		case opFrame:
			c.consumeData(wc, op.frame)
		case opControl:
			c.consumeControl(h, wc, op.env)
		}
	}
	c.teardown(h)
}

// teardown 收尾：中止未完成的上传（保留 .part 供续传）、停掉下载、归还 SSH 连接。
// SFTP 会话由 close 负责掐断（见下），这里只处理本地资源。
func (c *sftpChannel) teardown(h *hub) {
	if c.upload != nil {
		c.upload.Abort()
		c.upload = nil
	}
	if c.dlStop != nil {
		c.dlStop()
	}
	h.pool.Release(c.sshConn)
}

// writeData 读循环入口：非阻塞入队。
// 队列满意味着这路传输已不可信（按窗口节流的客户端不可能触发），
// 终止整条通道而不是丢帧——丢一帧上传就悄悄坏了文件。
func (c *sftpChannel) writeData(wc *wsConn, f wire.Frame) bool {
	if c.input.pushFrame(f) {
		return true
	}
	c.abortInput(wc, len(f.Payload))
	return false
}

// abortInput 输入溢出终止：作废队列、补偿窗口并报错，让读循环摘除本通道。
// 补偿 ack 必须发：客户端的上传循环正等在对端 ack 上，没有它就连错误也看不到。
func (c *sftpChannel) abortInput(wc *wsConn, dropped int) {
	c.aborted.Store(true)
	total := c.released.Add(int64(dropped + c.input.clear()))
	c.input.close() // 关停工作协程；排在事务里的分片不再落盘
	_ = wc.writeError(c.chanID, 0, "上传输入积压超过窗口，通道已中止；重开发送端后可自动续传")
	_ = wc.writeEnvelope(msgAck, ackData{Chan: c.chanID, Bytes: total}, 0)
}

// control 读循环入口：动词入队交给工作协程执行。
func (c *sftpChannel) control(wc *wsConn, env wsEnvelope) {
	if c.input.pushControl(env) {
		return
	}
	if env.Type == msgResize {
		_ = wc.writeError(c.chanID, env.Seq, "resize: not a pty channel")
		return
	}
	// 请求都带 seq：必须回话，否则对端的 request() 会一直等到超时
	_ = wc.writeError(c.chanID, env.Seq, env.Type+": 通道输入积压，请稍后重试")
}

// consumeData 处理一帧上传分片：落盘；FlagEnd 表示写完并请求落定。
// 没有活动上传时收到数据帧属协议误用：出声即可（别的通道还在正常干活）。
func (c *sftpChannel) consumeData(wc *wsConn, f wire.Frame) {
	payload := len(f.Payload)
	switch {
	case c.upload == nil:
		_ = wc.writeError(f.Chan, 0, "no upload in progress on this channel")
	case payload > 0:
		if _, err := c.upload.Write(f.Payload); err != nil {
			c.upload.Abort() // .part 保留，重开通道即可续传
			c.upload = nil
			// 通道正因溢出终止时错误已报过（abortInput）：这次写失败是它的余波，不再重复出声
			if !c.aborted.Load() {
				_ = wc.writeError(f.Chan, 0, err.Error())
			}
		}
	}
	// 落盘成败都推进窗口：失败这路已经作废，让客户端从错误里退出，
	// 而不是死等一个永远不会到的 ack（作废字节对窗口同样算已消费）。
	if payload > 0 {
		c.release(wc, int64(payload))
	}
	if f.Flags&wire.FlagEnd == 0 || c.upload == nil {
		return
	}
	u := c.upload
	c.upload = nil
	size, err := u.Finish()
	if err != nil {
		_ = wc.writeError(f.Chan, 0, err.Error())
		return
	}
	_ = wc.writeEnvelope(msgUploadEnd, map[string]any{"chan": f.Chan, "size": size}, 0)
}

// release 推进输入窗口并回 ack（窗口语义见 ackData）。
func (c *sftpChannel) release(wc *wsConn, n int64) {
	_ = wc.writeEnvelope(msgAck, ackData{Chan: c.chanID, Bytes: c.released.Add(n)}, 0)
}

// consumeControl 工作协程上执行控制动词：解析请求、执行并回响应。
func (c *sftpChannel) consumeControl(h *hub, wc *wsConn, env wsEnvelope) {
	var req sftpRequest
	if err := decodeData(env.Data, &req); err != nil {
		_ = wc.writeError(0, env.Seq, "invalid "+env.Type+" data: "+err.Error())
		return
	}

	switch env.Type {
	case msgLS:
		items, err := c.sc.List(req.Path)
		c.reply(wc, env, req.Chan, lsResponse{Chan: req.Chan, Path: req.Path, Files: items}, err)

	case msgMkdir:
		err := requirePath(req.Path, "mkdir")
		if err == nil {
			err = c.sc.Mkdir(req.Path)
		}
		c.reply(wc, env, req.Chan, pathResponse{Chan: req.Chan, Path: req.Path}, err)

	case msgRM:
		err := requirePath(req.Path, "rm")
		if err == nil {
			err = c.sc.Delete(req.Path)
		}
		c.reply(wc, env, req.Chan, pathResponse{Chan: req.Chan, Path: req.Path}, err)

	case msgUpload:
		body, err := c.beginUpload(req, h.inFrameSize())
		c.reply(wc, env, req.Chan, body, err)

	case msgDownload:
		c.startDownload(h, wc, env, req)

	case msgResize:
		_ = wc.writeError(req.Chan, env.Seq, "resize: not a pty channel")
	}
}

// reply 统一回响应：成功回同 type、同 seq 的 body，失败回 error 帧。
// chan 同时出现在响应体和 error 里：前端两种路径都能定位到出事的那路标签页。
func (c *sftpChannel) reply(wc *wsConn, env wsEnvelope, chanID uint32, body any, err error) {
	if err != nil {
		_ = wc.writeError(chanID, env.Seq, err.Error())
		return
	}
	_ = wc.writeEnvelope(env.Type, body, env.Seq)
}

// requirePath 目录类动词的路径卫语句：空路径只会让 sftp 报出看不懂的错，先在入口挡住。
func requirePath(p, verb string) error {
	if p == "" {
		return fmt.Errorf("%s: path is required", verb)
	}
	return nil
}

// beginUpload 按 (size, mtime) 身份打开 .part 并回续传点与切片大小；此后该通道上的数据帧直接落盘。
// 收到第二个 upload 请求即认定前一路已被客户端放弃：一条通道只有一个写入者，
// 且帧有序，所以「没发 FlagEnd 就再开一路」只可能是中途出错/断线后的重试。
// 此时接管而不是拒绝，否则客户端在整条连接余生里都传不了文件（只能重开标签页）。
func (c *sftpChannel) beginUpload(req sftpRequest, chunkSize int) (uploadResponse, error) {
	if c.upload != nil {
		c.upload.Abort() // 落盘的字节留在 .part 里，新请求会从该偏移续上
		c.upload = nil
	}
	if req.Path == "" {
		return uploadResponse{}, errors.New("upload: path is required")
	}
	u, err := c.sc.BeginUpload(req.Path, req.Filename, req.Size, req.Mtime)
	if err != nil {
		return uploadResponse{}, err
	}
	c.upload = u
	// chunk_size 下发给客户端定切片：分片大小全仓只有 config 一处定义
	return uploadResponse{Chan: req.Chan, Offset: u.Offset(), ChunkSize: chunkSize}, nil
}

// startDownload 先写出 download 响应，再把数据交给 pump 推送。
// 顺序不能反：数据帧排在响应之前时，前端无从知道这路字节属于哪一次请求。
func (c *sftpChannel) startDownload(h *hub, wc *wsConn, env wsEnvelope, req sftpRequest) {
	if c.dlDone != nil && !isClosed(c.dlDone) {
		c.reply(wc, env, req.Chan, nil, errors.New("download already running on this channel"))
		return
	}
	if req.Path == "" {
		c.reply(wc, env, req.Chan, nil, errors.New("download: path is required"))
		return
	}
	reader, info, err := c.sc.OpenRead(req.Path)
	if err != nil {
		c.reply(wc, env, req.Chan, nil, err)
		return
	}
	// 续传点由客户端给（断线重发同一文件时从已收到的字节数继续），越界即判失败
	if req.Offset > 0 {
		if _, err := reader.Seek(req.Offset, io.SeekStart); err != nil {
			_ = reader.Close()
			c.reply(wc, env, req.Chan, nil, err)
			return
		}
	}
	c.reply(wc, env, req.Chan, downloadResponse{
		Chan: req.Chan, Filename: path.Base(req.Path), Total: info.Size(),
	}, nil)

	ctx, cancel := context.WithCancel(h.ctx)
	done := make(chan struct{})
	c.dlDone, c.dlStop = done, cancel
	go func() {
		defer close(done)
		if err := pump(ctx, wc, req.Chan, reader, h.outFrameSize()); err != nil {
			// 流被截断必须出声：只补 error、不补 FlagEnd，前端据此判定失败而非当成传完
			_ = wc.writeError(req.Chan, 0, "download: "+err.Error())
			slog.Debug("download pump stopped", "chan", req.Chan, "err", err)
		}
	}()
}

// close 停止工作协程并掐断 SFTP 会话，让阻塞中的写入（上传落盘、下载读）尽快出错返回；
// 本地资源的回收在 runInput 退出路径（release）完成。
// sc.Close 走独立协程：关闭本身可能阻塞在半死连接的写侧，读循环不能在这里停摆。
func (c *sftpChannel) close(h *hub) {
	c.input.close()
	go func() {
		if err := c.sc.Close(); err != nil {
			slog.Debug("sftp channel close error", "err", err)
		}
	}()
}

// isClosed 通道是否已关闭（此处用于判断下载 pump 是否已退出）。
func isClosed(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}
