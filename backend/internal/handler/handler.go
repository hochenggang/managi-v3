// Package handler 实现 HTTP 与 WebSocket 端点。
// 对应 v2 的 routers.py，路由保持兼容。
// 设计见 ../design-v3.md §4.1 与 §4.4。
package handler

import (
	"bytes"
	"net/http"
	"time"

	"managi/internal/config"
	"managi/internal/sshpool"
)

// Register 注册全部路由到给定 mux。
// done 用于通知后台 goroutine（pool cleaner 等）退出。
func Register(mux *http.ServeMux, cfg *config.Config, done <-chan struct{}) *sshpool.Pool {
	pool := sshpool.New(cfg)
	pool.StartCleaner(done)

	// 静态首页（v2 GET /）
	mux.HandleFunc("/", indexHandler(cfg))

	// SSH 命令执行
	mux.HandleFunc("/api/ssh/test", testHandler(pool, cfg))
	mux.HandleFunc("/api/ssh/batch", batchHandler(pool, cfg))

	// WebSocket 端点
	mgr := newSessionManager(pool, cfg)
	mux.HandleFunc("/ws", terminalWSHandler(mgr, cfg))
	mux.HandleFunc("/ws/sftp", sftpWSHandler(pool, cfg))

	// v3 新增：SFTP 下载（HTTP Range，断点续传）
	mux.HandleFunc("/api/sftp/download", sftpDownloadHandler(pool, cfg))

	return pool
}

func indexHandler(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if len(cfg.IndexHTML) > 0 {
			http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(cfg.IndexHTML))
			return
		}
		http.ServeFile(w, r, cfg.IndexHTMLPath)
	}
}
