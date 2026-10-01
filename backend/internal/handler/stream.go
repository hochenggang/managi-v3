// Package handler - stream.go：把「按帧灌字节」这件事收在一处。
//
// 数据面只有两个方向，每个方向只有一个切片大小：
//   - 入站（客户端→服务端）：inFrameSize 是单帧载荷上限，open/upload 响应把它下发给前端，
//     wsReadLimit 据此设连接级硬闸，超限帧由 gorilla 直接断连（1009）。
//   - 出站（服务端→客户端）：outFrameSize 是 scrollback 回放与文件下载每帧写出的字节数。
//
// 两个大小都来自 config，且只在 config.Normalize 一处兜底默认值：
// 使用点再各写一遍「<=0 就用默认」会把同一个不变量散落到四个文件里。
package handler

import (
	"context"
	"errors"
	"fmt"
	"io"

	"managi/internal/config"
	"managi/internal/wire"
)

// inFrameSize 客户端→服务端每帧载荷上限。
func (h *hub) inFrameSize() int { return h.cfg.ChunkSize }

// outFrameSize 服务端→客户端每帧字节数（scrollback 回放与文件下载共用）。
func (h *hub) outFrameSize() int { return h.cfg.DownloadChunkSize }

// wsReadLimit 连接级单帧上限：一帧载荷最大为入站切片大小，再加上帧头与余量。
// 上限必须有，否则一条超大帧就能把内存打爆（分片大小已不再对外开放配置）。
func wsReadLimit(cfg *config.Config) int64 {
	return int64(wire.HeaderLen + cfg.ChunkSize + 4096)
}

// splitBytes 把切片按 chunk 分块，最后一块直接收到尾；空输入不分块。
// 空输入必须返回 nil：否则「回放空快照」会写出一帧零长度载荷，白跑一趟编解码。
func splitBytes(data []byte, chunk int) [][]byte {
	if len(data) == 0 {
		return nil
	}
	if chunk <= 0 { // 整块一帧发出
		chunk = len(data)
	}
	var out [][]byte
	for pos := 0; pos < len(data); pos += chunk {
		end := min(pos+chunk, len(data))
		out = append(out, data[pos:end])
	}
	return out
}

// pump 把 src 的字节按帧写到通道上，读到 EOF 后补一帧 FlagEnd 收尾。
// ctx 取消时关闭 src 解除 Read 阻塞；写失败直接返回（连接已不可用）。
// 读侧出错时「不补 FlagEnd」是有意为之：前端据此把这路判为失败，而不是当成传完。
func pump(ctx context.Context, fw frameWriter, chanID uint32, src io.ReadCloser, frameSize int) error {
	defer func() { _ = src.Close() }()
	stopped := make(chan struct{})
	defer close(stopped)
	go func() {
		select {
		case <-ctx.Done():
			_ = src.Close()
		case <-stopped:
		}
	}()

	buf := make([]byte, frameSize)
	f := wire.Frame{Chan: chanID}
	for {
		n, err := src.Read(buf)
		if n > 0 {
			f.Payload = buf[:n]
			if werr := fw.writeFrame(f); werr != nil {
				return werr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return fw.writeFrame(wire.End(chanID))
			}
			return fmt.Errorf("read source: %w", err)
		}
	}
}
