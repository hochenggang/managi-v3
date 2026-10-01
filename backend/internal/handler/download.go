// Package handler - HTTP 下载端点 POST /api/sftp/download。
// 大文件走 HTTP 而不是 WS：浏览器原生支持 Range 续传与进度，
// 也不必和终端输出抢同一条 WS 的写锁。设计见 design-v5.md §6.5。
package handler

import (
	"net/http"
	"path"

	"managi/internal/model"
	"managi/internal/sftp"
	"managi/internal/sshpool"
)

// sftpDownloadRequest 是 POST /api/sftp/download 的请求体。
// 节点凭据（auth_value）必须走请求体而非 URL：URL 会被浏览器历史、
// 代理日志与服务端访问日志记录下来。
type sftpDownloadRequest struct {
	Node model.Node `json:"node"`
	Path string     `json:"path"`
}

// sftpDownloadHandler POST /api/sftp/download，请求体 {node, path}。
// Range 语义整份交给标准库：闭区间（bytes=a-b）、开区间（bytes=a-）、后缀区间
// （bytes=-n）、不可满足区间回 416 并带 Content-Range: bytes */total，
// 外加 Content-Length 与 Last-Modified——这些自己写只会写不全，然后各处客户端踩坑。
// 实测确认 ServeContent 在 POST 上同样按 Range 处理，故凭据仍留在请求体里。
func sftpDownloadHandler(pool *sshpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req sftpDownloadRequest
		if !decodeJSONRequest(w, r, &req) {
			return
		}
		if req.Node.Host == "" || req.Path == "" {
			writeJSONError(w, http.StatusBadRequest, "missing node or path")
			return
		}

		sshConn, err := pool.Get(req.Node)
		if err != nil {
			writeJSONError(w, http.StatusBadGateway, "ssh connect: "+err.Error())
			return
		}
		defer pool.Release(sshConn)

		sc, err := sftp.New(sshConn.Client())
		if err != nil {
			writeJSONError(w, http.StatusBadGateway, "sftp init: "+err.Error())
			return
		}
		defer func() { _ = sc.Close() }()

		reader, info, err := sc.OpenRead(req.Path)
		if err != nil {
			writeJSONError(w, http.StatusBadGateway, "sftp open: "+err.Error())
			return
		}
		defer func() { _ = reader.Close() }()

		w.Header().Set("Content-Type", "application/octet-stream")
		http.ServeContent(w, r, path.Base(req.Path), info.ModTime(), reader)
	}
}
