// Package handler - WebSocket SFTP 端点 /ws/sftp 与 HTTP 下载端点。
// 对应 v2 routers.py 的 sftp_websocket_endpoint。
// 修复 v2 缺陷：上传/下载断点续传（design-v3.md §6.4 §6.5）。
package handler

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

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
//   3. 客户端发 FileOperationRequest JSON
//      v2 操作: list/mkdir/delete/rename/move/upload/download
//      v3 扩展: upload_init/upload_chunk/upload_complete（断点续传）
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

		for {
			msgType, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if msgType != websocket.TextMessage {
				continue // 忽略非 JSON 帧
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
		// data 通过 base64 携带在请求中（简化协议；二进制帧优化见 design-v3.md §6.4）
		raw, err := base64.StdEncoding.DecodeString(req.Filename) // 复用 Filename 字段携带 data（协议简化）
		if err != nil {
			writeSftpError(conn, "decode chunk data: "+err.Error())
			return
		}
		if err := sc.UploadChunk(req.UploadID, req.ChunkIndex, req.Offset, raw); err != nil {
			writeSftpError(conn, err.Error())
			return
		}
		writeSftpMsg(conn, map[string]any{"type": "chunk_ack", "chunk_index": req.ChunkIndex})

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
		buf, err := io.ReadAll(reader)
		if err != nil {
			writeSftpError(conn, "read file: "+err.Error())
			return
		}
		conn.WriteMessage(websocket.BinaryMessage, buf)
		_ = total

	default:
		writeSftpError(conn, "unknown operation: "+string(req.Operation))
	}
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
		if offset > 0 {
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
