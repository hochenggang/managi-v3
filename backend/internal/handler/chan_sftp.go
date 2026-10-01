// Package handler - SFTP 通道：一路文件管理标签。
// 控制面负责目录操作与「开始上传/开始下载」，字节本身一律走数据面二进制帧。
package handler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"

	"managi/internal/model"
	"managi/internal/sftp"
	"managi/internal/sshpool"
	"managi/internal/wire"
)

// sftpChannel 一路文件管理：一条 sftp 会话 + 当前上传/下载状态。
// 字段只由读协程读写；下载 goroutine 只关闭交给它的 done 通道，不回写状态。
type sftpChannel struct {
	sc      *sftp.Client
	sshConn *sshpool.Connection

	upload *sftp.Upload // 非 nil：正在接收数据面上的上传分片

	dlDone chan struct{}      // 非 nil 且未关闭：有一路下载在跑
	dlStop context.CancelFunc // 连接结束时停掉下载 pump
}

// sftpRequest SFTP 通道控制帧负载（按动词取用不同字段）。
// 切片大小不由客户端决定：服务端在 upload 响应里下发 chunk_size，客户端按它切帧。
type sftpRequest struct {
	Chan     uint32 `json:"chan"`
	Path     string `json:"path,omitempty"`
	Filename string `json:"filename,omitempty"`
	Size     int64  `json:"size,omitempty"`
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
	h.chans[chanID] = &channel{id: chanID, handler: &sftpChannel{sc: sc, sshConn: sshConn}}
	// home 是 sftp 子系统的初始目录（通常为主目录）：硬编码 "/" 会让无根目录读权限的
	// 账号一进来就报错，故由服务端报给客户端作为起始路径。
	_ = h.wc.writeEnvelope(msgOpen, openResponse{Kind: kindSFTP, Chan: chanID, Home: sc.Home()}, seq)
}

// sftpOp 分发 SFTP 动词：解析请求、定位通道、执行并回响应。
func (h *hub) sftpOp(env wsEnvelope) {
	var req sftpRequest
	if err := decodeData(env.Data, &req); err != nil {
		_ = h.wc.writeError(0, env.Seq, "invalid "+env.Type+" data: "+err.Error())
		return
	}
	ch := h.get(req.Chan, env.Seq)
	if ch == nil {
		return
	}
	c, ok := ch.handler.(*sftpChannel)
	if !ok {
		_ = h.wc.writeError(req.Chan, env.Seq, env.Type+": not an sftp channel")
		return
	}

	switch env.Type {
	case msgLS:
		items, err := c.sc.List(req.Path)
		h.sftpReply(env, req.Chan, lsResponse{Chan: req.Chan, Path: req.Path, Files: items}, err)

	case msgMkdir:
		err := requirePath(req.Path, "mkdir")
		if err == nil {
			err = c.sc.Mkdir(req.Path)
		}
		h.sftpReply(env, req.Chan, pathResponse{Chan: req.Chan, Path: req.Path}, err)

	case msgRM:
		err := requirePath(req.Path, "rm")
		if err == nil {
			err = c.sc.Delete(req.Path)
		}
		h.sftpReply(env, req.Chan, pathResponse{Chan: req.Chan, Path: req.Path}, err)

	case msgUpload:
		body, err := c.beginUpload(req, h.inFrameSize())
		h.sftpReply(env, req.Chan, body, err)

	case msgDownload:
		c.startDownload(h, env, req)
	}
}

// sftpReply 统一回响应：成功回同 type、同 seq 的 body，失败回 error 帧。
// chan 同时出现在响应体和 error 里：前端两种路径都能定位到出事的那路标签页。
func (h *hub) sftpReply(env wsEnvelope, chanID uint32, body any, err error) {
	if err != nil {
		_ = h.wc.writeError(chanID, env.Seq, err.Error())
		return
	}
	_ = h.wc.writeEnvelope(env.Type, body, env.Seq)
}

// requirePath 目录类动词的路径卫语句：空路径只会让 sftp 报出看不懂的错，先在入口挡住。
func requirePath(p, verb string) error {
	if p == "" {
		return fmt.Errorf("%s: path is required", verb)
	}
	return nil
}

// beginUpload 打开 .part 并回续传点与切片大小；此后该通道上的数据帧直接落盘。
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
	u, err := c.sc.BeginUpload(req.Path, req.Filename, req.Size)
	if err != nil {
		return uploadResponse{}, err
	}
	c.upload = u
	// chunk_size 下发给客户端定切片：分片大小全仓只有 config 一处定义
	return uploadResponse{Chan: req.Chan, Offset: u.Offset(), ChunkSize: chunkSize}, nil
}

// writeData 处理该通道上的数据帧：上传分片落盘，FlagEnd 表示写完并请求落定。
// 没有活动上传时收到数据帧属协议误用：出声即可，不断连（别的通道还在正常干活）。
func (c *sftpChannel) writeData(wc *wsConn, f wire.Frame) {
	if c.upload == nil {
		_ = wc.writeError(f.Chan, 0, "no upload in progress on this channel")
		return
	}
	if len(f.Payload) > 0 {
		if _, err := c.upload.Write(f.Payload); err != nil {
			c.upload.Abort() // .part 保留，重开通道即可续传
			c.upload = nil
			_ = wc.writeError(f.Chan, 0, err.Error())
			return
		}
	}
	if f.Flags&wire.FlagEnd == 0 {
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

// startDownload 先写出 download 响应，再把数据交给 pump 推送。
// 顺序不能反：数据帧排在响应之前时，前端无从知道这路字节属于哪一次请求。
func (c *sftpChannel) startDownload(h *hub, env wsEnvelope, req sftpRequest) {
	if c.dlDone != nil && !isClosed(c.dlDone) {
		h.sftpReply(env, req.Chan, nil, errors.New("download already running on this channel"))
		return
	}
	if req.Path == "" {
		h.sftpReply(env, req.Chan, nil, errors.New("download: path is required"))
		return
	}
	reader, info, err := c.sc.OpenRead(req.Path)
	if err != nil {
		h.sftpReply(env, req.Chan, nil, err)
		return
	}
	// 续传点由客户端给（断线重发同一文件时从已收到的字节数继续），越界即判失败
	if req.Offset > 0 {
		if _, err := reader.Seek(req.Offset, io.SeekStart); err != nil {
			_ = reader.Close()
			h.sftpReply(env, req.Chan, nil, err)
			return
		}
	}
	h.sftpReply(env, req.Chan, downloadResponse{
		Chan: req.Chan, Filename: path.Base(req.Path), Total: info.Size(),
	}, nil)

	ctx, cancel := context.WithCancel(h.ctx)
	done := make(chan struct{})
	c.dlDone, c.dlStop = done, cancel
	go func() {
		defer close(done)
		if err := pump(ctx, h.wc, req.Chan, reader, h.outFrameSize()); err != nil {
			// 流被截断必须出声：只补 error、不补 FlagEnd，前端据此判定失败而非当成传完
			_ = h.wc.writeError(req.Chan, 0, "download: "+err.Error())
			slog.Debug("download pump stopped", "chan", req.Chan, "err", err)
		}
	}()
}

// close 释放通道资源：中止上传（保留 .part 供续传）、停掉下载、关会话、归还连接。
func (c *sftpChannel) close(h *hub) {
	if c.upload != nil {
		c.upload.Abort()
		c.upload = nil
	}
	if c.dlStop != nil {
		c.dlStop()
	}
	if err := c.sc.Close(); err != nil {
		slog.Debug("sftp channel close error", "err", err)
	}
	h.pool.Release(c.sshConn)
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
