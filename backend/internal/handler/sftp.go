// Package handler - WebSocket SFTP 端点 /ws/sftp 与 HTTP 下载端点。
// 对应 v2 routers.py 的 sftp_websocket_endpoint。
// 修复 v2 缺陷：上传/下载断点续传（design-v3.md §6.4 §6.5）。
package handler

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"managi/internal/config"
	"managi/internal/model"
	"managi/internal/sshpool"
	"managi/internal/sftp"
)

var sftpUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// sftpWSHandler WS /ws/sftp
// 协议（与 v2 兼容 + v3 扩展）：
//   1. 首帧: Node JSON
//   2. 服务端回 {type:"connected"}
//   3. 客户端发 FileOperationRequest JSON（TextMessage）
//      v2 操作: list/mkdir/delete/rename/move/upload/download
//      v3 扩展: upload_init/upload_complete（断点续传）
//   4. upload 分片数据通过 BinaryMessage 发送（二进制帧头协议，design-v3.md §6.4）
func sftpWSHandler(pool *sshpool.Pool, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := sftpUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return // Upgrade 已写错误响应
		}
		defer conn.Close()

		// 首帧认证
		_, msg, err := conn.ReadMessage()
		if err != nil {
			writeSftpError(conn, "read auth frame: "+err.Error())
			return
		}
		var node model.Node
		if err := json.Unmarshal(msg, &node); err != nil {
			writeSftpError(conn, "invalid node json: "+err.Error())
			return
		}

		sshConn, err := pool.Get(node)
		if err != nil {
			writeSftpError(conn, "ssh connect: "+err.Error())
			return
		}
		defer pool.Release(node)

		sc, err := sftp.New(node, sshConn.Client())
		if err != nil {
			writeSftpError(conn, "sftp init: "+err.Error())
			return
		}
		defer sc.Close()

		writeSftpMsg(conn, map[string]any{"type": "connected"})

		// 心跳超时检测：收到任意消息即刷新 deadline（design-v3.md §6.3）
		deadline := time.Duration(cfg.WSReadDeadline) * time.Second
		if deadline <= 0 {
			deadline = 60 * time.Second
		}
		_ = conn.SetReadDeadline(time.Now().Add(deadline))

		for {
			msgType, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			// 收到任意消息刷新读超时（前端 ping/业务帧均视为活跃）
			_ = conn.SetReadDeadline(time.Now().Add(deadline))
			if msgType == websocket.BinaryMessage {
				handleBinaryChunk(conn, sc, data)
				continue
			}
			if msgType != websocket.TextMessage {
				continue // 忽略其他类型帧
			}
			// 先解析为 raw map 识别 ping 心跳帧（与 useWebSocket.ts startHeartbeat 对齐）
			var raw map[string]any
			if json.Unmarshal(data, &raw) == nil {
				if t, _ := raw["type"].(string); t == "ping" {
					writeSftpMsg(conn, map[string]any{"type": "pong"})
					continue
				}
			}
			var req model.FileOperationRequest
			if err := json.Unmarshal(data, &req); err != nil {
				writeSftpError(conn, "invalid request: "+err.Error())
				continue
			}
			handleSftpOp(conn, sc, &req)
		}
	}
}

// handleSftpOp 分发单个 SFTP 操作。
func handleSftpOp(conn *websocket.Conn, sc *sftp.Client, req *model.FileOperationRequest) {
	switch req.Operation {
	case model.OpList:
		items, err := sc.List(req.RemotePath)
		if err != nil {
			writeSftpError(conn, err.Error())
			return
		}
		writeSftpMsg(conn, map[string]any{"type": "list", "files": items})

	case model.OpMkdir:
		if err := sc.Mkdir(req.RemotePath); err != nil {
			writeSftpError(conn, err.Error())
			return
		}
		writeSftpMsg(conn, map[string]any{"type": "ok"})

	case model.OpDelete:
		if err := sc.Delete(req.RemotePath); err != nil {
			writeSftpError(conn, err.Error())
			return
		}
		writeSftpMsg(conn, map[string]any{"type": "ok"})

	case model.OpRename:
		if err := sc.Rename(req.RemotePath, req.NewPath); err != nil {
			writeSftpError(conn, err.Error())
			return
		}
		writeSftpMsg(conn, map[string]any{"type": "ok"})

	case model.OpUploadInit:
		uploadID, offset, err := sc.UploadInit(req.RemotePath, req.Filename, req.TotalSize, req.ChunkSize)
		if err != nil {
			writeSftpError(conn, err.Error())
			return
		}
		writeSftpMsg(conn, map[string]any{
			"type":      "upload_init",
			"upload_id": uploadID,
			"offset":    offset,
		})

	case model.OpUploadChunk:
		// v3 协议：分片数据通过 BinaryMessage 二进制帧头发送，不再走 JSON。
		// 保留此 case 仅作错误提示，避免客户端误用旧协议。
		writeSftpError(conn, "upload_chunk must use binary frame protocol")

	case model.OpUploadDone:
		if err := sc.UploadComplete(req.UploadID); err != nil {
			writeSftpError(conn, err.Error())
			return
		}
		writeSftpMsg(conn, map[string]any{"type": "ok"})

	case model.OpDownload:
		reader, total, err := sc.DownloadStream(req.RemotePath, req.Offset)
		if err != nil {
			writeSftpError(conn, err.Error())
			return
		}
		defer reader.Close()
		// 先发 download_start 含总大小，供前端计算进度
		writeSftpMsg(conn, map[string]any{"type": "download_start", "total": total})
		// 流式分块发送，避免大文件 OOM
		buf := make([]byte, 32*1024)
		for {
			n, rerr := reader.Read(buf)
			if n > 0 {
				if werr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
					return
				}
			}
			if rerr == io.EOF {
				break
			}
			if rerr != nil {
				writeSftpError(conn, "read file: "+rerr.Error())
				return
			}
		}
		writeSftpMsg(conn, map[string]any{"type": "complete", "filename": path.Base(req.RemotePath), "complete": true})

	default:
		writeSftpError(conn, "unknown operation: "+string(req.Operation))
	}
}

