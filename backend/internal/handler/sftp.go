// Package handler - WebSocket SFTP 端点 /ws/sftp 与 HTTP 下载端点。
// v3 协议：统一 {type, data} envelope。登录成功后主动列根目录。
//
//	客户端 → 服务端：
//	  - 首帧: {type:"login", data: Node}
//	  - {type:"list", data: {path}}
//	  - {type:"mkdir"|"delete", data: {path}}
//	  - {type:"rename", data: {old_path, new_path}}
//	  - {type:"download", data: {path, offset?}}
//	  - {type:"upload_init", data: {remote_path, filename, total_size, chunk_size}}
//	  - {type:"upload_complete", data: {upload_id}}
//	  - {type:"ping"}
//	  - 二进制分片帧（上传）
//	服务端 → 客户端：
//	  - {type:"login", data: {success, message?}}（成功后立即推送 list /）
//	  - {type:"list", data: {files, path}}
//	  - {type:"ok"}
//	  - {type:"error", data: {message}}
//	  - {type:"download_start", data: {total}}
//	  - {type:"complete", data: {filename}}
//	  - {type:"chunk_ack", data: {chunk_index}}
//	  - {type:"upload_init", data: {upload_id, offset}}
//	  - {type:"pong"}
package handler

import (
	"context"
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
	"managi/internal/sftp"
	"managi/internal/sshpool"
)

var sftpUpgrader = websocket.Upgrader{
	CheckOrigin: checkOrigin,
}

// sftpRequestData SFTP 操作 data 负载（按 type 解释不同字段）。
type sftpRequestData struct {
	Path      string `json:"path,omitempty"`
	OldPath   string `json:"old_path,omitempty"`
	NewPath   string `json:"new_path,omitempty"`
	Offset    int64  `json:"offset,omitempty"`
	UploadID  string `json:"upload_id,omitempty"`
	Filename  string `json:"filename,omitempty"`
	TotalSize int64  `json:"total_size,omitempty"`
	ChunkSize int    `json:"chunk_size,omitempty"`
	// 兼容字段：upload_init 使用 remote_path 表示目标目录
	RemotePath string `json:"remote_path,omitempty"`
}

// sftpWSHandler WS /ws/sftp
func sftpWSHandler(pool *sshpool.Pool, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := sftpUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		wc := newWSConn(conn)

		deadline := wsReadDeadline(cfg)
		_ = wc.setReadDeadline(time.Now().Add(deadline))

		lf, err := readLoginFrame(wc, deadline)
		if err != nil {
			_ = wc.writeError(err.Error())
			return
		}
		node := lf.Node

		sshConn, err := pool.Get(node)
		if err != nil {
			_ = wc.writeLoginResult(false, err.Error())
			return
		}
		defer pool.Release(node)

		sc, err := sftp.New(node, sshConn.Client())
		if err != nil {
			_ = wc.writeLoginResult(false, err.Error())
			return
		}
		defer func() { _ = sc.Close() }()

		// 登录成功
		_ = wc.writeLoginResult(true, "")

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()

		// 服务端 WS 心跳：控制帧 Ping
		go startPingLoop(ctx, wc, deadline, cfg.WSPingInterval)

		// 主动列根目录（修复：原 connected 后前端空白需手动刷新）
		listRoot(wc, sc, "/")

		for {
			msgType, data, err := wc.readMessage()
			if err != nil {
				return
			}
			_ = wc.setReadDeadline(time.Now().Add(deadline))
			if msgType == websocket.BinaryMessage {
				handleBinaryChunk(wc, sc, data)
				continue
			}
			if msgType != websocket.TextMessage {
				continue
			}
			env, ok := parseEnvelope(data)
			if !ok {
				continue
			}
			if env.Type == msgTypePing {
				_ = wc.writePong()
				continue
			}
			handleSftpOp(wc, sc, env)
		}
	}
}

// listRoot 列目录并推送 list 消息。失败时推送 error。
func listRoot(wc *wsConn, sc *sftp.Client, p string) {
	items, err := sc.List(p)
	if err != nil {
		_ = wc.writeError("list " + p + ": " + err.Error())
		return
	}
	_ = wc.writeEnvelope(msgTypeList, map[string]any{"files": items, "path": p})
}

