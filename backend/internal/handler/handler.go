// Package handler 实现 HTTP 与 WebSocket 端点。
// 对应 v2 的 routers.py，路由保持兼容。
// 设计见 ../design-v3.md §4.1 与 §4.4。
package handler

import (
	"net/http"

	"managi/internal/config"
	"managi/internal/sshpool"
)

// Register 注册全部路由到给定 mux。
func Register(mux *http.ServeMux, cfg *config.Config) {
	pool := sshpool.New(cfg)
	pool.StartCleaner()

	// 静态首页（v2 GET /）
	mux.HandleFunc("/", indexHandler(cfg))

	// SSH 命令执行
	mux.HandleFunc("/api/ssh/test", testHandler(pool, cfg))
	mux.HandleFunc("/api/ssh/batch", batchHandler(pool, cfg))

	// WebSocket 端点
	mux.HandleFunc("/ws", terminalWSHandler(pool, cfg))
	mux.HandleFunc("/ws/sftp", sftpWSHandler(pool, cfg))

	// v3 新增：SFTP 下载（HTTP Range，断点续传）
	mux.HandleFunc("/api/sftp/download", sftpDownloadHandler(pool, cfg))
}

// TODO(P0): 以下 handler 占位，实现阶段填充：
//   - indexHandler:  返回 index.html（v3 由 Go 直接服务前端单 HTML）
//   - testHandler:   单节点命令执行（对应 v2 /api/ssh/test）
//   - batchHandler:  批量并发执行（errgroup，对应 v2 /api/ssh/batch）
//   - terminalWSHandler: /ws 终端会话（首包认证 + 字节透传 + Ping/Pong 心跳）
//   - sftpWSHandler:     /ws/sftp 文件操作（含 upload_chunk/download 扩展）
//   - sftpDownloadHandler: HTTP Range 下载（design-v3.md §6.5）

func indexHandler(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// TODO(P0): 读取 cfg.IndexHTMLPath 返回 HTMLResponse
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		http.ServeFile(w, r, cfg.IndexHTMLPath)
	}
}