// handleBinaryChunk 解析二进制分片帧并写入远程 .part 文件，回 chunk_ack。
// 帧格式（大端序）：[4字节 upload_id_len][upload_id][4字节 chunk_index][8字节 offset][8字节 data_len][data]
func handleBinaryChunk(conn *websocket.Conn, sc *sftp.Client, data []byte) {
	uploadID, chunkIndex, offset, chunkData, err := parseChunkFrame(data)
	if err != nil {
		writeSftpError(conn, "parse binary frame: "+err.Error())
		return
	}
	if err := sc.UploadChunk(uploadID, chunkIndex, offset, chunkData); err != nil {
		writeSftpError(conn, err.Error())
		return
	}
	writeSftpMsg(conn, map[string]any{"type": "chunk_ack", "chunk_index": chunkIndex})
}

// parseChunkFrame 解析二进制分片帧头。
func parseChunkFrame(data []byte) (uploadID string, chunkIndex int, offset int64, chunkData []byte, err error) {
	if len(data) < 4 {
		return "", 0, 0, nil, fmt.Errorf("frame too short: need header")
	}
	idLen := binary.BigEndian.Uint32(data[:4])
	headerLen := 4 + int(idLen) + 4 + 8 + 8
	if len(data) < headerLen {
		return "", 0, 0, nil, fmt.Errorf("frame header incomplete: need %d, got %d", headerLen, len(data))
	}
	pos := 4
	uploadID = string(data[pos : pos+int(idLen)])
	pos += int(idLen)
	chunkIndex = int(binary.BigEndian.Uint32(data[pos:]))
	pos += 4
	offset = int64(binary.BigEndian.Uint64(data[pos:]))
	pos += 8
	dataLen := binary.BigEndian.Uint64(data[pos:])
	pos += 8
	if uint64(len(data)-pos) < dataLen {
		return "", 0, 0, nil, fmt.Errorf("frame data incomplete: need %d, got %d", dataLen, len(data)-pos)
	}
	chunkData = data[pos : pos+int(dataLen)]
	return uploadID, chunkIndex, offset, chunkData, nil
}

func writeSftpMsg(conn *websocket.Conn, v any) {
	_ = conn.WriteJSON(v)
}

func writeSftpError(conn *websocket.Conn, msg string) {
	_ = conn.WriteJSON(map[string]any{"type": "error", "message": msg})
}

// sftpDownloadHandler GET /api/sftp/download?node=...&path=...
// v3 新增：HTTP Range 下载，支持断点续传。
// 设计见 design-v3.md §6.5。
func sftpDownloadHandler(pool *sshpool.Pool, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		nodeStr := r.URL.Query().Get("node")
		remotePath := r.URL.Query().Get("path")
		if nodeStr == "" || remotePath == "" {
			http.Error(w, "missing node or path", http.StatusBadRequest)
			return
		}
		var node model.Node
		if err := json.Unmarshal([]byte(nodeStr), &node); err != nil {
			http.Error(w, "invalid node json: "+err.Error(), http.StatusBadRequest)
			return
		}

		sshConn, err := pool.Get(node)
		if err != nil {
			http.Error(w, "ssh connect: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer pool.Release(node)

		sc, err := sftp.New(node, sshConn.Client())
		if err != nil {
			http.Error(w, "sftp init: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer sc.Close()

		offset := parseRangeOffset(r.Header.Get("Range"))
		reader, total, err := sc.DownloadStream(remotePath, offset)
		if err != nil {
			http.Error(w, "sftp open: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer reader.Close()

		w.Header().Set("Content-Type", "application/octet-stream")
		// 仅当 offset 有效且 total 大于 offset 时才发 206，避免空文件生成非法 Content-Range
		if offset > 0 && total > offset {
			w.Header().Set("Content-Range", "bytes "+strconv.FormatInt(offset, 10)+"-"+strconv.FormatInt(total-1, 10)+"/"+strconv.FormatInt(total, 10))
			w.Header().Set("Accept-Ranges", "bytes")
			w.WriteHeader(http.StatusPartialContent)
		} else {
			w.Header().Set("Accept-Ranges", "bytes")
			w.WriteHeader(http.StatusOK)
		}
		_, _ = io.Copy(w, reader)
	}
}

// parseRangeOffset 解析 "bytes=offset-" 格式的 Range 头，返回 offset。
func parseRangeOffset(rangeHeader string) int64 {
	if rangeHeader == "" {
		return 0
	}
	// "bytes=10-" → 10
	parts := strings.SplitN(rangeHeader, "=", 2)
	if len(parts) != 2 || !strings.HasPrefix(parts[0], "bytes") {
		return 0
	}
	rangeSpec := strings.SplitN(parts[1], "-", 2)
	if len(rangeSpec) == 0 {
		return 0
	}
	offset, err := strconv.ParseInt(strings.TrimSpace(rangeSpec[0]), 10, 64)
	if err != nil {
		return 0
	}
	return offset
}