// handleSftpOp 分发单个 SFTP 操作。
func handleSftpOp(wc *wsConn, sc *sftp.Client, env wsEnvelope) {
	var req sftpRequestData
	if len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, &req); err != nil {
			_ = wc.writeError("invalid request data: " + err.Error())
			return
		}
	}
	switch env.Type {
	case msgTypeList:
		items, err := sc.List(req.Path)
		if err != nil {
			_ = wc.writeError(err.Error())
			return
		}
		_ = wc.writeEnvelope(msgTypeList, map[string]any{"files": items, "path": req.Path})

	case msgTypeMkdir:
		if err := sc.Mkdir(req.Path); err != nil {
			_ = wc.writeError(err.Error())
			return
		}
		_ = wc.writeEnvelope(msgTypeOk, nil)

	case msgTypeDelete:
		if err := sc.Delete(req.Path); err != nil {
			_ = wc.writeError(err.Error())
			return
		}
		_ = wc.writeEnvelope(msgTypeOk, nil)

	case msgTypeRename:
		if err := sc.Rename(req.OldPath, req.NewPath); err != nil {
			_ = wc.writeError(err.Error())
			return
		}
		_ = wc.writeEnvelope(msgTypeOk, nil)

	case msgTypeUploadInit:
		// upload_init 用 remote_path 字段表示目标目录（与历史协议兼容）
		targetDir := req.RemotePath
		if targetDir == "" {
			targetDir = req.Path
		}
		uploadID, offset, err := sc.UploadInit(targetDir, req.Filename, req.TotalSize, req.ChunkSize)
		if err != nil {
			_ = wc.writeError(err.Error())
			return
		}
		_ = wc.writeEnvelope(msgTypeUploadInit, map[string]any{"upload_id": uploadID, "offset": offset})

	case msgTypeUploadDone:
		if err := sc.UploadComplete(req.UploadID); err != nil {
			_ = wc.writeError(err.Error())
			return
		}
		_ = wc.writeEnvelope(msgTypeOk, nil)

	case msgTypeDownload:
		// T2：异步下载，避免大文件阻塞 WS 读循环（心跳/其他操作）
		go handleDownload(wc, sc, req)

	default:
		_ = wc.writeError("unknown operation: " + env.Type)
	}
}

// handleDownload 异步处理下载：推送 download_start → 二进制流 → complete。
// wc 写操作有互斥锁保护，可与主循环并发安全写入。
func handleDownload(wc *wsConn, sc *sftp.Client, req sftpRequestData) {
	reader, total, err := sc.DownloadStream(req.Path, req.Offset)
	if err != nil {
		_ = wc.writeError(err.Error())
		return
	}
	defer func() { _ = reader.Close() }()
	_ = wc.writeEnvelope(msgTypeDownloadStart, map[string]any{"total": total})
	buf := make([]byte, 32*1024)
	for {
		n, rerr := reader.Read(buf)
		if n > 0 {
			if werr := wc.writeRaw(websocket.BinaryMessage, buf[:n]); werr != nil {
				return
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			_ = wc.writeError("read file: " + rerr.Error())
			return
		}
	}
	_ = wc.writeEnvelope(msgTypeComplete, map[string]any{"filename": path.Base(req.Path)})
}

// handleBinaryChunk 解析二进制分片帧并写入远程 .part 文件，回 chunk_ack。
// 帧格式（大端序）：[4字节 upload_id_len][upload_id][4字节 chunk_index][8字节 offset][8字节 data_len][data]
func handleBinaryChunk(wc *wsConn, sc *sftp.Client, data []byte) {
	uploadID, chunkIndex, offset, chunkData, err := parseChunkFrame(data)
	if err != nil {
		_ = wc.writeError("parse binary frame: " + err.Error())
		return
	}
	if err := sc.UploadChunk(uploadID, chunkIndex, offset, chunkData); err != nil {
		_ = wc.writeError(err.Error())
		return
	}
	_ = wc.writeEnvelope(msgTypeChunkAck, map[string]any{"chunk_index": chunkIndex})
}

// parseChunkFrame 解析二进制分片帧头。
//
//nolint:gosec // G115: 帧长度已通过 headerLen 校验，转换值远小于 int 上限
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

// sftpDownloadHandler GET /api/sftp/download?node=...&path=...
// v3 新增：HTTP Range 下载，支持断点续传。设计见 design-v3.md §6.5。
//
//nolint:unparam // cfg 保留供未来扩展（下载限速/权限校验），并与同包 handler 签名一致
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
		defer func() { _ = sc.Close() }()

		offset := parseRangeOffset(r.Header.Get("Range"))
		reader, total, err := sc.DownloadStream(remotePath, offset)
		if err != nil {
			http.Error(w, "sftp open: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer func() { _ = reader.Close() }()

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
